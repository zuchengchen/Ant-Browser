package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testMofaAuthorizeURL(site string) *url.URL {
	return &url.URL{Scheme: "https", Host: "auth.openai.com", Path: "/api/accounts/authorize", RawQuery: url.Values{
		"response_type": {"code"}, "client_id": {"registered-example"}, "redirect_uri": {site + mofaCallbackPath},
		"state": {strings.Repeat("a", 64)}, "nonce": {strings.Repeat("b", 64)}, "prompt": {"consent"},
		"scope": {"openid profile email offline_access"}, "code_challenge": {strings.Repeat("z", 43)}, "code_challenge_method": {"S256"},
	}.Encode()}
}

func TestMofaTOTP(t *testing.T) {
	code, err := mofaTOTP("gez dgnbv gy3tqojq gezdgnbvgy3tqojq", time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("TOTP result = %q, %v", code, err)
	}
	if _, err := mofaTOTP("not a base32 secret!", time.Now()); err == nil {
		t.Fatal("invalid seed accepted")
	}
}

func TestMofaAuthorizationBoundary(t *testing.T) {
	const site = "https://mofa.example.test:8443"
	for name, alter := range map[string]func(url.Values){
		"borrowed client":  func(q url.Values) { q.Set("client_id", "app_EMoamEEZ73f0CkXaXp7hrann") },
		"foreign callback": func(q url.Values) { q.Set("redirect_uri", "https://mofa.example.test.evil.example"+mofaCallbackPath) },
		"loopback":         func(q url.Values) { q.Set("redirect_uri", "http://localhost:1455/auth/callback") },
		"duplicate state":  func(q url.Values) { q.Add("state", q.Get("state")) },
		"missing PKCE":     func(q url.Values) { q.Del("code_challenge") },
		"no consent":       func(q url.Values) { q.Set("prompt", "none") },
	} {
		t.Run(name, func(t *testing.T) {
			u := testMofaAuthorizeURL(site)
			q := u.Query()
			alter(q)
			u.RawQuery = q.Encode()
			if _, err := parseMofaAuthorization(u.String(), site); err == nil {
				t.Fatal("unsafe authorization address accepted")
			}
		})
	}
	a, err := parseMofaAuthorization(testMofaAuthorizeURL(site).String(), site)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://other.example" + mofaCallbackPath, site + mofaCallbackPath + "/nested"} {
		if _, matched, _ := a.callbackCode(raw + "?state=" + a.state + "&code=sentinel"); matched {
			t.Fatal("foreign callback matched")
		}
	}
	for _, suffix := range []string{"code=sentinel&state=wrong", "code=sentinel&state=" + a.state + "&code=other", "error=access_denied&state=" + a.state, "code=sentinel&state=" + a.state + "#fragment"} {
		if code, matched, err := a.callbackCode(a.callback + "?" + suffix); !matched || err == nil || code != "" {
			t.Fatalf("invalid callback accepted: matched=%t, err=%v", matched, err)
		}
	}
	if code, matched, err := a.callbackCode(a.callback + "?state=" + a.state + "&code=example"); err != nil || !matched || code != "example" {
		t.Fatalf("failed to accept callback: matched=%t, err=%v", matched, err)
	}
	u := testMofaAuthorizeURL("https://mofa.example.test")
	q := u.Query()
	q.Set("redirect_uri", "https://MOFA.example.test:443"+mofaCallbackPath)
	u.RawQuery = q.Encode()
	if _, err := parseMofaAuthorization(u.String(), "https://mofa.example.test"); err != nil {
		t.Fatalf("explicit default-port callback rejected: %v", err)
	}
}

func TestMofaImportClient(t *testing.T) {
	const email = "test+instance@example.test"
	const seed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	for _, test := range []struct{ action, wantError string }{
		{action: "skipped"}, {action: "group_added"}, {action: "imported"}, {action: "reauthorized"},
		{wantError: "未开启 Ant 账号导入接口"},
	} {
		t.Run(test.action+test.wantError, func(t *testing.T) {
			needsOAuth := test.action == "imported" || test.action == "reauthorized"
			var jobPath string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var fields map[string]string
				if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&fields) != nil || r.Header.Get("Origin") != "https://"+r.Host {
					t.Error("bad request envelope")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					if fields["email"] != "admin@example.test" || fields["password"] != "only-a-fixture" {
						t.Error("wrong administrator login fields")
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"requires_2fa":true,"temp_token":"challenge"}}`))
				case r.URL.Path == "/api/v1/auth/login/2fa":
					current, _ := mofaTOTP(seed, time.Now())
					previous, _ := mofaTOTP(seed, time.Now().Add(-30*time.Second))
					if fields["temp_token"] != "challenge" || (fields["totp_code"] != current && fields["totp_code"] != previous) || len(fields) != 2 {
						t.Error("wrong TOTP response fields")
					}
					_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"session","user":{"role":"admin"}}}`))
				case strings.HasSuffix(r.URL.Path, "/start"):
					if r.Header.Get("Authorization") != "Bearer session" || fields["email"] != email || len(fields) != 1 {
						t.Error("wrong job submission")
					}
					jobPath = strings.TrimSuffix(r.URL.Path, "/start")
					if test.wantError != "" {
						http.NotFound(w, r)
					} else if needsOAuth {
						u := testMofaAuthorizeURL("https://" + r.Host)
						_ = json.NewEncoder(w).Encode(map[string]string{"status": "authorization_required", "authorization_url": u.String()})
					} else {
						_ = json.NewEncoder(w).Encode(mofaImportResult{Status: "member", Action: test.action, AccountID: 99, GroupID: 37})
					}
				case r.URL.Path == jobPath+"/complete":
					if !needsOAuth || r.Header.Get("Authorization") != "Bearer session" || fields["email"] != email || fields["code"] != "example" || fields["state"] != strings.Repeat("a", 64) {
						t.Error("complete did not use the same job and bound state")
					}
					_ = json.NewEncoder(w).Encode(mofaImportResult{Status: "member", Action: test.action, AccountID: 99, GroupID: 37})
				default:
					t.Error("unexpected account operation")
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
			result, err := client.importAccount(ctx, email, func(_ context.Context, a mofaAuthorization) (string, error) {
				if !needsOAuth {
					t.Fatal("unexpected request for consent")
				}
				code, _, err := a.callbackCode(a.callback + "?state=" + a.state + "&code=example")
				return code, err
			})
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected MOFA version error, got %v", err)
				}
				return
			}
			if err != nil || result.Action != test.action || result.AccountID != 99 || result.GroupID != 37 {
				t.Fatalf("bad import result: action=%q, err=%v", result.Action, err)
			}
		})
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

func TestMofaWaitsForWriteAcknowledgementAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		_ = json.NewEncoder(w).Encode(mofaImportResult{Status: "member", Action: "reauthorized", AccountID: 99, GroupID: 37})
	}))
	defer server.Close()
	client := mofaClient{site: server.URL, http: server.Client()}
	result, err := client.importRequest(ctx, "job", "complete", map[string]string{"code": "example"})
	if err != nil || result.Action != "reauthorized" {
		t.Fatalf("write acknowledgement lost after cancellation: status=%q, err=%v", result.Action, err)
	}
}
