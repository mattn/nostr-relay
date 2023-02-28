package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/fiatjaf/relayer"
	"github.com/fiatjaf/relayer/storage/sqlite3"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
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

func (r *Relay) OnInitialized(*relayer.Server)     {}
func (r *Relay) Init() error                       { return nil }
func (r *Relay) AcceptEvent(evt *nostr.Event) bool { return true }
func (r *Relay) BeforeSave(evt *nostr.Event)       {}
func (r *Relay) AfterSave(evt *nostr.Event) {
	json.NewEncoder(os.Stderr).Encode(evt)
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
