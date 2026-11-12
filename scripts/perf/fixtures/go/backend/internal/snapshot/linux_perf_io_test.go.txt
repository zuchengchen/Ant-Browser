//go:build linux && linuxperf

package snapshot

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLinuxPerfSnapshotArchive uses the actual ZipDir and UnzipTo functions.
// File creation, content verification and fixture cleanup are outside timings.
func TestLinuxPerfSnapshotArchive(t *testing.T) {
	samples := 30
	if raw := os.Getenv("LINUXPERF_IO_SAMPLES"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			t.Fatalf("invalid LINUXPERF_IO_SAMPLES: %q", raw)
		}
		samples = value
	}
	metrics := map[string][]float64{}
	units := map[string]string{}
	inputs := map[string]interface{}{}
	for _, compressible := range []bool{true, false} {
		name := "incompressible"
		if compressible {
			name = "compressible"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			want := linuxPerfSnapshotMakeTree(t, source, compressible)
			manifest := linuxPerfSnapshotManifest(want)
			var firstArchiveHash string
			var archiveSize int64
			measure := func(operation string, fn func() error) {
				var beforeMem, afterMem runtime.MemStats
				runtime.ReadMemStats(&beforeMem)
				beforeCPU := linuxPerfSnapshotCPU(t)
				started := time.Now()
				err := fn()
				elapsed := float64(time.Since(started)) / float64(time.Millisecond)
				cpu := linuxPerfSnapshotCPU(t) - beforeCPU
				runtime.ReadMemStats(&afterMem)
				if err != nil {
					t.Fatalf("%s failed: %v", operation, err)
				}
				for metric, value := range map[string]float64{
					"wall_ms": elapsed, "cpu_ms": cpu, "alloc_bytes": float64(afterMem.TotalAlloc - beforeMem.TotalAlloc),
				} {
					key := "snapshot_" + name + "_" + operation + "_" + metric
					metrics[key] = append(metrics[key], value)
					units[key] = "ms"
					if metric == "alloc_bytes" {
						units[key] = "bytes"
					}
				}
			}
			for sample := 0; sample < samples; sample++ {
				archive := filepath.Join(root, fmt.Sprintf("archive-%03d.zip", sample))
				destination := filepath.Join(root, fmt.Sprintf("unpacked-%03d", sample))
				measure("zip", func() error { return ZipDir(source, archive) })
				linuxPerfSnapshotNoFilesOpen(t, root)
				data, err := os.ReadFile(archive)
				if err != nil || len(data) == 0 {
					t.Fatalf("archive absent or empty: %v", err)
				}
				digest := sha256.Sum256(data)
				archiveHash := hex.EncodeToString(digest[:])
				if sample == 0 {
					firstArchiveHash, archiveSize = archiveHash, int64(len(data))
				} else if archiveHash != firstArchiveHash {
					t.Fatalf("same fixture produced different archive content: %s != %s", archiveHash, firstArchiveHash)
				}
				measure("unzip", func() error { return UnzipTo(archive, destination) })
				linuxPerfSnapshotNoFilesOpen(t, root)
				linuxPerfSnapshotAssertTree(t, destination, want)
				if err := os.RemoveAll(destination); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			}
			linuxPerfSnapshotAssertTree(t, source, want)
			inputs[name] = map[string]interface{}{
				"files": len(want), "uncompressed_bytes": 32 * 128 * 1024,
				"tree_sha256": manifest, "archive_sha256": firstArchiveHash, "archive_bytes": archiveSize,
			}
		})
	}
	t.Run("invalid_archives_and_resource_cleanup", linuxPerfSnapshotInvalidArchives)
	if t.Failed() {
		return
	}
	report, err := json.Marshal(map[string]interface{}{
		"schema_version": 1, "suite": "file_io", "component": "snapshot_archive", "samples": samples, "diagnostic": samples < 30,
		"metrics": metrics, "metric_units": units,
		"evidence": map[string]interface{}{
			"inputs": inputs, "hash_and_file_set_verified_each_sample": true, "no_fixture_file_descriptors_each_sample": true,
			"errors":          "missing source, traversal, symlink, duplicate paths, truncated ZIP and expansion limit must reject",
			"cancellation":    "ZipDir/UnzipTo have no context or cancellation interface; download fixture verifies existing cancellation behavior",
			"cpu_measurement": "getrusage RUSAGE_SELF user+system", "allocation_measurement": "runtime.TotalAlloc delta during operation",
			"timing": "includes archive writer finalization and close; excludes fixture generation, verification and cleanup",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LINUXPERF_JSON:%s", report)
}

func linuxPerfSnapshotMakeTree(t *testing.T, source string, compressible bool) map[string]string {
	t.Helper()
	want := map[string]string{}
	for index := 0; index < 33; index++ {
		relative := fmt.Sprintf("group-%02d/file-%02d.bin", index%4, index)
		path := filepath.Join(source, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		size := 128 * 1024
		if index == 32 {
			size = 0
		}
		data := make([]byte, size)
		if compressible {
			for offset := range data {
				data[offset] = byte((offset + index) % 31)
			}
		} else {
			if _, err := rand.New(rand.NewSource(int64(7102026 + index))).Read(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		want[relative] = hex.EncodeToString(digest[:])
	}
	return want
}

func linuxPerfSnapshotManifest(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		fmt.Fprintf(hash, "%s\x00%s\n", path, files[path])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func linuxPerfSnapshotAssertTree(t *testing.T, root string, want map[string]string) {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); want[relative] != got {
			return fmt.Errorf("unexpected file or hash mismatch: %s", relative)
		}
		seen[relative] = true
		return nil
	})
	if err != nil || len(seen) != len(want) {
		t.Fatalf("tree verification failed: %v, got %d files want %d", err, len(seen), len(want))
	}
}

func linuxPerfSnapshotCPU(t *testing.T) float64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec)*1000 + float64(usage.Utime.Usec+usage.Stime.Usec)/1000
}

func linuxPerfSnapshotNoFilesOpen(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(target, root+string(os.PathSeparator)) {
			t.Fatalf("fixture file descriptor leaked: %s -> %s", entry.Name(), target)
		}
	}
}

func linuxPerfSnapshotInvalidArchives(t *testing.T) {
	root := t.TempDir()
	if err := ZipDir(filepath.Join(root, "missing"), filepath.Join(root, "missing.zip")); err == nil {
		t.Fatal("missing source was accepted")
	}
	for _, name := range []string{"traversal", "symlink", "duplicate", "truncated", "limit"} {
		archive := filepath.Join(root, name+".zip")
		file, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		header := zip.FileHeader{Name: "safe.txt", Method: zip.Store}
		if name == "traversal" {
			header.Name = "../escape"
		} else if name == "symlink" {
			header.SetMode(os.ModeSymlink | 0777)
		}
		count := 1
		if name == "duplicate" {
			count = 2
		}
		for index := 0; index < count; index++ {
			entry, err := writer.CreateHeader(&zip.FileHeader{Name: header.Name, Method: header.Method, ExternalAttrs: header.ExternalAttrs, CreatorVersion: header.CreatorVersion})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte("safe-fixture-content")); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if name == "truncated" {
			info, err := os.Stat(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(archive, info.Size()-22); err != nil {
				t.Fatal(err)
			}
		}
		destination := filepath.Join(root, name)
		if name == "limit" {
			err = UnzipToWithLimits(archive, destination, UnzipLimits{MaxSingleFileBytes: 1})
		} else {
			err = UnzipTo(archive, destination)
		}
		if err == nil {
			t.Fatalf("%s archive was accepted", name)
		}
		if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
			t.Fatalf("unsafe file created outside destination or stat failed: %v", err)
		}
		linuxPerfSnapshotNoFilesOpen(t, root)
	}
}
