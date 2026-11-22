package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"ant-chrome/backend/internal/browser"
	"github.com/google/uuid"
)

func isolateKeyPouchUser(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	bundled := bundledKeyPouchURL
	t.Cleanup(func() { bundledKeyPouchURL = bundled })
	bundledKeyPouchURL = ""
}

func TestKeyPouchSiteSwitchCancelsReporterAndRestoresPairedFallback(t *testing.T) {
	isolateKeyPouchUser(t)
	type requests struct {
		started   chan string
		cancelled chan struct{}
	}
	server := func() (*httptest.Server, requests) {
		events := requests{make(chan string, 3), make(chan struct{}, 3)}
		stop := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/ant/device/instance-runtime/bindings" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			events.started <- r.Header.Get("X-Ant-Device")
			select {
			case <-r.Context().Done():
				events.cancelled <- struct{}{}
			case <-stop:
			}
		}))
		t.Cleanup(func() { close(stop); srv.Close() })
		return srv, events
	}
	started := func(site requests) string {
		t.Helper()
		select {
		case id := <-site.started:
			if id == "" {
				t.Fatal("runtime request omitted its device identity")
			}
			return id
		case <-time.After(3 * time.Second):
			t.Fatal("reporter did not start")
			return ""
		}
	}
	cancelled := func(site requests) {
		t.Helper()
		select {
		case <-site.cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("in-flight request to the old site was not cancelled")
		}
	}
	original, originalRequests := server()
	saved, savedRequests := server()
	unpaired, unpairedRequests := server()
	t.Setenv("ANT_KEYPOUCH_URL", original.URL+"/")
	app := NewApp(t.TempDir())
	for _, site := range []string{original.URL, saved.URL} {
		if _, err := app.keyPouchDevice(site, true); err != nil {
			t.Fatal(err)
		}
	}
	app.setRuntimeContext(context.Background())
	t.Cleanup(func() { app.stopRuntimeEvents(); app.waitBackgroundTasks() })
	app.restoreKeyPouchRuntime()
	firstID := started(originalRequests)
	if _, err := app.SaveKeyPouchSettings(unpaired.URL); err != nil {
		t.Fatal(err)
	}
	cancelled(originalRequests)
	app.waitBackgroundTasks()
	if _, err := app.keyPouchDevice(unpaired.URL, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saving an unpaired site created an identity: %v", err)
	}
	select {
	case <-unpairedRequests.started:
		t.Fatal("unpaired site received a heartbeat")
	default:
	}
	if _, err := app.SaveKeyPouchSettings(saved.URL); err != nil {
		t.Fatal(err)
	}
	if started(savedRequests) == firstID {
		t.Fatal("another site received the previous site's device identity")
	}
	if _, err := app.SaveKeyPouchSettings(""); err != nil {
		t.Fatal(err)
	}
	cancelled(savedRequests)
	if got := started(originalRequests); got != firstID {
		t.Fatal("clearing the setting did not resume the original device identity")
	}
}

func TestKeyPouchSavedSiteOpensWithoutRestartAndPreservesLegacyMapping(t *testing.T) {
	isolateKeyPouchUser(t)
	app, db := startPersistenceTestApp(t)
	app.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
	const record = 76
	const ticket = "test-ticket"
	type finished struct {
		ProfileID string `json:"profile_id"`
		Opened    bool   `json:"opened"`
	}
	type claimPause struct{ entered, resume chan struct{} }
	pause := make(chan claimPause, 1)
	newSite := func(email string) (*httptest.Server, chan finished) {
		complete := make(chan finished, 3)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prefix := "/api/ant/device/instance-opens/" + ticket + "/"
			switch r.URL.Path {
			case prefix + "claim":
				select {
				case step := <-pause:
					close(step.entered)
					select {
					case <-step.resume:
					case <-r.Context().Done():
						return
					}
				default:
				}
				if r.Header.Get("X-Ant-Device") == "" {
					http.Error(w, "missing identity", http.StatusUnauthorized)
					return
				}
				json.NewEncoder(w).Encode(keyPouchOpenClaim{Action: "create-open", RecordID: record, Email: email, GroupID: 1, GroupName: "original-group"})
			case prefix + "complete":
				var result finished
				if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
					t.Error(err)
				}
				complete <- result
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		return srv, complete
	}
	const oldEmail = "original@example.invalid"
	const newEmail = "saved@example.invalid"
	oldSite, oldCompleted := newSite(oldEmail)
	newSiteServer, newCompleted := newSite(newEmail)
	t.Setenv("ANT_KEYPOUCH_URL", oldSite.URL+"/")
	debug := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/list":
			io.WriteString(w, `[{"type":"page","id":"tab"}]`)
		case "/json/activate/tab":
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(debug.Close)
	addRunning := func(id, email string) {
		p := &browser.Profile{ProfileId: id, ProfileName: email, UserDataDir: id, GroupId: "original-group"}
		if err := app.browserMgr.ProfileDAO.Upsert(p); err != nil {
			t.Fatal(err)
		}
		p.Running, p.DebugReady = true, true
		p.DebugPort = debug.Listener.Addr().(*net.TCPAddr).Port
		if runtime.GOOS == "windows" {
			p.Pid = 1
		}
		app.browserMgr.Profiles[id] = p
	}
	legacy, err := app.keyPouchDevice(oldSite.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	links, err := legacy.LoadProfileLinks()
	if err != nil {
		t.Fatal(err)
	}
	const adoptedID = "legacy-local-profile"
	if err := links.Bind(record, adoptedID); err != nil {
		t.Fatal(err)
	}
	addRunning(adoptedID, oldEmail)
	if _, err = app.keyPouchDevice(newSiteServer.URL, true); err != nil {
		t.Fatal(err)
	}
	derived := uuid.NewSHA1(uuid.NameSpaceURL, []byte(newSiteServer.URL+"/records/76")).String()
	addRunning(derived, newEmail)

	open := func() error {
		return app.handleKeyPouchAction(context.Background(), "open", ticket, func(int) bool { return true })
	}
	checkComplete := func(channel chan finished, want string) {
		t.Helper()
		select {
		case result := <-channel:
			if !result.Opened || result.ProfileID != want {
				t.Fatalf("wrong site or profile mapping: %+v", result)
			}
		default:
			t.Fatal("open was not acknowledged on the expected site")
		}
	}
	// The old site's claim is already in flight while the setting changes.
	// Its profile resolution and completion still belong to the same site.
	step := claimPause{make(chan struct{}), make(chan struct{})}
	pause <- step
	opened := make(chan error, 1)
	go func() { opened <- open() }()
	select {
	case <-step.entered:
	case <-time.After(3 * time.Second):
		close(step.resume)
		t.Fatal("old site never received the claim")
	}
	switched := make(chan error, 1)
	switching := make(chan struct{})
	go func() {
		close(switching)
		_, err := app.SaveKeyPouchSettings(newSiteServer.URL)
		switched <- err
	}()
	<-switching
	close(step.resume)
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	checkComplete(oldCompleted, adoptedID)
	if err := open(); err != nil {
		t.Fatal(err)
	}
	checkComplete(newCompleted, derived)
	if _, err = app.SaveKeyPouchSettings(""); err != nil {
		t.Fatal(err)
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	checkComplete(oldCompleted, adoptedID)
	t.Setenv("ANT_KEYPOUCH_URL", "")
	if err := app.handleKeyPouchAction(context.Background(), "open", ticket, nil); err == nil || !strings.Contains(err.Error(), "系统设置 → 基础设置 → KeyPouch 站点地址") || !strings.Contains(err.Error(), "保存") {
		t.Fatalf("missing site did not direct user to settings: %v", err)
	}
}
