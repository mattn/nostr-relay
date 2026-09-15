package main

import (
	"context"
	"testing"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/eventstore/sqlite3"
)

// The store wrapper must expose Notifier exactly when the backend has one:
// relayer routes every delivery through it if present, so exposing it for a
// backend without one would make NewServer fail.
func TestStorageExposesNotifier(t *testing.T) {
	for _, tt := range []struct {
		relay *Relay
		want  bool
	}{
		{&Relay{driverName: "postgresql", postgresStorage: &postgresql.PostgresBackend{}}, true},
		{&Relay{driverName: "sqlite3", sqlite3Storage: &sqlite3.SQLite3Backend{}}, false},
	} {
		_, got := tt.relay.Storage(context.Background()).(eventstore.Notifier)
		if got != tt.want {
			t.Errorf("%s: Notifier exposed = %v, want %v", tt.relay.driverName, got, tt.want)
		}
		if _, ok := tt.relay.Storage(context.Background()).(eventstore.Counter); !ok {
			t.Errorf("%s: Counter no longer exposed", tt.relay.driverName)
		}
	}
}
