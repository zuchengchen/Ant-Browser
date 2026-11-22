package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

const keyPouchExtensionDirectory = "extensions/" + browser.KeyPouchGPTExtensionID

// Store the site alongside the native identities, outside the backup payload.
// Each Ant installation has its own preference for the current OS user.
func (a *App) keyPouchSettingsPath() (string, error) {
	if a == nil {
		return "", errors.New("应用未初始化")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("无法定位 KeyPouch 本机设置目录：%w", err)
	}
	installation := sha256.Sum256([]byte(a.appRoot))
	name := "site-" + hex.EncodeToString(installation[:16]) + ".url"
	return filepath.Join(dir, "Ant Browser", "KeyPouch", name), nil
}

func readKeyPouchSite(path string) (string, error) {
	data, err := readKeyPouchSiteFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取 KeyPouch 本机设置失败：%w", err)
	}
	return keypouch.NormalizeOrigin(string(data))
}

func readKeyPouchSiteFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		// An explicitly configured but broken link is different from an absent preference.
		if _, linkErr := os.Lstat(path); linkErr != nil {
			return nil, linkErr
		}
		return nil, errors.New("KeyPouch 本机设置文件的链接已失效")
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("KeyPouch 本机设置路径必须指向常规文件")
	}
	return os.ReadFile(path)
}

func validKeyPouchFallback() string {
	site := keyPouchFallbackSite()
	if _, err := keypouch.NormalizeOrigin(site); err != nil {
		return ""
	}
	// Existing pairings use the original trimmed spelling as their identity key.
	return site
}

func effectiveKeyPouchSite(saved, fallback string) string {
	if saved == "" {
		return fallback
	}
	if normalized, err := keypouch.NormalizeOrigin(fallback); err == nil && normalized == saved {
		return fallback
	}
	return saved
}

func (a *App) keyPouchSite() string {
	path, err := a.keyPouchSettingsPath()
	if err != nil {
		return ""
	}
	saved, err := readKeyPouchSite(path)
	if err != nil {
		// A broken explicit preference must not switch an existing connection to a different site.
		return ""
	}
	return effectiveKeyPouchSite(saved, validKeyPouchFallback())
}

func (a *App) GetKeyPouchSettings() (map[string]string, error) {
	path, err := a.keyPouchSettingsPath()
	if err != nil {
		return nil, err
	}
	a.keyPouchActionMu.Lock()
	defer a.keyPouchActionMu.Unlock()
	saved, err := readKeyPouchSite(path)
	if err != nil {
		return nil, err
	}
	return map[string]string{"siteUrl": saved, "fallbackUrl": validKeyPouchFallback()}, nil
}

func (a *App) SaveKeyPouchSettings(siteURL string) (map[string]string, error) {
	path, err := a.keyPouchSettingsPath()
	if err != nil {
		return nil, err
	}
	site := strings.TrimSpace(siteURL)
	if site != "" {
		site, err = keypouch.NormalizeOrigin(site)
		if err != nil {
			return nil, err
		}
	}

	a.keyPouchActionMu.Lock()
	defer a.keyPouchActionMu.Unlock()
	fallback := validKeyPouchFallback()
	err = a.updateKeyPouchSiteLocked(effectiveKeyPouchSite(site, fallback), func() error {
		if site != "" {
			return writeKeyPouchSiteFile(path, []byte(site+"\n"))
		}
		info, err := os.Lstat(path)
		if err == nil {
			if info.IsDir() {
				return errors.New("KeyPouch 本机设置路径是目录")
			}
			err = os.Remove(path)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("保存 KeyPouch 本机设置失败：%w", err)
	}
	return map[string]string{"siteUrl": site, "fallbackUrl": fallback}, nil
}

// The extension and the signed device connection must follow the same local preference.
// Publish a complete JSON file before committing the preference. On a failed save or
// reset, republish the site that actually survived the operation.
func (a *App) updateKeyPouchSiteLocked(next string, save func() error) error {
	before := a.keyPouchSite()
	if err := a.syncKeyPouchExtensionSite(next); err != nil {
		return fmt.Errorf("同步「登录 GPT」插件的站点失败：%w", err)
	}
	err := save()
	if err != nil {
		next = a.keyPouchSite()
		err = errors.Join(err, a.syncKeyPouchExtensionSite(next))
	}
	if next != before {
		a.reloadKeyPouchSiteLocked()
	}
	return err
}

func (a *App) syncKeyPouchExtensionSite(site string) (err error) {
	if a.browserMgr != nil {
		defer func() { a.browserMgr.KeyPouchSiteUnavailable.Store(err != nil) }()
	}
	dir := a.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // The built-in can be absent when it has been replaced by the user.
		}
		return err
	}
	data, err := json.Marshal(map[string]string{"site": site})
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "site.json")
	data = append(data, '\n')
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() == int64(len(data)) {
		if previous, err := os.ReadFile(path); err == nil && bytes.Equal(previous, data) {
			return nil
		}
	}
	return replaceKeyPouchSiteFile(path, data)
}

// The canonical installation-specific preference lives outside the app backup.
// Its generated extension copy, including an in-progress atomic write, stays local too.
func isKeyPouchExtensionSiteFile(dataRelativePath string) bool {
	name, ok := strings.CutPrefix(strings.ToLower(filepath.ToSlash(dataRelativePath)), keyPouchExtensionDirectory+"/")
	return ok && (name == "site.json" || strings.HasPrefix(name, ".site-"))
}

func writeKeyPouchSiteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return replaceKeyPouchSiteFile(path, data)
}

func replaceKeyPouchSiteFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".site-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
