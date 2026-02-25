package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/mysql"
	"github.com/fiatjaf/eventstore/opensearch"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/eventstore/sqlite3"
	"github.com/fiatjaf/relayer/v2"
	"github.com/jmoiron/sqlx"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	"github.com/nbd-wtf/go-nostr/nip70"
)

type Relay struct {
	driverName        string
	sqlite3Storage    *sqlite3.SQLite3Backend
	postgresStorage   *postgresql.PostgresBackend
	mysqlStorage      *mysql.MySQLBackend
	opensearchStorage *opensearch.OpensearchStorage
	storeWithHooks    *relayStore
	customSearchURL   string
	initStoreOnce     sync.Once

	serviceURL string
	mu         sync.Mutex
	allowlist  []string
	blocklist  []string
}

func (r *Relay) Name() string {
	return "nostr-relay"
}

func (r *Relay) DB() *sqlx.DB {
	switch r.driverName {
	case "sqlite3":
		return r.sqlite3Storage.DB
	case "postgresql":
		return r.postgresStorage.DB
	case "mysql":
		return r.mysqlStorage.DB
	case "opensearch":
		return nil
	default:
		panic("unsupported backend driver")
	}
}

func (r *Relay) ServiceURL() string {
	return r.serviceURL
}

func (r *Relay) Storage(ctx context.Context) eventstore.Store {
	r.initStoreOnce.Do(func() {
		var baseStore eventstore.Store
		switch r.driverName {
		case "sqlite3":
			baseStore = r.sqlite3Storage
		case "postgresql":
			baseStore = r.postgresStorage
		case "mysql":
			baseStore = r.mysqlStorage
		case "opensearch":
			baseStore = r.opensearchStorage
		default:
			panic("unsupported backend driver")
		}

		r.storeWithHooks = &relayStore{Store: baseStore}
	})
	return r.storeWithHooks
}

// relayStore wraps eventstore.Store and implements AdvancedSaver with pushover notification.
type relayStore struct {
	eventstore.Store
}

func (s *relayStore) BeforeSave(ctx context.Context, evt *nostr.Event) {}

func (s *relayStore) AfterSave(evt *nostr.Event) {
	// NIP-56: Reporting (kind 1984)
	if evt.Kind != 1984 {
		return
	}

	pushoverToken := os.Getenv("PUSHOVER_TOKEN")
	pushoverUser := os.Getenv("PUSHOVER_USER")
	if pushoverToken == "" || pushoverUser == "" {
		return
	}

	var reportedPubkey, reportedEvent, reportType string
	for _, tag := range evt.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "p":
				reportedPubkey = tag[1]
				if len(tag) >= 3 {
					reportType = tag[2]
				}
			case "e":
				reportedEvent = tag[1]
				if len(tag) >= 3 {
					reportType = tag[2]
				}
			}
		}
	}

	message := fmt.Sprintf("Reporter: %s\nType: %s\nPubkey: %s\nEvent: %s\n%s",
		evt.PubKey, reportType, reportedPubkey, reportedEvent, evt.Content)

	go func() {
		form := url.Values{}
		form.Set("token", pushoverToken)
		form.Set("user", pushoverUser)
		form.Set("title", "Nostr Report (kind 1984)")
		form.Set("message", message)

		resp, err := http.PostForm("https://api.pushover.net/1/messages.json", form)
		if err != nil {
			slog.Error("failed to send pushover notification", "error", err)
			return
		}
		resp.Body.Close()
		slog.Info("pushover notification sent", "status", resp.StatusCode, "reporter", evt.PubKey)
	}()
}

func (r *Relay) performCustomSearch(ctx context.Context, search string, filter nostr.Filter) (chan *nostr.Event, error) {
	req, err := http.NewRequest("POST", r.customSearchURL, strings.NewReader(search))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	ch := make(chan *nostr.Event)
	go func() {
		defer close(ch)
		dec := json.NewDecoder(resp.Body)
		for dec.More() {
			var evt nostr.Event
			if err := dec.Decode(&evt); err != nil {
				return
			}
			ch <- &evt
		}
	}()
	return ch, nil
}

func (r *Relay) Init() error {
	return nil
}

func (r *Relay) AcceptEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	if evt.CreatedAt > nostr.Now()+30*60 {
		return false, ""
	}

	if nip70.IsProtected(*evt) {
		pubkey, ok := relayer.GetAuthStatus(ctx)
		if !ok {
			return false, "auth-required: need to authenticate"
		}
		if evt.PubKey != pubkey {
			return false, "auth-required: need to authenticate"
		}
	}

	// NIP-26: Delegated Event Signing validation
	if !validateDelegation(evt) {
		return false, "invalid: malformed delegation"
	}

	// NIP-65: Relay List Metadata validation
	if evt.Kind == 10002 {
		if !validateRelayListMetadata(evt) {
			return false, "invalid: malformed relay list metadata"
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.blocklist, evt.PubKey) {
		return false, ""
	}
	if len(r.allowlist) > 0 {
		for _, a := range r.allowlist {
			if evt.PubKey != a {
				return false, ""
			}
		}
	}
	if len(evt.Content) > relayLimitationDocument.MaxContentLength {
		return false, ""
	}

	slog.Debug("AcceptEvent", "event", []any{"EVENT", evt})
	return true, ""
}

func (r *Relay) AcceptReq(ctx context.Context, id string, filters nostr.Filters, auth string) bool {
	if len(filters) > 200 {
		slog.Debug("AcceptReq", "limit", fmt.Sprintf("filters is limited as %d (but %d)", 200, len(filters)))
		return false
	}
	slog.Debug("AcceptReq", "req", []any{"REQ", id, filters})
	return true
}

var relayLimitationDocument = &nip11.RelayLimitationDocument{
	MaxMessageLength: 524288,
	MaxSubscriptions: 20,    //
	MaxLimit:         500,   //
	MaxSubidLength:   100,   //
	MaxEventTags:     100,   //
	MaxContentLength: 16384, //
	MinPowDifficulty: 0,     // No PoW requirement
	AuthRequired:     false,
	PaymentRequired:  false,
}

func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	info := nip11.RelayInformationDocument{
		Name:           "nostr-relay",
		Description:    "relay powered by the relayer framework",
		PubKey:         "2c7cc62a697ea3a7826521f3fd34f0cb273693cbe5e9310f35449f43622a5cdc",
		Contact:        "mattn.jp@gmail.com",
		SupportedNIPs:  supportedNIPs,
		Software:       "https://github.com/mattn/nostr-relay",
		Icon:           "https://nostr.compile-error.net/logo.png",
		Version:        version,
		Limitation:     relayLimitationDocument,
		RelayCountries: []string{},
		LanguageTags:   []string{},
		Tags:           []string{},
		PostingPolicy:  "",
		PaymentsURL:    "",
		Fees: &nip11.RelayFeesDocument{
			Admission: []struct {
				Amount int    "json:\"amount\""
				Unit   string "json:\"unit\""
			}{},
		},
	}
	if err := envconfig.Process("NOSTR_RELAY", &info); err != nil {
		log.Fatalf("failed to read from env: %v", err)
	}
	return info
}

func (r *Relay) Infof(format string, v ...any) {
	slog.Info(fmt.Sprintf(format, v...))
}

func (r *Relay) Warningf(format string, v ...any) {
	slog.Warn(fmt.Sprintf(format, v...))
}

func (r *Relay) Errorf(format string, v ...any) {
	slog.Error(fmt.Sprintf(format, v...))
}

type Info struct {
	Version       string `json:"version"`
	NumEvents     int64  `json:"num_events"`
	NumSessions   int64  `json:"num_sessions"`
	SupportedNIPs []any  `json:"supported_nips"`
}

func (r *Relay) ready() {
	db := r.DB()
	if db == nil {
		return
	}

	_, err := db.Exec(`
    CREATE TABLE IF NOT EXISTS blocklist (
      pubkey text NOT NULL
    );
    `)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	_, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS allowlist (
      pubkey text NOT NULL
    );
    `)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	r.reload()
}

func (r *Relay) reload() {
	db := r.DB()
	if db == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	rows, err := db.Query(`
    SELECT pubkey FROM blocklist
    `)
	if err != nil {
		log.Printf("failed to create server: %v", err)
		return
	}
	defer rows.Close()

	r.blocklist = []string{}
	for rows.Next() {
		var pubkey string
		err := rows.Scan(&pubkey)
		if err != nil {
			return
		}
		r.blocklist = append(r.blocklist, pubkey)
	}

	rows, err = db.Query(`
    SELECT pubkey FROM allowlist
    `)
	if err != nil {
		log.Printf("failed to create server: %v", err)
		return
	}
	defer rows.Close()

	r.allowlist = []string{}
	for rows.Next() {
		var pubkey string
		err := rows.Scan(&pubkey)
		if err != nil {
			return
		}
		r.allowlist = append(r.allowlist, pubkey)
	}
}
