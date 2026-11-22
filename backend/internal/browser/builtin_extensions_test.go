package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGPTCredentialDestinationMatchesBundledExtension(t *testing.T) {
	dir := t.TempDir()
	if err := writeBuiltinExtensionFiles("builtinassets/keypouch-gpt", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site.json"), []byte(`{"site":"https://vault.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyKeyPouchGPTFiles(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "login.js"), []byte("/* incomplete backup */"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifyKeyPouchGPTFiles(dir) == nil {
		t.Fatal("same-version modified installation was trusted")
	}
}

func TestSyncBuiltinExtensionsInstallsWithoutDefaultInstall(t *testing.T) {
	manager := newIsolationManager(t)
	spec := builtinExtensionSpecs[0]

	if err := manager.SyncBuiltinExtensions(); err != nil {
		t.Fatalf("SyncBuiltinExtensions returned error: %v", err)
	}

	extension, err := manager.ExtensionDAO.Get(spec.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if !extension.Enabled {
		t.Fatalf("extension = %#v, want enabled", extension)
	}
	if extension.DefaultInstall {
		t.Fatalf("extension = %#v, 内置插件不应默认安装到实例", extension)
	}
	manifestPath := filepath.Join(extension.InstallDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("内置插件未解包: %v", err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("manifest 解析失败: %v", err)
	}
	if extension.Version != manifest.Version {
		t.Fatalf("version = %q, want %q", extension.Version, manifest.Version)
	}
}

// 重复启动不能覆盖用户对内置插件的开关设置。
func TestSyncBuiltinExtensionsKeepsUserSettings(t *testing.T) {
	manager := newIsolationManager(t)
	spec := builtinExtensionSpecs[0]

	if err := manager.SyncBuiltinExtensions(); err != nil {
		t.Fatalf("SyncBuiltinExtensions returned error: %v", err)
	}
	if err := manager.ExtensionDAO.SetDefaultInstall(spec.ID, false); err != nil {
		t.Fatalf("SetDefaultInstall returned error: %v", err)
	}
	if err := manager.ExtensionDAO.SetEnabled(spec.ID, false); err != nil {
		t.Fatalf("SetEnabled returned error: %v", err)
	}

	if err := manager.SyncBuiltinExtensions(); err != nil {
		t.Fatalf("SyncBuiltinExtensions (second) returned error: %v", err)
	}

	extension, err := manager.ExtensionDAO.Get(spec.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if extension.DefaultInstall {
		t.Fatal("重复同步把用户的默认安装设置改回去了")
	}
	if extension.Enabled {
		t.Fatal("重复同步把用户的启用状态改回去了")
	}
}

func TestKeyPouchExtensionFollowsDefaultAndProfileOverrides(t *testing.T) {
	m := newIsolationManager(t)
	dao := m.ExtensionDAO
	// An existing instance that explicitly chose no extensions must stay empty after upgrading.
	if _, err := dao.SetProfileSettings("empty", nil, true); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	gpt, err := dao.Get(KeyPouchGPTExtensionID)
	if err != nil || !gpt.Enabled || !gpt.DefaultInstall || gpt.Name != "登录 GPT" {
		t.Fatalf("unexpected built-in GPT extension: %+v, %v", gpt, err)
	}
	mofa, err := dao.Get(MofaImportExtensionID)
	if err != nil || !mofa.Enabled || !mofa.DefaultInstall || mofa.Name != "导入到 MOFA" {
		t.Fatalf("unexpected built-in MOFA extension: %+v, %v", mofa, err)
	}
	check := func(profile string, want ...string) {
		t.Helper()
		got := m.ProfileExtensionLoadPaths(&Profile{ProfileId: profile})
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s loads %v, want %v", profile, got, want)
		}
	}
	check("inherit", gpt.InstallDir, mofa.InstallDir)
	check("empty")
	if _, err := dao.SetProfileSettings("explicit", []string{KeyPouchGPTExtensionID}, true); err != nil {
		t.Fatal(err)
	}
	if err := dao.SetDefaultInstall(KeyPouchGPTExtensionID, false); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	check("inherit", mofa.InstallDir)
	check("explicit", gpt.InstallDir)
	if err := dao.SetEnabled(KeyPouchGPTExtensionID, false); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	check("explicit")
}

func TestMofaSiteSurvivesBuiltinUpdate(t *testing.T) {
	dir := t.TempDir()
	if err := writeBuiltinExtensionFiles("builtinassets/mofa-import", dir); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(dir, "site.json")
	if got, err := os.ReadFile(site); err != nil || string(got) != "{\"site\":\"\"}\n" {
		t.Fatalf("initial site configuration = %q, %v", got, err)
	}
	configured := []byte(`{"site":"https://mofa.example.test"}`)
	if err := os.WriteFile(site, configured, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBuiltinExtensionFiles("builtinassets/mofa-import", dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(site); err != nil || !slices.Equal(got, configured) {
		t.Fatalf("site configuration after reinstallation = %q, %v", got, err)
	}
}
