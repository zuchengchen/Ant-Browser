package keypouch

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// LocalGPTCredential is the complete snapshot for one explicitly selected record.
// Empty passwords and seeds are significant: a refresh must clear the old value.
type LocalGPTCredential struct {
	Site         string `json:"site"`
	RecordID     int64  `json:"record_id"`
	Email        string `json:"email"`
	Password     string `json:"password"`
	TOTPSecret   string `json:"totp_secret"`
	LoginBinding string `json:"login_binding,omitempty"`
}

// A clone or an imported copy may log in locally but cannot report for the original device.
func (c LocalGPTCredential) PortableCopy() LocalGPTCredential {
	c.LoginBinding = ""
	return c
}

var loginBindingPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}\.[A-Za-z0-9_-]{43}$`)

func (c LocalGPTCredential) Validate() error {
	if c.LoginBinding != "" && !loginBindingPattern.MatchString(c.LoginBinding) {
		return errors.New("GPT 登录回执绑定无效")
	}
	site, siteErr := NormalizeOrigin(c.Site)
	addr, emailErr := mail.ParseAddress(c.Email)
	if siteErr != nil || c.Site != site || c.RecordID <= 0 || emailErr != nil || addr.Address != c.Email ||
		len(c.Email) > 254 || strings.ContainsAny(c.Email, "\r\n ") {
		return errors.New("GPT 登录凭据的站点、记录或邮箱无效")
	}
	return nil
}

// Every installation gets its own vault outside the database and Chromium profiles.
// Only this process publishes changes to the installation; the single-instance
// lock already serializes desktop sessions. Reads can safely use an old or new file.
var localCredentialMu sync.Mutex

const LocalGPTVaultName = "gpt-credentials"

func LocalGPTVaultDirectory() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Ant Browser", LocalGPTVaultName), nil
}

func credentialPath(installation, profileID string) (string, error) {
	root, err := LocalGPTVaultDirectory()
	if err != nil {
		return "", err
	}
	if installation == "" || profileID == "" || strings.TrimSpace(profileID) != profileID {
		return "", errors.New("GPT 登录凭据缺少安装或实例标识")
	}
	installHash := sha256.Sum256([]byte(installation))
	profileHash := sha256.Sum256([]byte(profileID))
	return filepath.Join(root, hex.EncodeToString(installHash[:]), hex.EncodeToString(profileHash[:])+".cred"), nil
}

func privateCredentialDir(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("GPT 本机凭据目录必须仅对当前用户开放")
	}
	return nil
}

func readCredentialFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("GPT 本机凭据文件或权限无效")
	}
	return os.ReadFile(path)
}

func publishCredentialFile(path string, data []byte, replace bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".gpt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if replace {
		return os.Rename(f.Name(), path)
	}
	return os.Link(f.Name(), path)
}

func localCredentialCipher(dir string, create bool) (cipher.AEAD, error) {
	path := filepath.Join(dir, "key")
	blob, err := readCredentialFile(path)
	if create && errors.Is(err, os.ErrNotExist) {
		entries, scanErr := os.ReadDir(dir)
		if scanErr != nil {
			return nil, scanErr
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".cred") {
				return nil, errors.New("已有 GPT 凭据的本机密钥缺失")
			}
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		// Windows DPAPI binds the independent key to this OS user. Unix uses
		// owner-only files in an owner-only directory, as for device identities.
		if blob, err = protectDeviceSeed(key); err != nil {
			return nil, err
		}
		err = publishCredentialFile(path, blob, false)
		if errors.Is(err, os.ErrExist) {
			blob, err = readCredentialFile(path)
		}
	}
	if err != nil {
		return nil, err
	}
	key, err := unprotectDeviceSeed(blob)
	if err != nil || len(key) != 32 {
		return nil, errors.New("GPT 本机凭据密钥不可用")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

const localCredentialAAD = "ant-gpt-credential-v1\x00"

func readLocalCredential(path, profileID string) (*LocalGPTCredential, error) {
	dir := filepath.Dir(path)
	err := privateCredentialDir(dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	blob, err := readCredentialFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	box, err := localCredentialCipher(dir, false)
	if err != nil {
		return nil, err
	}
	n := box.NonceSize()
	if len(blob) < n+box.Overhead() {
		return nil, errors.New("GPT 本机凭据不完整")
	}
	plain, err := box.Open(nil, blob[:n], blob[n:], []byte(localCredentialAAD+profileID))
	if err != nil {
		return nil, errors.New("GPT 本机凭据无法解密或与实例不匹配")
	}
	var c LocalGPTCredential
	if err := json.Unmarshal(plain, &c); err != nil {
		return nil, errors.New("GPT 本机凭据格式错误")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func ReadLocalGPTCredential(installation, profileID string) (*LocalGPTCredential, error) {
	path, err := credentialPath(installation, profileID)
	if err != nil {
		return nil, err
	}
	return readLocalCredential(path, profileID)
}

func SaveLocalGPTCredential(installation, profileID string, c LocalGPTCredential) error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := credentialPath(installation, profileID)
	if err != nil {
		return err
	}
	localCredentialMu.Lock()
	defer localCredentialMu.Unlock()
	old, err := readLocalCredential(path, profileID)
	if err != nil {
		return err
	}
	if old != nil {
		if c.Site != old.Site || c.RecordID != old.RecordID {
			return errors.New("实例已保存其他 KeyPouch 记录的 GPT 凭据")
		}
		if c == *old {
			return nil
		}
	}
	dir := filepath.Dir(path)
	if err := privateCredentialDir(dir, true); err != nil {
		return err
	}
	box, err := localCredentialCipher(dir, true)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	nonce := make([]byte, box.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := box.Seal(nonce, nonce, plain, []byte(localCredentialAAD+profileID))
	return publishCredentialFile(path, sealed, old != nil)
}

func RemoveLocalGPTCredential(installation, profileID string) error {
	path, err := credentialPath(installation, profileID)
	if err != nil {
		return err
	}
	localCredentialMu.Lock()
	defer localCredentialMu.Unlock()
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("删除 GPT 本机凭据失败：%w", err)
	}
	return nil
}
