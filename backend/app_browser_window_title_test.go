package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstanceTitleBarPreservesUnrelatedPreferences(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux system title bar")
	}
	for _, initial := range []string{"", `{"browser":{"custom_chrome_frame":true,"show_home_button":true},"large_id":9007199254740993,"credentials_enable_service":false}`} {
		t.Run(initial, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Default", "Preferences")
			if initial != "" {
				writePasswordTestPreferences(t, path, initial)
			}
			if err := ensureBrowserInstanceTitleBar(dir); err != nil {
				t.Fatal(err)
			}
			prefs := readPasswordTestPreferences(t, path)
			var browser map[string]json.RawMessage
			if err := json.Unmarshal(prefs["browser"], &browser); err != nil {
				t.Fatal(err)
			}
			if string(browser["custom_chrome_frame"]) != "false" {
				t.Fatal("system title bar was not enabled")
			}
			if initial != "" && (string(browser["show_home_button"]) != "true" || string(prefs["large_id"]) != "9007199254740993" || string(prefs["credentials_enable_service"]) != "false") {
				t.Fatal("unrelated preferences changed")
			}
			before, _ := os.ReadFile(path)
			if err := ensureBrowserInstanceTitleBar(dir); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("unchanged preferences were rewritten")
			}
		})
	}
}

func TestInstanceTitleBarDoesNotReplaceInvalidPreferences(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux system title bar")
	}
	for _, invalid := range []string{`{broken`, `null`, `{"browser":null}`, `{"browser":[]}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "Default", "Preferences")
		writePasswordTestPreferences(t, path, invalid)
		if err := ensureBrowserInstanceTitleBar(dir); err == nil {
			t.Fatalf("accepted invalid preferences: %s", invalid)
		}
		actual, _ := os.ReadFile(path)
		if string(actual) != invalid {
			t.Fatal("invalid existing preferences were overwritten")
		}
	}
}
