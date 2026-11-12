// Package testutil contains fixtures shared by backend integration tests.
package testutil

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// CopyBundledRuntime selects the current platform's bundled executable and
// copies it to a test-owned directory. Runtime managers may chmod their binary;
// using a copy keeps tests from changing tracked files in the repository.
func CopyBundledRuntime(t testing.TB, name string) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find repository root for bundled runtime")
		}
		root = parent
	}

	binaryName := name
	platformDir := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	source := filepath.Join(root, "bin", platformDir, binaryName)
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		source = filepath.Join(root, "bin", binaryName)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("bundled %s runtime for %s is required: %v", name, platformDir, err)
	}
	defer in.Close()

	dest := filepath.Join(t.TempDir(), binaryName)
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return dest
}
