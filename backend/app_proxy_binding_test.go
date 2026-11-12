package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

type reconcileCountingProxyDAO struct {
	browser.ProxyDAO
	lists int
	err   error
}

func (d *reconcileCountingProxyDAO) List() ([]browser.Proxy, error) {
	d.lists++
	if d.err != nil {
		return nil, d.err
	}
	return d.ProxyDAO.List()
}

type reconcileCountingProfileDAO struct {
	browser.ProfileDAO
	saves int
}

func (d *reconcileCountingProfileDAO) Upsert(profile *browser.Profile) error {
	d.saves++
	return d.ProfileDAO.Upsert(profile)
}

func TestReconcileProfileProxyBindingsUsesFreshCatalogEachPass(t *testing.T) {
	app, proxyDAO, profileDAO := newReconcileBindingTestApp(t)
	for _, item := range []browser.Proxy{
		{ProxyId: "node-a", ProxyName: "Shared", ProxyConfig: "http://a:8080", SourceID: "a"},
		{ProxyId: "node-b", ProxyName: "Shared", ProxyConfig: "http://b:8080", SourceID: "b"},
	} {
		if err := proxyDAO.Upsert(item); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []*browser.Profile{
		{ProfileId: "profile-a", ProxyId: "node-a", GroupId: "group-a", Keywords: []string{"retain"}},
		{ProfileId: "profile-b", ProxyId: "node-b", GroupId: "group-b"},
	} {
		if err := profileDAO.ProfileDAO.Upsert(profile); err != nil {
			t.Fatal(err)
		}
	}
	app.browserMgr.InitData()
	app.reconcileProfileProxyBindings()
	if proxyDAO.lists != 1 || profileDAO.saves != 2 {
		t.Fatalf("first pass: catalog reads=%d profile saves=%d", proxyDAO.lists, profileDAO.saves)
	}
	first, err := profileDAO.GetById("profile-a")
	if err != nil || first.ProxyConfig != "http://a:8080" || first.ProxyBindSourceID != "a" || first.GroupId != "group-a" || !reflect.DeepEqual(first.Keywords, []string{"retain"}) {
		t.Fatalf("first persisted binding/other fields: profile=%+v error=%v", first, err)
	}

	// Recreate a subscribed node with a different ID, as a proxy refresh can do.
	// The next reconciliation must read the new catalog and remap by source/name.
	if err := proxyDAO.Delete("node-a"); err != nil {
		t.Fatal(err)
	}
	if err := proxyDAO.Upsert(browser.Proxy{ProxyId: "node-a-new", ProxyName: "Shared", ProxyConfig: "http://a-new:8080", SourceID: "a"}); err != nil {
		t.Fatal(err)
	}
	app.reconcileProfileProxyBindings()
	if proxyDAO.lists != 2 || profileDAO.saves != 4 {
		t.Fatalf("second pass: catalog reads=%d profile saves=%d", proxyDAO.lists, profileDAO.saves)
	}
	stored, err := profileDAO.GetById("profile-a")
	if err != nil || stored.ProxyId != "node-a-new" || stored.ProxyConfig != "http://a-new:8080" || stored.ProxyBindSourceID != "a" || stored.CreatedAt != first.CreatedAt {
		t.Fatalf("refreshed persisted binding: profile=%+v error=%v", stored, err)
	}
	app.reconcileProfileProxyBindings()
	if proxyDAO.lists != 3 || profileDAO.saves != 4 {
		t.Fatalf("unchanged pass: catalog reads=%d profile saves=%d", proxyDAO.lists, profileDAO.saves)
	}
	restarted := browser.NewManager(config.DefaultConfig(), t.TempDir())
	restarted.ProfileDAO = profileDAO.ProfileDAO
	restarted.InitData()
	if got := restarted.Profiles["profile-a"]; got == nil || got.ProxyId != stored.ProxyId || got.ProxyConfig != stored.ProxyConfig {
		t.Fatalf("reloaded binding=%+v", got)
	}
}

func TestReconcileProfileProxyBindingsDAOFailureKeepsMissingBinding(t *testing.T) {
	app, proxyDAO, profileDAO := newReconcileBindingTestApp(t)
	profile := &browser.Profile{ProfileId: "profile", ProxyId: "missing", ProxyConfig: "http://old:8080", ProxyBindName: "Old node"}
	if err := profileDAO.ProfileDAO.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	app.browserMgr.InitData()
	before := *app.browserMgr.Profiles[profile.ProfileId]
	// A configured fallback must not override an unavailable authoritative DAO.
	app.browserMgr.Config.Browser.Proxies = []config.BrowserProxy{{ProxyId: "missing", ProxyName: "Old node", ProxyConfig: "http://stale:8080"}}
	proxyDAO.err = errors.New("synthetic catalog read failure")
	app.reconcileProfileProxyBindings()
	if proxyDAO.lists != 1 || profileDAO.saves != 0 || !reflect.DeepEqual(*app.browserMgr.Profiles[profile.ProfileId], before) {
		t.Fatalf("failed catalog read changed a binding: reads=%d saves=%d profile=%+v", proxyDAO.lists, profileDAO.saves, app.browserMgr.Profiles[profile.ProfileId])
	}
}

func newReconcileBindingTestApp(t *testing.T) (*App, *reconcileCountingProxyDAO, *reconcileCountingProfileDAO) {
	t.Helper()
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "bindings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	manager := browser.NewManager(config.DefaultConfig(), root)
	proxies := &reconcileCountingProxyDAO{ProxyDAO: browser.NewSQLiteProxyDAO(db.GetConn())}
	profiles := &reconcileCountingProfileDAO{ProfileDAO: browser.NewSQLiteProfileDAO(db.GetConn())}
	manager.ProxyDAO = proxies
	manager.ProfileDAO = profiles
	return &App{browserMgr: manager}, proxies, profiles
}
