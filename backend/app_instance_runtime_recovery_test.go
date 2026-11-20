package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoverBrowserRuntimeStatesRestoresActiveProfile(t *testing.T) {
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	app.config.Browser.MaxConcurrentInstances = 1
	app.browserMgr = browser.NewManager(app.config, app.appRoot)
	profile := &BrowserProfile{ProfileId: "preserved-profile", UserDataDir: "preserved-profile"}
	app.browserMgr.Profiles[profile.ProfileId] = profile

	app.browserMgr.Profiles["inactive-profile"] = &BrowserProfile{ProfileId: "inactive-profile", UserDataDir: "inactive"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Browser":"Chrome"}`)
	}))
	t.Cleanup(server.Close)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	dir := keyPouchDirKey(app.browserMgr.ResolveUserDataDir(profile))
	original := listBrowserProcessesForStartup
	scans := 0
	listBrowserProcessesForStartup = func(context.Context) (map[string]browserRuntimeDetection, error) {
		scans++
		return map[string]browserRuntimeDetection{dir: {DebugPort: port, PID: 923}}, nil
	}
	t.Cleanup(func() { listBrowserProcessesForStartup = original })

	app.recoverBrowserRuntimeStates()

	if scans != 1 || !profile.Running || !profile.DebugReady || profile.DebugPort != port || profile.Pid != 923 {
		t.Fatalf("recovered profile runtime = %+v", profile)
	}
	gate := app.launchGate()
	gate.mu.Lock()
	occupied := gate.occupied()
	gate.mu.Unlock()
	if occupied != 1 {
		t.Fatalf("launch gate occupied = %d, want 1", occupied)
	}

	app.browserMgr.Mutex.Lock()
	app.markProfileStoppedLocked(profile.ProfileId, profile)
	app.browserMgr.Mutex.Unlock()
}
