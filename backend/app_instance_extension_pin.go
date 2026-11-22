package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ant-chrome/backend/internal/browser"
)

// Apply the initial toolbar choice once per Chromium profile and extension
// origin. Keeping the marker beside the list in the same Preferences write
// means a later manual unpin or reorder remains the user's choice.
func (a *App) prepareGPTToolbarPin(userDataDir string, args, extensionPaths []string) error {
	return a.prepareBuiltinToolbarPin(userDataDir, args, extensionPaths, keyPouchExtensionDirectory, "ant_gpt_toolbar_pin_default")
}

func (a *App) prepareMofaToolbarPin(userDataDir string, args, extensionPaths []string) error {
	return a.prepareBuiltinToolbarPin(userDataDir, args, extensionPaths, mofaExtensionDirectory, "ant_mofa_toolbar_pin_default")
}

func (a *App) prepareBuiltinToolbarPin(userDataDir string, args, extensionPaths []string, directory, marker string) error {
	builtin := a.resolveAppPath(filepath.Join("data", directory))
	for _, path := range extensionPaths {
		if !sameGPTDirectory(path, builtin) {
			continue
		}
		id, err := browser.UnpackedExtensionID(path)
		if err != nil {
			return err
		}
		profile, err := browserToolbarProfileDir(userDataDir, args)
		if err != nil {
			return err
		}
		return updateBrowserPreferences(profile, func(prefs map[string]json.RawMessage) (bool, error) {
			changed, err := setBuiltinToolbarPinDefault(prefs, id, marker)
			if err != nil {
				return false, err
			}
			createSentinel, err := needsGPTFirstRunSentinel(userDataDir, profile, args)
			if err != nil || (!changed && !createSentinel) {
				return false, err
			}
			// Recheck before a first write: some fixed-port processes have no
			// DevToolsActivePort file, or survived orphan cleanup.
			processes, err := findBrowserUserDataProcesses(userDataDir)
			if err != nil {
				return false, err
			}
			if len(processes) > 0 {
				return false, errors.New("实例仍在运行")
			}
			if createSentinel {
				if err := os.MkdirAll(userDataDir, 0700); err != nil {
					return false, err
				}
				file, err := os.OpenFile(filepath.Join(userDataDir, "First Run"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					err = file.Close()
				}
				if err != nil && !os.IsExist(err) {
					return false, err
				}
			}
			return changed, nil
		})
	}
	return nil
}

// Chromium's first-run importer replaces Default/Preferences wholesale, even
// when Ant already prepared it. An empty "First Run" is Chromium's own per-user
// data directory sentinel. --no-first-run does not write the sentinel itself;
// a later launch without that switch must preserve the prepared preferences.
func needsGPTFirstRunSentinel(userDataDir, profile string, args []string) (bool, error) {
	if !sameGPTDirectory(profile, filepath.Join(userDataDir, "Default")) {
		return false, nil
	}
	if browserArgWithKey(args, "--force-first-run") != "" {
		return false, errors.New("显式指定了 --force-first-run，Chromium 可以重置默认资料的工具栏设置")
	}
	_, err := os.Stat(filepath.Join(userDataDir, "First Run"))
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

func browserToolbarProfileDir(userDataDir string, args []string) (string, error) {
	name := processCommandLineFlagValue(args, "--profile-directory")
	if name == "" {
		var state struct {
			Profile struct {
				LastUsed string `json:"last_used"`
			} `json:"profile"`
		}
		data, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
		if err == nil {
			if err := json.Unmarshal(data, &state); err != nil {
				return "", err
			}
			name = state.Profile.LastUsed
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if name == "" {
		name = "Default"
	}
	if name == "." || !filepath.IsLocal(name) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("无效的 Chromium 实例子目录")
	}
	return filepath.Join(userDataDir, name), nil
}

func setBuiltinToolbarPinDefault(prefs map[string]json.RawMessage, id, marker string) (bool, error) {
	var previous string
	if value, ok := prefs[marker]; ok {
		if err := json.Unmarshal(value, &previous); err != nil {
			return false, err
		}
		if previous == id {
			return false, nil
		}
	}
	extensions := map[string]json.RawMessage{}
	if value, ok := prefs["extensions"]; ok {
		if err := json.Unmarshal(value, &extensions); err != nil || extensions == nil {
			return false, errors.New("Chromium 扩展偏好格式无效")
		}
	}
	pins := []string{}
	if value, ok := extensions["pinned_extensions"]; ok {
		if err := json.Unmarshal(value, &pins); err != nil || pins == nil {
			return false, errors.New("Chromium 工具栏固定列表格式无效")
		}
	}
	// A profile package can be opened from a different Ant installation.
	// Move this extension's existing pin in place, or keep its prior unpin.
	prefs[marker], _ = json.Marshal(id)
	oldIndex := slices.Index(pins, previous)
	switch {
	case previous != "" && oldIndex >= 0 && slices.Contains(pins, id):
		pins = slices.Delete(pins, oldIndex, oldIndex+1)
	case previous != "" && oldIndex >= 0:
		pins[oldIndex] = id
	case previous == "" && !slices.Contains(pins, id):
		pins = append(pins, id)
	default:
		return true, nil
	}
	extensions["pinned_extensions"], _ = json.Marshal(pins)
	prefs["extensions"], _ = json.Marshal(extensions)
	return true, nil
}
