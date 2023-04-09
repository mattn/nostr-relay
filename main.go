package main

import (
	_ "embed"
	"encoding/json"
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

const version = "0.0.5"

var revision = "HEAD"

var (
	//go:embed static/index.html
	indexPage []byte

	//go:embed static/favicon.ico
	favicon []byte
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
	s.Router().Handle("/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
}

func (r *Relay) Init() error                       { return nil }
func (r *Relay) AcceptEvent(evt *nostr.Event) bool { return true }
func (r *Relay) BeforeSave(evt *nostr.Event)       {}
func (r *Relay) AfterSave(evt *nostr.Event) {
	json.NewEncoder(os.Stderr).Encode(evt)
}
func (r *Relay) ServiceURL() string {
	return "https://nostr.compile-error.net"
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
