package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// paidWrites charges a fee for each event from a per-pubkey prepaid balance.
// The balance is topped up through a Bark wallet daemon (barkd): either with
// an Ark payment to the pubkey's own Ark address, or with a Lightning invoice
// received through the Ark server's Lightning gateway. Payments are found by
// polling the wallet history, so nothing has to reach the relay from outside.
//
// It is off unless BARKD_URL is set, and needs a SQL driver for the balances.
type paidWrites struct {
	db          *sqlx.DB
	barkd       *barkd
	fee         int64            // msats per event
	kinds       map[int]struct{} // charged kinds; all when empty
	ark         bool
	lightning   bool
	paymentsURL string
}

func newPaidWrites(db *sqlx.DB, serviceURL string) (*paidWrites, error) {
	url := envDef("BARKD_URL", "")
	if url == "" {
		return nil, nil
	}
	if db == nil {
		return nil, errors.New("paid writes need a SQL driver")
	}
	p := &paidWrites{
		db:          db,
		barkd:       &barkd{url: strings.TrimRight(url, "/"), token: envDef("BARKD_TOKEN", "")},
		fee:         int64(envIntDef("PAID_WRITES_FEE_MSATS", 1000)),
		kinds:       map[int]struct{}{},
		paymentsURL: relayInfo.PaymentsURL,
	}
	for _, rail := range strings.Split(envDef("PAID_WRITES_RAILS", "ark,lightning"), ",") {
		switch strings.TrimSpace(rail) {
		case "ark":
			p.ark = true
		case "lightning":
			p.lightning = true
		default:
			return nil, fmt.Errorf("unknown payment rail %q (want ark and/or lightning)", rail)
		}
	}
	for _, s := range strings.Split(envDef("PAID_WRITES_KINDS", ""), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		kind, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid PAID_WRITES_KINDS: %v", err)
		}
		p.kinds[kind] = struct{}{}
	}
	if p.paymentsURL == "" && serviceURL != "" {
		p.paymentsURL = strings.Replace(strings.TrimRight(serviceURL, "/"), "ws", "http", 1) + "/pay"
	}
	if p.paymentsURL == "" {
		return nil, errors.New("paid writes need -service-url or NOSTR_RELAY_PAYMENTSURL for payments_url")
	}
	return p, p.init()
}

func (p *paidWrites) init() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS paid_balance (pubkey varchar(64) NOT NULL PRIMARY KEY, msats bigint NOT NULL)`,
		// payment targets (Ark addresses, invoices) are keyed by a hash, as invoices are too long for a MySQL key
		`CREATE TABLE IF NOT EXISTS paid_target (hash char(64) NOT NULL PRIMARY KEY, pubkey varchar(64) NOT NULL, rail varchar(16) NOT NULL, value text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS paid_credit (id varchar(128) NOT NULL PRIMARY KEY)`,
	} {
		if _, err := p.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// advertise puts the fee and where to pay into the NIP-11 document.
func (p *paidWrites) advertise(info *nip11.RelayInformationDocument) {
	info.PaymentsURL = p.paymentsURL
	info.Limitation.PaymentRequired = true
	info.Limitation.RestrictedWrites = true
	kinds := []int{}
	for kind := range p.kinds {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	fee, _ := json.Marshal(map[string]any{"publication": []any{
		map[string]any{"kinds": kinds, "amount": p.fee, "unit": "msats"},
	}})
	json.Unmarshal(fee, info.Fees) // keeps admission and subscription fees, if any
}

// charge takes the fee for evt from its author's balance.
func (p *paidWrites) charge(ctx context.Context, evt *nostr.Event) (bool, string) {
	if _, charged := p.kinds[evt.Kind]; len(p.kinds) > 0 && !charged {
		return true, ""
	}
	res, err := p.db.ExecContext(ctx, p.db.Rebind(
		`UPDATE paid_balance SET msats = msats - ? WHERE pubkey = ? AND msats >= ?`), p.fee, evt.PubKey, p.fee)
	if err != nil {
		slog.Error("paid writes: charge", "error", err)
		return false, "error: could not charge the event"
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, fmt.Sprintf("restricted: %d msats per event, pay at %s?pubkey=%s", p.fee, p.paymentsURL, evt.PubKey)
	}
	return true, ""
}

func (p *paidWrites) balance(ctx context.Context, pubkey string) int64 {
	var msats int64
	p.db.QueryRowContext(ctx, p.db.Rebind(`SELECT msats FROM paid_balance WHERE pubkey = ?`), pubkey).Scan(&msats)
	return msats
}

func targetHash(rail, value string) string {
	h := sha256.Sum256([]byte(rail + ":" + value))
	return hex.EncodeToString(h[:])
}

// arkAddress returns the pubkey's Ark address, asking barkd for a new one the
// first time.
func (p *paidWrites) arkAddress(ctx context.Context, pubkey string) (string, error) {
	var address string
	err := p.db.QueryRowContext(ctx, p.db.Rebind(
		`SELECT value FROM paid_target WHERE pubkey = ? AND rail = 'ark'`), pubkey).Scan(&address)
	if err != sql.ErrNoRows {
		return address, err
	}
	var res struct {
		Address string `json:"address"`
	}
	if err := p.barkd.do(ctx, "POST", "/api/v1/wallet/addresses/next", nil, &res); err != nil {
		return "", err
	}
	return res.Address, p.addTarget(ctx, pubkey, "ark", res.Address)
}

func (p *paidWrites) invoice(ctx context.Context, pubkey string, sats int64) (string, error) {
	var res struct {
		Invoice string `json:"invoice"`
	}
	req := map[string]any{"amount_sat": sats, "description": "nostr relay credit for " + pubkey}
	if err := p.barkd.do(ctx, "POST", "/api/v1/lightning/receives/invoice", req, &res); err != nil {
		return "", err
	}
	return res.Invoice, p.addTarget(ctx, pubkey, "invoice", res.Invoice)
}

func (p *paidWrites) addTarget(ctx context.Context, pubkey, rail, value string) error {
	_, err := p.db.ExecContext(ctx, p.db.Rebind(
		`INSERT INTO paid_target (hash, pubkey, rail, value) VALUES (?, ?, ?, ?)`),
		targetHash(rail, value), pubkey, rail, value)
	return err
}

// handlePay serves payments_url: GET /pay?pubkey=<hex>[&amount=<sats>] returns
// the balance, the pubkey's Ark address and, with an amount, a Lightning invoice.
// "bip321" carries both as one BIP-321 URI that a client can hand unchanged to
// its wallet (NWC-321 pay): the wallet pays ark= when the address is on its own
// Ark server, otherwise lightning=. Either one credits the same pubkey.
func (p *paidWrites) handlePay(w http.ResponseWriter, req *http.Request) {
	pubkey := req.URL.Query().Get("pubkey")
	if !nostr.IsValid32ByteHex(pubkey) {
		http.Error(w, "pubkey must be 64 hex characters", http.StatusBadRequest)
		return
	}
	ctx := req.Context()
	res := map[string]any{
		"pubkey":        pubkey,
		"balance_msats": p.balance(ctx, pubkey),
		"fee_msats":     p.fee,
	}
	var err error
	if p.ark {
		res["ark_address"], err = p.arkAddress(ctx, pubkey)
	}
	if amount := req.URL.Query().Get("amount"); err == nil && p.lightning && amount != "" {
		sats, perr := strconv.ParseInt(amount, 10, 64)
		if perr != nil || sats <= 0 {
			http.Error(w, "amount must be a positive number of sats", http.StatusBadRequest)
			return
		}
		res["invoice"], err = p.invoice(ctx, pubkey, sats)
		res["amount"] = sats
	}
	if err != nil {
		slog.Error("paid writes: barkd", "error", err)
		http.Error(w, "wallet unavailable", http.StatusBadGateway)
		return
	}
	if uri := bip321(res); uri != "" {
		res["bip321"] = uri
	}
	delete(res, "amount")
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// bip321 builds bitcoin:?ark=<address>&lightning=<invoice>&amount=<btc> from the
// /pay result, with the parts it has. BIP-321 uses a bech32m type's HRP as the
// key, and the key stays "ark" for tark1 addresses. Without an invoice there is
// no amount: any amount sent to the Ark address is credited.
func bip321(res map[string]any) string {
	var params []string
	if addr, _ := res["ark_address"].(string); addr != "" {
		params = append(params, "ark="+addr)
	}
	if invoice, _ := res["invoice"].(string); invoice != "" {
		params = append(params, "lightning="+invoice)
		if sats, _ := res["amount"].(int64); sats > 0 {
			params = append(params, "amount="+strings.TrimRight(strings.TrimRight(strconv.FormatFloat(float64(sats)/1e8, 'f', 8, 64), "0"), "."))
		}
	}
	if len(params) == 0 {
		return ""
	}
	return "bitcoin:?" + strings.Join(params, "&")
}

// poll credits the successful receives in the wallet history that went to a
// pubkey's Ark address or invoice. Each one is credited once.
func (p *paidWrites) poll(ctx context.Context) error {
	var movements []struct {
		ID         json.RawMessage `json:"id"`
		Status     string          `json:"status"`
		ReceivedOn []struct {
			Destination struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"destination"`
			AmountSat int64 `json:"amount_sat"`
		} `json:"received_on"`
	}
	if err := p.barkd.do(ctx, "GET", "/api/v1/history", nil, &movements); err != nil {
		return err
	}
	for _, m := range movements {
		if m.Status != "successful" {
			continue
		}
		for i, r := range m.ReceivedOn {
			if r.AmountSat <= 0 {
				continue
			}
			var pubkey string
			err := p.db.QueryRowContext(ctx, p.db.Rebind(`SELECT pubkey FROM paid_target WHERE hash = ?`),
				targetHash(r.Destination.Type, r.Destination.Value)).Scan(&pubkey)
			if err == sql.ErrNoRows {
				continue
			} else if err != nil {
				return err
			}
			id := fmt.Sprintf("%s:%d", strings.Trim(string(m.ID), `"`), i)
			if err := p.credit(ctx, id, pubkey, r.AmountSat*1000); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *paidWrites) credit(ctx context.Context, id, pubkey string, msats int64) error {
	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, tx.Rebind(`SELECT count(*) FROM paid_credit WHERE id = ?`), id).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO paid_credit (id) VALUES (?)`), id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, tx.Rebind(`UPDATE paid_balance SET msats = msats + ? WHERE pubkey = ?`), msats, pubkey)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, tx.Rebind(`INSERT INTO paid_balance (pubkey, msats) VALUES (?, ?)`), pubkey, msats); err != nil {
			return err
		}
	}
	slog.Info("paid writes: credited", "pubkey", pubkey, "msats", msats, "payment", id)
	return tx.Commit()
}

func (p *paidWrites) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.poll(ctx); err != nil {
			slog.Warn("paid writes: polling barkd", "error", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// barkd is the part of the Bark wallet daemon's REST API used here.
type barkd struct {
	url   string
	token string
}

var barkdClient = &http.Client{Timeout: 10 * time.Second}

func (b *barkd) do(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, b.url+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if b.token != "" {
		req.Header.Set("authorization", "Bearer "+b.token)
	}
	resp, err := barkdClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("barkd %s %s: %s", method, path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
