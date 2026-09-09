package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/fiatjaf/eventstore/firestore"
	"github.com/fiatjaf/eventstore/mysql"
	"github.com/fiatjaf/eventstore/opensearch"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/eventstore/sqlite3"
	"github.com/fiatjaf/eventstore/turso"
	"github.com/fiatjaf/relayer/v2"
	"github.com/nbd-wtf/go-nostr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const name = "nostr-relay"

const version = "0.0.261"

var revision = "HEAD"

var (
	_ relayer.Relay         = (*Relay)(nil)
	_ relayer.ReqAccepter   = (*Relay)(nil)
	_ relayer.Informationer = (*Relay)(nil)
	_ relayer.Logger        = (*Relay)(nil)
	_ relayer.Auther        = (*Relay)(nil)

	supportedNIPs = []any{1, 4, 9, 11, 13, 17, 26, 40, 42, 45, 50, 59, 66, 70, 78}

	//go:embed static
	assets embed.FS
)

func envDef(name, def string) string {
	value := os.Getenv(name)
	if value != "" {
		return value
	}
	return def
}

func envIntDef(name string, def int) int {
	value := envDef(name, "")
	if value == "" {
		return def
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf("invalid %s: %v", name, err)
	}
	return n
}

func init() {
	level := new(slog.LevelVar)
	level.Set(parseLogLevel(envDef("LOG_LEVEL", "info")))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
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

// MEM_PROFILE_RATE sets runtime.MemProfileRate, the average number of bytes
// allocated between heap profile samples. The 512KB default is too coarse to
// attribute a slow leak: a few hundred small objects an hour disappear into the
// sampling quantum, and the counts that come back are extrapolations in
// multiples of it. Lowering it (4096, say) makes a diff of two profiles name the
// allocation site exactly, at the cost of some allocation speed.
//
// This runs in init rather than main because the rate has to be set before the
// allocations it is meant to sample, and by the time main runs the packages
// have already allocated.
func init() {
	v := os.Getenv("MEM_PROFILE_RATE")
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("ignoring MEM_PROFILE_RATE=%q: want a non-negative integer", v)
		return
	}
	runtime.MemProfileRate = n
}

// memSnapshot picks the counters that separate live objects from memory the
// runtime is merely holding on to.
func memSnapshot(m *runtime.MemStats) map[string]uint64 {
	return map[string]uint64{
		"sys":           m.Sys,
		"heap_alloc":    m.HeapAlloc,
		"heap_sys":      m.HeapSys,
		"heap_idle":     m.HeapIdle,
		"heap_inuse":    m.HeapInuse,
		"heap_released": m.HeapReleased,
		"heap_objects":  m.HeapObjects,
		"num_gc":        uint64(m.NumGC),
	}
}

func main() {
	var r Relay
	var ver bool
	var addr string
	var databaseURL string

	flag.StringVar(&addr, "addr", "0.0.0.0:7447", "listen address")
	flag.StringVar(&r.driverName, "driver", "sqlite3", "driver name (sqlite3/turso/postgresql/mysql/opensearch/firestore)")
	flag.StringVar(&databaseURL, "database", envDef("DATABASE_URL", "nostr-relay.sqlite"), "connection string (firestore: GCP project ID)")
	flag.StringVar(&r.serviceURL, "service-url", envDef("SERVICE_URL", ""), "service URL")
	flag.StringVar(&r.customSearchURL, "custom-search", envDef("CUSTOM_SEARCH_URL", ""), "custom search URL for NIP-50")
	flag.IntVar(&relayLimitationDocument.MinPowDifficulty, "min-pow", envIntDef("MIN_POW_DIFFICULTY", 0), "minimum proof of work difficulty required for events (NIP-13)")
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

	if envDef("ENABLE_PPROF", "no") == "yes" {
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
	case "turso":
		r.tursoStorage = &turso.TursoBackend{
			DatabaseURL:    databaseURL,
			QueryLimit:     relayLimitationDocument.MaxLimit,
			QueryTagsLimit: relayLimitationDocument.MaxEventTags,
		}
	case "postgresql":
		r.postgresStorage = &postgresql.PostgresBackend{
			DatabaseURL:       databaseURL,
			QueryLimit:        relayLimitationDocument.MaxLimit,
			QueryTagsLimit:    relayLimitationDocument.MaxEventTags,
			QueryAuthorsLimit: 1000,
			QueryKindsLimit:   100,
			KeepRecentEvents:  true,
			// NIP-50 search by substring (ILIKE '%q%') so partial and CJK terms
			// match (e.g. "東京" finds "東京都"), which tsvector misses. Needs a
			// pg_trgm "gin (content gin_trgm_ops)" index to stay fast.
			SubstringSearch: true,
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
	case "firestore":
		r.firestoreStorage = &firestore.FirestoreBackend{
			ProjectID:  databaseURL,
			DatabaseID: envDef("FIRESTORE_DATABASE", ""),
			Collection: envDef("FIRESTORE_COLLECTION", "nostr-events"),
			QueryLimit: relayLimitationDocument.MaxLimit,
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
			Version:       version,
			SupportedNIPs: supportedNIPs,
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
	// /gc runs a full collection and returns the free spans to the OS, so that
	// resident memory can be told apart from a real leak: what the runtime was
	// only holding for reuse goes away here, what is still reachable does not.
	// The MemStats on either side say which of the two happened.
	server.Router().HandleFunc("/gc", func(w http.ResponseWriter, req *http.Request) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		debug.FreeOSMemory()
		runtime.ReadMemStats(&after)
		w.Header().Add("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"before": memSnapshot(&before),
			"after":  memSnapshot(&after),
		})
	})
	// /metrics exposes the Go runtime counters that say whether resident
	// memory is a leak: go_memstats_sys_bytes stops growing when the runtime
	// is only reusing what it already holds, go_memstats_heap_alloc_bytes
	// keeps growing when something is still reachable, and go_goroutines does
	// not come back down when a goroutine is lost.
	server.Router().Handle("/metrics", promhttp.Handler())
	server.Router().Handle("/", http.FileServer(http.FS(sub)))

	server.Log = &r
	if err := server.Start(host, port); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
