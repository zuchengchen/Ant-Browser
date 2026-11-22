package backend

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"ant-chrome/backend/internal/keypouch"
)

func TestKeyPouchExtensionSiteTracksSettingsAndRestart(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	db, err := database.NewDB(filepath.Join(app.appRoot, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(); err != nil {
		t.Fatal(err)
	}
	start := func(a *App) {
		t.Helper()
		a.db = db
		a.config = config.DefaultConfig()
		if err := a.startupInitManagers(a.config, db); err != nil {
			t.Fatal(err)
		}
	}
	siteFile := app.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory, "site.json"))
	wantSite := func(want string) {
		t.Helper()
		data, err := os.ReadFile(siteFile)
		var settings map[string]string
		if err != nil || json.Unmarshal(data, &settings) != nil || len(settings) != 1 || settings["site"] != want {
			t.Fatalf("extension site = %q (%v), want %q", data, err, want)
		}
	}
	start(app)
	wantSite("https://fallback.example.test")
	const saved = "https://chosen.example.test"
	if _, err := app.SaveKeyPouchSettings(saved + "/"); err != nil {
		t.Fatal(err)
	}
	wantSite(saved)
	if _, err := app.SaveKeyPouchSettings("https://user@invalid.example.test"); err == nil {
		t.Fatal("invalid origin accepted")
	}
	wantSite(saved)
	if err := os.Remove(siteFile); err != nil {
		t.Fatal(err)
	}
	restarted := NewApp(app.appRoot)
	start(restarted)
	wantSite(saved)
	if err := restarted.ResetManagedSettings(); err != nil {
		t.Fatal(err)
	}
	wantSite("https://fallback.example.test")

	// An unwritable extension config must not commit a site change or block the rest of Ant.
	if err := os.Remove(siteFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(siteFile, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.SaveKeyPouchSettings(saved); err == nil || restarted.keyPouchSite() != "https://fallback.example.test" {
		t.Fatalf("unwritable extension allowed an inconsistent save: %v", err)
	}
	start(restarted)
	if !restarted.browserMgr.KeyPouchSiteUnavailable.Load() {
		t.Fatal("failed configuration did not suspend this extension")
	}
	if err := os.Remove(siteFile); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANT_KEYPOUCH_URL", "")
	if _, err := restarted.SaveKeyPouchSettings(""); err != nil {
		t.Fatal(err)
	}
	wantSite("")
	if restarted.browserMgr.KeyPouchSiteUnavailable.Load() {
		t.Fatal("repair did not resume loading the extension")
	}
}

func TestBackupOmitsLocalKeyPouchExtensionSite(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, keyPouchExtensionDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"site.json", ".site-pending", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if backupShouldSkipAppDataFile(filepath.Join(keyPouchExtensionDirectory, name)) != (name != "manifest.json") {
			t.Fatalf("unexpected backup import selection for %s", name)
		}
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	stats, err := backupZipAddDir(zw, root, "payload/app/data", "", "", nil)
	if closeErr := zw.Close(); err != nil || closeErr != nil || stats.fileCount != 1 {
		t.Fatalf("archive contains %d files: %v, %v", stats.fileCount, err, closeErr)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Open("payload/app/data/" + keyPouchExtensionDirectory + "/manifest.json"); err != nil {
		t.Fatal("extension assets must remain in app-data backups", err)
	}
}

func newKeyPouchSettingsTestApp(t *testing.T) *App {
	t.Helper()
	userDir := t.TempDir()
	t.Setenv("HOME", userDir)
	t.Setenv("XDG_CONFIG_HOME", userDir)
	t.Setenv("AppData", userDir)
	t.Setenv("ANT_KEYPOUCH_URL", " https://fallback.example.test/ ")
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	return app
}

func TestKeyPouchSettingsPersistForOnlyOneInstallation(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	const fallback = "https://fallback.example.test"
	const site = "http://127.0.0.1:8090"
	got, err := app.GetKeyPouchSettings()
	if err != nil || got["siteUrl"] != "" || got["fallbackUrl"] != fallback || app.keyPouchSite() != fallback {
		t.Fatalf("initial settings = %v, %v", got, err)
	}
	got, err = app.SaveKeyPouchSettings("  " + site + "/// ")
	if err != nil || got["siteUrl"] != site || got["fallbackUrl"] != fallback {
		t.Fatalf("saved settings = %v, %v", got, err)
	}
	if NewApp(app.appRoot).keyPouchSite() != site || NewApp(t.TempDir()).keyPouchSite() != fallback {
		t.Fatal("setting must persist after restart and be isolated by installation")
	}
	path, err := app.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("expected a private site file: %v, %v", info, err)
	}
	if _, err := app.SaveKeyPouchSettings("https://secret@example.test"); err == nil || app.keyPouchSite() != site {
		t.Fatal("invalid input changed the saved site")
	}
	got, err = app.SaveKeyPouchSettings("  ")
	if err != nil || got["siteUrl"] != "" || app.keyPouchSite() != fallback {
		t.Fatalf("cleared settings = %v, %v", got, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("override file after clearing: %v", err)
	}
	if _, err := app.SaveKeyPouchSettings(""); err != nil {
		t.Fatalf("repeated clear: %v", err)
	}
}

func TestKeyPouchSettingsHandleInvalidLocalAndFallbackSites(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	t.Setenv("ANT_KEYPOUCH_URL", "https://login:private@example.test/?token=private")
	got, err := app.GetKeyPouchSettings()
	if err != nil || got["fallbackUrl"] != "" || app.keyPouchSite() != "" {
		t.Fatalf("invalid fallback should be omitted: %v, %v", got, err)
	}
	if _, err := app.SaveKeyPouchSettings("https://override.example.test"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANT_KEYPOUCH_URL", "https://fallback.example.test")
	path, err := app.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("https://example.test/?token=private"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := app.GetKeyPouchSettings(); err == nil || got != nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("expected a sanitized error for corrupt local settings: %v, %v", got, err)
	}
	if site := app.keyPouchSite(); site != "" {
		t.Fatalf("broken explicit settings silently switched to %q", site)
	}
	if _, err := app.SaveKeyPouchSettings("https://repaired.example.test"); err != nil || app.keyPouchSite() != "https://repaired.example.test" {
		t.Fatalf("repairing an invalid setting: %v", err)
	}
}

func TestKeyPouchSettingsBrokenLinkDoesNotSelectFallback(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	path, err := app.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := app.GetKeyPouchSettings(); err == nil || got != nil || app.keyPouchSite() != "" {
		t.Fatalf("dangling setting must report its error and suspend the connection: %v, %v", got, err)
	}
	if _, err := app.SaveKeyPouchSettings(""); err != nil || app.keyPouchSite() != "https://fallback.example.test" {
		t.Fatalf("explicitly clearing a dangling site failed: %v", err)
	}
}

func TestResetKeyPouchSitePreservesPairedIdentityAndFallsBack(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	const site = "https://paired.example.test"
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	device, err := keypouch.OpenDevice(dir, app.appRoot, site, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveKeyPouchSettings(site); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetManagedSettings(); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetKeyPouchSettings()
	if err != nil || got["siteUrl"] != "" || app.keyPouchSite() != "https://fallback.example.test" {
		t.Fatalf("reset settings = %v, %v", got, err)
	}
	original, err := keypouch.OpenDevice(dir, app.appRoot, site, false)
	if err != nil || original.Fingerprint() != device.Fingerprint() {
		t.Fatalf("reset lost the existing device identity: %v", err)
	}
}

func TestResetKeyPouchSiteRollsBackOnLocalSettingsFailure(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	app.config.Automation.Enabled = true
	path, err := app.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveKeyPouchSettings(""); err == nil {
		t.Fatal("clearing an unreadable site must fail")
	}
	if err := app.ResetManagedSettings(); err == nil || !app.config.Automation.Enabled {
		t.Fatal("reset must report the filesystem failure and retain other settings")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	const paired = "https://paired.example.test"
	if _, err := app.SaveKeyPouchSettings(paired); err != nil {
		t.Fatal(err)
	}
	blocked := app.backupLocalConfigPath()
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "occupied"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetManagedSettings(); err == nil || app.keyPouchSite() != paired || !app.config.Automation.Enabled {
		t.Fatal("failure resetting local backup settings must retain the KeyPouch override")
	}
}
