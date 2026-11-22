package backend

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

func testLocalGPTCredential() localGPTCredential {
	return localGPTCredential{Site: "https://keys.example.test", RecordID: 92, Email: "member@example.test", Password: " synthetic-password-for-member ", TOTPSecret: "synthetic-verification-seed", LoginBinding: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
}

func TestLocalGPTExplicitCloneAndRecycle(t *testing.T) {
	newKeyPouchSettingsTestApp(t) // temporary OS user, including on Windows and macOS
	app := ugCompatApp(t)
	credential := testLocalGPTCredential()
	source, err := app.BrowserProfileCreate(BrowserProfileInput{ProfileName: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.saveLocalGPTCredential(source.ProfileId, credential); err != nil {
		t.Fatal(err)
	}
	// Old callers, plain clones, and templates must not copy a stored login.
	ordinary, err := app.BrowserProfileCopyWithOptions(source.ProfileId, "Ordinary", BrowserProfileCopyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := app.browserMgr.CopyBoundProfile(source.ProfileId, "different@example.test", "bound-profile", "Work")
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []*browser.Profile{ordinary, bound} {
		if has, err := app.BrowserProfileHasLocalGPTCredential(profile.ProfileId); err != nil || has {
			t.Fatal("an ordinary or template clone inherited a credential")
		}
	}
	optIn := BrowserProfileCopyOptions{CopyGPTCredential: true}
	if clone, err := app.BrowserProfileCopyWithOptions(ordinary.ProfileId, "No credentials", optIn); clone != nil || err == nil {
		t.Fatal("claimed to have copied absent credentials")
	}
	copy, err := app.BrowserProfileCopyWithOptions(source.ProfileId, "With GPT", optIn)
	if err != nil {
		t.Fatal(err)
	}
	refresh := credential
	refresh.Password, refresh.TOTPSecret = "", ""
	if err := app.saveLocalGPTCredential(source.ProfileId, refresh); err != nil {
		t.Fatal(err)
	}
	got, err := app.readLocalGPTCredential(copy.ProfileId)
	if err != nil || got == nil || *got != credential.PortableCopy() {
		t.Fatal("clone did not retain its independent snapshot")
	}
	if err := app.BrowserProfileDelete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if _, err := app.BrowserProfileRestore(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if got, err := app.readLocalGPTCredential(copy.ProfileId); err != nil || got == nil || *got != credential.PortableCopy() {
		t.Fatal("restored profile lost its credential")
	}
	if err := app.BrowserProfileDelete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if err := app.BrowserProfilePermanentlyDelete(copy.ProfileId); err != nil {
		t.Fatal(err)
	}
	if got, err := app.readLocalGPTCredential(copy.ProfileId); err != nil || got != nil {
		t.Fatal("permanently deleted profile retained a credential")
	}
}

func TestLocalGPTCredentialArchivesAndMigration(t *testing.T) {
	newKeyPouchSettingsTestApp(t)
	source, target := ugCompatApp(t), ugCompatApp(t)
	c := testLocalGPTCredential()
	profile, err := source.BrowserProfileCreate(BrowserProfileInput{ProfileName: "Source"})
	if err != nil {
		t.Fatal(err)
	}
	dir := source.browserMgr.ResolveUserDataDir(profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Preferences"), []byte("synthetic-profile-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rememberGPTSessionExtension(dir, filepath.Join(source.appRoot, "data", keyPouchExtensionDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.appRoot, "config.yaml"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Even an unusually broad browser data scope encompassing the local vault
	// must exclude the master key, the encrypted snapshots and symlinks to them.
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	if err := source.saveLocalGPTCredential(profile.ProfileId, c); err != nil {
		t.Fatal(err)
	}
	vaultDir, err := keypouch.LocalGPTVaultDirectory()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := filepath.Glob(filepath.Join(vaultDir, "*", "key"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("expected one isolated installation key, found %d", len(keys))
	}
	localKey, err := os.ReadFile(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(keys[0], filepath.Join(dir, "local-key-link")); err != nil {
			t.Fatal(err)
		}
	}
	plainPackage := filepath.Join(t.TempDir(), "plain.zip")
	if _, err := source.backupExportProfilePackageToPath(plainPackage, []string{profile.ProfileId}); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := source.backupExportPackageToPath(full); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(t.TempDir(), "protected.zip")
	profiles := []browser.Profile{*profile}
	snapshot, err := source.collectProfilePackageDatabase(profiles)
	if err != nil {
		t.Fatal(err)
	}
	const pass = "synthetic transfer passphrase"
	// The browser collector normally supplies these cookies. The actual
	// package writer still reads the credential from the source installation.
	cookies := map[string][]CookieInfo{profile.ProfileId: nil}
	if _, n, err := source.writeProfilePackageArchive(protected, profiles, snapshot, cookies, pass); err != nil || n != 1 {
		t.Fatalf("write protected package: count=%d err=%v", n, err)
	}
	var protectedEntry []byte
	for _, path := range []string{plainPackage, full, protected} {
		entries, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		sawMarker := false
		for _, entry := range entries.File {
			r, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(entry.Name) == gptSessionMarker || strings.Contains(entry.Name, keypouch.LocalGPTVaultName) || strings.Contains(entry.Name, "local-key-link") ||
				bytes.Contains(data, localKey) || bytes.Contains(data, []byte(c.Password)) || bytes.Contains(data, []byte(c.TOTPSecret)) || bytes.Contains(data, []byte(c.LoginBinding)) {
				t.Fatalf("archive included private data in %s", entry.Name)
			}
			sawMarker = sawMarker || bytes.Equal(data, []byte("synthetic-profile-marker"))
			if strings.HasPrefix(entry.Name, portableSessionDir+"/") {
				if path != protected || entry.Name != portableSessionArchivePath(profile.ProfileId) {
					t.Fatal("unexpected migration entry")
				}
				protectedEntry = data
			}
		}
		if err := entries.Close(); err != nil || !sawMarker {
			t.Fatal("ordinary profile data missing from archive")
		}
	}
	session, err := decryptPortableProfileSession(profile.ProfileId, protectedEntry, pass)
	if err != nil || session.GPTCredential == nil || *session.GPTCredential != c.PortableCopy() {
		t.Fatal("password-protected entry did not include the complete credential")
	}

	// Generate the destination key before importing, simulating a second
	// installation. Its pre-existing profile is the overwrite target below.
	existing, err := target.BrowserProfileCreate(BrowserProfileInput{ProfileName: profile.ProfileName})
	if err != nil {
		t.Fatal(err)
	}
	prior := localGPTCredential{Site: c.Site, RecordID: 1, Email: "prior@example.test", Password: "original value"}
	if err := target.saveLocalGPTCredential(existing.ProfileId, prior); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.zip")
	corruptGPTPackage(t, protected, corrupt, portableSessionArchivePath(profile.ProfileId), false)
	missing := filepath.Join(t.TempDir(), "missing.zip")
	corruptGPTPackage(t, protected, missing, portableSessionArchivePath(profile.ProfileId), true)
	for _, bad := range []struct{ path, password string }{{protected, "wrong-password"}, {corrupt, pass}, {missing, pass}} {
		_, err := target.importProfilePackageFromPathWithModeAndPortableSession(bad.path, profilePackageImportModeOverwrite, true, nil, bad.password)
		if err == nil {
			t.Fatal("accepted an invalid password or damaged migration")
		}
		got, err := target.readLocalGPTCredential(existing.ProfileId)
		if err != nil || got == nil || *got != prior || target.browserMgr.Profiles[existing.ProfileId] == nil {
			t.Fatal("failed import replaced the destination")
		}
	}
	result, err := target.importProfilePackageFromPathWithModeAndPortableSession(protected, profilePackageImportModeOverwrite, true, nil, pass)
	if err != nil {
		t.Fatal(err)
	}
	newID := result.ProfileMappings[profile.ProfileId]
	got, err := NewApp(target.appRoot).readLocalGPTCredential(newID)
	if err != nil || got == nil || *got != c.PortableCopy() || newID == existing.ProfileId || newID == profile.ProfileId || result.OverwrittenCount != 1 {
		t.Fatal("restarted destination could not use the imported credential")
	}
	result, err = target.importProfilePackageFromPathWithModeAndConfirmation(plainPackage, profilePackageImportModeNew, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := target.readLocalGPTCredential(result.ProfileMappings[profile.ProfileId]); err != nil || got != nil {
		t.Fatal("plain package imported a credential")
	}
}

func corruptGPTPackage(t *testing.T, source, destination, entryName string, drop bool) {
	t.Helper()
	contents, err := zip.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer contents.Close()
	out, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(out)
	found := false
	for _, f := range contents.File {
		if f.Name == entryName {
			found = true
			if !drop {
				entry, err := w.Create(f.Name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("{}")); err != nil {
					t.Fatal(err)
				}
			}
		} else if err := w.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(w.Close(), out.Close()); err != nil || !found {
		t.Fatal("could not construct an invalid archive calibration sample", err)
	}
}
