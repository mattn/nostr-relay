package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/nbd-wtf/go-nostr"
)

func TestAcceptEventAllowlistMatchesAnyEntry(t *testing.T) {
	r := &Relay{}
	r.lists.Store(&relayLists{
		allowlist: map[string]struct{}{
			"allowed": {},
			"other":   {},
		},
	})

	accepted, _ := r.AcceptEvent(context.Background(), &nostr.Event{
		PubKey:    "allowed",
		CreatedAt: nostr.Now(),
	})
	if !accepted {
		t.Fatal("expected allowlisted pubkey to be accepted")
	}

	rejected, _ := r.AcceptEvent(context.Background(), &nostr.Event{
		PubKey:    "blocked",
		CreatedAt: nostr.Now(),
	})
	if rejected {
		t.Fatal("expected non-allowlisted pubkey to be rejected")
	}
}

func TestPerformCustomSearchStreamsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","pubkey":"p1","created_at":1,"kind":1,"tags":[],"content":"one","sig":"s1"}`)
	}))
	defer srv.Close()

	r := &Relay{customSearchURL: srv.URL}
	ch, err := r.performCustomSearch(context.Background(), "test", nostr.Filter{})
	if err != nil {
		t.Fatalf("perform custom search: %v", err)
	}

	evt, ok := <-ch
	if !ok {
		t.Fatal("expected one event from custom search")
	}
	if evt.Content != "one" {
		t.Fatalf("unexpected event content: %q", evt.Content)
	}
}

func TestValidateDelegationRejectsForgedSignature(t *testing.T) {
	delegateeSecret := bytes32Hex(0x11)
	delegatorSecret := bytes32Hex(0x22)

	delegateePubkey := pubkeyFromSecret(t, delegateeSecret)
	delegatorPubkey := pubkeyFromSecret(t, delegatorSecret)
	conditions := "kind=1&created_at>1&created_at<4102444800"

	validSignature := delegationSignature(t, delegatorSecret, delegateePubkey, conditions)

	valid := &nostr.Event{
		PubKey:    delegateePubkey,
		CreatedAt: 100,
		Kind:      1,
		Tags: nostr.Tags{
			{"delegation", delegatorPubkey, conditions, validSignature},
		},
	}
	if !validateDelegation(valid) {
		t.Fatal("expected valid delegation to be accepted")
	}

	forged := &nostr.Event{
		PubKey:    delegateePubkey,
		CreatedAt: 100,
		Kind:      1,
		Tags: nostr.Tags{
			{"delegation", delegatorPubkey, conditions, bytes64Hex(0x33)},
		},
	}
	if validateDelegation(forged) {
		t.Fatal("expected forged delegation to be rejected")
	}
}

func delegationSignature(t *testing.T, delegatorSecret, delegateePubkey, conditions string) string {
	t.Helper()

	rawSecret, err := hex.DecodeString(delegatorSecret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	sk, _ := btcec.PrivKeyFromBytes(rawSecret)

	token := fmt.Sprintf("nostr:delegation:%s:%s", delegateePubkey, conditions)
	hash := sha256.Sum256([]byte(token))
	sig, err := schnorr.Sign(sk, hash[:], schnorr.FastSign())
	if err != nil {
		t.Fatalf("sign delegation token: %v", err)
	}
	return hex.EncodeToString(sig.Serialize())
}

func pubkeyFromSecret(t *testing.T, secret string) string {
	t.Helper()

	rawSecret, err := hex.DecodeString(secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	_, pk := btcec.PrivKeyFromBytes(rawSecret)
	compressed := pk.SerializeCompressed()
	return hex.EncodeToString(compressed[1:])
}

func bytes32Hex(b byte) string {
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = b
	}
	return hex.EncodeToString(buf)
}

func bytes64Hex(b byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = b
	}
	return hex.EncodeToString(buf)
}

func BenchmarkAcceptEventAllowlist(b *testing.B) {
	r := &Relay{}
	r.lists.Store(&relayLists{
		allowlist: map[string]struct{}{
			"allowed": {},
			"other":   {},
		},
	})

	evt := &nostr.Event{
		PubKey:    "allowed",
		CreatedAt: nostr.Now(),
	}

	b.ReportAllocs()
	for b.Loop() {
		accepted, _ := r.AcceptEvent(context.Background(), evt)
		if !accepted {
			b.Fatal("expected event to be accepted")
		}
	}
}
