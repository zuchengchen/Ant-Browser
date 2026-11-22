package keypouch

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func credentialTestInstallation(t *testing.T) (string, string) {
	t.Helper()
	userDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userDir)
	t.Setenv("AppData", userDir)
	installation := t.TempDir()
	path, err := credentialPath(installation, "first")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	return installation, path
}

func TestLocalGPTCredentialEncryptedBoundAndRenewed(t *testing.T) {
	root, path := credentialTestInstallation(t)
	c := LocalGPTCredential{"https://keys.example.test", 17, "one@example.test", "  synthetic password  ", "synthetic-seed", ""}
	if got, err := ReadLocalGPTCredential(root, "first"); err != nil || got != nil {
		t.Fatal("new instance should have no credential")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("an empty read created a vault")
	}
	if err := SaveLocalGPTCredential(root, "first", c); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{c.Email, c.Password, c.TOTPSecret} {
		if bytes.Contains(blob, []byte(value)) {
			t.Fatal("cleartext present in saved credential")
		}
	}
	if runtime.GOOS != "windows" {
		for file, mode := range map[string]os.FileMode{path: 0600, filepath.Join(filepath.Dir(path), "key"): 0600, filepath.Dir(path): 0700} {
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm()&^mode != 0 {
				t.Fatal("local credential permissions are too broad")
			}
		}
	}
	if got, err := ReadLocalGPTCredential(root, "first"); err != nil || got == nil || c != *got {
		t.Fatal("credential failed to round trip")
	}
	otherPath, err := credentialPath(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, blob, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLocalGPTCredential(root, "second"); err == nil || got != nil {
		t.Fatal("another instance accepted the copied ciphertext")
	}
	for _, change := range []func(*LocalGPTCredential){
		func(other *LocalGPTCredential) { other.RecordID++ },
		func(other *LocalGPTCredential) { other.Site = "https://other.example.test" },
	} {
		other := c
		change(&other)
		if err := SaveLocalGPTCredential(root, "first", other); err == nil {
			t.Fatal("rebound an existing credential to another source")
		}
	}
	c.Password, c.TOTPSecret = "", ""
	if err := SaveLocalGPTCredential(root, "first", c); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLocalGPTCredential(root, "first"); err != nil || got == nil || *got != c {
		t.Fatal("refresh did not clear removed values")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(path), "key")); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalGPTCredential(root, "third", c); err == nil {
		t.Fatal("replaced the missing encryption key of an existing vault")
	}
	if got, err := ReadLocalGPTCredential(root, "first"); err == nil || got != nil {
		t.Fatal("missing key was treated as an absent credential")
	}
}

func TestLocalGPTCredentialConcurrentSnapshots(t *testing.T) {
	root, _ := credentialTestInstallation(t)
	old := LocalGPTCredential{"https://keys.example.test", 8, "steps@example.test", "first password", "first seed", ""}
	next := old
	next.Email, next.Password, next.TOTPSecret = "renamed@example.test", "second password", "second seed"
	if err := SaveLocalGPTCredential(root, "first", old); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for _, snapshot := range []LocalGPTCredential{old, next} {
		group.Add(1)
		go func(snapshot LocalGPTCredential) {
			defer group.Done()
			for i := 0; i < 12; i++ {
				if err := SaveLocalGPTCredential(root, "first", snapshot); err != nil {
					t.Error(err)
					return
				}
				got, err := ReadLocalGPTCredential(root, "first")
				if err != nil || got == nil || *got != old && *got != next {
					t.Error("interleaved steps produced a partial credential")
					return
				}
			}
		}(snapshot)
	}
	group.Wait()
}
