package backend

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

type snapshotTestEntry struct {
	name string
	body string
}

func newSnapshotRestoreTestApp(t *testing.T) (*App, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "user-data")
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	profile := &browser.Profile{ProfileId: "snapshot-profile", UserDataDir: "snapshot-profile"}
	app.browserMgr.Profiles[profile.ProfileId] = profile
	target := app.browserMgr.ResolveUserDataDir(profile)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, filepath.Join(target, "original.txt"), "original data")
	snapDir, err := app.snapshotDir(profile.ProfileId)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, filepath.Join(snapDir, "restore_example.meta.json"), "{}")
	return app, target, filepath.Join(snapDir, "restore_example.zip")
}

func writeSnapshotTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshotTestArchive(t *testing.T, path string, entries ...snapshotTestEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSnapshotTestContents(t *testing.T, path, expected string) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != expected {
		t.Fatalf("read %s = %q, %v; want %q", path, actual, err, expected)
	}
}

func assertSnapshotRestoreCleaned(t *testing.T, target string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".snapshot-restore-") {
			t.Fatalf("restore temporary directory was not cleaned: %s", entry.Name())
		}
	}
}

func TestBrowserSnapshotRestorePreservesDataOnInvalidArchive(t *testing.T) {
	app, target, archive := newSnapshotRestoreTestApp(t)
	writeSnapshotTestFile(t, archive, "not a ZIP file")
	if err := app.BrowserSnapshotRestore("snapshot-profile", "restore"); err == nil {
		t.Fatal("corrupt ZIP was accepted")
	}
	assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
	assertSnapshotRestoreCleaned(t, target)
}

func TestBrowserSnapshotRestorePreservesDataOnExtractionFailure(t *testing.T) {
	app, target, archive := newSnapshotRestoreTestApp(t)
	// The first entry extracts successfully; the next cannot create a child of
	// that regular file. This exercises cleanup after partial decompression.
	writeSnapshotTestArchive(t, archive,
		snapshotTestEntry{"Default/Preferences", "restored"},
		snapshotTestEntry{"Default/Preferences/child", "invalid"})
	if err := app.BrowserSnapshotRestore("snapshot-profile", "restore"); err == nil {
		t.Fatal("partly extractable ZIP was accepted")
	}
	assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
	if _, err := os.Stat(filepath.Join(target, "Default")); !os.IsNotExist(err) {
		t.Fatalf("partial snapshot leaked into original directory: %v", err)
	}
	assertSnapshotRestoreCleaned(t, target)
}

func TestBrowserSnapshotRestoreSuccessfulReplacement(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			app, target, archive := newSnapshotRestoreTestApp(t)
			if !existing {
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
			}
			writeSnapshotTestArchive(t, archive,
				snapshotTestEntry{"Default/Preferences", "restored preferences"},
				snapshotTestEntry{"Local State", "restored state"})
			if err := app.BrowserSnapshotRestore("snapshot-profile", "restore"); err != nil {
				t.Fatal(err)
			}
			assertSnapshotTestContents(t, filepath.Join(target, "Default", "Preferences"), "restored preferences")
			assertSnapshotTestContents(t, filepath.Join(target, "Local State"), "restored state")
			if _, err := os.Stat(filepath.Join(target, "original.txt")); !os.IsNotExist(err) {
				t.Fatalf("old file survived replacement: %v", err)
			}
			if existing {
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("directory permissions were not preserved: %v, %v", info, err)
				}
			}
			assertSnapshotRestoreCleaned(t, target)
		})
	}
}

func TestSnapshotRestoreRenameFailuresPreserveOriginalData(t *testing.T) {
	for _, phase := range []string{"backup", "install", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			app, target, archive := newSnapshotRestoreTestApp(t)
			writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
			failure := errors.New("injected " + phase + " rename error")
			var prepared *snapshotRestore
			err := app.restoreBrowserSnapshot("snapshot-profile", "restore", func(zipPath, target string) (*snapshotRestore, error) {
				r, err := prepareSnapshotRestore(zipPath, target)
				if err != nil {
					return nil, err
				}
				prepared = r
				r.rename = func(from, to string) error {
					if (phase == "backup" && from == r.target) ||
						(phase != "backup" && from == r.staged) ||
						(phase == "rollback" && from == r.backup) {
						return failure
					}
					return os.Rename(from, to)
				}
				return r, nil
			})
			if !errors.Is(err, failure) {
				t.Fatalf("restore error = %v, want injected rename error", err)
			}
			if prepared == nil {
				t.Fatal("restore did not reach preparation")
			}
			if phase == "rollback" {
				assertSnapshotTestContents(t, filepath.Join(prepared.backup, "original.txt"), "original data")
				if !strings.Contains(err.Error(), prepared.backup) {
					t.Fatalf("recovery location absent from error: %v", err)
				}
				if _, err := os.Stat(prepared.staged); !os.IsNotExist(err) {
					t.Fatalf("partial new data was not cleaned: %v", err)
				}
			} else {
				assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
				assertSnapshotRestoreCleaned(t, target)
			}
		})
	}
}

func TestBrowserSnapshotRestoreRejectsRunningInstances(t *testing.T) {
	for _, state := range []string{"running", "pid", "tracked", "shared_directory"} {
		t.Run(state, func(t *testing.T) {
			app, target, archive := newSnapshotRestoreTestApp(t)
			writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
			profile := app.browserMgr.Profiles["snapshot-profile"]
			switch state {
			case "running":
				profile.Running = true
			case "pid":
				profile.Pid = os.Getpid()
			case "tracked":
				process, err := os.FindProcess(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				defer process.Release()
				app.browserMgr.BrowserProcesses[profile.ProfileId] = &exec.Cmd{Process: process}
			case "shared_directory":
				app.browserMgr.Profiles["other-profile"] = &browser.Profile{
					ProfileId: "other-profile", UserDataDir: target, Running: true,
				}
			}
			if err := app.BrowserSnapshotRestore("snapshot-profile", "restore"); err == nil || !strings.Contains(err.Error(), "请先停止") {
				t.Fatalf("running instance restore error = %v", err)
			}
			assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
			assertSnapshotRestoreCleaned(t, target)
		})
	}
}

func TestBrowserSnapshotRestoreRechecksAfterPreparation(t *testing.T) {
	for _, change := range []string{"running", "directory", "deleted"} {
		t.Run(change, func(t *testing.T) {
			app, target, archive := newSnapshotRestoreTestApp(t)
			writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
			err := app.restoreBrowserSnapshot("snapshot-profile", "restore", func(zipPath, target string) (*snapshotRestore, error) {
				r, err := prepareSnapshotRestore(zipPath, target)
				if err != nil {
					return nil, err
				}
				// The actual start path and profile edits use this lock. They may
				// run while extraction takes place; commit must recheck them.
				if !app.browserMgr.Mutex.TryLock() {
					_ = r.cleanup()
					return nil, fmt.Errorf("preparation unexpectedly holds the browser manager lock")
				}
				profile := app.browserMgr.Profiles["snapshot-profile"]
				switch change {
				case "running":
					profile.Running = true
				case "directory":
					profile.UserDataDir = "changed-directory"
				case "deleted":
					delete(app.browserMgr.Profiles, profile.ProfileId)
				}
				app.browserMgr.Mutex.Unlock()
				return r, nil
			})
			if err == nil {
				t.Fatal("restore committed despite profile changing during extraction")
			}
			assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
			assertSnapshotRestoreCleaned(t, target)
		})
	}
}

func TestBrowserSnapshotRestoreSerializesWithInstanceStart(t *testing.T) {
	app, target, archive := newSnapshotRestoreTestApp(t)
	writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
	commitStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	var releaseOnce sync.Once
	releaseCommit := func() { releaseOnce.Do(func() { close(allowCommit) }) }
	defer releaseCommit()
	restoreDone := make(chan error, 1)
	go func() {
		restoreDone <- app.restoreBrowserSnapshot("snapshot-profile", "restore", func(zipPath, target string) (*snapshotRestore, error) {
			r, err := prepareSnapshotRestore(zipPath, target)
			if err != nil {
				return nil, err
			}
			r.rename = func(from, to string) error {
				if from == r.target {
					close(commitStarted)
					<-allowCommit
				}
				return os.Rename(from, to)
			}
			return r, nil
		})
	}()
	select {
	case <-commitStarted:
	case err := <-restoreDone:
		t.Fatalf("restore ended before commit: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not reach commit")
	}
	startCalled := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		close(startCalled)
		// A missing profile returns immediately after acquiring the same lock
		// as every real instance start, without launching a test browser.
		_, err := app.BrowserInstanceStart("missing-profile")
		startDone <- err
	}()
	<-startCalled
	var prematureStart bool
	select {
	case <-startDone:
		prematureStart = true
	case <-time.After(100 * time.Millisecond):
	}
	releaseCommit()
	select {
	case err := <-restoreDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore failed to finish")
	}
	if prematureStart {
		t.Fatal("instance start was not excluded while the data directory was replaced")
	}
	select {
	case err := <-startDone:
		if err == nil || !strings.Contains(err.Error(), "未找到实例") {
			t.Fatalf("instance start after restore returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("instance start remained blocked after restore")
	}
	assertSnapshotTestContents(t, filepath.Join(target, "replacement.txt"), "replacement")
	assertSnapshotRestoreCleaned(t, target)
}

func TestSnapshotRestoreRejectsChangedDirectory(t *testing.T) {
	_, target, archive := newSnapshotRestoreTestApp(t)
	writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
	r, err := prepareSnapshotRestore(archive, target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.cleanup(); err != nil {
			t.Error(err)
		}
	}()
	moved := target + "-moved"
	if err := os.Rename(target, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, filepath.Join(target, "external.txt"), "external data")
	if err := r.commit(); err == nil {
		t.Fatal("restore replaced a directory changed during preparation")
	}
	assertSnapshotTestContents(t, filepath.Join(moved, "original.txt"), "original data")
	assertSnapshotTestContents(t, filepath.Join(target, "external.txt"), "external data")
}

func TestBrowserSnapshotRestoreRejectsUntrackedRuntimeOrUnknownState(t *testing.T) {
	for _, probeFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("probe_fails=%t", probeFails), func(t *testing.T) {
			app, target, archive := newSnapshotRestoreTestApp(t)
			writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
			probeErr := errors.New("process inspection unavailable")
			originalFinder := findBrowserUserDataProcesses
			findBrowserUserDataProcesses = func(path string) ([]browserUserDataProcess, error) {
				if path != target {
					t.Errorf("inspected directory = %q, want %q", path, target)
				}
				if probeFails {
					return nil, probeErr
				}
				return []browserUserDataProcess{{PID: os.Getpid()}}, nil
			}
			t.Cleanup(func() { findBrowserUserDataProcesses = originalFinder })
			err := app.BrowserSnapshotRestore("snapshot-profile", "restore")
			if err == nil {
				t.Fatal("restore ignored an untracked runtime or failed process inspection")
			}
			if probeFails && !errors.Is(err, probeErr) {
				t.Fatalf("process inspection failure was lost: %v", err)
			}
			assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
			assertSnapshotRestoreCleaned(t, target)
		})
	}
}

func TestSnapshotRestoreRejectsSymlinkTarget(t *testing.T) {
	_, target, archive := newSnapshotRestoreTestApp(t)
	writeSnapshotTestArchive(t, archive, snapshotTestEntry{"replacement.txt", "replacement"})
	link := target + "-link"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSnapshotRestore(archive, link); err == nil {
		t.Fatal("symlink restore target was accepted")
	}
	assertSnapshotTestContents(t, filepath.Join(target, "original.txt"), "original data")
	assertSnapshotRestoreCleaned(t, target)
}
