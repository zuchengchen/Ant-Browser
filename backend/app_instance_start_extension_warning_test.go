//go:build linux

package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Run startup in a child test process so a regressed lock cannot leave a
// blocked goroutine in the suite. A local CDP server exercises the actual
// extension warning path while the caller holds the required Manager lock.
func TestBrowserStartupExtensionWarningDoesNotDeadlock(t *testing.T) {
	const modeEnv = "ANT_BROWSER_TEST_EXTENSION_WARNING_MODE"
	if mode := os.Getenv(modeEnv); mode != "" {
		runExtensionWarningStartupChild(t, mode)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"control", "warning"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestBrowserStartupExtensionWarningDoesNotDeadlock$", "-test.timeout=5s", "-test.v")
			child.Env = append(os.Environ(), modeEnv+"="+mode)
			output, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("startup helper failed (mode=%s context=%v error=%v):\n%s", mode, ctx.Err(), err, output)
			}
			if !strings.Contains(string(output), "startup-returned") {
				t.Fatalf("helper exited without completing startup:\n%s", output)
			}
			if mode == "warning" && !strings.Contains(string(output), "cdp-extension-load-rejected") {
				t.Fatalf("helper did not exercise the failed extension path:\n%s", output)
			}
		})
	}
}

func runExtensionWarningStartupChild(t *testing.T, mode string) {
	t.Helper()
	var server *httptest.Server
	upgrader := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Browser": "Chromium/Test", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/probe",
			})
		case "/devtools/browser/probe":
			conn, err := upgrader.Upgrade(w, request, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var command cdpMessage
			if err := conn.ReadJSON(&command); err != nil {
				return
			}
			if command.Method == cdpMethodExtensionsGet {
				_ = conn.WriteJSON(map[string]any{"id": command.Id, "result": map[string]any{"extensions": []any{}}})
				return
			}
			if command.Method == cdpMethodExtensionsLoad {
				_ = conn.WriteJSON(map[string]any{"id": command.Id, "error": map[string]any{"code": -32000, "message": "synthetic extension load failure"}})
				fmt.Println("cdp-extension-load-rejected")
				return
			}
			http.Error(w, "unexpected CDP command", http.StatusBadRequest)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	manager := browser.NewManager(cfg, t.TempDir())
	profile := &browser.Profile{ProfileId: "probe", ProfileName: "probe", RuntimeWarning: "existing runtime warning"}
	manager.Profiles[profile.ProfileId] = profile
	app := &App{config: cfg, browserMgr: manager}
	plan := &browserStartPlan{
		profile: profile, chromeBinaryPath: truePath, assignedDebugPort: port,
		startReadyTimeout: time.Second, totalReadyTimeout: time.Second, maxStartAttempts: 1,
		extensionWarning: "preparation warning",
	}
	if mode == "warning" {
		plan.extensionPaths = []string{"/synthetic/invalid-extension"}
	}
	manager.Mutex.Lock()
	result, err := app.startBrowserProfileWithPlan(browserStartInput{ProfileID: profile.ProfileId}, plan)
	snapshot := copyBrowserProfileSnapshot(result)
	manager.Mutex.Unlock()
	if err != nil || snapshot == nil {
		t.Fatalf("startup result=%v error=%v", snapshot, err)
	}
	if mode == "warning" && !strings.Contains(snapshot.RuntimeWarning, "synthetic extension load failure") {
		t.Fatalf("startup lost extension failure warning: %q", snapshot.RuntimeWarning)
	}
	if !snapshot.Running || !snapshot.DebugReady || snapshot.LastError != "" {
		t.Fatalf("extension warning changed successful startup state: %+v", snapshot)
	}
	if !strings.Contains(snapshot.RuntimeWarning, "existing runtime warning") || !strings.Contains(snapshot.RuntimeWarning, "preparation warning") {
		t.Fatalf("startup lost existing warnings: %q", snapshot.RuntimeWarning)
	}
	fmt.Println("startup-returned")
}
