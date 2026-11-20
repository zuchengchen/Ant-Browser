package keypouch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDevicePairsAndSignsNativeRuntimeTransitions(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	reports := make(chan string, 4)
	const ticket = "synthetic-ticket"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		digest := sha256.Sum256(pub)
		identity := base64.RawURLEncoding.EncodeToString(digest[:])
		if r.URL.Path == "/api/ant/device/pair" {
			var offered map[string]string
			if json.Unmarshal(data, &offered) != nil || offered["public_key"] != base64.RawURLEncoding.EncodeToString(pub) {
				t.Error("wrong public key")
			}
			short := strings.ToUpper(hex.EncodeToString(digest[:5]))
			json.NewEncoder(w).Encode(map[string]string{"device_id": identity, "fingerprint": short[:5] + "-" + short[5:]})
			return
		}
		nonce := r.Header.Get("X-Ant-Nonce")
		hash := sha256.Sum256(data)
		signature, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Ant-Signature"))
		value := strings.Join([]string{"ANT1", r.Header.Get("X-Ant-Time"), nonce, r.Method, r.URL.EscapedPath(), hex.EncodeToString(hash[:])}, "\n")
		if seen[nonce] || r.Header.Get("X-Ant-Device") != identity || !ed25519.Verify(pub, []byte(value), signature) {
			t.Error("invalid native device signature")
			w.WriteHeader(401)
			return
		}
		seen[nonce] = true
		if r.URL.Path == namespace+"bindings" {
			json.NewEncoder(w).Encode(map[string]any{"sample_ms": time.Now().UnixMilli(), "instances": []binding{{Ticket: ticket, ProfileID: "one"}}})
			return
		}
		var report struct {
			Items []observation `json:"instances"`
		}
		if r.URL.Path != namespace+"report" || json.Unmarshal(data, &report) != nil || len(report.Items) != 1 || report.Items[0].Ticket != ticket || bytes.Contains(data, []byte("password")) {
			t.Error("invalid observation")
			w.WriteHeader(400)
			return
		}
		reports <- report.Items[0].State
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	d, err := NewDevice(srv.URL, private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Offer(ctx, ticket, "Test device"); err != nil {
		t.Fatal(err)
	}
	wake := make(chan struct{}, 1)
	snapshots := make(chan bool, 2)
	snapshots <- true
	r := Reporter{Post: d.Post, Snapshot: func(ctx context.Context, _ []string) (map[string]bool, error) {
		select {
		case open := <-snapshots:
			return map[string]bool{"one": open}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx, wake) }()
	next := func(want string) {
		t.Helper()
		select {
		case got := <-reports:
			if got != want {
				t.Fatal("expected", want, "got", got)
			}
		case <-time.After(time.Second * 3):
			t.Fatal("missing runtime report", want)
		}
	}
	next("open")
	snapshots <- false
	wake <- struct{}{}
	next("closed")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second * 3):
		t.Fatal("reporter did not shut down")
	}
}
