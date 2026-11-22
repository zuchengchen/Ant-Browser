package backend

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeMofaSite(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"  ", ""},
		{"https://MOFA.example.test:443/admin/accounts/", "https://mofa.example.test"},
		{"https://mofa.example.test:9443/", "https://mofa.example.test:9443"},
		{"https://127.0.0.1:9443/admin/accounts", "https://127.0.0.1:9443"},
		{"https://[2001:DB8::1]:9443/admin/accounts", "https://[2001:db8::1]:9443"},
		{"https://bücher.example.test/", "https://xn--bcher-kva.example.test"},
	} {
		got, err := normalizeMofaSite(test.input)
		if err != nil || got != test.want {
			t.Errorf("normalizeMofaSite(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{
		"http://mofa.example.test", "mofa.example.test", "https://mofa.example.test/admin",
		"https://someone@example.test", "https://example.test/admin/accounts?",
		"https://example.test/#", "https://example.test:65536", "https://example.test:",
		"https://example.test/admin/%61ccounts", "https://[fe80::1%25eth0]",
		"https://127.001.0.1", "https://2130706433", "https://example.0x1",
	} {
		if _, err := normalizeMofaSite(input); err == nil {
			t.Errorf("invalid site accepted: %q", input)
		}
	}
}

func TestMofaSiteLocalToInstallationAndRestoredFromPreference(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	if got, err := app.GetMofaSite(); err != nil || got != "" {
		t.Fatalf("unconfigured MOFA site = %q, %v", got, err)
	}
	const site = "https://mofa.example.test:9443"
	if got, err := app.SaveMofaSite(site + "/admin/accounts"); err != nil || got != site {
		t.Fatalf("save MOFA site = %q, %v", got, err)
	}
	restarted := NewApp(app.appRoot)
	if got, err := restarted.GetMofaSite(); err != nil || got != site {
		t.Fatalf("restarted MOFA site = %q, %v", got, err)
	}
	if got, err := NewApp(t.TempDir()).GetMofaSite(); err != nil || got != "" {
		t.Fatalf("second installation read first site's setting: %q, %v", got, err)
	}
	file := restarted.resolveAppPath(filepath.Join("data", mofaExtensionDirectory, "site.json"))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"site":"https://old-backup.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restarted.syncSavedMofaExtensionSite(); err != nil {
		t.Fatal(err)
	}
	assertMofaExtensionSite(t, file, site)
	if !backupShouldSkipAppDataFile(filepath.Join(mofaExtensionDirectory, "site.json")) ||
		!backupShouldSkipAppDataFile(filepath.Join(mofaExtensionDirectory, ".site-restore")) {
		t.Fatal("generated local MOFA site could be copied from an archive")
	}
	if got, err := restarted.SaveMofaSite(""); err != nil || got != "" {
		t.Fatalf("clear MOFA site = %q, %v", got, err)
	}
	assertMofaExtensionSite(t, file, "")
}

func TestMofaSiteSaveKeepsPriorPreferenceOnPublicationError(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	const site = "https://mofa.example.test"
	if _, err := app.SaveMofaSite(site); err != nil {
		t.Fatal(err)
	}
	file := app.resolveAppPath(filepath.Join("data", mofaExtensionDirectory, "site.json"))
	if err := os.MkdirAll(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveMofaSite("https://different.example.test"); err == nil {
		t.Fatal("save succeeded although a directory blocked site publication")
	}
	if got, err := app.GetMofaSite(); err != nil || got != site {
		t.Fatalf("failed save changed prior MOFA site: %q, %v", got, err)
	}
}

func TestMofaSiteResetKeepsOtherSettingsConsistent(t *testing.T) {
	app := newKeyPouchSettingsTestApp(t)
	file := app.resolveAppPath(filepath.Join("data", mofaExtensionDirectory, "site.json"))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	const site = "https://mofa.example.test"
	if _, err := app.SaveMofaSite(site); err != nil {
		t.Fatal(err)
	}
	keyPouch, err := app.keyPouchSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyPouch, 0700); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetManagedSettings(); err == nil {
		t.Fatal("reset should report invalid KeyPouch setting")
	}
	if got, err := app.GetMofaSite(); err != nil || got != site {
		t.Fatalf("failed reset changed MOFA site: %q, %v", got, err)
	}
	assertMofaExtensionSite(t, file, site)
	if err := os.Remove(keyPouch); err != nil {
		t.Fatal(err)
	}
	if err := app.ResetManagedSettings(); err != nil {
		t.Fatal(err)
	}
	if got, err := app.GetMofaSite(); err != nil || got != "" {
		t.Fatalf("reset MOFA site = %q, %v; want empty", got, err)
	}
	assertMofaExtensionSite(t, file, "")
}

func TestMofaSiteOmittedFromBackup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, mofaExtensionDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"site.json", ".site-previous", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	if _, err := backupZipAddDir(w, root, "payload/app/data", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	content := output.Bytes()
	r, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "payload/app/data/" + mofaExtensionDirectory + "/"
	manifest, err := r.Open(prefix + "manifest.json")
	if err != nil {
		t.Fatal("backup should preserve ordinary extensions files", err)
	}
	if err := manifest.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"site.json", ".site-previous"} {
		handle, err := r.Open(prefix + file)
		if handle != nil {
			_ = handle.Close()
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("backup retained locally generated %s: %v", file, err)
		}
	}
}

func assertMofaExtensionSite(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(data, &actual); err != nil || len(actual) != 1 || actual["site"] != want {
		t.Fatalf("unpacked MOFA config = %q, %v; want only %q", data, err, want)
	}
}
