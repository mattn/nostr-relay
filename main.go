package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/fiatjaf/relayer/v2"
	"github.com/fiatjaf/relayer/v2/storage/sqlite3"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

const name = "nostr-relay"

const version = "0.0.73"

var revision = "HEAD"

var (
	//go:embed static
	assets embed.FS
)

type Relay struct {
	storage *sqlite3.SQLite3Backend

	mu        sync.Mutex
	allowlist []string
	blocklist []string
}

func (r *Relay) Name() string {
	return "nostr-relay"
}

func (r *Relay) Storage(ctx context.Context) relayer.Storage {
	return r.storage
}

func (r *Relay) Init() error {
	return nil
}

func (r *Relay) AcceptEvent(ctx context.Context, evt *nostr.Event) bool {
	// reject events that have timestamps greater than 30 minutes in the future.
	if evt.CreatedAt > nostr.Now()+30*60 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.blocklist {
		if evt.PubKey == b {
			return false
		}
	}
	if len(r.allowlist) > 0 {
		for _, a := range r.allowlist {
			if evt.PubKey != a {
				return false
			}
		}
	}
	return true
}

func (r *Relay) BeforeSave(ctx context.Context, evt *nostr.Event) {
}

func (r *Relay) AfterSave(evt *nostr.Event) {
	json.NewEncoder(os.Stderr).Encode(evt)
}

func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	info := nip11.RelayInformationDocument{
		Name:          "nostr-relay",
		Description:   "relay powered by the relayer framework",
		PubKey:        "2c7cc62a697ea3a7826521f3fd34f0cb273693cbe5e9310f35449f43622a5cdc",
		Contact:       "mattn.jp@gmail.com",
		SupportedNIPs: []int{1, 2, 4, 9, 11, 12, 15, 16, 20, 22, 33, 42, 45, 50},
		Software:      "https://github.com/mattn/nostr-relay",
		Icon:          "https://mattn.github.io/assets/image/mattn-mohawk.webp",
		Version:       version,
		Limitation: &nip11.RelayLimitationDocument{
			MaxMessageLength: 524288,
			MaxSubscriptions: 10,
			MaxFilters:       2500,
			MaxLimit:         5000,
			MaxSubidLength:   256,
			MinPrefix:        4,
			MaxEventTags:     2500,
			MaxContentLength: 65536,
			MinPowDifficulty: 0,
			AuthRequired:     false,
			PaymentRequired:  false,
		},
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
	log.Printf("[INFO] "+format, v...)
}

func (r *Relay) Warningf(format string, v ...any) {
	log.Printf("[WARN] "+format, v...)
}

func (r *Relay) Errorf(format string, v ...any) {
	log.Printf("[ERROR] "+format, v...)
}

type Info struct {
	Version string `json:"version"`
	Count   int64  `json:"count"`
}

func (r *Relay) ready() {
	_, err := r.storage.DB.Exec(`
    CREATE TABLE IF NOT EXISTS blocklist (
      pubkey text NOT NULL
    );
    `)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	_, err = r.storage.DB.Exec(`
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
	rows, err := r.storage.DB.Query(`
    SELECT pubkey FROM blocklist
    `)
	if err != nil {
		log.Printf("failed to create server: %v", err)
		return
	}
	defer rows.Close()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocklist = []string{}
	for rows.Next() {
		var pubkey string
		err := rows.Scan(&pubkey)
		if err != nil {
			return
		}
		r.blocklist = append(r.blocklist, pubkey)
	}

	rows, err = r.storage.DB.Query(`
    SELECT pubkey FROM allowlist
    `)
	if err != nil {
		log.Printf("failed to create server: %v", err)
		return
	}
	defer rows.Close()

	r.mu.Lock()
	defer r.mu.Unlock()
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

func main() {
	r := Relay{}
	r.storage = &sqlite3.SQLite3Backend{DatabaseURL: os.Getenv("DATABASE_URL")}
	r.ready()
	server, err := relayer.NewServer(&r, relayer.WithPerConnectionLimiter(5.0, 1))
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	sub, _ := fs.Sub(assets, "static")
	server.Router().HandleFunc("/info", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Add("content-type", "application/json")
		info := Info{
			Version: version,
		}
		if err := r.storage.QueryRow("select count(*) from event").Scan(&info.Count); err != nil {
			log.Println(err)
		}
		json.NewEncoder(w).Encode(info)
	})
	server.Router().HandleFunc("/reload", func(w http.ResponseWriter, req *http.Request) {
		r.reload()
	})
	server.Router().Handle("/", http.FileServer(http.FS(sub)))
	if err := server.Start("0.0.0.0", 7447); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
