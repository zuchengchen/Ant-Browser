package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareBrowserLaunchContextPersistsOnlyChangedProfile(t *testing.T) {
	app, db := startPersistenceTestApp(t)
	// The manager deliberately retains the old group, as existing group actions
	// can update the persisted group without refreshing every in-memory profile.
	if _, err := db.GetConn().Exec(`UPDATE browser_profiles SET group_id='new-database-group' WHERE profile_id='untouched'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetConn().Exec(`CREATE TABLE start_write_audit (profile_id TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetConn().Exec(`CREATE TRIGGER track_start_profile_updates
		AFTER UPDATE ON browser_profiles BEGIN
		INSERT INTO start_write_audit (profile_id) VALUES (NEW.profile_id); END`); err != nil {
		t.Fatal(err)
	}
	profile := app.browserMgr.Profiles["target"]
	if len(profile.FingerprintArgs) != 0 || len(profile.LaunchArgs) != 0 {
		t.Fatal("fixture must require default completion")
	}
	app.browserMgr.Mutex.Lock()
	_, _, _, _, _, err := app.prepareBrowserLaunchContext(browserStartInput{ProfileID: "target"}, profile, nil)
	app.browserMgr.Mutex.Unlock()
	// The intentionally missing core stops before process, network or profile
	// directory work. A later launch failure must retain its existing message.
	if err == nil || !strings.Contains(err.Error(), "未配置可用浏览器内核") || profile.LastError != err.Error() {
		t.Fatalf("launch failure changed: error=%v LastError=%q", err, profile.LastError)
	}
	stored, err := app.browserMgr.ProfileDAO.GetById("target")
	if err != nil || stored == nil || !reflect.DeepEqual(stored.FingerprintArgs, app.config.Browser.DefaultFingerprintArgs) || !reflect.DeepEqual(stored.LaunchArgs, app.config.Browser.DefaultLaunchArgs) {
		t.Fatalf("target defaults were not persisted: profile=%+v error=%v", stored, err)
	}
	untouched, err := app.browserMgr.ProfileDAO.GetById("untouched")
	if err != nil || untouched == nil || untouched.GroupId != "new-database-group" {
		t.Fatalf("startup overwrote an unrelated persisted change: profile=%+v error=%v", untouched, err)
	}
	var totalWrites, targetWrites int
	if err := db.GetConn().QueryRow(`SELECT COUNT(*), SUM(CASE WHEN profile_id='target' THEN 1 ELSE 0 END) FROM start_write_audit`).Scan(&totalWrites, &targetWrites); err != nil {
		t.Fatal(err)
	}
	if totalWrites != 1 || targetWrites != 1 {
		t.Fatalf("startup profile writes: total=%d target=%d expected both=1", totalWrites, targetWrites)
	}
}

func TestPrepareBrowserLaunchContextKeepsWriteFailurePolicy(t *testing.T) {
	app, db := startPersistenceTestApp(t)
	if _, err := db.GetConn().Exec(`CREATE TRIGGER reject_start_profile_update
		BEFORE INSERT ON browser_profiles BEGIN SELECT RAISE(ABORT, 'test start persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	profile := app.browserMgr.Profiles["target"]
	app.browserMgr.Mutex.Lock()
	_, _, _, _, _, err := app.prepareBrowserLaunchContext(browserStartInput{ProfileID: "target"}, profile, nil)
	app.browserMgr.Mutex.Unlock()
	// Startup has always logged a default-save failure and continued resolving
	// the launch plan. The persistence optimization must not mask the real core
	// resolution error or change that error-handling policy.
	if err == nil || !strings.Contains(err.Error(), "未配置可用浏览器内核") || strings.Contains(err.Error(), "test start persistence failure") || profile.LastError != err.Error() {
		t.Fatalf("default-save failure changed launch result: error=%v LastError=%q", err, profile.LastError)
	}
	if !reflect.DeepEqual(profile.FingerprintArgs, app.config.Browser.DefaultFingerprintArgs) {
		t.Fatal("write failure changed in-memory default completion")
	}
	stored, err := app.browserMgr.ProfileDAO.GetById("target")
	if err != nil || stored == nil || len(stored.FingerprintArgs) != 0 || len(stored.LaunchArgs) != 0 {
		t.Fatalf("rejected default save unexpectedly persisted: profile=%+v error=%v", stored, err)
	}
}

func startPersistenceTestApp(t *testing.T) (*App, *database.DB) {
	t.Helper()
	root := t.TempDir()
	db, err := database.NewDB(filepath.Join(root, "profiles.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Browser.DefaultFingerprintArgs = []string{"--fingerprint=123456"}
	cfg.Browser.DefaultLaunchArgs = []string{"--lang=en-US"}
	manager := browser.NewManager(cfg, root)
	manager.ProfileDAO = browser.NewSQLiteProfileDAO(db.GetConn())
	for _, id := range []string{"target", "untouched"} {
		if err := manager.ProfileDAO.Upsert(&browser.Profile{
			ProfileId: id, ProfileName: id, UserDataDir: id,
			ProxyMode: browser.ProxyModeLocal, ProxyConfig: "http://127.0.0.1:18080",
			GroupId: "original-group", FingerprintArgs: []string{}, LaunchArgs: []string{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	manager.InitData()
	return &App{config: cfg, browserMgr: manager, appRoot: root}, db
}
