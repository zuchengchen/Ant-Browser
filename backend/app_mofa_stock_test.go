package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const mofaStockFixtureEmail = "chat+test@example.invalid"

func mofaStockFixtureAccount(id int64, name, email, status string, groups ...int64) map[string]any {
	return map[string]any{
		"id": id, "name": name, "platform": "openai", "type": "oauth", "status": status, "group_ids": groups,
		"credentials": map[string]any{
			"email": email, "expires_at": "2999-01-01T00:00:00Z", "access_token": "ignored-fixture-secret",
		},
		"credentials_status": map[string]bool{"has_access_token": true, "has_refresh_token": true},
	}
}

func mofaStockFixtureGroups() []mofaStockGroup {
	return []mofaStockGroup{
		{ID: 4, Name: mofaStockGroupName, Platform: "anthropic", Status: "active"},
		{ID: 5, Name: mofaStockGroupName + " 2", Platform: "openai", Status: "active"},
		{ID: 6, Name: mofaStockGroupName, Platform: "openai", Status: "inactive"},
		{ID: 7, Name: mofaStockGroupName, Platform: "openai", Status: "active"},
	}
}

func mofaStockFixture(t *testing.T, groups []mofaStockGroup, accounts []map[string]any) (*mofaClient, *atomic.Int32) {
	t.Helper()
	var accountCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.ContentLength != 0 || r.Header.Get("Authorization") != "Bearer fixture-admin-session" {
			t.Errorf("unexpected lookup request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		query := r.URL.Query()
		var data any
		switch r.URL.Path {
		case "/api/v1/admin/groups/all":
			if query.Get("platform") != "openai" {
				t.Errorf("group platform query = %q", query.Get("platform"))
			}
			data = groups
		case "/api/v1/admin/accounts":
			accountCalls.Add(1)
			if query.Get("platform") != "openai" || query.Get("type") != "oauth" || query.Get("lite") != "true" ||
				query.Get("sort_by") != "id" || query.Get("sort_order") != "asc" || query.Has("search") || query.Has("group") {
				t.Errorf("unexpected account filters %s", r.URL.RawQuery)
			}
			page, _ := strconv.Atoi(query.Get("page"))
			size, _ := strconv.Atoi(query.Get("page_size"))
			if page < 1 || size < 1 {
				http.Error(w, "invalid page", http.StatusBadRequest)
				return
			}
			total := len(accounts)
			start, end := min(total, (page-1)*size), min(total, page*size)
			items := append([]map[string]any{}, accounts[start:end]...)
			data = map[string]any{"items": items, "total": total, "page": page, "page_size": size, "pages": max(1, (total+size-1)/size)}
		default:
			t.Errorf("unexpected lookup path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return &mofaClient{site: server.URL, jwt: "fixture-admin-session", http: server.Client()}, &accountCalls
}

func TestMofaStockLookup(t *testing.T) {
	email := mofaStockFixtureEmail
	const stockLabel = "workspace-label@example.invalid"
	result := func(action, hint string, id int64, inGroup bool) mofaStockResult {
		return mofaStockResult{Action: action, AccountID: id, GroupID: 7, SearchHint: hint, InTargetGroup: inGroup}
	}
	account := func(status string, groupIDs ...int64) map[string]any {
		return mofaStockFixtureAccount(31, "Fixture ChatGPT", strings.ToUpper(email), status, groupIDs...)
	}
	expired := account("active", 7)
	expired["credentials"].(map[string]any)["expires_at"] = 1
	expired["credentials_status"] = map[string]bool{"has_access_token": true}
	refreshableExpired := account("active", 7)
	refreshableExpired["credentials"].(map[string]any)["expires_at"] = "1"
	revoked := account("error", 7)
	revoked["error_message"] = `Authentication failed (401): {"error":{"code":"token_invalidated","message":"fixture-error-detail"}}`
	revokedOutsideGroup := account("error", 8)
	revokedOutsideGroup["error_message"] = revoked["error_message"]
	delete(revokedOutsideGroup, "credentials_status")
	unhealthy := account("error", 7)
	unhealthy["error_message"] = "Rate limited (429): fixture-error-detail"
	wrongPlatform := mofaStockFixtureAccount(10, email, email, "active", 7)
	wrongPlatform["platform"] = "anthropic"
	wrongType := mofaStockFixtureAccount(11, email, email, "active", 7)
	wrongType["type"] = "apikey"
	pat := account("active", 7)
	pat["credentials"].(map[string]any)["auth_mode"] = "personalAccessToken"
	empty := account("inactive", 7)
	empty["credentials_status"] = map[string]bool{"has_access_token": false, "has_refresh_token": false}
	unknownCredentials := account("inactive", 7)
	delete(unknownCredentials, "credentials_status")

	for _, tc := range []struct {
		name     string
		accounts []map[string]any
		want     mofaStockResult
		err      error
	}{
		{"provider email and group", []map[string]any{account("active", 7)}, result("skipped", "Fixture ChatGPT", 31, true), nil},
		{"verified provider email with different email as label", []map[string]any{mofaStockFixtureAccount(31, stockLabel, email, "active", 7)}, result("skipped", stockLabel, 31, true), nil},
		{"inactive with usable authorization", []map[string]any{account("inactive", 7)}, result("skipped", "Fixture ChatGPT", 31, true), nil},
		{"exact name fallback outside group", []map[string]any{mofaStockFixtureAccount(31, email, "", "active", 8)}, result("group_required", email, 31, false), nil},
		{"wrong platform and type and partial name", []map[string]any{wrongPlatform, wrongType, mofaStockFixtureAccount(12, "shared "+email, "", "active", 7)}, result("creation_required", strings.ToUpper(email), 0, false), nil},
		{"revoked authorization", []map[string]any{revoked}, result("reauthorization_required", "Fixture ChatGPT", 31, true), nil},
		{"revoked authorization outside group", []map[string]any{revokedOutsideGroup}, result("reauthorization_required", "Fixture ChatGPT", 31, false), nil},
		{"expired without refresh", []map[string]any{expired}, result("reauthorization_required", "Fixture ChatGPT", 31, true), nil},
		{"inactive without credentials", []map[string]any{empty}, result("reauthorization_required", "Fixture ChatGPT", 31, true), nil},
		{"missing credential indicators", []map[string]any{unknownCredentials}, mofaStockResult{}, errMofaStockHealth},
		{"expired with refresh", []map[string]any{refreshableExpired}, mofaStockResult{}, errMofaStockHealth},
		{"unrelated failure", []map[string]any{unhealthy}, mofaStockResult{}, errMofaStockHealth},
		{"identity conflict", []map[string]any{mofaStockFixtureAccount(31, email, "other@example.invalid", "active", 7)}, mofaStockResult{}, errMofaStockAccount},
		{"target identity conflict blocks outside account", []map[string]any{mofaStockFixtureAccount(30, email, "other@example.invalid", "active", 7), account("active", 8)}, mofaStockResult{}, errMofaStockAccount},
		{"provider identity resolves another account using its email as label", []map[string]any{mofaStockFixtureAccount(30, email, "other@example.invalid", "active", 7), account("active", 7)}, result("skipped", "Fixture ChatGPT", 31, true), nil},
		{"multiple accounts in target group", []map[string]any{mofaStockFixtureAccount(30, email, "", "active", 7), account("active", 7)}, mofaStockResult{}, errMofaStockGroupDuplicates},
		{"incompatible grant", []map[string]any{pat}, mofaStockResult{}, errMofaStockAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := mofaStockFixture(t, mofaStockFixtureGroups(), tc.accounts)
			got, err := client.lookupStockAccount(context.Background(), strings.ToUpper(email))
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("result = %+v, err = %v; want %+v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestMofaStockLookupPaginatesForIdentity(t *testing.T) {
	accounts := make([]map[string]any, 101)
	for i := range accounts {
		accounts[i] = mofaStockFixtureAccount(int64(i+1), fmt.Sprintf("account %d", i+1), "", "active")
	}
	accounts[100]["credentials"].(map[string]any)["email"] = mofaStockFixtureEmail
	client, calls := mofaStockFixture(t, mofaStockFixtureGroups(), accounts)
	result, err := client.lookupStockAccount(context.Background(), mofaStockFixtureEmail)
	if err != nil || result.AccountID != 101 || result.Action != "group_required" || calls.Load() != 2 {
		t.Fatalf("paginated result = %+v, err = %v, calls = %d", result, err, calls.Load())
	}

	accounts[0]["name"] = mofaStockFixtureEmail
	client, calls = mofaStockFixture(t, mofaStockFixtureGroups(), accounts)
	result, err = client.lookupStockAccount(context.Background(), mofaStockFixtureEmail)
	if !errors.Is(err, errMofaStockAccount) || result != (mofaStockResult{}) || calls.Load() != 2 {
		t.Fatalf("ambiguous result = %+v, err = %v, calls = %d", result, err, calls.Load())
	}

	accounts[1]["credentials"].(map[string]any)["email"] = mofaStockFixtureEmail
	accounts[100]["group_ids"] = []int64{7, 8}
	accounts[100]["name"] = "workspace-label@example.invalid"
	accounts[100]["status"] = "error"
	accounts[100]["error_message"] = "Token revoked (401): fixture-error-detail"
	client, calls = mofaStockFixture(t, mofaStockFixtureGroups(), accounts)
	result, err = client.lookupStockAccount(context.Background(), mofaStockFixtureEmail)
	if err != nil || result != (mofaStockResult{
		Action: "reauthorization_required", AccountID: 101, GroupID: 7, SearchHint: "workspace-label@example.invalid", InTargetGroup: true,
	}) || calls.Load() != 2 {
		t.Fatalf("target group result = %+v, err = %v, calls = %d", result, err, calls.Load())
	}
}

func TestMofaStockLookupRequiresUniqueActiveGroup(t *testing.T) {
	groups := append(mofaStockFixtureGroups(), mofaStockGroup{ID: 8, Name: mofaStockGroupName, Platform: "openai", Status: "active"})
	for _, groups := range [][]mofaStockGroup{groups[:3], groups} {
		client, calls := mofaStockFixture(t, groups, nil)
		got, err := client.lookupStockAccount(context.Background(), mofaStockFixtureEmail)
		if !errors.Is(err, errMofaStockGroup) || got != (mofaStockResult{}) || calls.Load() != 0 {
			t.Fatalf("result = %+v, err = %v, account calls = %d", got, err, calls.Load())
		}
	}
}

func TestMofaStockLookupRejectsIncompleteResults(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/groups/all" {
			fmt.Fprint(w, `{"code":0,"data":[{"id":7,"name":"GPT-不降智","platform":"openai","status":"active"}]}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"items":[],"total":1,"page":1,"page_size":100,"pages":1}}`)
	}))
	defer server.Close()
	client := &mofaClient{site: server.URL, jwt: "fixture-admin-session", http: server.Client()}
	result, err := client.lookupStockAccount(context.Background(), mofaStockFixtureEmail)
	if !errors.Is(err, errMofaStockList) || result != (mofaStockResult{}) {
		t.Fatalf("incomplete result = %+v, err = %v", result, err)
	}
}
