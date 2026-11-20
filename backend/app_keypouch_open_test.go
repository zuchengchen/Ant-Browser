package backend

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

func TestKeyPouchOpenResolvesStableProfileAndKeepsTheTemplateInPlace(t *testing.T) {
	app, db := startPersistenceTestApp(t)
	app.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
	device, err := keypouch.OpenDevice(t.TempDir(), "test", "https://example.invalid", true)
	if err != nil {
		t.Fatal(err)
	}
	links, err := device.LoadProfileLinks()
	if err != nil {
		t.Fatal(err)
	}
	noPrompt := func() bool { t.Fatal("unnecessary association prompt"); return false }
	c := keyPouchOpenClaim{Action: "create-open", RecordID: 41, Email: "Test+Native@Example.invalid", GroupID: 3, GroupName: "Native group"}
	open := func() string {
		id, reason := app.keyPouchProfile(c, links, noPrompt)
		if reason != "" || id != keyPouchProfileID(c.RecordID) {
			t.Fatalf("profile %q, reason %q", id, reason)
		}
		return id
	}
	id := open()
	if open() != id {
		t.Fatal("retry changed profile")
	}
	p, err := app.browserMgr.ProfileDAO.GetById(id)
	if err != nil || p.ProfileName != c.Email || p.GroupId == "" || p.UserDataDir != id {
		t.Fatal("incorrect bound profile", p, err)
	}
	group, err := app.browserMgr.GroupDAO.GetById(p.GroupId)
	if err != nil || group.GroupName != c.GroupName {
		t.Fatal("wrong group", group, err)
	}
	var template *BrowserProfile
	rows, _ := app.browserMgr.ProfileDAO.List()
	for _, p := range rows {
		if p.ProfileName == keyPouchTemplate {
			template = p
		}
	}
	if template == nil || template.GroupId != "" || template.UserDataDir == p.UserDataDir {
		t.Fatal("invalid or shared template", template)
	}
	c.RecordID++
	if _, reason := app.keyPouchProfile(c, links, noPrompt); reason != "profile_conflict" {
		t.Fatalf("adopted unbound same-name profile: %s", reason)
	}
	c.Email = "Another@example.invalid"
	legacy, err := app.BrowserProfileCreate(BrowserProfileInput{
		GroupId:     "original-group",
		GroupIds:    []string{"original-group", group.GroupId},
		ProfileName: c.Email,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, reason := app.keyPouchProfile(c, links, func() bool { return false }); reason != "cancelled" {
		t.Fatal("expected cancelled adoption", reason)
	}
	if id, reason := app.keyPouchProfile(c, links, func() bool { return true }); reason != "" || id != legacy.ProfileId {
		t.Fatal("existing local profile not reused", reason)
	}
	links, err = device.LoadProfileLinks()
	if err != nil {
		t.Fatal(err)
	}
	if id, reason := app.keyPouchProfile(c, links, noPrompt); reason != "" || id != legacy.ProfileId {
		t.Fatal("lost association after restart", reason)
	}
	c.RecordID++
	if _, reason := app.keyPouchProfile(c, links, noPrompt); reason != "profile_conflict" {
		t.Fatal("same legacy profile bound to another record", reason)
	}
	c.Email = "Third@example.invalid"
	if err := app.BrowserProfileDelete(template.ProfileId); err != nil {
		t.Fatal(err)
	}
	if _, reason := app.keyPouchProfile(c, links, noPrompt); reason != "template_missing" {
		t.Fatalf("recreated a deleted template: %s", reason)
	}
	c.RecordID = 41
	c.Email = p.ProfileName
	if open() != id {
		t.Fatal("existing profile should work without a template")
	}
	if err := app.BrowserProfileDelete(id); err != nil {
		t.Fatal(err)
	}
	if _, reason := app.keyPouchProfile(c, links, noPrompt); reason != "profile_unavailable" {
		t.Fatalf("reused a deleted instance: %s", reason)
	}
}

func TestKeyPouchFocusRequiresARealPageActivation(t *testing.T) {
	activated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			w.Write([]byte(`[{"type":"service_worker","id":"background"},{"type":"page","id":"visible"}]`))
		case "/json/activate/visible":
			activated = true
		default:
			t.Errorf("unexpected focus target %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := keyPouchFocusTab(context.Background(), port); err != nil || !activated {
		t.Fatal("page wasn't activated", err)
	}
}

func TestKeyPouchSiteHonorsConfiguredOrigin(t *testing.T) {
	previous := bundledKeyPouchURL
	t.Cleanup(func() { bundledKeyPouchURL = previous })
	bundledKeyPouchURL = ""
	t.Setenv("ANT_KEYPOUCH_URL", "")
	if site := keyPouchSite(); site != "" {
		t.Fatalf("unconfigured site = %q", site)
	}
	bundledKeyPouchURL = "https://bundle.example.test/"
	if site := keyPouchSite(); site != "https://bundle.example.test" {
		t.Fatalf("bundled site = %q", site)
	}
	t.Setenv("ANT_KEYPOUCH_URL", " https://device.example.test/ ")
	if site := keyPouchSite(); site != "https://device.example.test" {
		t.Fatalf("device site = %q", site)
	}
}

func keyPouchRemainingGroupFixture(t *testing.T) (*App, *BrowserProfile, *keypouch.Device, keyPouchOpenClaim) {
	t.Helper()
	const site = "https://keypouch.example.invalid"
	t.Setenv("ANT_KEYPOUCH_URL", site)
	app, db := startPersistenceTestApp(t)
	app.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
	const primary = "original-group"
	const remaining = "new-database-group"
	claim := keyPouchOpenClaim{Action: "create-open", RecordID: 92, Email: "groups@example.invalid", GroupID: 1, GroupName: primary}
	profile, err := app.BrowserProfileCreate(BrowserProfileInput{
		ProfileName: claim.Email, GroupId: primary, GroupIds: []string{primary, remaining}, ProxyId: "__direct__",
	})
	if err != nil || profile.GroupId != primary || len(profile.GroupIds) != 2 {
		t.Fatalf("initial group memberships: %+v, %v", profile, err)
	}
	device, err := keypouch.OpenDevice(t.TempDir(), "test", site, true)
	if err != nil {
		t.Fatal(err)
	}
	links, err := device.LoadProfileLinks()
	if err != nil {
		t.Fatal(err)
	}
	if id, reason := app.keyPouchProfile(claim, links, func() bool { return true }); reason != "" || id != profile.ProfileId {
		t.Fatalf("associate via primary group: id=%q reason=%q", id, reason)
	}
	if err := app.DeleteGroup(primary); err != nil {
		t.Fatal(err)
	}
	stored, err := app.browserMgr.ProfileDAO.GetById(profile.ProfileId)
	if err != nil || stored.GroupId != remaining || len(stored.GroupIds) != 1 || stored.GroupIds[0] != remaining {
		t.Fatalf("persisted remaining group: %+v, %v", stored, err)
	}
	if profile.GroupId != remaining || len(profile.GroupIds) != 1 || profile.GroupIds[0] != remaining {
		t.Fatalf("cached remaining group: %+v", profile)
	}
	claim.GroupID, claim.GroupName = 2, remaining
	return app, profile, device, claim
}

func TestKeyPouchOpenUsesRemainingGroupAfterDeletingPrimary(t *testing.T) {
	app, profile, device, claim := keyPouchRemainingGroupFixture(t)
	var activated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			w.Write([]byte(`[{"type":"page","id":"open-page"}]`))
		case "/json/activate/open-page":
			activated.Store(true)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	profile.Running, profile.DebugReady = true, true
	profile.DebugPort = server.Listener.Addr().(*net.TCPAddr).Port
	if runtime.GOOS == "windows" {
		profile.Pid = 1
	}
	id, reason := app.keyPouchOpen(context.Background(), claim, device, func(int) bool {
		activated.Store(true)
		return true
	})
	if reason != "" || id != profile.ProfileId || !activated.Load() {
		t.Fatalf("open via remaining group: id=%q reason=%q activated=%t", id, reason, activated.Load())
	}
}
