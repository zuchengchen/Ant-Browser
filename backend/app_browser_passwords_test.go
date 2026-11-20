package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPasswordManagerDefaultsEnableBothWithoutChangingOtherPreferences(t *testing.T) {
	for _, initial := range []string{"", `{"profile":{"name":"kept"},"large_id":9007199254740993}`} {
		t.Run(initial, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Default", "Preferences")
			if initial != "" {
				writePasswordTestPreferences(t, path, initial)
			}
			if err := ensureBrowserPasswordManagerDefaults(root); err != nil {
				t.Fatal(err)
			}
			prefs := readPasswordTestPreferences(t, path)
			for _, key := range []string{"credentials_enable_service", "credentials_enable_autosignin"} {
				if string(prefs[key]) != "true" {
					t.Fatalf("%s = %s, want true", key, prefs[key])
				}
			}
			if initial != "" && (string(prefs["large_id"]) != "9007199254740993" || !strings.Contains(string(prefs["profile"]), `"kept"`)) {
				t.Fatalf("unrelated preferences changed: %s", prefs)
			}
		})
	}
}

func TestPasswordManagerDefaultsPreserveIndependentChoicesAndRestart(t *testing.T) {
	for _, key := range []string{"credentials_enable_service", "credentials_enable_autosignin"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Default", "Preferences")
			writePasswordTestPreferences(t, path, `{"`+key+`":false}`)
			if err := ensureBrowserPasswordManagerDefaults(root); err != nil {
				t.Fatal(err)
			}
			prefs := readPasswordTestPreferences(t, path)
			if string(prefs[key]) != "false" || len(prefs) != 2 {
				t.Fatalf("explicit choice changed or other default missing: %s", prefs)
			}
			// Chrome saves a user's later choice. Subsequent starts must not reset it.
			const disabled = `{"credentials_enable_service":false,"credentials_enable_autosignin":false}`
			writePasswordTestPreferences(t, path, disabled)
			if err := ensureBrowserPasswordManagerDefaults(root); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != disabled {
				t.Fatalf("later choice was rewritten: %s, %v", actual, err)
			}
		})
	}
}

func TestPasswordManagerDefaultsRejectUnreadableOrInvalidPreferences(t *testing.T) {
	for _, invalid := range []string{"{broken", "null", "[]", ""} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "Default", "Preferences")
			writePasswordTestPreferences(t, path, invalid)
			if err := ensureBrowserPasswordManagerDefaults(root); err == nil {
				t.Fatal("invalid preferences accepted")
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != invalid {
				t.Fatalf("invalid original was replaced: %s, %v", actual, err)
			}
		})
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Default", "Preferences"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureBrowserPasswordManagerDefaults(root); err == nil {
		t.Fatal("unreadable preferences path accepted")
	}
}

func TestNewAndCopiedProfilesPreparePasswordManagerDefaults(t *testing.T) {
	root := t.TempDir()
	coreDir := filepath.Join(root, "core")
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "chrome.exe"), []byte("test browser, never executed"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "user-data")
	cfg.Browser.Cores = []config.BrowserCore{{CoreId: "test-core", CorePath: coreDir, IsDefault: true}}
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	oldFind := findBrowserUserDataProcesses
	findBrowserUserDataProcesses = func(string) ([]browserUserDataProcess, error) { return nil, nil }
	t.Cleanup(func() { findBrowserUserDataProcesses = oldFind })
	source, err := app.browserMgr.Create(browser.ProfileInput{ProfileName: "Source", ProxyId: "__direct__"})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(profile *browser.Profile) string {
		t.Helper()
		_, _, _, _, dir, err := app.prepareBrowserLaunchContext(browserStartInput{ProfileID: profile.ProfileId}, profile, nil)
		if err != nil {
			t.Fatal(err)
		}
		prefs := readPasswordTestPreferences(t, filepath.Join(dir, "Default", "Preferences"))
		if string(prefs["credentials_enable_service"]) != "true" || string(prefs["credentials_enable_autosignin"]) != "true" {
			t.Fatalf("prepared profile does not enable both toggles: %s", prefs)
		}
		return dir
	}
	sourceDir := prepare(source)
	const disabled = `{"credentials_enable_service":false,"credentials_enable_autosignin":false}`
	writePasswordTestPreferences(t, filepath.Join(sourceDir, "Default", "Preferences"), disabled)
	for _, mode := range []string{"legacy", "regular", "auto_fingerprint"} {
		var copied *browser.Profile
		if mode == "legacy" {
			copied, err = app.browserMgr.Copy(source.ProfileId, mode)
		} else {
			copied, err = app.browserMgr.CopyWithMode(source.ProfileId, mode, mode)
		}
		if err != nil {
			t.Fatal(err)
		}
		if dir := prepare(copied); dir == sourceDir {
			t.Fatal("copy shares source data directory")
		}
	}
	actual, err := os.ReadFile(filepath.Join(sourceDir, "Default", "Preferences"))
	if err != nil || string(actual) != disabled {
		t.Fatalf("copy changed source preference choices: %s, %v", actual, err)
	}
}

func writePasswordTestPreferences(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readPasswordTestPreferences(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefs map[string]json.RawMessage
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatal(err)
	}
	return prefs
}
