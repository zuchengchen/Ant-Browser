package backend

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

func runtimeRestoreTestApp(t *testing.T, profiles ...*BrowserProfile) (*App, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	for _, profile := range profiles {
		app.browserMgr.Profiles[profile.ProfileId] = profile
		// Reuse a marker without opening a connection to the developer's display.
		app.profileWindowMarkers[profile.ProfileId] = &profileWindowMarker{code: "1", stop: make(chan struct{})}
	}
	t.Cleanup(func() {
		app.browserMgr.Mutex.Lock()
		defer app.browserMgr.Mutex.Unlock()
		for id, profile := range app.browserMgr.Profiles {
			app.markProfileStoppedLocked(id, profile)
		}
	})
	return app, listener.Addr().(*net.TCPAddr).Port
}

func TestRestoreRunningBrowserProfilesResumesExistingRuntime(t *testing.T) {
	profile := &BrowserProfile{ProfileId: "a", UserDataDir: "existing", ProfileName: "Existing"}
	active := &BrowserProfile{ProfileId: "b", UserDataDir: "active", Running: true, Pid: 456}
	deleted := &BrowserProfile{ProfileId: "c", UserDataDir: "deleted", DeletedAt: "deleted"}
	app, port := runtimeRestoreTestApp(t, profile, active, deleted)
	calls := 0
	app.restoreRunningBrowserProfiles(context.Background(), func(dir string) (browserRuntimeDetection, bool) {
		calls++
		if filepath.Base(dir) != "existing" {
			t.Fatalf("unexpected directory %q", dir)
		}
		return browserRuntimeDetection{PID: 123, DebugPort: port, DebugReady: true}, true
	})
	app.browserMgr.Mutex.Lock()
	defer app.browserMgr.Mutex.Unlock()
	if calls != 1 || !profile.Running || !profile.DebugReady || profile.Pid != 123 || profile.DebugPort != port {
		t.Fatalf("existing runtime not restored: calls=%d profile=%+v", calls, profile)
	}
	if app.browserProcessMonitors[profile.ProfileId] == nil {
		t.Fatal("restored browser has no close monitor")
	}
	if active.Pid != 456 || !active.Running || deleted.Running {
		t.Fatal("recovery modified an active or deleted profile")
	}
}

func TestRestoreRunningBrowserProfilesDoesNotCompeteForSharedDirectory(t *testing.T) {
	a := &BrowserProfile{ProfileId: "a", UserDataDir: "shared"}
	b := &BrowserProfile{ProfileId: "b", UserDataDir: "shared"}
	app, port := runtimeRestoreTestApp(t, a, b)
	calls := 0
	app.restoreRunningBrowserProfiles(context.Background(), func(string) (browserRuntimeDetection, bool) {
		calls++
		return browserRuntimeDetection{DebugPort: port, DebugReady: true}, true
	})
	app.browserMgr.Mutex.Lock()
	defer app.browserMgr.Mutex.Unlock()
	if calls != 1 || !a.Running || b.Running {
		t.Fatalf("shared runtime handled more than once: calls=%d a=%v b=%v", calls, a.Running, b.Running)
	}
}

func TestRestoreRunningBrowserProfilesSkipsDirectoryAlreadyManaged(t *testing.T) {
	a := &BrowserProfile{ProfileId: "a", UserDataDir: "shared"}
	b := &BrowserProfile{ProfileId: "b", UserDataDir: "shared", Running: true}
	app, _ := runtimeRestoreTestApp(t, a, b)
	app.restoreRunningBrowserProfiles(context.Background(), func(string) (browserRuntimeDetection, bool) {
		t.Fatal("already managed directory was probed again")
		return browserRuntimeDetection{}, false
	})
	if a.Running || !b.Running {
		t.Fatal("recovery changed ownership of an already managed runtime")
	}
}

func TestRestoreRunningBrowserProfilesRespectsInterveningChanges(t *testing.T) {
	for _, action := range []string{"start", "stop", "directory", "delete", "cancel"} {
		t.Run(action, func(t *testing.T) {
			profile := &BrowserProfile{ProfileId: "a", UserDataDir: "existing"}
			app, port := runtimeRestoreTestApp(t, profile)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			app.restoreRunningBrowserProfiles(ctx, func(string) (browserRuntimeDetection, bool) {
				app.browserMgr.Mutex.Lock()
				defer app.browserMgr.Mutex.Unlock()
				switch action {
				case "start":
					profile.Running = true
					profile.Pid = 999
				case "stop":
					profile.LastStopAt = "changed"
				case "directory":
					profile.UserDataDir = "new-dir"
				case "delete":
					delete(app.browserMgr.Profiles, "a")
				case "cancel":
					cancel()
				}
				return browserRuntimeDetection{PID: 123, DebugPort: port, DebugReady: true}, true
			})
			if profile.Pid == 123 || app.browserProcessMonitors["a"] != nil {
				t.Fatal("startup recovery overwrote a later action")
			}
		})
	}
}

func TestRestoreRunningBrowserProfilesSkipsUnreadyOrStoppedBrowsers(t *testing.T) {
	for _, detection := range []browserRuntimeDetection{{}, {PID: 123}, {PID: 123, DebugPort: 12345}} {
		profile := &BrowserProfile{ProfileId: "a", UserDataDir: "existing"}
		app, _ := runtimeRestoreTestApp(t, profile)
		app.restoreRunningBrowserProfiles(context.Background(), func(string) (browserRuntimeDetection, bool) {
			return detection, detection.PID != 0
		})
		if profile.Running || app.browserProcessMonitors["a"] != nil {
			t.Fatalf("unready runtime was taken over: %+v", detection)
		}
	}
}
