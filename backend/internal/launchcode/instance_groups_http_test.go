package launchcode

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func instanceGroupHTTPFixture(t *testing.T) (*LaunchServer, *browser.SQLiteProfileDAO, *browser.SQLiteGroupDAO, map[string]string) {
	t.Helper()
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	groups := browser.NewSQLiteGroupDAO(db.GetConn())
	profiles := browser.NewSQLiteProfileDAO(db.GetConn())
	ids := map[string]string{}
	for _, name := range []string{"Parent", "Child", "Extra", "Forbidden"} {
		parent := ""
		if name == "Child" {
			parent = ids["Parent"]
		}
		g, err := groups.Create(browser.GroupInput{GroupName: name, ParentId: parent})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = g.GroupId
	}
	for _, fixture := range []struct {
		id       string
		groupIDs []string
		keyword  string
	}{
		{"target", []string{ids["Extra"], ids["Child"]}, "needle"},
		{"child-only", []string{ids["Child"]}, "other"},
		{"extra-only", []string{ids["Extra"]}, "other"},
	} {
		p := &browser.Profile{ProfileId: fixture.id, ProfileName: fixture.id, UserDataDir: fixture.id, ProxyId: "__direct__", ProxyConfig: "direct://", GroupId: fixture.groupIDs[0], GroupIds: fixture.groupIDs, Keywords: []string{fixture.keyword}}
		if err := profiles.Upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	cfg.Browser.Proxies = []config.BrowserProxy{{ProxyId: "__direct__", ProxyName: "Fixture direct", ProxyConfig: "direct://"}}
	m := browser.NewManager(cfg, root)
	m.ProfileDAO = profiles
	m.GroupDAO = groups
	m.InitData()
	return NewLaunchServer(nil, nil, m, 0), profiles, groups, ids
}

func TestInstanceGroupHTTPNormalizationPreservesMissingNullAndEmptyIDs(t *testing.T) {
	for _, fixture := range []struct {
		name, body string
		want       []string
	}{
		{"missing", `{"profile":{"profileName":"fixture"}}`, nil},
		{"null", `{"profile":{"profileName":"fixture","groupIds":null}}`, nil},
		{"empty", `{"profile":{"profileName":"fixture","groupIds":[]}}`, []string{}},
		{"populated", `{"profile":{"profileName":"fixture","groupIds":["a","b"]}}`, []string{"a", "b"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/api/profiles/fixture", strings.NewReader(fixture.body))
			decoded, status, message := decodeProfileWriteRequest(request)
			if message != "" || status != http.StatusOK || decoded.Profile == nil {
				t.Fatalf("decode status=%d message=%s", status, message)
			}
			normalized := normalizeProfileInput(*decoded.Profile)
			if !reflect.DeepEqual(normalized.GroupIds, fixture.want) {
				t.Fatalf("normalized IDs=%#v want %#v", normalized.GroupIds, fixture.want)
			}
			if len(normalized.GroupIds) > 0 {
				normalized.GroupIds[0] = "changed"
				if decoded.Profile.GroupIds[0] == "changed" {
					t.Fatal("normalization aliases the request slice")
				}
			}
			rollback := profileToInput(&browser.Profile{GroupIds: browser.CloneGroupIDs(fixture.want)})
			if !reflect.DeepEqual(rollback.GroupIds, fixture.want) {
				t.Fatalf("rollback input lost nil/empty distinction: %#v", rollback.GroupIds)
			}
		})
	}
}

func TestInstanceGroupHTTPLegacyUpdateAppendsAndExplicitEmptyClears(t *testing.T) {
	server, profiles, _, ids := instanceGroupHTTPFixture(t)
	send := func(extra map[string]any) *httptest.ResponseRecorder {
		body := map[string]any{"profileName": "target", "userDataDir": "target", "proxyId": "__direct__", "proxyConfig": "direct://"}
		for key, value := range extra {
			body[key] = value
		}
		payload, err := json.Marshal(map[string]any{"profile": body})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/profiles/target", strings.NewReader(string(payload)))
		server.handleProfileByID(response, request)
		return response
	}
	response := send(map[string]any{"groupId": ids["Extra"], "tags": []string{"Legacy addition"}})
	if response.Code != http.StatusOK {
		t.Fatalf("legacy update status=%d body=%s", response.Code, response.Body.String())
	}
	p, err := profiles.GetById("target")
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, id := range p.GroupIds {
		set[id] = true
	}
	if len(set) != 3 || !set[ids["Extra"]] || !set[ids["Child"]] {
		t.Fatalf("legacy HTTP update discarded existing memberships: %v", p.GroupIds)
	}
	response = send(map[string]any{"groupIds": []string{}, "groupId": ids["Extra"], "tags": []string{"Legacy addition"}})
	if response.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", response.Code, response.Body.String())
	}
	p, err = profiles.GetById("target")
	if err != nil {
		t.Fatal(err)
	}
	if p.GroupIds == nil || len(p.GroupIds) != 0 || p.GroupId != "" || len(p.Tags) != 0 {
		t.Fatalf("explicit HTTP empty array did not clear memberships: %+v", p)
	}
	response = send(map[string]any{"groupIds": []string{" "}})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("blank group ID status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestInstanceGroupLaunchSelectorsUseSecondMembershipAncestorsAndAND(t *testing.T) {
	server, _, _, ids := instanceGroupHTTPFixture(t)
	for _, fixture := range []struct {
		name     string
		selector LaunchSelector
		want     []string
		status   int
	}{
		{"second-membership", LaunchSelector{GroupID: ids["Child"]}, []string{"child-only", "target"}, http.StatusOK},
		{"parent", LaunchSelector{GroupID: ids["Parent"]}, []string{"child-only", "target"}, http.StatusOK},
		{"names-and", LaunchSelector{Tags: []string{"Parent", "Extra"}}, []string{"target"}, http.StatusOK},
		{"id-and-name", LaunchSelector{GroupID: ids["Parent"], Tags: []string{"Extra"}}, []string{"target"}, http.StatusOK},
		{"id-and-group-rejection", LaunchSelector{ProfileID: "target", GroupID: ids["Forbidden"]}, nil, http.StatusNotFound},
		{"group-and-keyword", LaunchSelector{GroupID: ids["Parent"], Keywords: []string{"needle"}}, []string{"target"}, http.StatusOK},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			actual, status, message := server.findProfilesBySelector(normalizeLaunchSelector(fixture.selector))
			if status != fixture.status {
				t.Fatalf("status=%d message=%s want=%d", status, message, fixture.status)
			}
			if status != http.StatusOK {
				if message == "" {
					t.Fatal("failed selector has no error")
				}
				return
			}
			got := []string{}
			for _, p := range actual {
				got = append(got, p.ProfileId)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("profiles=%v want %v", got, fixture.want)
			}
		})
	}
}

func TestInstanceGroupLaunchSelectorRejectsAmbiguousName(t *testing.T) {
	server, _, groups, ids := instanceGroupHTTPFixture(t)
	for _, parent := range []string{"", ids["Parent"]} {
		if _, err := groups.Create(browser.GroupInput{GroupName: "Shared", ParentId: parent}); err != nil {
			t.Fatal(err)
		}
	}
	_, status, message := server.findProfilesBySelector(normalizeLaunchSelector(LaunchSelector{Tags: []string{"shared"}}))
	if status != http.StatusBadRequest || message == "" {
		t.Fatalf("ambiguous name status=%d message=%s", status, message)
	}
}
