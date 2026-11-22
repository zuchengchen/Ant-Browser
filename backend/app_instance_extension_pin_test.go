package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

func TestPrepareGPTToolbarPin(t *testing.T) {
	originalFinder := findBrowserUserDataProcesses
	t.Cleanup(func() { findBrowserUserDataProcesses = originalFinder })
	var running []browserUserDataProcess
	findBrowserUserDataProcesses = func(string) ([]browserUserDataProcess, error) { return running, nil }
	app := NewApp(t.TempDir())
	builtin := app.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	writePasswordTestPreferences(t, filepath.Join(builtin, "manifest.json"), `{"manifest_version":3}`)
	id, err := browser.UnpackedExtensionID(builtin)
	if err != nil {
		t.Fatal(err)
	}
	userData := t.TempDir()
	args := []string{"--profile-directory=Profile 3"}
	location := filepath.Join(userData, "Profile 3", "Preferences")
	other := t.TempDir()
	if err := app.prepareGPTToolbarPin(userData, args, []string{other}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(location); !os.IsNotExist(err) {
		t.Fatal("disabled or absent built-in changed the toolbar")
	}
	running = []browserUserDataProcess{{PID: 1234}}
	if err := app.prepareGPTToolbarPin(userData, args, []string{builtin}); err == nil {
		t.Fatal("unmanaged live Chromium was treated as stopped")
	}
	if _, err := os.Stat(location); !os.IsNotExist(err) {
		t.Fatal("live Chromium profile was modified")
	}
	running = nil
	apply := func() {
		t.Helper()
		if err := app.prepareGPTToolbarPin(userData, args, []string{other, builtin}); err != nil {
			t.Fatal(err)
		}
	}
	pins := func() []string {
		t.Helper()
		prefs := readPasswordTestPreferences(t, location)
		var extensions struct {
			Pinned []string `json:"pinned_extensions"`
		}
		if err := json.Unmarshal(prefs["extensions"], &extensions); err != nil {
			t.Fatal(err)
		}
		return extensions.Pinned
	}
	apply()
	if !slices.Equal(pins(), []string{id}) {
		t.Fatal("new Chromium profile did not pin the runtime ID")
	}
	writePasswordTestPreferences(t, location, `{"opaque":9007199254740993,"extensions":{"other":"keep","pinned_extensions":["second","first"]}}`)
	apply()
	if !slices.Equal(pins(), []string{"second", "first", id}) {
		t.Fatal("existing toolbar order was lost")
	}
	prefs := readPasswordTestPreferences(t, location)
	var extensions map[string]json.RawMessage
	if json.Unmarshal(prefs["extensions"], &extensions) != nil || string(extensions["other"]) != `"keep"` || string(prefs["opaque"]) != "9007199254740993" {
		t.Fatal("unrelated preferences changed")
	}
	// Simulate Chromium saving an unpin after Ant initialized the default.
	extensions["pinned_extensions"] = json.RawMessage(`["first","second"]`)
	prefs["extensions"], _ = json.Marshal(extensions)
	manual, _ := json.Marshal(prefs)
	writePasswordTestPreferences(t, location, string(manual))
	apply()
	actual, err := os.ReadFile(location)
	if err != nil || string(actual) != string(manual) {
		t.Fatal("restarting overwrote the manual selection")
	}
	const oldID = "abcdefghijklmnopabcdefghijklmnop"
	for _, tt := range []struct {
		old  []string
		want []string
	}{
		{[]string{"a", oldID, "b"}, []string{"a", id, "b"}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{[]string{oldID, "a", id}, []string{"a", id}},
	} {
		old, _ := json.Marshal(tt.old)
		writePasswordTestPreferences(t, location, fmt.Sprintf(`{"ant_gpt_toolbar_pin_default":%q,"extensions":{"pinned_extensions":%s}}`, oldID, old))
		apply()
		if !slices.Equal(pins(), tt.want) {
			t.Fatal("moving the installation changed toolbar order or unpin choice")
		}
	}
	for _, invalid := range []string{`{"extensions":null}`, `{"extensions":{"pinned_extensions":{}}}`, `{"broken"`} {
		writePasswordTestPreferences(t, location, invalid)
		if err := app.prepareGPTToolbarPin(userData, args, []string{builtin}); err == nil {
			t.Fatal("malformed existing preferences accepted")
		}
		actual, err := os.ReadFile(location)
		if err != nil || string(actual) != invalid {
			t.Fatal("malformed preferences were replaced")
		}
	}
	if _, err := os.Stat(filepath.Join(userData, "Default")); !os.IsNotExist(err) {
		t.Fatal("a different Chrome profile was changed")
	}
}

func TestGPTToolbarLastUsedChromeProfile(t *testing.T) {
	root := t.TempDir()
	writePasswordTestPreferences(t, filepath.Join(root, "Local State"), `{"profile":{"last_used":"Profile 2"}}`)
	if path, err := browserToolbarProfileDir(root, nil); err != nil || path != filepath.Join(root, "Profile 2") {
		t.Fatalf("last-used profile = %q, %v", path, err)
	}
	if _, err := browserToolbarProfileDir(root, []string{"--profile-directory=../outside"}); err == nil {
		t.Fatal("unsafe profile directory accepted")
	}
}

func TestGPTToolbarSurvivesChromiumFirstRun(t *testing.T) {
	app := NewApp(t.TempDir())
	builtin := app.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	writePasswordTestPreferences(t, filepath.Join(builtin, "manifest.json"), `{"manifest_version":3}`)
	for _, tc := range []struct {
		name      string
		args      []string
		selected  bool
		protected bool
		forced    bool
	}{
		{name: "new Key template", selected: true, protected: true},
		{name: "no first run flag removed on later launches", args: []string{"--no-first-run"}, selected: true, protected: true},
		{name: "explicit forced first run", args: []string{"--force-first-run"}, selected: true, forced: true},
		{name: "extension disabled"},
		{name: "different Chrome subprofile", args: []string{"--profile-directory=Profile 2"}, selected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			if tc.selected {
				paths = []string{builtin}
			}
			err := app.prepareGPTToolbarPin(dir, tc.args, paths)
			if (err != nil) != tc.forced {
				t.Fatalf("prepare: %v", err)
			}
			sentinel := filepath.Join(dir, "First Run")
			data, err := os.ReadFile(sentinel)
			if !tc.protected {
				if !os.IsNotExist(err) {
					t.Fatal("first-run behavior changed for another profile or explicit mode")
				}
				return
			}
			if err != nil || len(data) != 0 {
				t.Fatal("Chromium first-run import can replace the prepared default preferences")
			}
			prefs := filepath.Join(dir, "Default", "Preferences")
			id, err := browser.UnpackedExtensionID(builtin)
			if err != nil {
				t.Fatal(err)
			}
			manual := fmt.Sprintf(`{"ant_gpt_toolbar_pin_default":%q,"other":"keep","extensions":{"pinned_extensions":["another"]}}`, id)
			writePasswordTestPreferences(t, prefs, manual)
			// Cover profiles created in earlier versions under --no-first-run:
			// a subsequent launch must protect the stored manual unpin even if
			// the marker is already current and Chromium never wrote a sentinel.
			if err := os.Remove(sentinel); err != nil {
				t.Fatal(err)
			}
			if err := app.prepareGPTToolbarPin(dir, nil, paths); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(sentinel); err != nil {
				t.Fatal("missing first-run protection on the next launch")
			}
			if got, err := os.ReadFile(prefs); err != nil || string(got) != manual {
				t.Fatal("second launch changed saved choices")
			}
			if err := os.WriteFile(sentinel, []byte("keep existing"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := app.prepareGPTToolbarPin(dir, nil, paths); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep existing" {
				t.Fatal("existing Chromium sentinel overwritten")
			}
		})
	}
}

func TestBrowserStartPlanPinsBundledGPTWhenEnabled(t *testing.T) {
	app, db := startPersistenceTestApp(t)
	core := filepath.Join(app.appRoot, "core")
	if err := os.Mkdir(core, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "chrome.exe"), nil, 0755); err != nil {
		t.Fatal(err)
	}
	app.config.Browser.Cores = []config.BrowserCore{{CoreId: "test", CorePath: core, IsDefault: true}}
	app.browserMgr.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
	if err := app.browserMgr.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	originalFinder := findBrowserUserDataProcesses
	findBrowserUserDataProcesses = func(string) ([]browserUserDataProcess, error) { return nil, nil }
	t.Cleanup(func() { findBrowserUserDataProcesses = originalFinder })
	for _, id := range []string{"target", "untouched"} {
		if id == "untouched" {
			if _, err := app.browserMgr.ExtensionDAO.SetProfileSettings(id, nil, true); err != nil {
				t.Fatal(err)
			}
		}
		p := app.browserMgr.Profiles[id]
		p.CoreId = "test"
		if id == "untouched" {
			p.LaunchArgs = []string{"--lang=de-DE"}
		}
		plan, err := app.prepareBrowserStartPlan(browserStartInput{ProfileID: id, ForceDirectProxy: true}, p)
		if err != nil {
			t.Fatal(err)
		}
		if plan.extensionWarning != "" {
			t.Fatal(plan.extensionWarning)
		}
		wantLang := "--lang=en-US" // configured default for a freshly created profile
		if id == "untouched" {
			wantLang = "--lang=de-DE"
		}
		if !slices.Contains(plan.args, wantLang) {
			t.Fatal("first launch omitted the configured language flag", wantLang)
		}
		prefs := readPasswordTestPreferences(t, filepath.Join(plan.userDataDir, "Default", "Preferences"))
		_, initialized := prefs["ant_gpt_toolbar_pin_default"]
		if initialized != (id == "target") {
			t.Fatal("start plan did not honor the saved extension selection")
		}
		if _, err := os.Stat(filepath.Join(plan.userDataDir, "First Run")); (err == nil) != initialized {
			t.Fatal("start plan did not protect the selected extension from Chromium's first-run importer")
		}
	}
}
