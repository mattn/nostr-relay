package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"

	"github.com/fiatjaf/relayer"
	"github.com/fiatjaf/relayer/storage/sqlite3"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

const name = "nostr-relay"

const version = "0.0.9"

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

func (r *Relay) Storage() relayer.Storage {
	return r.storage
}

func (r *Relay) OnInitialized(s *relayer.Server) {
	sub, _ := fs.Sub(assets, "static")
	s.Router().PathPrefix("/").Handler(http.FileServer(http.FS(sub)))
}

func (r *Relay) Init() error                       { return nil }
func (r *Relay) AcceptEvent(evt *nostr.Event) bool { return true }
func (r *Relay) BeforeSave(evt *nostr.Event)       {}
func (r *Relay) AfterSave(evt *nostr.Event) {
	json.NewEncoder(os.Stderr).Encode(evt)
}
func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:          "nostr-relay",
		Description:   "relay powered by the relayer framework",
		PubKey:        "npub1937vv2nf06360qn9y8el6d8sevnndy7tuh5nzre4gj05xc32tnwqauhaj6",
		Contact:       "mattn.jp@gmail.com",
		SupportedNIPs: []int{9, 11, 12, 15, 16, 20, 49},
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

func main() {
	r := Relay{}
	if err := envconfig.Process("", &r); err != nil {
		log.Fatalf("failed to read from env: %v", err)
		return
	}
	r.storage = &sqlite3.SQLite3Backend{DatabaseURL: os.Getenv("DATABASE_URL")}
	if err := relayer.Start(&r); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
