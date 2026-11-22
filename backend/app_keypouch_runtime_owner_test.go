package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ant-chrome/backend/internal/browser"
	"github.com/gorilla/websocket"
)

func TestGPTSharedProfileCannotReuseOrStopCredentialedRuntime(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	dir := t.TempDir()
	a := &BrowserProfile{ProfileId: "a", UserDataDir: dir}
	b := &BrowserProfile{ProfileId: "b", UserDataDir: dir}
	app, port := runtimeRestoreTestApp(t, a, b)
	if err := app.saveLocalGPTCredential("a", testLocalGPTCredential()); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if _, err := app.BrowserInstanceStart("b"); !errors.Is(err, errGPTSharedRuntime) {
			t.Fatalf("start reused another account's directory: %v", err)
		}
		d := browserRuntimeDetection{PID: 101, DebugPort: port, DebugReady: true}
		if _, err := app.fingerprintCheckRuntimeProfileSnapshot("b", func(string) (browserRuntimeDetection, bool) { return d, true }, nil); !errors.Is(err, errGPTSharedRuntime) {
			t.Fatalf("status adopted another account's process: %v", err)
		}
		originalDetect := detectBrowserRuntimeForStop
		detectBrowserRuntimeForStop = func(string) (browserRuntimeDetection, bool) { return d, true }
		defer func() { detectBrowserRuntimeForStop = originalDetect }()
		if _, err := app.BrowserInstanceStop("b"); !errors.Is(err, errGPTSharedRuntime) {
			t.Fatalf("stop adopted another account's process: %v", err)
		}
		if b.Running || app.browserProcessMonitors["b"] != nil {
			t.Fatal("a failed ownership check registered a browser")
		}
		states, err := app.keyPouchRuntimeSnapshot(context.Background(), []string{"b"}, func(context.Context) (map[string]browserRuntimeDetection, error) {
			return map[string]browserRuntimeDetection{keyPouchDirKey(b.UserDataDir): d}, nil
		})
		if running, ok := states["b"]; err != nil || !ok || running {
			t.Fatalf("alias reported as a separate running instance: %v %v", states, err)
		}
	}
	check()
	// Capture the actual directory while the process is registered. Editing
	// A's saved directory does not free its old running Chrome for B.
	app.browserMgr.Mutex.Lock()
	app.markProfileRunningLocked("a", a, nil, 101, port, true, "")
	a.UserDataDir = t.TempDir()
	app.browserMgr.Mutex.Unlock()
	check()
	if app.uniqueGPTProfileDirLocked(b) {
		t.Fatal("credential delivery accepted a directory belonging to another live process")
	}
	if alias := filepath.Join(t.TempDir(), "linked"); os.Symlink(dir, alias) == nil {
		b.UserDataDir = alias
		check()
	}
}

type retainedGPTDebug struct {
	port           int
	tabs           atomic.Int32
	closed         atomic.Int32
	clears         atomic.Int32
	writes         atomic.Int32
	wrongDir       atomic.Bool
	loseClearReply atomic.Bool
	hasSession     atomic.Bool
	email          atomic.Value
}

func testRetainedGPTDebug(t *testing.T, profileDir, extension string) *retainedGPTDebug {
	t.Helper()
	const extensionID = "abcdefghijklmnopabcdefghijklmnop"
	probe := new(retainedGPTDebug)
	probe.hasSession.Store(true)
	upgrader := websocket.Upgrader{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(cdpBrowserVersion{WebSocketDebuggerUrl: "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/owner-test"})
			return
		case "/json/list":
			_, _ = w.Write([]byte(`[{"id":"page","type":"page"}]`))
			return
		case "/json/activate/page":
			w.WriteHeader(http.StatusOK)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var cmd cdpMessage
			if c.ReadJSON(&cmd) != nil {
				return
			}
			result := map[string]any{}
			switch cmd.Method {
			case "Target.createTarget":
				probe.tabs.Add(1)
				result["targetId"] = "handoff-test"
			case "Target.closeTarget":
				probe.closed.Add(1)
			case cdpMethodExtensionsGet:
				result["extensions"] = []map[string]any{{"id": extensionID, "path": extension}}
			case "Runtime.evaluate":
				expr, _ := cmd.Params["expression"].(string)
				if strings.Contains(expr, "profile_path") {
					dir := profileDir
					if probe.wrongDir.Load() {
						dir = filepath.Join(dir, "wrong")
					}
					result["result"] = map[string]any{"value": filepath.Join(dir, "Default")}
				} else {
					result["result"] = map[string]any{"objectId": "verified-storage"}
				}
			case "Runtime.callFunctionOn":
				f, _ := cmd.Params["functionDeclaration"].(string)
				args, _ := cmd.Params["arguments"].([]any)
				switch {
				case cmd.Params["objectId"] != "verified-storage":
					t.Error("write to an unverified storage object")
				case len(args) == 0 && strings.Contains(f, "remove('antLocalGPT')"):
					probe.hasSession.Store(false)
					probe.email.Store("")
					probe.clears.Add(1)
					if probe.loseClearReply.Swap(false) {
						return
					}
				case len(args) == 1 && strings.Contains(f, "antLocalGPT"):
					if previous, err := gptSessionExtension(profileDir); err != nil || previous != extension {
						t.Error("extension delivery started before persisting the recovery marker")
					}
					probe.hasSession.Store(true)
					arg, _ := args[0].(map[string]any)
					record, _ := arg["value"].(map[string]any)
					email, _ := record["email"].(string)
					probe.email.Store(email)
					probe.writes.Add(1)
				default:
					t.Error("unexpected storage operation")
				}
				result["result"] = map[string]any{"value": true}
			}
			if c.WriteJSON(map[string]any{"id": cmd.Id, "result": result}) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	probe.port = server.Listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(profileDir, "DevToolsActivePort"), []byte(fmt.Sprintf("%d\n/devtools/browser/owner-test", probe.port)), 0600); err != nil {
		t.Fatal(err)
	}
	return probe
}

func TestGPTStaleDebugPortFileCannotAdoptAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	probe := testRetainedGPTDebug(t, dir, filepath.Join(t.TempDir(), keyPouchExtensionDirectory))
	if _, ok := detectBrowserRuntimeByActivePort(dir); !ok {
		t.Fatal("current browser endpoint was not recognized")
	}
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(fmt.Sprintf("%d\n/devtools/browser/an-older-process", probe.port)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectBrowserRuntimeByActivePort(dir); ok {
		t.Fatal("a reused TCP port was treated as the original Chromium process")
	}
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(fmt.Sprint(probe.port)), 0600); err != nil {
		t.Fatal(err)
	}
	previous := findBrowserUserDataProcesses
	findBrowserUserDataProcesses = func(path string) ([]browserUserDataProcess, error) {
		if path != dir {
			t.Fatal("process fallback did not require the same user-data directory")
		}
		return []browserUserDataProcess{{PID: 101, DebugPort: probe.port}}, nil
	}
	defer func() { findBrowserUserDataProcesses = previous }()
	if d, ok := detectBrowserRuntimeByUserDataDir(dir); !ok || d.PID != 101 || d.DebugPort != probe.port {
		t.Fatal("older port-only kernels could not be recovered by process ownership")
	}
}

func TestGPTRecoveryClearsMarkedSessionBeforeRegisteringProcess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	dir := t.TempDir()
	p := &BrowserProfile{ProfileId: "reassigned", UserDataDir: dir}
	app, _ := runtimeRestoreTestApp(t, p)
	// Its previous extension can belong to an older Ant installation and
	// need not be enabled in this profile's current catalog.
	ext := filepath.Join(t.TempDir(), "data", keyPouchExtensionDirectory)
	if err := rememberGPTSessionExtension(dir, ext); err != nil {
		t.Fatal(err)
	}
	if excluded, err := isGPTVaultArchiveEntry(dir, filepath.Join(dir, gptSessionMarker), nil); err != nil || !excluded {
		t.Fatal("live process marker would enter an ordinary profile package")
	}
	probe := testRetainedGPTDebug(t, dir, ext)
	probe.wrongDir.Store(true)
	if _, err := app.BrowserInstanceStatus(p.ProfileId); !errors.Is(err, errGPTSessionRecovery) || p.Running || app.browserProcessMonitors[p.ProfileId] != nil {
		t.Fatalf("failed cleanup registered the existing browser: %v", err)
	}
	if probe.clears.Load() != 0 || !probe.hasSession.Load() {
		t.Fatal("a different Chromium profile was modified")
	}
	if previous, err := gptSessionExtension(dir); err != nil || previous != ext {
		t.Fatal("failed cleanup lost its retry marker")
	}
	probe.wrongDir.Store(false)
	if _, err := app.BrowserInstanceStatus(p.ProfileId); err != nil || !p.Running {
		t.Fatalf("verified cleanup did not let the profile resume: %v", err)
	}
	if probe.clears.Load() != 1 || probe.hasSession.Load() || probe.tabs.Load() != probe.closed.Load() {
		t.Fatal("cleanup lost the previous session or left an auxiliary page")
	}
	if previous, err := gptSessionExtension(dir); err != nil || previous != "" {
		t.Fatal("verified cleanup did not retire its marker")
	}
	app.browserMgr.Mutex.Lock()
	app.markProfileStoppedLocked(p.ProfileId, p)
	app.browserMgr.Mutex.Unlock()
	if _, err := app.BrowserInstanceStatus(p.ProfileId); err != nil || !p.Running || probe.tabs.Load() != 2 {
		t.Fatal("an ordinary retained profile opened another credential handoff page")
	}
}

func TestGPTDeliveryMarksDirectoryWithoutRecordingCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	app, db := startPersistenceTestApp(t)
	m := app.browserMgr
	m.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
	if err := m.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	p := m.Profiles["target"]
	dir := m.ResolveUserDataDir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ext := app.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	probe := testRetainedGPTDebug(t, dir, ext)
	p.DebugPort = probe.port
	c := testLocalGPTCredential()
	if err := app.publishGPTCredentialLocked(context.Background(), p, []string{ext}, &c); err != nil {
		t.Fatal(err)
	}
	if probe.writes.Load() != 1 || probe.closed.Load() != 1 {
		t.Fatal("verified session delivery did not finish")
	}
	content, err := os.ReadFile(filepath.Join(dir, gptSessionMarker))
	if err != nil || string(content) != ext {
		t.Fatal("recovery marker contains more than the unpacked extension directory")
	}
}
