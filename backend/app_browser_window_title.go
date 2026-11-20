package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Chrome's custom Linux frame does not show the native window title. Use the
// system title bar so the instance marker remains visible above all tabs.
// Called only after existing browser processes have been detected/handled.
func ensureBrowserInstanceTitleBar(userDataDir string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	profileDir := filepath.Join(userDataDir, "Default")
	prefsPath := filepath.Join(profileDir, "Preferences")
	prefs := map[string]json.RawMessage{}
	mode := os.FileMode(0o600)
	data, err := os.ReadFile(prefsPath)
	if err == nil {
		if err := json.Unmarshal(data, &prefs); err != nil || prefs == nil {
			return fmt.Errorf("浏览器偏好必须是有效的 JSON 对象")
		}
		info, err := os.Stat(prefsPath)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	browserPrefs := map[string]json.RawMessage{}
	if raw, exists := prefs["browser"]; exists {
		if err := json.Unmarshal(raw, &browserPrefs); err != nil || browserPrefs == nil {
			return fmt.Errorf("browser 偏好必须是有效的 JSON 对象")
		}
	}
	if string(browserPrefs["custom_chrome_frame"]) == "false" {
		return nil
	}
	browserPrefs["custom_chrome_frame"] = json.RawMessage("false")
	prefs["browser"], err = json.Marshal(browserPrefs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(profileDir, ".window-title-preferences-*.tmp")
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
