package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/mysql"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/eventstore/sqlite3"
	"github.com/fiatjaf/relayer/v2"
	"github.com/jmoiron/sqlx"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

const name = "nostr-relay"

const version = "0.0.111"

var revision = "HEAD"

var (
	_ relayer.Relay         = (*Relay)(nil)
	_ relayer.ReqAccepter   = (*Relay)(nil)
	_ relayer.Informationer = (*Relay)(nil)
	_ relayer.Logger        = (*Relay)(nil)

	//go:embed static
	assets embed.FS
)

type Relay struct {
	driverName      string
	sqlite3Storage  *sqlite3.SQLite3Backend
	postgresStorage *postgresql.PostgresBackend
	mysqlStorage    *mysql.MySQLBackend

	mu        sync.Mutex
	allowlist []string
	blocklist []string
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
	default:
		panic("unsupported backend driver")
	}
}

func (r *Relay) Storage(ctx context.Context) eventstore.Store {
	switch r.driverName {
	case "sqlite3":
		return r.sqlite3Storage
	case "postgresql":
		return r.postgresStorage
	case "mysql":
		return r.mysqlStorage
	default:
		panic("unsupported backend driver")
	}
}

func (r *Relay) Init() error {
	return nil
}

func (r *Relay) AcceptEvent(ctx context.Context, evt *nostr.Event) bool {
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
	if len(evt.Content) > relayLimitationDocument.MaxContentLength {
		return false
	}

	json.NewEncoder(os.Stderr).Encode(evt)
	return true
}

func (r *Relay) AcceptReq(ctx context.Context, id string, filters nostr.Filters, auto string) bool {
	if len(filters) > relayLimitationDocument.MaxFilters {
		return false
	}
	if len(filters) > relayLimitationDocument.MaxFilters {
		return false
	}

	info := struct {
		ID      string        `json:"id"`
		Filters nostr.Filters `json:"filters"`
	}{
		ID:      id,
		Filters: filters,
	}
	json.NewEncoder(os.Stderr).Encode(info)
	return true
}

var relayLimitationDocument = &nip11.RelayLimitationDocument{
	MaxMessageLength: 524288,
	MaxSubscriptions: 20,    //
	MaxFilters:       10,    //
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
		SupportedNIPs:  []int{1, 2, 4, 9, 11, 12, 15, 16, 20, 22, 33, 42, 45, 50},
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
	log.Printf("[INFO] "+format, v...)
}

func (r *Relay) Warningf(format string, v ...any) {
	log.Printf("[WARN] "+format, v...)
}

func (r *Relay) Errorf(format string, v ...any) {
	log.Printf("[ERROR] "+format, v...)
}

type Info struct {
	Version     string `json:"version"`
	NumEvents   int64  `json:"num_events"`
	NumSessions int64  `json:"num_sessions"`
}

func (r *Relay) ready() {
	_, err := r.DB().Exec(`
    CREATE TABLE IF NOT EXISTS blocklist (
      pubkey text NOT NULL
    );
    `)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	_, err = r.DB().Exec(`
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
	r.mu.Lock()
	defer r.mu.Unlock()

	rows, err := r.DB().Query(`
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

	rows, err = r.DB().Query(`
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

func envDef(name, def string) string {
	value := os.Getenv(name)
	if value != "" {
		return value
	}
	return def
}

func main() {
	var r Relay
	var ver bool
	var databaseURL string

	flag.StringVar(&r.driverName, "driver", "sqlite3", "driver name")
	flag.StringVar(&databaseURL, "database", envDef("DATABASE_URL", "nostr-relay.sqlite"), "driver name (sqlite3/postgresql/mysql)")
	flag.BoolVar(&ver, "version", false, "show version")
	flag.Parse()

	if ver {
		fmt.Println(version)
		os.Exit(0)
	}

	go func() {
		log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
	}()

	switch r.driverName {
	case "sqlite3", "":
		r.sqlite3Storage = &sqlite3.SQLite3Backend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	case "postgresql":
		r.postgresStorage = &postgresql.PostgresBackend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	case "mysql":
		r.mysqlStorage = &mysql.MySQLBackend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported backend driver")
		os.Exit(2)
	}

	server, err := relayer.NewServer(&r, relayer.WithPerConnectionLimiter(5.0, 1))
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	r.ready()

	r.DB().SetConnMaxLifetime(1 * time.Minute)
	r.DB().SetMaxOpenConns(80)
	r.DB().SetMaxIdleConns(10)
	r.DB().SetConnMaxIdleTime(30 * time.Second)

	sub, _ := fs.Sub(assets, "static")
	server.Router().HandleFunc("/info", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Add("content-type", "application/json")
		info := Info{
			Version: version,
		}
		if err := r.DB().QueryRow("select count(*) from event").Scan(&info.NumEvents); err != nil {
			log.Println(err)
		}
		info.NumSessions = int64(r.DB().Stats().OpenConnections)
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
