package backend

import (
	"path/filepath"
	"slices"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

// These fixtures contain only synthetic data. Reuse the existing migrated
// database fixture instead of depending on a user's installed application.
func ugCompatApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	db := newProfilePackageDatabase(t, root)
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "user-data")
	app := NewApp(root)
	app.config = cfg
	app.db = db
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.ProfileDAO = browser.NewSQLiteProfileDAO(db.GetConn())
	app.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
	app.browserMgr.ProxyDAO = browser.NewSQLiteProxyDAO(db.GetConn())
	app.browserMgr.CoreDAO = browser.NewSQLiteCoreDAO(db.GetConn())
	app.browserMgr.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
	return app
}

func ugCompatGroup(t *testing.T, app *App, name, parentID string) *browser.Group {
	t.Helper()
	group, err := app.CreateGroup(browser.GroupInput{GroupName: name, ParentId: parentID})
	if err != nil {
		t.Fatalf("create synthetic group: %v", err)
	}
	if group == nil || group.GroupId == "" {
		t.Fatal("group creation did not return an identity")
	}
	return group
}

func ugCompatInsertGroup(t *testing.T, app *App, id, name, parentID string) {
	t.Helper()
	if _, err := app.db.GetConn().Exec(`INSERT INTO browser_groups (group_id, group_name, parent_id) VALUES (?, ?, ?)`, id, name, parentID); err != nil {
		t.Fatalf("insert synthetic ID-collision group: %v", err)
	}
}

func ugCompatProfile(t *testing.T, app *App, id string, groupIDs ...string) *browser.Profile {
	t.Helper()
	profile := &browser.Profile{
		ProfileId: id, ProfileName: "Synthetic " + id, UserDataDir: id,
		GroupIds: append([]string{}, groupIDs...), Keywords: []string{"keep-keyword"},
		CreatedAt: "2026-11-12T00:00:00Z", UpdatedAt: "2026-11-12T00:00:00Z",
	}
	if err := app.browserMgr.ProfileDAO.Upsert(profile); err != nil {
		t.Fatalf("save synthetic profile: %v", err)
	}
	addProfilePackageTestProfile(t, app, *profile)
	return profile
}

func ugCompatIDs(t *testing.T, label string, got, want []string) {
	t.Helper()
	actual := append([]string{}, got...)
	expected := append([]string{}, want...)
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("%s: group IDs = %v, want %v", label, actual, expected)
	}
}

func ugCompatStoredGroups(t *testing.T, app *App, profileID string, want ...string) *browser.Profile {
	t.Helper()
	rows, err := app.db.GetConn().Query(`SELECT group_id FROM browser_profile_groups WHERE profile_id=? ORDER BY group_id`, profileID)
	if err != nil {
		t.Fatalf("read persisted membership rows: %v", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	ugCompatIDs(t, "relation table", ids, want)
	profile, err := app.browserMgr.ProfileDAO.GetById(profileID)
	if err != nil {
		t.Fatalf("read persisted profile: %v", err)
	}
	ugCompatIDs(t, "profile DAO", profile.GroupIds, want)
	return profile
}

func ugCompatMemoryGroups(t *testing.T, app *App, profileID string, want ...string) {
	t.Helper()
	app.browserMgr.Mutex.Lock()
	profile, exists := app.browserMgr.Profiles[profileID]
	var ids []string
	if exists && profile != nil {
		ids = append([]string{}, profile.GroupIds...)
	}
	app.browserMgr.Mutex.Unlock()
	if !exists || profile == nil {
		t.Fatalf("profile %q missing from manager", profileID)
	}
	ugCompatIDs(t, "manager snapshot", ids, want)
}

func ugCompatFindGroup(t *testing.T, app *App, name, parentID string) *browser.Group {
	t.Helper()
	groups, err := app.browserMgr.GroupDAO.List()
	if err != nil {
		t.Fatal(err)
	}
	var matches []*browser.Group
	for _, group := range groups {
		if group.GroupName == name && group.ParentId == parentID {
			matches = append(matches, group)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("synthetic group %q under %q: got %d matches, want 1", name, parentID, len(matches))
	}
	return matches[0]
}

func ugCompatCount(t *testing.T, app *App, query string, args ...any) int {
	t.Helper()
	var count int
	if err := app.db.GetConn().QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func ugCompatForeignKeys(t *testing.T, app *App) {
	t.Helper()
	if enabled := ugCompatCount(t, app, `PRAGMA foreign_keys`); enabled != 1 {
		t.Fatal("foreign key enforcement is disabled")
	}
	rows, err := app.db.GetConn().Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
