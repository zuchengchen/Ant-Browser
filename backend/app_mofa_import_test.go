package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMofaTOTP(t *testing.T) {
	code, err := mofaTOTP("gez dgnbv gy3tqojq gezdgnbvgy3tqojq", time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("TOTP result = %q, %v", code, err)
	}
	if _, err := mofaTOTP("not a base32 secret!", time.Now()); err == nil {
		t.Fatal("invalid seed accepted")
	}
}

func TestMofaAdministratorLoginWithTOTP(t *testing.T) {
	const seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var fields map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&fields) != nil || r.Header.Get("Origin") != "https://"+r.Host || r.Header.Get("Authorization") != "" {
			t.Error("bad login request envelope")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/login":
			if fields["email"] != "admin@example.test" || fields["password"] != "only-a-fixture" || len(fields) != 2 {
				t.Error("wrong administrator login fields")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"requires_2fa":true,"temp_token":"challenge"}}`))
		case "/api/v1/auth/login/2fa":
			current, _ := mofaTOTP(seed, time.Now())
			previous, _ := mofaTOTP(seed, time.Now().Add(-30*time.Second))
			if fields["temp_token"] != "challenge" || (fields["totp_code"] != current && fields["totp_code"] != previous) || len(fields) != 2 {
				t.Error("wrong TOTP response fields")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"session","user":{"role":"admin"}}}`))
		default:
			t.Error("unexpected authentication operation")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := mofaClient{site: server.URL, http: server.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.login(ctx, mofaImportSettings{Username: "admin@example.test", Password: "only-a-fixture", TOTPSecret: seed}); err != nil {
		t.Fatal(err)
	}
	if client.jwt != "session" {
		t.Fatal("administrator session was not established")
	}
}

func TestMofaRejectsLoginRedirect(t *testing.T) {
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/another-origin-or-path", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := *redirect.Client()
	client.CheckRedirect = mofaHTTPClient.CheckRedirect
	auth := mofaClient{site: redirect.URL, http: &client}
	err := auth.login(context.Background(), mofaImportSettings{Username: "admin@example.test", Password: "only-a-fixture"})
	if err == nil || auth.jwt != "" {
		t.Fatal("redirect accepted as administrator login")
	}
}
