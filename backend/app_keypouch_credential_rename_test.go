package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/keypouch"
)

func prepareKeyPouchRenameRuntime(t *testing.T, app *App, p *BrowserProfile, old localGPTCredential) *retainedGPTDebug {
	t.Helper()
	if err := app.browserMgr.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	dir := app.browserMgr.ResolveUserDataDir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ext := app.resolveAppPath(filepath.Join("data", keyPouchExtensionDirectory))
	probe := testRetainedGPTDebug(t, dir, ext)
	p.Running, p.DebugReady, p.DebugPort = true, true, probe.port
	if runtime.GOOS == "windows" {
		p.Pid = 1
	}
	if err := app.saveLocalGPTCredential(p.ProfileId, old); err != nil {
		t.Fatal(err)
	}
	if err := app.publishGPTCredentialLocked(context.Background(), p, []string{ext}, &old); err != nil {
		t.Fatal(err)
	}
	return probe
}

func TestKeyPouchOpenRenamedAccountReplacesOnlyItsSavedAndRunningLogin(t *testing.T) {
	for _, mode := range []string{"sync", "refused", "open only", "name already stored", "orphaned live session", "same account refused"} {
		t.Run(mode, func(t *testing.T) {
			isolateKeyPouchUser(t)
			app, db := startPersistenceTestApp(t)
			m := app.browserMgr
			m.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())
			m.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
			server := httptest.NewUnstartedServer(nil)
			site := "http://" + server.Listener.Addr().String()
			old := testLocalGPTCredential()
			old.Site = site
			p := m.Profiles["target"]
			claim := keyPouchOpenClaim{Action: "create-open", RecordID: old.RecordID, Email: "new@example.test",
				GroupID: 1, GroupName: p.GroupId, CredentialSync: mode != "open only"}
			if mode == "same account refused" {
				claim.Email = old.Email
			}
			p.ProfileName = old.Email
			if mode == "name already stored" || mode == "orphaned live session" {
				p.ProfileName = claim.Email
			}
			if err := m.ProfileDAO.Upsert(p); err != nil {
				t.Fatal(err)
			}
			probe := prepareKeyPouchRenameRuntime(t, app, p, old)
			if mode == "orphaned live session" {
				if err := keypouch.RemoveLocalGPTCredential(app.appRoot, p.ProfileId); err != nil {
					t.Fatal(err)
				}
			}
			if err := app.saveLocalGPTCredential("untouched", old); err != nil {
				t.Fatal(err)
			}
			device, err := app.keyPouchDevice(site, true)
			if err != nil {
				t.Fatal(err)
			}
			links, err := device.LoadProfileLinks()
			if err != nil || links.Bind(old.RecordID, p.ProfileId) != nil {
				t.Fatal("could not bind the selected test profile")
			}
			var requests atomic.Int32
			type receipt struct {
				ProfileID string  `json:"profile_id"`
				Opened    bool    `json:"opened"`
				Status    *string `json:"credential_status"`
			}
			complete := make(chan receipt, 1)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("X-Ant-Signature") == "" {
					t.Error("device request was not signed")
				}
				switch strings.TrimPrefix(r.URL.Path, "/api/ant/device/instance-opens/test-ticket/") {
				case "claim":
					_ = json.NewEncoder(w).Encode(claim)
				case "credentials":
					requests.Add(1)
					retained := claim.Email == old.Email
					if has, err := app.BrowserProfileHasLocalGPTCredential(p.ProfileId); err != nil || has != retained || probe.hasSession.Load() != retained {
						t.Error("old local or live account was still available when the credential request began")
					}
					var query struct {
						Profile string `json:"profile_id"`
						Record  int64  `json:"record_id"`
						Email   string `json:"email"`
					}
					if json.NewDecoder(r.Body).Decode(&query) != nil || query.Profile != p.ProfileId || query.Record != old.RecordID || query.Email != claim.Email {
						t.Error("wrong credential target")
					}
					if mode != "sync" {
						http.Error(w, "forbidden", http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"record_id": old.RecordID, "email": claim.Email, "password": "updated synthetic pass", "totp_secret": ""})
				case "complete":
					var got receipt
					if json.NewDecoder(r.Body).Decode(&got) != nil {
						t.Error("invalid open receipt")
					}
					complete <- got
				default:
					t.Error("unexpected open callback")
					http.NotFound(w, r)
				}
			})
			server.Start()
			t.Cleanup(server.Close)
			if err := app.openKeyPouchRecord(context.Background(), site, "test-ticket", func(int) bool { return true }); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-complete:
				if got.ProfileID != p.ProfileId || !got.Opened || (got.Status != nil) != claim.CredentialSync {
					t.Fatal("wrong open receipt")
				}
				if got.Status != nil {
					expected := "failed"
					if mode == "sync" {
						expected = "synced"
					}
					if *got.Status != expected {
						t.Fatal("wrong credential delivery receipt")
					}
				}
			default:
				t.Fatal("missing completed open")
			}
			if (requests.Load() == 1) != claim.CredentialSync || requests.Load() > 1 || p.ProfileName != claim.Email {
				t.Fatal("unexpected credential request count or instance name")
			}
			stored, err := app.readLocalGPTCredential(p.ProfileId)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "sync":
				if stored == nil || stored.Email != claim.Email || stored.Password != "updated synthetic pass" || stored.TOTPSecret != "" ||
					probe.email.Load() != claim.Email || probe.writes.Load() != 2 || probe.clears.Load() != 1 {
					t.Fatal("renamed account was not delivered as a complete snapshot")
				}
			case "same account refused":
				if stored == nil || *stored != old || probe.email.Load() != old.Email || probe.clears.Load() != 0 {
					t.Fatal("unchanged account lost its existing login")
				}
			default:
				if stored != nil || probe.hasSession.Load() || probe.clears.Load() != 1 {
					t.Fatal("old login survived an unavailable or unrequested refresh")
				}
			}
			if got, err := app.readLocalGPTCredential("untouched"); err != nil || got == nil || *got != old {
				t.Fatal("another profile's independent copy was changed")
			}
		})
	}
}

func TestKeyPouchRenameRevokesOnlyVerifiedRetainedRuntimeAndCanRollBack(t *testing.T) {
	isolateKeyPouchUser(t)
	app, db := startPersistenceTestApp(t)
	m := app.browserMgr
	m.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
	p := m.Profiles["target"]
	old := testLocalGPTCredential()
	claim := keyPouchOpenClaim{Email: "replacement@example.test", RecordID: old.RecordID}
	p.ProfileName = old.Email
	probe := prepareKeyPouchRenameRuntime(t, app, p, old)
	if err := app.publishOpenedGPTCredential(context.Background(), p.ProfileId, old.Site, claim); err == nil {
		t.Fatal("receipt accepted a display name different from the signed claim")
	}
	p.ProfileName = claim.Email
	if err := app.publishOpenedGPTCredential(context.Background(), p.ProfileId, old.Site, claim); err == nil {
		t.Fatal("receipt accepted a credential that still belonged to the previous email")
	}
	// The retained process is reachable via this directory's browser UUID, but
	// its port is no longer in the profile after an Ant restart.
	p.Running, p.DebugPort, p.Pid = false, 0, 0
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	ctx := context.Background()
	otherRecord := claim
	otherRecord.RecordID++
	if _, err := app.retireRenamedKeyPouchCredentialLocked(ctx, p, old.Site, otherRecord); err == nil {
		t.Fatal("accepted another record")
	}
	if _, err := app.retireRenamedKeyPouchCredentialLocked(ctx, p, "https://different.example.test", claim); err == nil {
		t.Fatal("accepted another site")
	}
	if undo, err := app.retireRenamedKeyPouchCredentialLocked(ctx, m.Profiles["untouched"], old.Site, claim); err != nil || undo != nil {
		t.Fatal("an ordinary instance without a saved account required a handoff")
	}
	if probe.tabs.Load() != 1 {
		t.Fatal("an unrelated change opened an extension handoff")
	}
	probe.wrongDir.Store(true)
	if _, err := app.retireRenamedKeyPouchCredentialLocked(ctx, p, old.Site, claim); err == nil || probe.clears.Load() != 0 ||
		probe.tabs.Load() != 2 || probe.writes.Load() != 1 {
		t.Fatal("a different browser profile received a storage operation")
	}
	if got, err := app.readLocalGPTCredential(p.ProfileId); err != nil || got == nil || *got != old {
		t.Fatal("failed handoff removed the old login")
	}
	probe.wrongDir.Store(false)
	probe.loseClearReply.Store(true)
	if _, err := app.retireRenamedKeyPouchCredentialLocked(ctx, p, old.Site, claim); err == nil ||
		probe.email.Load() != old.Email || probe.writes.Load() != 2 {
		t.Fatal("a lost removal reply did not restore the verified old login")
	}
	restore, err := app.retireRenamedKeyPouchCredentialLocked(ctx, p, old.Site, claim)
	if err != nil || restore == nil || probe.hasSession.Load() || probe.clears.Load() != 2 {
		t.Fatal("retained browser was not cleared before rename")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got, err := app.readLocalGPTCredential(p.ProfileId); err != nil || got == nil || *got != old ||
		probe.email.Load() != old.Email || probe.writes.Load() != 3 || probe.closed.Load() != probe.tabs.Load() {
		t.Fatal("failed database commit did not restore both verified logins")
	}
}
