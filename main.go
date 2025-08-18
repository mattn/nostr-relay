package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

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

const name = "nostr-relay"

const version = "0.0.202"

var revision = "HEAD"

var (
	_ relayer.Relay         = (*Relay)(nil)
	_ relayer.ReqAccepter   = (*Relay)(nil)
	_ relayer.Informationer = (*Relay)(nil)
	_ relayer.Logger        = (*Relay)(nil)
	_ relayer.Auther        = (*Relay)(nil)

	//go:embed static
	assets embed.FS
)

type Relay struct {
	driverName        string
	sqlite3Storage    *sqlite3.SQLite3Backend
	postgresStorage   *postgresql.PostgresBackend
	mysqlStorage      *mysql.MySQLBackend
	opensearchStorage *opensearch.OpensearchStorage

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
	switch r.driverName {
	case "sqlite3":
		return r.sqlite3Storage
	case "postgresql":
		return r.postgresStorage
	case "mysql":
		return r.mysqlStorage
	case "opensearch":
		return r.opensearchStorage
	default:
		panic("unsupported backend driver")
	}
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

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.blocklist {
		if evt.PubKey == b {
			return false, ""
		}
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

func (r *Relay) AcceptReq(ctx context.Context, id string, filters nostr.Filters, auto string) bool {
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
	MinPowDifficulty: 30,
	AuthRequired:     false,
	PaymentRequired:  false,
}

func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	info := nip11.RelayInformationDocument{
		Name:           "nostr-relay",
		Description:    "relay powered by the relayer framework",
		PubKey:         "2c7cc62a697ea3a7826521f3fd34f0cb273693cbe5e9310f35449f43622a5cdc",
		Contact:        "mattn.jp@gmail.com",
		SupportedNIPs:  []any{1, 2, 4, 9, 11, 12, 15, 16, 20, 22, 28, 33, 40, 42, 45, 50, 70},
		Software:       "https://github.com/mattn/nostr-relay",
		Icon:           "https://mattn.github.io/assets/image/mattn-mohawk.webp",
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
	Version     string `json:"version"`
	NumEvents   int64  `json:"num_events"`
	NumSessions int64  `json:"num_sessions"`
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

	blocklistRows, err := db.Query(`
    SELECT pubkey FROM blocklist
    `)
	if err != nil {
		log.Printf("failed to query blocklist: %v", err)
		return
	}
	defer blocklistRows.Close()

	blocklist := []string{}
	for blocklistRows.Next() {
		var pubkey string
		if err := blocklistRows.Scan(&pubkey); err != nil {
			log.Printf("failed to scan blocklist: %v", err)
			return
		}
		blocklist = append(blocklist, pubkey)
	}
	if err := blocklistRows.Err(); err != nil {
		log.Printf("failed to read blocklist: %v", err)
		return
	}
	r.blocklist = blocklist

	allowlistRows, err := db.Query(`
    SELECT pubkey FROM allowlist
    `)
	if err != nil {
		log.Printf("failed to query allowlist: %v", err)
		return
	}
	defer allowlistRows.Close()

	allowlist := []string{}
	for allowlistRows.Next() {
		var pubkey string
		if err := allowlistRows.Scan(&pubkey); err != nil {
			log.Printf("failed to scan allowlist: %v", err)
			return
		}
		allowlist = append(allowlist, pubkey)
	}
	if err := allowlistRows.Err(); err != nil {
		log.Printf("failed to read allowlist: %v", err)
		return
	}
	r.allowlist = allowlist
}

func envDef(name, def string) string {
	value := os.Getenv(name)
	if value != "" {
		return value
	}
	return def
}

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
}

func skipEventFunc(ev *nostr.Event) bool {
	now := nostr.Now()
	for _, ex := range ev.Tags.GetAll([]string{"expiration"}) {
		v, err := strconv.ParseUint(ex.Value(), 10, 64)
		if err == nil && nostr.Timestamp(v) <= now {
			return true
		}
	}
	return false
}

func main() {
	var r Relay
	var ver bool
	var addr string
	var databaseURL string

	flag.StringVar(&addr, "addr", "0.0.0.0:7447", "listen address")
	flag.StringVar(&r.driverName, "driver", "sqlite3", "driver name (sqlite3/postgresql/mysql/opensearch)")
	flag.StringVar(&databaseURL, "database", envDef("DATABASE_URL", "nostr-relay.sqlite"), "connection string")
	flag.StringVar(&r.serviceURL, "service-url", envDef("SERVICE_URL", ""), "service URL")
	flag.BoolVar(&ver, "version", false, "show version")
	flag.Parse()

	if ver {
		fmt.Println(version)
		os.Exit(0)
	}

	host, sport, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("failed to parse address: %v", err)
	}
	port, err := net.LookupPort("tcp", sport)
	if err != nil {
		log.Fatalf("failed to parse port number: %v", err)
	}

	if envDef("ENABLE_PPOROF", "no") == "yes" {
		go func() {
			log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
		}()
	}

	switch r.driverName {
	case "sqlite3", "":
		r.sqlite3Storage = &sqlite3.SQLite3Backend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	case "postgresql":
		r.postgresStorage = &postgresql.PostgresBackend{
			DatabaseURL:      databaseURL,
			QueryLimit:       relayLimitationDocument.MaxLimit,
			QueryTagsLimit:   relayLimitationDocument.MaxEventTags,
			KeepRecentEvents: true,
		}
	case "mysql":
		r.mysqlStorage = &mysql.MySQLBackend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	case "opensearch":
		r.opensearchStorage = &opensearch.OpensearchStorage{
			URL:       databaseURL,
			IndexName: "",
			Insecure:  true,
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported backend driver:", r.driverName)
		os.Exit(2)
	}

	server, err := relayer.NewServer(
		&r,
		relayer.WithPerConnectionLimiter(5.0, 1),
		relayer.WithSkipEventFunc(skipEventFunc),
	)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	r.ready()

	if db := r.DB(); db != nil {
		r.DB().SetConnMaxLifetime(1 * time.Minute)
		r.DB().SetMaxOpenConns(80)
		r.DB().SetMaxIdleConns(10)
		r.DB().SetConnMaxIdleTime(30 * time.Second)
	}

	sub, _ := fs.Sub(assets, "static")
	server.Router().HandleFunc("/info", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Add("content-type", "application/json")
		info := Info{
			Version: version,
		}
		if db := r.DB(); db != nil {
			if err := db.QueryRow("select count(*) from event").Scan(&info.NumEvents); err != nil {
				log.Println(err)
			}
			info.NumSessions = int64(r.DB().Stats().OpenConnections)
		}
		json.NewEncoder(w).Encode(info)
	})
	server.Router().HandleFunc("/reload", func(w http.ResponseWriter, req *http.Request) {
		r.reload()
	})
	server.Router().Handle("/", http.FileServer(http.FS(sub)))

	server.Log = &r
	if err := server.Start(host, port); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
