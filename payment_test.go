package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fiatjaf/eventstore/sqlite3"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// mockBarkd serves the three barkd endpoints paid writes use. Its history is
// whatever the test puts in it.
type mockBarkd struct {
	mu        sync.Mutex
	addresses int
	history   []map[string]any
}

func (m *mockBarkd) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.Header.Get("authorization") != "Bearer secret" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch req.Method + " " + req.URL.Path {
	case "POST /api/v1/wallet/addresses/next":
		m.addresses++
		json.NewEncoder(w).Encode(map[string]any{"address": "tark1address" + string(rune('0'+m.addresses))})
	case "POST /api/v1/lightning/receives/invoice":
		var body struct {
			AmountSat int64 `json:"amount_sat"`
		}
		json.NewDecoder(req.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{"invoice": "lntbs" + strings.Repeat("x", 400)})
	case "GET /api/v1/history":
		json.NewEncoder(w).Encode(m.history)
	default:
		http.NotFound(w, req)
	}
}

func receive(id int, status, typ, value string, sats int64) map[string]any {
	return map[string]any{
		"id":     id,
		"status": status,
		"received_on": []any{map[string]any{
			"destination": map[string]any{"type": typ, "value": value},
			"amount_sat":  sats,
		}},
	}
}

func newTestPaidWrites(t *testing.T, env map[string]string) (*paidWrites, *mockBarkd) {
	t.Helper()
	mock := &mockBarkd{}
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)

	store := &sqlite3.SQLite3Backend{DatabaseURL: filepath.Join(t.TempDir(), "paid.sqlite")}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	t.Setenv("BARKD_URL", srv.URL)
	t.Setenv("BARKD_TOKEN", "secret")
	for k, v := range env {
		t.Setenv(k, v)
	}
	p, err := newPaidWrites(store.DB, "wss://relay.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return p, mock
}

func pay(t *testing.T, p *paidWrites, query string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	p.handlePay(rec, httptest.NewRequest("GET", "/pay?"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/pay?%s: %d %s", query, rec.Code, rec.Body)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPaidWritesOffByDefault(t *testing.T) {
	t.Setenv("BARKD_URL", "")
	if p, err := newPaidWrites(nil, ""); p != nil || err != nil {
		t.Fatalf("newPaidWrites = %v, %v; want nil, nil", p, err)
	}
	t.Setenv("BARKD_URL", "http://127.0.0.1:3000")
	if _, err := newPaidWrites(nil, "wss://relay.example.com"); err == nil {
		t.Fatal("expected an error without a SQL driver")
	}
}

func TestPaidWritesArkAndLightning(t *testing.T) {
	p, mock := newTestPaidWrites(t, nil)
	ctx := context.Background()
	pubkey := strings.Repeat("a", 64)
	evt := &nostr.Event{PubKey: pubkey, Kind: 1, CreatedAt: nostr.Now()}

	ok, msg := p.charge(ctx, evt)
	if ok || !strings.HasPrefix(msg, "restricted: ") || !strings.Contains(msg, "https://relay.example.com/pay?pubkey="+pubkey) {
		t.Fatalf("charge without balance = %v, %q", ok, msg)
	}

	// one Ark address per pubkey, reused
	first := pay(t, p, "pubkey="+pubkey)
	again := pay(t, p, "pubkey="+pubkey+"&amount=10")
	if first["ark_address"] != "tark1address1" || again["ark_address"] != "tark1address1" {
		t.Fatalf("ark_address = %v then %v, want tark1address1 both times", first["ark_address"], again["ark_address"])
	}
	invoice, _ := again["invoice"].(string)
	if invoice == "" || first["invoice"] != nil {
		t.Fatalf("invoice = %q (with amount), %v (without)", invoice, first["invoice"])
	}

	mock.mu.Lock()
	mock.history = []map[string]any{
		receive(1, "successful", "ark", "tark1address1", 5),
		receive(2, "pending", "invoice", invoice, 10),
		receive(3, "successful", "ark", "tark1someoneelse", 100),
	}
	mock.mu.Unlock()
	if err := p.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := p.balance(ctx, pubkey); got != 5000 {
		t.Fatalf("balance after the Ark payment = %d, want 5000", got)
	}

	// the invoice settles; the Ark payment must not be credited twice
	mock.mu.Lock()
	mock.history[1] = receive(2, "successful", "invoice", invoice, 10)
	mock.mu.Unlock()
	if err := p.poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := p.balance(ctx, pubkey); got != 15000 {
		t.Fatalf("balance after the invoice = %d, want 15000", got)
	}

	if ok, msg := p.charge(ctx, evt); !ok {
		t.Fatalf("charge with balance: %s", msg)
	}
	if got := p.balance(ctx, pubkey); got != 14000 {
		t.Fatalf("balance after one event = %d, want 14000", got)
	}
}

func TestPaidWritesConfig(t *testing.T) {
	p, _ := newTestPaidWrites(t, map[string]string{
		"PAID_WRITES_RAILS":     "ark",
		"PAID_WRITES_KINDS":     "1, 30023",
		"PAID_WRITES_FEE_MSATS": "21000",
	})
	pubkey := strings.Repeat("b", 64)

	if res := pay(t, p, "pubkey="+pubkey+"&amount=10"); res["invoice"] != nil || res["ark_address"] == nil {
		t.Fatalf("ark only: %v", res)
	}
	if ok, _ := p.charge(context.Background(), &nostr.Event{PubKey: pubkey, Kind: 7}); !ok {
		t.Fatal("kind 7 is not charged and must be accepted")
	}
	if ok, _ := p.charge(context.Background(), &nostr.Event{PubKey: pubkey, Kind: 30023}); ok {
		t.Fatal("kind 30023 is charged and must be rejected without balance")
	}

	info := nip11.RelayInformationDocument{
		Limitation: &nip11.RelayLimitationDocument{},
		Fees:       &nip11.RelayFeesDocument{},
	}
	p.advertise(&info)
	b, _ := json.Marshal(info)
	for _, want := range []string{
		`"payments_url":"https://relay.example.com/pay"`,
		`"payment_required":true`,
		`"restricted_writes":true`,
		`"publication":[{"kinds":[1,30023],"amount":21000,"unit":"msats"}]`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("NIP-11 document lacks %s: %s", want, b)
		}
	}

	t.Setenv("PAID_WRITES_RAILS", "cashu")
	if _, err := newPaidWrites(p.db, "wss://relay.example.com"); err == nil {
		t.Error("expected an unknown rail to be rejected")
	}
}

func TestPayRejectsBadInput(t *testing.T) {
	p, _ := newTestPaidWrites(t, nil)
	for _, query := range []string{"", "pubkey=nothex", "pubkey=" + strings.Repeat("c", 64) + "&amount=-1"} {
		rec := httptest.NewRecorder()
		p.handlePay(rec, httptest.NewRequest("GET", "/pay?"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("/pay?%s: %d, want 400", query, rec.Code)
		}
	}
}
