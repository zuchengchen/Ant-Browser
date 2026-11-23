package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ant-chrome/backend/internal/browser"
	"golang.org/x/net/idna"
)

const mofaExtensionDirectory = "extensions/" + browser.MofaImportExtensionID
const mofaSettingsDirectoryName = "MOFA"

var errMofaSite = errors.New("请输入 MOFA 的 HTTPS 站点地址或 /admin/accounts 地址，地址不能包含登录信息、查询参数或片段")

func normalizeMofaSite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") || strings.Contains(u.Host, "%") {
		return "", errMofaSite
	}
	switch u.EscapedPath() {
	case "", "/", "/admin/accounts", "/admin/accounts/":
	default:
		return "", errMofaSite
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return "", errMofaSite
		}
		if n == 443 {
			port = ""
		} else {
			port = strconv.FormatUint(n, 10)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", errMofaSite
	}
	host := u.Hostname()
	ip, ipErr := netip.ParseAddr(host)
	if strings.HasPrefix(u.Host, "[") {
		if ipErr != nil || !ip.Is6() {
			return "", errMofaSite
		}
		host = "[" + ip.String() + "]"
	} else if ipErr == nil {
		if !ip.Is4() {
			return "", errMofaSite
		}
		host = ip.String()
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || !validMofaDomain(host) {
			return "", errMofaSite
		}
	}
	if port != "" {
		host += ":" + port
	}
	return "https://" + strings.ToLower(host), nil
}

func validMofaDomain(ascii string) bool {
	domain := strings.TrimSuffix(ascii, ".")
	if domain == "" {
		return false
	}
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	// Browsers interpret domains ending in a number as IPv4. Require IPv4 to
	// have already parsed above so short, octal and hexadecimal forms stay out.
	last := strings.ToLower(labels[len(labels)-1])
	numeric := strings.Trim(last, "0123456789") == ""
	hex := strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == ""
	return !numeric && !hex && len(domain) <= 253
}

func mofaSettingsDirectory() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("无法定位 MOFA 本机设置目录")
	}
	return filepath.Join(dir, "Ant Browser", mofaSettingsDirectoryName), nil
}

func (a *App) mofaSettingsPath() (string, error) {
	dir, err := mofaSettingsDirectory()
	if err != nil {
		return "", err
	}
	installation := sha256.Sum256([]byte(a.appRoot))
	return filepath.Join(dir, "site-"+hex.EncodeToString(installation[:16])+".url"), nil
}

func readMofaSite(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("MOFA 本机设置文件不可用")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("读取 MOFA 本机设置失败")
	}
	return normalizeMofaSite(string(data))
}

func (a *App) GetMofaSite() (string, error) {
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	path, err := a.mofaSettingsPath()
	if err != nil {
		return "", err
	}
	return readMofaSite(path)
}

func (a *App) SaveMofaSite(input string) (string, error) {
	site, err := normalizeMofaSite(input)
	if err != nil {
		return "", err
	}
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	path, err := a.mofaSettingsPath()
	if err != nil {
		return "", err
	}
	before, _ := readMofaSite(path)
	if site != "" && site == before {
		err = a.saveMofaSiteLocked(path, site)
	} else {
		var blob []byte
		blob, err = readMofaCredentialFile(path)
		if err == nil {
			err = a.saveMofaImportSettingsLocked(path, blob, mofaImportSettings{Site: site})
		}
	}
	if err != nil {
		return "", err
	}
	return site, nil
}

func (a *App) saveMofaSiteLocked(path, site string) error {
	before, _ := readMofaSite(path)
	if err := a.syncMofaExtensionSite(site); err != nil {
		return fmt.Errorf("同步 MOFA 插件地址失败：%w", err)
	}
	if err := writeLocalSiteFile(path, []byte(site+"\n")); err != nil {
		return errors.Join(fmt.Errorf("保存 MOFA 本机设置失败：%w", err), a.syncMofaExtensionSite(before))
	}
	return nil
}

func (a *App) syncSavedMofaExtensionSite() error {
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	return a.syncSavedMofaExtensionSiteLocked()
}

func (a *App) syncSavedMofaExtensionSiteLocked() error {
	path, err := a.mofaSettingsPath()
	if err != nil {
		return errors.Join(err, a.syncMofaExtensionSite(""))
	}
	site, err := readMofaSite(path)
	return errors.Join(err, a.syncMofaExtensionSite(site))
}

func (a *App) syncMofaExtensionSite(site string) error {
	dir := a.resolveAppPath(filepath.Join("data", mofaExtensionDirectory))
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil // The user may have replaced the bundled extension.
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("MOFA 内置插件目录无效")
	}
	config := struct {
		Site       string `json:"site"`
		BridgePort int    `json:"bridgePort,omitempty"`
	}{Site: site}
	if a.mofaToolbarBridge != nil {
		config.BridgePort = a.mofaToolbarBridge.port
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "site.json")
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() == int64(len(data)) {
		if before, err := os.ReadFile(path); err == nil && bytes.Equal(before, data) {
			return nil
		}
	}
	return replaceLocalSiteFile(path, data)
}

// The canonical setting belongs to the installation. An archive must never
// provide the generated extension copy for a different installation.
func isMofaExtensionSiteFile(rel string) bool {
	name, ok := strings.CutPrefix(strings.ToLower(filepath.ToSlash(rel)), mofaExtensionDirectory+"/")
	return ok && (name == "site.json" || strings.HasPrefix(name, ".site-"))
}
