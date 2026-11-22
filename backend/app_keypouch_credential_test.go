package backend

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ant-chrome/backend/internal/keypouch"
	"github.com/gorilla/websocket"
)

func TestKeyPouchCredentialRefreshBindsRecordAndClearsRemovedFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app, _ := startPersistenceTestApp(t)
	id := "target"
	email := "profile@example.test"
	p := app.browserMgr.Profiles[id]
	p.ProfileName = email
	claim := keyPouchOpenClaim{Action: "create-open", RecordID: 42, Email: email, CredentialSync: true}
	const route = "/api/ant/device/instance-opens/test-ticket/"
	const binding = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var response map[string]any
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			LoginReporting bool   `json:"login_reporting"`
			ID             int64  `json:"record_id"`
			Profile        string `json:"profile_id"`
			Email          string `json:"email"`
		}
		if r.Method != "POST" || r.URL.Path != route+"credentials" || r.Header.Get("X-Ant-Signature") == "" ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.LoginReporting != claim.LoginReporting || request.Profile != id || request.ID != claim.RecordID || request.Email != email {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		calls++
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	device, err := keypouch.NewDevice(server.URL, make([]byte, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	refresh := func() error {
		return app.syncKeyPouchCredential(context.Background(), device, route, server.URL, claim, id)
	}
	check := func(password, seed string) {
		t.Helper()
		c, err := app.readLocalGPTCredential(id)
		if err != nil || c == nil || c.RecordID != claim.RecordID || c.Site != server.URL || c.Email != email || c.Password != password || c.TOTPSecret != seed || claim.LoginReporting && response["login_binding"] == binding && (c.LoginBinding != binding || c.PortableCopy().LoginBinding != "") {
			t.Fatal("stored credential did not match the expected snapshot")
		}
	}
	for _, fields := range [][2]string{{"test pass", "test seed"}, {" changed password ", "changed seed"}} {
		response = map[string]any{"record_id": 42, "email": email, "password": fields[0], "totp_secret": fields[1]}
		if fields[0] == " changed password " {
			claim.LoginReporting = true
			response["login_binding"] = binding
		}
		if err := refresh(); err != nil {
			t.Fatal(err)
		}
		check(fields[0], fields[1])
	}
	for _, invalid := range []map[string]any{
		{"record_id": 41, "email": email, "password": "", "totp_secret": ""},
		{"record_id": 42, "email": "other@example.test", "password": "", "totp_secret": ""},
		{"record_id": 42, "email": email, "password": ""},
		{"record_id": 42, "email": email, "password": nil, "totp_secret": ""},
	} {
		response = invalid
		if refresh() == nil {
			t.Fatal("incomplete or mismatched response was accepted")
		}
		check(" changed password ", "changed seed")
	}
	response = map[string]any{"record_id": 42, "email": email, "password": "", "totp_secret": ""}
	other := app.browserMgr.Profiles["untouched"]
	savedDir := other.UserDataDir
	other.UserDataDir = p.UserDataDir
	if refresh() == nil {
		t.Fatal("two instances sharing a browser directory were accepted")
	}
	other.UserDataDir = savedDir
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	check("", "")
	if calls != 8 {
		t.Fatalf("signed server requests = %d, want 8", calls)
	}
}

func TestGPTCredentialDeliveryChecksProfileAndUnpackedExtension(t *testing.T) {
	const extID = "abcdefghijklmnopabcdefghijklmnop"
	for _, mode := range []string{"ok", "different profile", "different extension"} {
		t.Run(mode, func(t *testing.T) {
			profileDir, ext := t.TempDir(), t.TempDir()
			sample := localGPTCredential{RecordID: 17, Site: "https://vault.example.test", Email: "a@example.test", Password: "synthetic", TOTPSecret: "seed"}
			var writes atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/json/version" {
					_ = json.NewEncoder(w).Encode(cdpBrowserVersion{WebSocketDebuggerUrl: "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"})
					return
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var command cdpMessage
					if conn.ReadJSON(&command) != nil {
						return
					}
					result := map[string]any{}
					switch command.Method {
					case "Target.createTarget":
						result["targetId"] = "temporary-tab"
					case "Extensions.getExtensions":
						path := ext
						if mode == "different extension" {
							path = filepath.Join(ext, "different")
						}
						result["extensions"] = []map[string]string{{"id": extID, "path": path}}
					case "Runtime.evaluate":
						expr, _ := command.Params["expression"].(string)
						if strings.Contains(expr, "profile_path") {
							dir := profileDir
							if mode == "different profile" {
								dir = filepath.Join(profileDir, "different")
							}
							result["result"] = map[string]any{"value": filepath.Join(dir, "Default")}
						} else {
							result["result"] = map[string]any{"objectId": "trusted-session"}
						}
					case "Runtime.callFunctionOn":
						args, _ := command.Params["arguments"].([]any)
						if len(args) != 1 || command.Params["objectId"] != "trusted-session" {
							t.Error("missing pinned extension storage object or record")
							return
						}
						arg, _ := args[0].(map[string]any)
						fields, _ := arg["value"].(map[string]any)
						if fields["email"] != sample.Email || fields["password"] != sample.Password || fields["totpSecret"] != sample.TOTPSecret {
							t.Error("unexpected extension snapshot")
							return
						}
						writes.Add(1)
						result["result"] = map[string]any{"value": true}
					}
					if conn.WriteJSON(map[string]any{"id": command.Id, "result": result}) != nil {
						return
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := installGPTSession(ctx, server.Listener.Addr().(*net.TCPAddr).Port, profileDir, ext, &sample)
			if mode == "ok" {
				if err != nil || writes.Load() != 1 {
					t.Fatalf("verified delivery: writes=%d error=%v", writes.Load(), err)
				}
			} else if err == nil || writes.Load() != 0 {
				t.Fatal("unverified target reached the write step")
			}
		})
	}
}
