package main

import (
	"context"
	"encoding/json"
	"reflect"
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

// The NIP-11 document is read from the environment once, at startup, and the
// limits it advertises are the ones AcceptEvent enforces.
func TestLoadRelayInfo(t *testing.T) {
	savedInfo, savedLimitation := relayInfo, *relayLimitationDocument
	savedFees := *relayInfo.Fees
	t.Cleanup(func() {
		relayInfo, *relayLimitationDocument = savedInfo, savedLimitation
		*relayInfo.Fees = savedFees
	})

	t.Setenv("NOSTR_RELAY_NAME", "my relay")
	t.Setenv("NOSTR_RELAY_PAYMENTSURL", "https://relay.example.com/pay")
	t.Setenv("NOSTR_RELAY_LIMITATION_MAXCONTENTLENGTH", "10")
	t.Setenv("NOSTR_RELAY_LIMITATION_MAXLIMIT", "100")
	t.Setenv("NOSTR_RELAY_LIMITATION_PAYMENTREQUIRED", "true")
	t.Setenv("NOSTR_RELAY_FEES", `{"admission":[{"amount":1000000,"unit":"msats"}],"publication":[{"kinds":[1],"amount":100,"unit":"msats"}]}`)
	if err := loadRelayInfo(); err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal((&Relay{}).GetNIP11InformationDocument())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	limitation := doc["limitation"].(map[string]any)
	for _, tt := range []struct {
		name string
		got  any
		want any
	}{
		{"name", doc["name"], "my relay"},
		{"payments_url", doc["payments_url"], "https://relay.example.com/pay"},
		{"max_content_length", limitation["max_content_length"], 10.0},
		{"max_limit", limitation["max_limit"], 100.0},
		{"default_limit", limitation["default_limit"], 100.0},
		{"payment_required", limitation["payment_required"], true},
		{"created_at_upper_limit", limitation["created_at_upper_limit"], 1800.0},
		{"fees", doc["fees"], map[string]any{
			"admission":   []any{map[string]any{"amount": 1000000.0, "unit": "msats"}},
			"publication": []any{map[string]any{"kinds": []any{1.0}, "amount": 100.0, "unit": "msats"}},
		}},
	} {
		if !reflect.DeepEqual(tt.got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	accepted, _ := (&Relay{}).AcceptEvent(context.Background(), &nostr.Event{
		CreatedAt: nostr.Now(),
		Content:   "more than ten characters",
	})
	if accepted {
		t.Error("expected max_content_length from env to be enforced")
	}

	t.Setenv("NOSTR_RELAY_FEES", `{"admission":`)
	if err := loadRelayInfo(); err == nil {
		t.Error("expected malformed NOSTR_RELAY_FEES to be reported")
	}
}

// restricted_writes follows the allowlist, which can change on /reload.
func TestNIP11RestrictedWrites(t *testing.T) {
	r := &Relay{}
	if r.GetNIP11InformationDocument().Limitation.RestrictedWrites {
		t.Error("restricted_writes without an allowlist")
	}
	r.lists.Store(&relayLists{allowlist: map[string]struct{}{"allowed": {}}})
	if !r.GetNIP11InformationDocument().Limitation.RestrictedWrites {
		t.Error("expected restricted_writes with an allowlist")
	}
	if relayLimitationDocument.RestrictedWrites {
		t.Error("the shared limitation document must not be modified")
	}
}

func TestAcceptEventCreatedAtLimits(t *testing.T) {
	old := *relayLimitationDocument
	t.Cleanup(func() { *relayLimitationDocument = old })
	relayLimitationDocument.CreatedAtLowerLimit = 3600

	r := &Relay{}
	now := nostr.Now()
	for _, tt := range []struct {
		createdAt nostr.Timestamp
		accepted  bool
	}{
		{now, true},
		{now - 3000, true},
		{now - 4000, false},
		{now + 1000, true},
		{now + 2000, false},
	} {
		if accepted, _ := r.AcceptEvent(context.Background(), &nostr.Event{CreatedAt: tt.createdAt}); accepted != tt.accepted {
			t.Errorf("created_at now%+d: accepted = %v, want %v", int64(tt.createdAt)-int64(now), accepted, tt.accepted)
		}
	}
}

func TestAcceptEventMaxEventTags(t *testing.T) {
	old := relayLimitationDocument.MaxEventTags
	relayLimitationDocument.MaxEventTags = 2
	t.Cleanup(func() { relayLimitationDocument.MaxEventTags = old })

	r := &Relay{}
	tags := nostr.Tags{{"t", "a"}, {"t", "b"}}
	if accepted, msg := r.AcceptEvent(context.Background(), &nostr.Event{CreatedAt: nostr.Now(), Tags: tags}); !accepted {
		t.Fatalf("expected %d tags to be accepted: %s", len(tags), msg)
	}
	tags = append(tags, nostr.Tag{"t", "c"})
	if accepted, _ := r.AcceptEvent(context.Background(), &nostr.Event{CreatedAt: nostr.Now(), Tags: tags}); accepted {
		t.Fatalf("expected %d tags to be rejected", len(tags))
	}
}
