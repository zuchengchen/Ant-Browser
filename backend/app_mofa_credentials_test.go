package backend

import (
	"archive/zip"
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMofaAdminSettingsPersistAndMaskSecrets(t *testing.T) {
	a := newKeyPouchSettingsTestApp(t)
	const site = "https://mofa.example.test"
	const username = "synthetic-admin"
	const password = " synthetic password with spaces "
	const totp = "JBSWY3DPEHPK3PXP"
	extensionSite := a.resolveAppPath(filepath.Join("data", mofaExtensionDirectory, "site.json"))
	if err := os.MkdirAll(filepath.Dir(extensionSite), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSite(site + "/admin/accounts"); err != nil {
		t.Fatal(err)
	}
	initial, err := a.GetMofaSettings()
	assertMofaPublicSettings(t, initial, err, site, "", "false", "false")
	result, err := a.SaveMofaSettings(map[string]string{
		"siteUrl":    "https://MOFA.example.test:443/admin/accounts/",
		"username":   " " + username + " ",
		"password":   password,
		"totpSecret": " " + totp + " ",
	})
	assertMofaPublicSettings(t, result, err, site, username, "true", "true")
	restarted := NewApp(a.appRoot)
	result, err = restarted.GetMofaSettings()
	assertMofaPublicSettings(t, result, err, site, username, "true", "true")
	result, err = NewApp(t.TempDir()).GetMofaSettings()
	assertMofaPublicSettings(t, result, err, "", "", "false", "false")
	assertMofaPrivateSettings(t, restarted, mofaImportSettings{Site: site, Username: username, Password: password, TOTPSecret: totp})
	assertMofaExtensionSite(t, extensionSite, site)

	sitePath, err := a.mofaSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	path := mofaCredentialPath(sitePath)
	for _, file := range []string{path, filepath.Join(filepath.Dir(path), "key")} {
		blob, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{username, password, totp} {
			if bytes.Contains(blob, []byte(private)) {
				t.Fatal("MOFA 本机凭据文件包含明文")
			}
		}
		if runtime.GOOS != "windows" {
			stat, err := os.Stat(file)
			if err != nil || stat.Mode().Perm() != 0600 {
				t.Fatal("MOFA 本机凭据文件的权限错误", err)
			}
		}
	}
	if runtime.GOOS != "windows" {
		stat, err := os.Stat(filepath.Dir(path))
		if err != nil || stat.Mode().Perm() != 0700 {
			t.Fatal("MOFA 本机凭据目录的权限错误", err)
		}
	}
}

func TestMofaAdminSettingsPreserveByOriginAndUsernameAndClear(t *testing.T) {
	a := newKeyPouchSettingsTestApp(t)
	const firstSite = "https://first.example.test"
	const secondSite = "https://second.example.test:9443"
	const username = "synthetic-admin"
	const password = "synthetic password"
	const totp = "JBSWY3DPEHPK3PXP"
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": firstSite, "username": username, "password": password, "totpSecret": totp}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSite("https://FIRST.example.test:443/admin/accounts"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": "https://FIRST.example.test:443/admin/accounts", "username": username}); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: firstSite, Username: username, Password: password, TOTPSecret: totp})
	const replacement = "replacement password"
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": firstSite, "username": username, "password": replacement, "totpSecret": ""}); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: firstSite, Username: username, Password: replacement, TOTPSecret: totp})
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": firstSite, "username": "another-admin"}); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: firstSite, Username: "another-admin"})
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": firstSite, "username": username, "password": password, "totpSecret": totp}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": secondSite, "username": username, "totpSecret": totp}); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: secondSite, Username: username, TOTPSecret: totp})
	result, err := a.SaveMofaSettings(map[string]string{
		"siteUrl": secondSite, "username": username, "password": password, "totpSecret": totp, "clearCredentials": "true",
	})
	assertMofaPublicSettings(t, result, err, secondSite, username, "false", "false")
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: secondSite, Username: username})
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": secondSite, "username": username, "password": password}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSite(firstSite); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: firstSite})
	if _, err := a.SaveMofaSite(secondSite); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{Site: secondSite})
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": secondSite, "password": password}); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("缺少管理员账号时应返回脱敏错误")
	}
}

func TestMofaAdminSettingsRollbackAndReset(t *testing.T) {
	a := newKeyPouchSettingsTestApp(t)
	const site = "https://mofa.example.test"
	const nextSite = "https://next.example.test"
	const password = "synthetic password"
	settings := mofaImportSettings{Site: site, Username: "administrator", Password: password, TOTPSecret: "JBSWY3DPEHPK3PXP"}
	extensionSite := a.resolveAppPath(filepath.Join("data", mofaExtensionDirectory, "site.json"))
	if err := os.MkdirAll(filepath.Dir(extensionSite), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSettings(map[string]string{
		"siteUrl": site, "username": settings.Username, "password": password, "totpSecret": settings.TOTPSecret,
	}); err != nil {
		t.Fatal(err)
	}
	path, err := a.mofaSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	credential := mofaCredentialPath(path)
	key := filepath.Join(filepath.Dir(credential), "key")
	keyBefore, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": nextSite, "username": settings.Username, "password": password}); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("缺少密钥时应返回脱敏错误")
	}
	assertMofaExtensionSite(t, extensionSite, site)
	if err := os.WriteFile(key, keyBefore, 0600); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, settings)
	if err := os.Remove(extensionSite); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(extensionSite, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveMofaSettings(map[string]string{"siteUrl": nextSite, "username": settings.Username, "password": "changed password"}); err == nil {
		t.Fatal("扩展站点文件不可写时应报告保存失败")
	}
	assertMofaPrivateSettings(t, a, settings)
	if err := os.Remove(extensionSite); err != nil {
		t.Fatal(err)
	}
	if err := a.syncSavedMofaExtensionSite(); err != nil {
		t.Fatal(err)
	}
	assertMofaExtensionSite(t, extensionSite, site)

	keyPouch, err := a.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyPouch, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetManagedSettings(); err == nil {
		t.Fatal("KeyPouch 设置无效时应报告重置失败")
	}
	assertMofaPrivateSettings(t, a, settings)
	assertMofaExtensionSite(t, extensionSite, site)
	if err := os.Remove(keyPouch); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetManagedSettings(); err != nil {
		t.Fatal(err)
	}
	assertMofaPrivateSettings(t, a, mofaImportSettings{})
	assertMofaExtensionSite(t, extensionSite, "")
	if _, err := os.Stat(credential); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("重置后保留了 MOFA 本机凭据")
	}
}

func TestMofaAdminSettingsExcludedFromArchives(t *testing.T) {
	a := newKeyPouchSettingsTestApp(t)
	if _, err := a.SaveMofaSettings(map[string]string{
		"siteUrl": "https://mofa.example.test", "username": "synthetic admin", "password": "synthetic password",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := a.mofaSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	configRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "ordinary.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	credential := mofaCredentialPath(path)
	link := filepath.Join(configRoot, "local-credential-link")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(credential, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, write := range []func(*zip.Writer) error{
		func(w *zip.Writer) error {
			_, err := backupZipAddDir(w, configRoot, "backup", "", "", nil)
			return err
		},
		func(w *zip.Writer) error {
			_, err := writeProfilePackageDir(w, configRoot, "profile")
			return err
		},
	} {
		var out bytes.Buffer
		w := zip.NewWriter(&out)
		if err := write(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var ordinary bool
		for _, file := range r.File {
			if strings.Contains(file.Name, "/MOFA/") || strings.HasSuffix(file.Name, "/local-credential-link") {
				t.Fatalf("zip 中包含本机 MOFA 设置：%s", file.Name)
			}
			ordinary = ordinary || strings.HasSuffix(file.Name, "/ordinary.json")
		}
		if !ordinary {
			t.Fatal("zip 缺少普通配置文件")
		}
	}
	w := zip.NewWriter(&bytes.Buffer{})
	defer w.Close()
	if err := writeProfilePackageFile(w, credential, "credential.txt"); err == nil {
		t.Fatal("实例包允许将本机凭据作为附件")
	}
}

func assertMofaPublicSettings(t *testing.T, got map[string]string, err error, site, username, passwordConfigured, totpConfigured string) {
	t.Helper()
	want := map[string]string{"siteUrl": site, "username": username, "passwordConfigured": passwordConfigured, "totpConfigured": totpConfigured}
	if err != nil || !maps.Equal(got, want) {
		t.Fatal("MOFA 设置未返回预期的脱敏结果", err)
	}
}

func assertMofaPrivateSettings(t *testing.T, a *App, want mofaImportSettings) {
	t.Helper()
	if got, err := a.readMofaImportSettings(); err != nil || got != want {
		t.Fatal("MOFA 本机凭据与预期状态不一致", err)
	}
}
