package backend

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

func TestKeyPouchRuntimeFollowsNativeTransitionsAndRecoversDetachedProfiles(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	profile := &BrowserProfile{ProfileId: "tracked", UserDataDir: filepath.Join(root, "profile")}
	app.browserMgr.Profiles[profile.ProfileId] = profile
	var pageOpen atomic.Bool
	pageOpen.Store(true)
	debug := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		if pageOpen.Load() {
			w.Write([]byte(`[{"type":"page","id":"tab"}]`))
		} else {
			w.Write([]byte(`[{"type":"service_worker","id":"background"}]`))
		}
	}))
	defer debug.Close()
	port := debug.Listener.Addr().(*net.TCPAddr).Port
	var exists, unavailable bool
	probes := 0
	running := func(context.Context) (map[string]browserRuntimeDetection, error) {
		probes++
		if unavailable {
			return nil, errors.New("process list unavailable")
		}
		if exists {
			return map[string]browserRuntimeDetection{keyPouchDirKey(profile.UserDataDir): {PID: os.Getpid(), DebugPort: port}}, nil
		}
		return map[string]browserRuntimeDetection{}, nil
	}
	check := func(want map[string]bool) {
		t.Helper()
		got, err := app.keyPouchRuntimeSnapshot(context.Background(), []string{"tracked", "missing"}, running)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("incorrect runtime state", got, err)
		}
	}
	unavailable = true
	check(map[string]bool{})
	unavailable = false
	exists = true
	check(map[string]bool{"tracked": true})
	exists = false
	check(map[string]bool{"tracked": false})
	// A browser restarted outside the manager is detected on the next heartbeat.
	exists = true
	check(map[string]bool{"tracked": true})
	exists = false
	before := probes
	app.browserMgr.Mutex.Lock()
	app.markProfileRunningLocked("tracked", profile, nil, os.Getpid(), port, false, "")
	app.browserMgr.Mutex.Unlock()
	select {
	case <-app.keyPouchRuntimeChanged:
	default:
		t.Fatal("native start did not wake reporter")
	}
	check(map[string]bool{"tracked": true})
	pageOpen.Store(false)
	check(map[string]bool{"tracked": false})
	if probes != before {
		t.Fatal("a live tracked profile should use its native handle")
	}
	app.browserMgr.Mutex.Lock()
	app.markProfileStoppedLocked("tracked", profile)
	app.browserMgr.Mutex.Unlock()
	select {
	case <-app.keyPouchRuntimeChanged:
	default:
		t.Fatal("native stop did not wake reporter")
	}
	check(map[string]bool{"tracked": false})
}
