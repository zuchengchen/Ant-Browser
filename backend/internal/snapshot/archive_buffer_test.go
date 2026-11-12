package snapshot

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestZipDirReportsBufferedFinalizationError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full-device error test")
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tiny.txt"), []byte("small payload"), 0600); err != nil {
		t.Fatal(err)
	}
	// The compressed entry fits in the buffer. ENOSPC is returned at finalization.
	err := ZipDir(source, "/dev/full")
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("final writer error was lost: %v", err)
	}
}

func TestZipDirKeepsTraversalErrorWhenFinalizationAlsoFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full-device error test")
	}
	source := filepath.Join(t.TempDir(), "missing")
	err := ZipDir(source, "/dev/full")
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("lost original or finalization error: %v", err)
	}
}

func TestArchiveCopyBufferPreservesEmptyAndNestedFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "empty-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"empty":       {},
		"short":       []byte("短文本"),
		"many-chunks": bytes.Repeat([]byte("content-\x00"), 25000),
		"last":        []byte("a"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(root, "contents.zip")
	if err := ZipDir(source, archive); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		closeErr := stream.Close()
		if err != nil || closeErr != nil || !bytes.Equal(data, files[file.Name]) {
			t.Fatalf("entry differs: %q / %v / %v", file.Name, err, closeErr)
		}
		seen++
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if seen != len(files) {
		t.Fatalf("entry count: %d", seen)
	}
	dest := filepath.Join(root, "restored")
	if err := UnzipTo(archive, dest); err != nil {
		t.Fatal(err)
	}
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("restored file differs: %s / %v", name, err)
		}
	}
	info, err := os.Stat(filepath.Join(dest, "empty-directory"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty directory missing: %v", err)
	}
	err = UnzipToWithLimits(archive, filepath.Join(root, "limited"), UnzipLimits{MaxUncompressedBytes: 32 * 1024})
	if err == nil || !strings.Contains(err.Error(), "大小超过限制") {
		t.Fatalf("expanded limit bypassed: %v", err)
	}
}
