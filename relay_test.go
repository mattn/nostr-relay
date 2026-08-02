package main

import (
	"context"
	"math"
	"slices"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func timestamp(v int64) *nostr.Timestamp {
	ts := nostr.Timestamp(v)
	return &ts
}

// recordingStore is a minimal eventstore.Store (plus eventstore.Counter) that
// captures the filter it was handed, so a test can assert what actually reached
// the backend rather than what a helper returned.
type recordingStore struct {
	queried []nostr.Filter
	counted []nostr.Filter
}

func (s *recordingStore) Init() error { return nil }
func (s *recordingStore) Close()      {}

func (s *recordingStore) QueryEvents(_ context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	s.queried = append(s.queried, filter)
	ch := make(chan *nostr.Event)
	close(ch)
	return ch, nil
}

func (s *recordingStore) CountEvents(_ context.Context, filter nostr.Filter) (int64, error) {
	s.counted = append(s.counted, filter)
	return 0, nil
}

func (s *recordingStore) DeleteEvent(context.Context, *nostr.Event) error  { return nil }
func (s *recordingStore) SaveEvent(context.Context, *nostr.Event) error    { return nil }
func (s *recordingStore) ReplaceEvent(context.Context, *nostr.Event) error { return nil }

// A since/until the sql `created_at` column cannot hold used to fail the whole
// query with "out of range for type integer" (sqlstate 22003) on the postgresql
// backend. No stored row can hold such a value, so an unreachable bound must come
// back as no-match and an always-true bound must be dropped — never forwarded.
func TestSanitizeFilterRewritesOutOfRangeCreatedAt(t *testing.T) {
	tests := []struct {
		name          string
		filter        nostr.Filter
		unsatisfiable bool
		wantSince     *nostr.Timestamp
		wantUntil     *nostr.Timestamp
	}{
		{
			name:          "since above column max can never match",
			filter:        nostr.Filter{Since: timestamp(9_999_999_999)},
			unsatisfiable: true,
		},
		{
			name:          "until below column min can never match",
			filter:        nostr.Filter{Until: timestamp(-9_999_999_999)},
			unsatisfiable: true,
		},
		{
			name:      "since below column min is no constraint",
			filter:    nostr.Filter{Since: timestamp(-9_999_999_999)},
			wantSince: nil,
		},
		{
			name:      "until above column max is no constraint",
			filter:    nostr.Filter{Until: timestamp(9_999_999_999)},
			wantUntil: nil,
		},
		{
			name:      "in-range bounds are untouched",
			filter:    nostr.Filter{Since: timestamp(1_700_000_000), Until: timestamp(1_800_000_000)},
			wantSince: timestamp(1_700_000_000),
			wantUntil: timestamp(1_800_000_000),
		},
		{
			name:      "the column bounds themselves are in range",
			filter:    nostr.Filter{Since: timestamp(math.MinInt32), Until: timestamp(math.MaxInt32)},
			wantSince: timestamp(math.MinInt32),
			wantUntil: timestamp(math.MaxInt32),
		},
		{
			name:   "absent bounds stay absent",
			filter: nostr.Filter{Kinds: []int{1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unsatisfiable := sanitizeFilter(tt.filter)
			if unsatisfiable != tt.unsatisfiable {
				t.Fatalf("unsatisfiable = %v, want %v", unsatisfiable, tt.unsatisfiable)
			}
			if unsatisfiable {
				return
			}
			if !equalTimestamp(got.Since, tt.wantSince) {
				t.Errorf("Since = %v, want %v", showTimestamp(got.Since), showTimestamp(tt.wantSince))
			}
			if !equalTimestamp(got.Until, tt.wantUntil) {
				t.Errorf("Until = %v, want %v", showTimestamp(got.Until), showTimestamp(tt.wantUntil))
			}
		})
	}
}

// `kind` is the same 32-bit sql column with the same unchecked binding as
// `created_at`, so it overflows the same way.
func TestSanitizeFilterRewritesOutOfRangeKinds(t *testing.T) {
	tests := []struct {
		name          string
		kinds         []int
		unsatisfiable bool
		want          []int
	}{
		{
			name:          "every kind out of range can never match",
			kinds:         []int{9_999_999_999, -9_999_999_999},
			unsatisfiable: true,
		},
		{
			name:  "out-of-range kinds are dropped from a mixed list",
			kinds: []int{1, 9_999_999_999, 7},
			want:  []int{1, 7},
		},
		{
			name:  "in-range kinds are untouched",
			kinds: []int{0, 1, 30023},
			want:  []int{0, 1, 30023},
		},
		{
			name:  "the column bounds themselves are in range",
			kinds: []int{math.MinInt32, math.MaxInt32},
			want:  []int{math.MinInt32, math.MaxInt32},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unsatisfiable := sanitizeFilter(nostr.Filter{Kinds: tt.kinds})
			if unsatisfiable != tt.unsatisfiable {
				t.Fatalf("unsatisfiable = %v, want %v", unsatisfiable, tt.unsatisfiable)
			}
			if unsatisfiable {
				return
			}
			if !slices.Equal(got.Kinds, tt.want) {
				t.Errorf("Kinds = %v, want %v", got.Kinds, tt.want)
			}
		})
	}
}

// The sanitizing has to reach the backend through the store wrapper, not just be
// correct in isolation: this is the REQ leg, and it is the wiring that a future
// refactor could silently drop while every helper-level test above stayed green.
func TestRelayStoreQueryEventsSanitizesBeforeBackend(t *testing.T) {
	backend := &recordingStore{}
	store := &relayStore{Store: backend}

	if _, err := store.QueryEvents(context.Background(), nostr.Filter{
		Kinds: []int{1},
		Until: timestamp(9_999_999_999),
	}); err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(backend.queried) != 1 {
		t.Fatalf("backend saw %d queries, want 1", len(backend.queried))
	}
	if backend.queried[0].Until != nil {
		t.Errorf("out-of-range Until reached the backend: %v", *backend.queried[0].Until)
	}

	ch, err := store.QueryEvents(context.Background(), nostr.Filter{Since: timestamp(9_999_999_999)})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if _, ok := <-ch; ok {
		t.Error("unsatisfiable filter returned an event")
	}
	if len(backend.queried) != 1 {
		t.Errorf("unsatisfiable filter was forwarded to the backend: %+v", backend.queried[1:])
	}
}

// COUNT is the leg that a filter hook on the REQ path cannot reach: relayer's
// doCount never calls AcceptReq, so NIP-45 must be sanitized in the store wrapper
// to be covered at all.
func TestRelayStoreCountEventsSanitizesBeforeBackend(t *testing.T) {
	backend := &recordingStore{}
	store := &relayStore{Store: backend}

	if _, err := store.CountEvents(context.Background(), nostr.Filter{
		Kinds: []int{1},
		Until: timestamp(9_999_999_999),
	}); err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if len(backend.counted) != 1 {
		t.Fatalf("backend saw %d counts, want 1", len(backend.counted))
	}
	if backend.counted[0].Until != nil {
		t.Errorf("out-of-range Until reached the backend: %v", *backend.counted[0].Until)
	}

	count, err := store.CountEvents(context.Background(), nostr.Filter{Since: timestamp(9_999_999_999)})
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if count != 0 {
		t.Errorf("unsatisfiable filter counted %d, want 0", count)
	}
	if len(backend.counted) != 1 {
		t.Errorf("unsatisfiable filter was forwarded to the backend: %+v", backend.counted[1:])
	}
}

func equalTimestamp(got, want *nostr.Timestamp) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func showTimestamp(ts *nostr.Timestamp) any {
	if ts == nil {
		return nil
	}
	return *ts
}
