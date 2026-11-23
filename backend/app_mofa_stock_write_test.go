package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	mofaWriteAccountID  = 31
	mofaWriteGroupID    = 7
	mofaWriteProxyID    = 13
	mofaWriteWorkspace  = "fixture-workspace"
	mofaWriteLabel      = "workspace-label@example.invalid"
	mofaWriteExpiry     = 32472144000
	mofaWriteAccountURL = "/admin/accounts/31"
)

type mofaWriteStep struct {
	method, path string
	data         any
	check        func(*http.Request, map[string]any)
}

func mofaWriteFixture(t *testing.T, steps ...mofaWriteStep) *mofaClient {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(requests.Add(1)) - 1
		if index >= len(steps) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		step := steps[index]
		if r.Method != step.method || r.URL.Path != "/api/v1"+step.path || r.Header.Get("Authorization") != "Bearer fixture-admin-session" {
			t.Errorf("request %d = %s %s", index, r.Method, r.URL.Path)
		}
		var body map[string]any
		if r.Method != http.MethodGet {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("request %d did not send a JSON body", index)
			}
		} else if r.ContentLength != 0 {
			t.Errorf("request %d sent a GET body", index)
		}
		if step.check != nil {
			step.check(r, body)
		}
		w.Header().Set("Content-Type", "application/json")
		if status, ok := step.data.(int); ok {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "message": "fixture-sensitive-response"})
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": step.data}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		if int(requests.Load()) != len(steps) {
			t.Errorf("made %d requests, expected %d", requests.Load(), len(steps))
		}
	})
	return &mofaClient{site: server.URL, http: server.Client(), jwt: "fixture-admin-session"}
}

func mofaWriteGroupStep() mofaWriteStep {
	return mofaWriteStep{method: http.MethodGet, path: "/admin/groups/7", data: mofaStockFixtureGroups()[3]}
}

func mofaWriteAuthURL() string {
	query := url.Values{
		"state": {"fixture-state"}, "response_type": {"code"}, "client_id": {"fixture-client"},
		"code_challenge": {strings.Repeat("A", 43)}, "code_challenge_method": {"S256"}, "redirect_uri": {mofaOAuthRedirectURI},
	}
	return "https://auth.openai.com/oauth/authorize?" + query.Encode()
}

func mofaWriteLinkStep(t *testing.T, proxy any) mofaWriteStep {
	t.Helper()
	return mofaWriteStep{method: http.MethodPost, path: "/admin/openai/generate-auth-url", data: map[string]string{
		"auth_url": mofaWriteAuthURL(), "session_id": "fixture-session",
	}, check: func(_ *http.Request, body map[string]any) {
		if body["redirect_uri"] != mofaOAuthRedirectURI || body["proxy_id"] != proxy {
			t.Error("authorization link used the wrong redirect or proxy")
		}
	}}
}

func mofaWriteExchangeStep(t *testing.T, grant any, proxy any) mofaWriteStep {
	t.Helper()
	return mofaWriteStep{method: http.MethodPost, path: "/admin/openai/exchange-code", data: grant, check: func(_ *http.Request, body map[string]any) {
		if body["redirect_uri"] != mofaOAuthRedirectURI || body["proxy_id"] != proxy ||
			body["session_id"] != "fixture-session" || body["state"] != "fixture-state" || body["code"] != "fixture-code" {
			t.Error("authorization exchange did not match the original request")
		}
	}}
}

func mofaWriteGrantFixture(email, workspace string) mofaStockOAuthGrant {
	return mofaStockOAuthGrant{
		AccessToken: "fixture-access", RefreshToken: "fixture-refresh", IDToken: "fixture-id-token",
		Email: email, ChatGPTAccountID: workspace, ExpiresAt: mofaWriteExpiry, ClientID: "fixture-client", PrivacyMode: "training_off",
	}
}

func mofaWriteAccountFixture(status, name, email, workspace string, proxyID any, groups ...int64) map[string]any {
	account := mofaStockFixtureAccount(mofaWriteAccountID, name, email, status, groups...)
	account["proxy_id"] = proxyID
	account["schedulable"] = false
	credentials := account["credentials"].(map[string]any)
	credentials["chatgpt_account_id"] = workspace
	credentials["expires_at"] = mofaWriteExpiry
	if status == "error" || status == "inactive" {
		account["error_message"] = "Token revoked (401): fixture"
	}
	return account
}

func TestMofaStockOAuthCreatesAfterVerification(t *testing.T) {
	email := mofaStockFixtureEmail
	saved := mofaWriteAccountFixture("active", email, email, mofaWriteWorkspace, nil, mofaWriteGroupID)
	client := mofaWriteFixture(t, mofaWriteLinkStep(t, nil), mofaWriteGroupStep(),
		mofaWriteExchangeStep(t, mofaWriteGrantFixture(email, mofaWriteWorkspace), nil),
		mofaWriteStep{method: http.MethodGet, path: "/admin/groups/all", data: mofaStockFixtureGroups()},
		mofaWriteStep{method: http.MethodGet, path: "/admin/accounts", data: map[string]any{
			"items": []any{}, "total": 0, "page": 1, "page_size": 100, "pages": 1,
		}},
		mofaWriteStep{method: http.MethodPost, path: "/admin/accounts", data: map[string]int64{"id": mofaWriteAccountID}, check: func(r *http.Request, body map[string]any) {
			key := r.Header.Get("Idempotency-Key")
			if !strings.HasPrefix(key, "ant-mofa-") || strings.Contains(key, "fixture-session") || body["proxy_id"] != nil ||
				body["name"] != email || body["type"] != "oauth" || body["platform"] != "openai" ||
				!reflect.DeepEqual(body["group_ids"], []any{float64(mofaWriteGroupID)}) {
				t.Error("create request did not target an idempotent OpenAI account creation in the expected group")
			}
			credentials, _ := body["credentials"].(map[string]any)
			if credentials["email"] != email || credentials["chatgpt_account_id"] != mofaWriteWorkspace ||
				credentials["refresh_token"] != "fixture-refresh" || credentials["expires_at"] != float64(mofaWriteExpiry) {
				t.Error("write omitted verified OpenAI grant properties")
			}
		}},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: saved},
	)
	flow, err := client.startStockOAuth(context.Background(), nil)
	if err != nil || flow.AuthURL != mofaWriteAuthURL() || flow.State != "fixture-state" {
		t.Fatalf("authorization link not prepared (%v)", err)
	}
	result, err := client.completeStockOAuth(context.Background(), flow, "fixture-code", flow.State, email, mofaStockResult{Action: "creation_required", GroupID: mofaWriteGroupID})
	if err != nil || result.Action != "imported" || result.AccountID != mofaWriteAccountID || !result.InTargetGroup || result.SearchHint != email {
		t.Fatalf("created account confirmation = %+v, err = %v", result, err)
	}
}

func TestMofaStockOAuthRenewsOriginalAccountAndAddsGroup(t *testing.T) {
	email := mofaStockFixtureEmail
	old := mofaWriteAccountFixture("error", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34)
	latest := mofaWriteAccountFixture("error", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34, 52)
	renewed := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34, 52)
	saved := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34, 52, mofaWriteGroupID)
	client := mofaWriteFixture(t, mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
		mofaWriteLinkStep(t, float64(mofaWriteProxyID)), mofaWriteGroupStep(),
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
		mofaWriteExchangeStep(t, mofaWriteGrantFixture(email, mofaWriteWorkspace), float64(mofaWriteProxyID)),
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: latest},
		mofaWriteStep{method: http.MethodPost, path: mofaWriteAccountURL + "/apply-oauth-credentials", data: map[string]int64{"id": mofaWriteAccountID}, check: func(_ *http.Request, body map[string]any) {
			if len(body) != 3 || body["type"] != "oauth" || body["credentials"] == nil || body["extra"] == nil {
				t.Error("renewal sent fields outside the native OAuth update")
			}
		}},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: renewed},
		mofaWriteStep{method: http.MethodPut, path: mofaWriteAccountURL, data: saved, check: func(_ *http.Request, body map[string]any) {
			if len(body) != 1 || !reflect.DeepEqual(body["group_ids"], []any{float64(34), float64(52), float64(mofaWriteGroupID)}) {
				t.Error("group update did not preserve the latest group membership")
			}
		}},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: saved},
	)
	target := mofaStockResult{Action: "reauthorization_required", AccountID: mofaWriteAccountID, GroupID: mofaWriteGroupID, workspaceID: mofaWriteWorkspace}
	proxyID, err := client.stockOAuthProxyID(context.Background(), email, target)
	if err != nil || proxyID == nil || *proxyID != mofaWriteProxyID {
		t.Fatalf("existing proxy not resolved (%v)", err)
	}
	flow, err := client.startStockOAuth(context.Background(), proxyID)
	if err != nil {
		t.Fatal(err)
	}
	*proxyID = 14 // The flow retains the proxy used to generate its link.
	result, err := client.completeStockOAuth(context.Background(), flow, "fixture-code", flow.State, email, target)
	if err != nil || result.Action != "reauthorized" || result.AccountID != mofaWriteAccountID || !result.InTargetGroup || result.SearchHint != mofaWriteLabel {
		t.Fatalf("renewed account confirmation = %+v, err = %v", result, err)
	}
}

func TestMofaStockOAuthRejectsUnverifiedCallbacks(t *testing.T) {
	validURL := mofaWriteAuthURL()
	if state, clientID := parseMofaStockAuthorizationURL(validURL); state != "fixture-state" || clientID != "fixture-client" {
		t.Fatal("stock authorization URL rejected")
	}
	for _, invalid := range []string{
		strings.Replace(validURL, "auth.openai.com", "auth.openai.com.example.invalid", 1),
		strings.Replace(validURL, "auth.openai.com", "foreign@auth.openai.com", 1),
		strings.Replace(validURL, "1455", "1445", 1),
		strings.Replace(validURL, "S256", "plain", 1),
		validURL + "&state=another", validURL + "#fragment",
	} {
		if state, clientID := parseMofaStockAuthorizationURL(invalid); state != "" || clientID != "" {
			t.Errorf("unexpectedly accepted an invalid authorization URL")
		}
	}
	client := mofaWriteFixture(t)
	flow := mofaStockOAuth{State: "fixture-state", sessionID: "fixture-session", clientID: "fixture-client"}
	_, err := client.completeStockOAuth(context.Background(), flow, "fixture-code", "unrelated-state", mofaStockFixtureEmail, mofaStockResult{
		Action: "creation_required", GroupID: mofaWriteGroupID,
	})
	if !errors.Is(err, errMofaStockOAuthCallback) {
		t.Fatalf("wrong state yielded %v", err)
	}
}

func TestMofaStockOAuthExchangeRequiresConfirmedEmail(t *testing.T) {
	wrongClient := mofaWriteGrantFixture(mofaStockFixtureEmail, mofaWriteWorkspace)
	wrongClient.ClientID = "other-client"
	for _, test := range []struct {
		name  string
		grant any
		want  error
	}{
		{"wrong email", mofaWriteGrantFixture("other@example.invalid", mofaWriteWorkspace), errMofaStockOAuthIdentity},
		{"missing email", mofaWriteGrantFixture("", mofaWriteWorkspace), errMofaStockOAuthIdentity},
		{"different OAuth client", wrongClient, errMofaStockOAuthExchange},
		{"private remote error", http.StatusBadRequest, errMofaStockOAuthExchange},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := mofaWriteFixture(t, mofaWriteGroupStep(), mofaWriteExchangeStep(t, test.grant, nil))
			flow := mofaStockOAuth{State: "fixture-state", sessionID: "fixture-session", clientID: "fixture-client"}
			_, err := client.completeStockOAuth(context.Background(), flow, "fixture-code", flow.State, mofaStockFixtureEmail, mofaStockResult{
				Action: "creation_required", GroupID: mofaWriteGroupID,
			})
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "fixture-sensitive-response") {
				t.Fatalf("expected the sanitized OAuth error, got %v", err)
			}
		})
	}
}

func TestMofaStockOAuthRenewalGuardsAccountState(t *testing.T) {
	email := mofaStockFixtureEmail
	old := mofaWriteAccountFixture("error", mofaWriteLabel, email, mofaWriteWorkspace, nil, mofaWriteGroupID)
	for _, test := range []struct {
		name  string
		steps []mofaWriteStep
		want  error
	}{
		{"changed proxy", []mofaWriteStep{{method: http.MethodGet, path: mofaWriteAccountURL, data: mofaWriteAccountFixture("error", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID)}}, errMofaStockTargetChanged},
		{"explicitly inactive", []mofaWriteStep{{method: http.MethodGet, path: mofaWriteAccountURL, data: mofaWriteAccountFixture("inactive", mofaWriteLabel, email, mofaWriteWorkspace, nil)}}, errMofaStockOAuthInactive},
		{"different OAuth workspace", []mofaWriteStep{
			{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
			mofaWriteExchangeStep(t, mofaWriteGrantFixture(email, "other-workspace"), nil),
			{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
		}, errMofaStockOAuthWorkspace},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := mofaWriteFixture(t, append([]mofaWriteStep{mofaWriteGroupStep()}, test.steps...)...)
			flow := mofaStockOAuth{State: "fixture-state", sessionID: "fixture-session", clientID: "fixture-client"}
			target := mofaStockResult{Action: "reauthorization_required", AccountID: mofaWriteAccountID, GroupID: mofaWriteGroupID, workspaceID: mofaWriteWorkspace}
			_, err := client.completeStockOAuth(context.Background(), flow, "fixture-code", flow.State, email, target)
			if !errors.Is(err, test.want) {
				t.Fatalf("renewal yielded %v, expected %v", err, test.want)
			}
		})
	}
}

func TestMofaStockLegacyOAuthNeedsWorkspace(t *testing.T) {
	email := mofaStockFixtureEmail
	legacy := mofaWriteAccountFixture("error", email, "", "", mofaWriteProxyID)
	identified := mofaWriteAccountFixture("error", email, "", mofaWriteWorkspace, mofaWriteProxyID)
	client := mofaWriteFixture(t,
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: legacy},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: identified},
	)
	target := mofaStockResult{Action: "reauthorization_required", AccountID: mofaWriteAccountID, GroupID: mofaWriteGroupID}
	if _, err := client.stockOAuthProxyID(context.Background(), email, target); !errors.Is(err, errMofaStockLegacyOAuth) {
		t.Fatalf("legacy record without a workspace yielded %v", err)
	}
	if proxyID, err := client.stockOAuthProxyID(context.Background(), email, target); err != nil || proxyID == nil || *proxyID != mofaWriteProxyID {
		t.Fatalf("legacy record with a workspace could not start renewal (%v)", err)
	}
}

func TestMofaStockGroupUpdateAndReadback(t *testing.T) {
	email := mofaStockFixtureEmail
	old := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34)
	saved := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34, mofaWriteGroupID)
	client := mofaWriteFixture(t, mofaWriteGroupStep(),
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
		mofaWriteStep{method: http.MethodPut, path: mofaWriteAccountURL, data: saved, check: func(_ *http.Request, body map[string]any) {
			if len(body) != 1 || !reflect.DeepEqual(body["group_ids"], []any{float64(34), float64(mofaWriteGroupID)}) {
				t.Error("group-only update did not send exactly the expected memberships")
			}
		}},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: saved},
		mofaWriteGroupStep(), mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: saved},
	)
	target := mofaStockResult{Action: "group_required", AccountID: mofaWriteAccountID, GroupID: mofaWriteGroupID}
	for _, action := range []string{"group_added", "skipped"} {
		result, err := client.addStockTargetGroup(context.Background(), email, target)
		if err != nil || result.Action != action || !result.InTargetGroup || result.AccountID != mofaWriteAccountID {
			t.Fatalf("group update confirmation = %+v, err = %v", result, err)
		}
	}
}

func TestMofaStockGroupRejectsUnverifiedNameAndUnconfirmedWrite(t *testing.T) {
	email := mofaStockFixtureEmail
	legacy := mofaWriteAccountFixture("active", email, "", mofaWriteWorkspace, nil, 34)
	old := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, 34)
	changed := mofaWriteAccountFixture("active", mofaWriteLabel, email, mofaWriteWorkspace, mofaWriteProxyID, mofaWriteGroupID)
	client := mofaWriteFixture(t, mofaWriteGroupStep(), mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: legacy}, mofaWriteGroupStep(),
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: old},
		mofaWriteStep{method: http.MethodPut, path: mofaWriteAccountURL, data: changed},
		mofaWriteStep{method: http.MethodGet, path: mofaWriteAccountURL, data: changed},
	)
	target := mofaStockResult{Action: "group_required", AccountID: mofaWriteAccountID, GroupID: mofaWriteGroupID}
	for _, want := range []error{errMofaStockLegacyGroup, errMofaStockGroupWrite} {
		result, err := client.addStockTargetGroup(context.Background(), email, target)
		if !errors.Is(err, want) || result != (mofaStockResult{}) {
			t.Fatalf("unsafe group write yielded %+v and %v, expected %v", result, err, want)
		}
	}
}
