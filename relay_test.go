package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/eventstore/sqlite3"
	"github.com/nbd-wtf/go-nostr"
)

// The store wrapper must expose Notifier exactly when there is one, either
// configured (Redis) or built into the backend: relayer routes every delivery
// through it if present, so exposing it without one would make NewServer fail.
func TestStorageExposesNotifier(t *testing.T) {
	redis := &redisNotifier{}
	pg := &postgresql.PostgresBackend{}
	for _, tt := range []struct {
		name  string
		relay *Relay
		want  eventstore.Notifier // nil when none must be exposed
	}{
		{"postgresql", &Relay{driverName: "postgresql", postgresStorage: pg}, pg},
		{"sqlite3", &Relay{driverName: "sqlite3", sqlite3Storage: &sqlite3.SQLite3Backend{}}, nil},
		// the configured Redis notifier works on any driver and wins over the backend's
		{"sqlite3+redis", &Relay{driverName: "sqlite3", sqlite3Storage: &sqlite3.SQLite3Backend{}, notifier: redis}, redis},
		{"postgresql+redis", &Relay{driverName: "postgresql", postgresStorage: pg, notifier: redis}, redis},
	} {
		store := tt.relay.Storage(context.Background())
		if _, exposed := store.(eventstore.Notifier); exposed != (tt.want != nil) {
			t.Errorf("%s: Notifier exposed = %v, want %v", tt.name, exposed, tt.want != nil)
		}
		if s, ok := store.(*notifyingRelayStore); ok && s.notifier != tt.want {
			t.Errorf("%s: notifier = %T, want %T", tt.name, s.notifier, tt.want)
		}
		if _, ok := tt.relay.Storage(context.Background()).(eventstore.Counter); !ok {
			t.Errorf("%s: Counter no longer exposed", tt.name)
		}
	}
}

// NIP-01: an empty filter list matches nothing and must not be treated as absent.
func TestSanitizeFilterEmptyLists(t *testing.T) {
	for _, tt := range []struct {
		json          string
		unsatisfiable bool
	}{
		{`{"limit":3}`, false},
		{`{"kinds":[]}`, true},
		{`{"authors":[]}`, true},
		{`{"ids":[]}`, true},
		{`{"#e":[]}`, true},
		{`{"#t":[],"kinds":[1]}`, true},
		{`{"kinds":[1],"authors":["` + strings.Repeat("a", 64) + `"]}`, false},
	} {
		var filter nostr.Filter
		if err := json.Unmarshal([]byte(tt.json), &filter); err != nil {
			t.Fatalf("%s: %v", tt.json, err)
		}
		if _, got := sanitizeFilter(filter); got != tt.unsatisfiable {
			t.Errorf("%s: unsatisfiable = %v, want %v", tt.json, got, tt.unsatisfiable)
		}
	}
}
