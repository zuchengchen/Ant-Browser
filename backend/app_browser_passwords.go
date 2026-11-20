package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ensureBrowserPasswordManagerDefaults runs before a new browser process starts.
// Copies have their own user-data directory, so they receive the same defaults.
// Explicit choices, including false, remain authoritative on subsequent starts.
func ensureBrowserPasswordManagerDefaults(userDataDir string) error {
	profileDir := filepath.Join(userDataDir, "Default")
	prefsPath := filepath.Join(profileDir, "Preferences")
	prefs := map[string]json.RawMessage{}
	mode := os.FileMode(0o600)
	data, err := os.ReadFile(prefsPath)
	if err == nil {
		if err := json.Unmarshal(data, &prefs); err != nil {
			return fmt.Errorf("解析浏览器偏好失败: %w", err)
		}
		if prefs == nil {
			return fmt.Errorf("浏览器偏好必须是 JSON 对象")
		}
		info, err := os.Stat(prefsPath)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}

	// These are the two toggle pref keys in Chromium's password manager settings.
	changed := false
	for _, key := range []string{"credentials_enable_service", "credentials_enable_autosignin"} {
		if _, exists := prefs[key]; !exists {
			prefs[key] = json.RawMessage("true")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(profileDir, ".password-preferences-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(prefs); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), prefsPath)
}
