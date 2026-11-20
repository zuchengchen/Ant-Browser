package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"database/sql"
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
	if proxyDAO.lists != 2 || profileDAO.saves != 3 {
		t.Fatalf("second pass: catalog reads=%d profile saves=%d", proxyDAO.lists, profileDAO.saves)
	}
	stored, err := profileDAO.GetById("profile-a")
	if err != nil || stored.ProxyId != "node-a-new" || stored.ProxyConfig != "http://a-new:8080" || stored.ProxyBindSourceID != "a" || stored.CreatedAt != first.CreatedAt {
		t.Fatalf("refreshed persisted binding: profile=%+v error=%v", stored, err)
	}
	app.reconcileProfileProxyBindings()
	if proxyDAO.lists != 3 || profileDAO.saves != 3 {
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

func TestReconcileDirectProfilesPreservesLegacyProfileBytes(t *testing.T) {
	app, _, profiles := newReconcileBindingTestApp(t)
	db := app.db.GetConn()
	app.browserMgr.ProfileDAO = profiles.ProfileDAO
	if err := profiles.ProfileDAO.Upsert(&browser.Profile{ProfileId: "direct", ProxyId: "__direct__"}); err != nil {
		t.Fatal(err)
	}
	// The -19 GUI stores an empty launch argument list as the JSON text null.
	// ProfileDAO.List presents [] to the frontend. Reconciliation must never
	// serialize that read projection back into unrelated profile settings.
	execProxyBindingFixture(t, db, `INSERT INTO browser_profiles(profile_id,profile_name,proxy_id,launch_args,fingerprint_args,created_at,updated_at)
		VALUES('legacy','Legacy','','null',' [ ] ','before','before'),
		('needs-binding','Legacy binding','__direct__','null',' [ ] ','before','before')`)
	execProxyBindingFixture(t, db, `INSERT INTO browser_profile_groups(profile_id,group_id) VALUES('legacy','group-a')`)
	execProxyBindingFixture(t, db, `CREATE TEMP TABLE fixture_updated(id TEXT);
		CREATE TEMP TRIGGER fixture_audit AFTER UPDATE ON browser_profiles BEGIN INSERT INTO fixture_updated VALUES(NEW.profile_id); END;
		CREATE TEMP TRIGGER fixture_group_insert AFTER INSERT ON browser_profile_groups BEGIN INSERT INTO fixture_updated VALUES('membership'); END;
		CREATE TEMP TRIGGER fixture_group_delete AFTER DELETE ON browser_profile_groups BEGIN INSERT INTO fixture_updated VALUES('membership'); END;`)
	app.browserMgr.InitData()
	app.reconcileProfileProxyBindings()
	for _, id := range []string{"legacy", "needs-binding"} {
		var launch, fingerprint string
		if err := db.QueryRow(`SELECT launch_args,fingerprint_args FROM browser_profiles WHERE profile_id=?`, id).Scan(&launch, &fingerprint); err != nil || launch != "null" || fingerprint != " [ ] " {
			t.Fatalf("existing row %s: launch_args=%q fingerprint_args=%q err=%v", id, launch, fingerprint, err)
		}
	}
	if ugCompatCount(t, app, `SELECT count(*) FROM fixture_updated WHERE id IN ('legacy','membership')`) != 0 {
		t.Fatal("startup rewrote an unrelated instance or membership")
	}
	for _, id := range []string{"needs-binding", "direct"} {
		p, err := app.browserMgr.ProfileDAO.GetById(id)
		if err != nil || p.ProxyBindName == "" || p.ProxyId != "__direct__" || app.browserMgr.Profiles[id].ProxyBindName != p.ProxyBindName {
			t.Fatal("proxy binding and manager cache were not repaired", err)
		}
	}
	execProxyBindingFixture(t, db, `DELETE FROM fixture_updated`)
	app.reconcileProfileProxyBindings()
	if ugCompatCount(t, app, `SELECT count(*) FROM fixture_updated`) != 0 {
		t.Fatal("unchanged restart rewrote existing rows")
	}
}

func TestReconcileProfileProxyWriteFailureCanRetry(t *testing.T) {
	app, _, profiles := newReconcileBindingTestApp(t)
	db := app.db.GetConn()
	app.browserMgr.ProfileDAO = profiles.ProfileDAO
	if err := profiles.ProfileDAO.Upsert(&browser.Profile{ProfileId: "direct", ProxyId: "__direct__"}); err != nil {
		t.Fatal(err)
	}
	app.browserMgr.InitData()
	p := app.browserMgr.Profiles["direct"]
	execProxyBindingFixture(t, db, `CREATE TEMP TRIGGER fixture_deny BEFORE UPDATE ON browser_profiles BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	app.reconcileProfileProxyBindings()
	if p.ProxyBindName != "" {
		t.Fatal("failed write was published to the runtime cache")
	}
	execProxyBindingFixture(t, db, `DROP TRIGGER fixture_deny`)
	app.reconcileProfileProxyBindings()
	if p.ProxyBindName == "" {
		t.Fatal("binding did not recover on next reconciliation")
	}
}

func execProxyBindingFixture(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
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
	for _, id := range []string{"group-a", "group-b"} {
		if _, err := db.GetConn().Exec(`INSERT INTO browser_groups(group_id,group_name) VALUES (?,?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	manager := browser.NewManager(config.DefaultConfig(), root)
	proxies := &reconcileCountingProxyDAO{ProxyDAO: browser.NewSQLiteProxyDAO(db.GetConn())}
	profiles := &reconcileCountingProfileDAO{ProfileDAO: browser.NewSQLiteProfileDAO(db.GetConn())}
	manager.ProxyDAO = proxies
	manager.ProfileDAO = profiles
	return &App{db: db, browserMgr: manager}, proxies, profiles
}
