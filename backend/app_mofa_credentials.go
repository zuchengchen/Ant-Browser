package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ant-chrome/backend/internal/keypouch"
)

type mofaImportSettings struct {
	Site       string `json:"site"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	TOTPSecret string `json:"totp_secret,omitempty"`
}

func (s mofaImportSettings) publicSettings() map[string]string {
	return map[string]string{
		"siteUrl":            s.Site,
		"username":           s.Username,
		"passwordConfigured": strconv.FormatBool(s.Password != ""),
		"totpConfigured":     strconv.FormatBool(s.TOTPSecret != ""),
	}
}

func (a *App) GetMofaSettings() (map[string]string, error) {
	settings, err := a.readMofaImportSettings()
	if err != nil {
		return nil, err
	}
	return settings.publicSettings(), nil
}

func (a *App) SaveMofaSettings(input map[string]string) (map[string]string, error) {
	site, err := normalizeMofaSite(input["siteUrl"])
	if err != nil {
		return nil, err
	}
	next := mofaImportSettings{Site: site, Username: strings.TrimSpace(input["username"])}
	clearCredentials := input["clearCredentials"] == "true"
	if !clearCredentials {
		next.Password = input["password"]
		next.TOTPSecret = strings.TrimSpace(input["totpSecret"])
	}
	if site == "" || next.Username == "" {
		if next.Password != "" || next.TOTPSecret != "" {
			return nil, errors.New("请先填写 MOFA 站点和管理员账号")
		}
		next.Username = ""
	}

	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	path, err := a.mofaSettingsPath()
	if err != nil {
		return nil, err
	}
	before, _ := readMofaSite(path)
	blob, err := readMofaCredentialFile(path)
	if err != nil {
		return nil, err
	}
	if !clearCredentials && next.Username != "" && before == site && (next.Password == "" || next.TOTPSecret == "") {
		previous, err := openMofaCredentials(path, before, blob)
		if err != nil {
			return nil, err
		}
		if previous.Username == next.Username {
			if next.Password == "" {
				next.Password = previous.Password
			}
			if next.TOTPSecret == "" {
				next.TOTPSecret = previous.TOTPSecret
			}
		}
	}
	if err := a.saveMofaImportSettingsLocked(path, blob, next); err != nil {
		return nil, err
	}
	return next.publicSettings(), nil
}

func (a *App) readMofaImportSettings() (mofaImportSettings, error) {
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	path, err := a.mofaSettingsPath()
	if err != nil {
		return mofaImportSettings{}, err
	}
	site, err := readMofaSite(path)
	if err != nil || site == "" {
		return mofaImportSettings{}, err
	}
	blob, err := readMofaCredentialFile(path)
	if err != nil {
		return mofaImportSettings{}, err
	}
	return openMofaCredentials(path, site, blob)
}

func mofaCredentialPath(sitePath string) string {
	installation := strings.TrimSuffix(filepath.Base(sitePath), ".url")
	return filepath.Join(filepath.Dir(sitePath), installation, "admin.cred")
}

func mofaCredentialBinding(sitePath string) string {
	return "ant-mofa-admin-v1\x00" + filepath.Base(sitePath)
}

func readMofaCredentialFile(sitePath string) ([]byte, error) {
	blob, err := keypouch.ReadProtectedCredentialFile(mofaCredentialPath(sitePath))
	if err != nil {
		return nil, errors.New("读取 MOFA 本机凭据失败")
	}
	return blob, nil
}

func openMofaCredentials(sitePath, site string, blob []byte) (mofaImportSettings, error) {
	if blob == nil {
		return mofaImportSettings{Site: site}, nil
	}
	plain, err := keypouch.OpenProtectedCredential(mofaCredentialPath(sitePath), mofaCredentialBinding(sitePath), blob)
	if err != nil {
		return mofaImportSettings{}, errors.New("无法解密 MOFA 本机凭据，请重新配置")
	}
	defer clear(plain)
	var settings mofaImportSettings
	if err := json.Unmarshal(plain, &settings); err != nil {
		return mofaImportSettings{}, errors.New("MOFA 本机凭据格式错误，请重新配置")
	}
	// A preference change can be interrupted between writes. Only credentials
	// whose recorded origin matches the current preference can be returned.
	if settings.Site != site || settings.Username == "" {
		return mofaImportSettings{Site: site}, nil
	}
	return settings, nil
}

func writeMofaCredentials(sitePath string, previous []byte, settings mofaImportSettings) error {
	path := mofaCredentialPath(sitePath)
	if settings.Username == "" {
		if previous != nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.New("清除 MOFA 本机凭据失败")
			}
		}
		return nil
	}
	plain, err := json.Marshal(settings)
	if err != nil {
		return errors.New("保存 MOFA 本机凭据失败")
	}
	defer clear(plain)
	if err := keypouch.WriteProtectedCredential(path, mofaCredentialBinding(sitePath), plain, previous != nil); err != nil {
		return errors.New("保存 MOFA 本机凭据失败")
	}
	return nil
}

func restoreMofaCredentials(sitePath string, previous []byte) error {
	path := mofaCredentialPath(sitePath)
	if previous == nil {
		if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
	} else if err := writeLocalSiteFile(path, previous); err == nil {
		return nil
	}
	return errors.New("恢复 MOFA 本机凭据失败")
}

func (a *App) saveMofaImportSettingsLocked(path string, previous []byte, settings mofaImportSettings) error {
	// Commit credentials before changing the canonical site so an interrupted
	// change cannot expose an old record when switching back to its origin.
	if err := writeMofaCredentials(path, previous, settings); err != nil {
		return err
	}
	if err := a.saveMofaSiteLocked(path, settings.Site); err != nil {
		return errors.Join(err, restoreMofaCredentials(path, previous))
	}
	return nil
}
