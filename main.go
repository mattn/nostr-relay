package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/fiatjaf/relayer/v2"
	"github.com/fiatjaf/relayer/v2/storage/sqlite3"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

const name = "nostr-relay"

const version = "0.0.11"

var revision = "HEAD"

var (
	//go:embed static
	assets embed.FS
)

type Relay struct {
	storage *sqlite3.SQLite3Backend
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
	return true
}

func (r *Relay) BeforeSave(evt *nostr.Event) {
}

func (r *Relay) AfterSave(evt *nostr.Event) {
	json.NewEncoder(os.Stderr).Encode(evt)
}
func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:          "nostr-relay",
		Description:   "relay powered by the relayer framework",
		PubKey:        "npub1937vv2nf06360qn9y8el6d8sevnndy7tuh5nzre4gj05xc32tnwqauhaj6",
		Contact:       "mattn.jp@gmail.com",
		SupportedNIPs: []int{9, 11, 12, 15, 16, 20, 33, 42, 49},
		Software:      "https://github.com/mattn/nostr-relay",
		Version:       version,
	}
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

func main() {
	r := Relay{}
	if err := envconfig.Process("", &r); err != nil {
		log.Fatalf("failed to read from env: %v", err)
	}
	r.storage = &sqlite3.SQLite3Backend{DatabaseURL: os.Getenv("DATABASE_URL")}
	server, err := relayer.NewServer(&r)
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
	server.Router().Handle("/", http.FileServer(http.FS(sub)))
	if err := server.Start("0.0.0.0", 7447); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
