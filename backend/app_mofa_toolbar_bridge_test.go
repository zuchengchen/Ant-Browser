package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ant-chrome/backend/internal/browser"
)

func TestMofaToolbarBridgeRequestBoundary(t *testing.T) {
	const site = "https://mofa.example.test"
	proof := strings.Repeat("ab", 32)
	valid := `{"site":"` + site + `","proof":"` + proof + `"}`
	bridge := mofaToolbarBridge{host: "127.0.0.1:17737", origin: "chrome-extension://" + strings.Repeat("a", 32)}
	for _, test := range []struct {
		name, method, path, body string
		alter                    func(*http.Request)
		status                   int
		allowOrigin              bool
	}{
		{name: "only the fixed endpoint", path: "/other", status: http.StatusNotFound},
		{name: "no query or alternate route", path: mofaToolbarPath + "?account=42", status: http.StatusNotFound},
		{name: "reject remote peer", alter: func(r *http.Request) { r.RemoteAddr = "192.0.2.8:40123" }, status: http.StatusForbidden},
		{name: "reject wrong Host", alter: func(r *http.Request) { r.Host = "rebound.example.test" }, status: http.StatusForbidden},
		{name: "reject missing Origin", alter: func(r *http.Request) { r.Header.Del("Origin") }, status: http.StatusForbidden},
		{name: "reject a website Origin", alter: func(r *http.Request) { r.Header.Set("Origin", site) }, status: http.StatusForbidden},
		{name: "reject multiple Origins", alter: func(r *http.Request) { r.Header.Add("Origin", site) }, status: http.StatusForbidden},
		{name: "only POST or OPTIONS", method: http.MethodGet, status: http.StatusMethodNotAllowed, allowOrigin: true},
		{name: "reject unknown target input", body: `{"url":"https://external.example.test","site":"` + site + `","proof":"` + proof + `"}`, status: http.StatusBadRequest, allowOrigin: true},
		{name: "reject unknown action", body: strings.TrimSuffix(valid, "}") + `,"action":"other"}`, status: http.StatusBadRequest, allowOrigin: true},
		{name: "reject malformed proof", body: strings.Replace(valid, proof, strings.Repeat("gg", 32), 1), status: http.StatusBadRequest, allowOrigin: true},
		{name: "reject trailing JSON", body: valid + `{"extra":true}`, status: http.StatusBadRequest, allowOrigin: true},
		{name: "reject oversized body", body: strings.Repeat(" ", 2048) + valid, status: http.StatusBadRequest, allowOrigin: true},
		{name: "meet startup gate first", body: valid, status: http.StatusServiceUnavailable, allowOrigin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			method, path := test.method, test.path
			if method == "" {
				method = http.MethodPost
			}
			if path == "" {
				path = mofaToolbarPath
			}
			r := httptest.NewRequest(method, path, strings.NewReader(test.body))
			r.Host, r.RemoteAddr = bridge.host, "127.0.0.1:40123"
			r.Header.Set("Origin", bridge.origin)
			r.Header.Set("Content-Type", "application/json")
			if test.alter != nil {
				test.alter(r)
			}
			w := httptest.NewRecorder()
			bridge.handle(nil, w, r)
			allowOrigin := w.Header().Get("Access-Control-Allow-Origin")
			if w.Code != test.status || (allowOrigin == bridge.origin) != test.allowOrigin || strings.Contains(w.Body.String(), proof) {
				t.Fatalf("toolbar boundary returned %d with allow-origin %q", w.Code, allowOrigin)
			}
		})
	}
	r := httptest.NewRequest(http.MethodOptions, mofaToolbarPath, nil)
	r.Host, r.RemoteAddr = bridge.host, "127.0.0.1:40123"
	r.Header.Set("Origin", bridge.origin)
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	r.Header.Set("Access-Control-Request-Headers", "content-type")
	r.Header.Set("Access-Control-Request-Private-Network", "true")
	w := httptest.NewRecorder()
	bridge.handle(nil, w, r)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != bridge.origin ||
		w.Header().Get("Access-Control-Allow-Headers") != "Content-Type" || w.Header().Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("toolbar preflight returned %d with headers %v", w.Code, w.Header())
	}
	r = httptest.NewRequest(http.MethodPost, mofaToolbarPath, strings.NewReader(valid))
	r.Header.Set("Content-Type", "application/json")
	request, digest, ok := readMofaToolbarRequest(httptest.NewRecorder(), r)
	bytes, _ := hex.DecodeString(proof)
	sha := sha256.Sum256(bytes)
	if !ok || request.Site != site || digest != hex.EncodeToString(sha[:]) {
		t.Fatal("toolbar proof was not reduced to the browser's digest")
	}
}

func TestMofaToolbarBridgeFocusOnlyItsActiveJob(t *testing.T) {
	const site = "https://mofa.example.test"
	proofBytes := make([]byte, 32)
	proof := hex.EncodeToString(proofBytes)
	sha := sha256.Sum256(proofBytes)
	digest := hex.EncodeToString(sha[:])
	bridge := mofaToolbarBridge{host: "127.0.0.1:17737", origin: "chrome-extension://" + strings.Repeat("a", 32)}
	app := &App{}
	app.setRuntimeContext(context.Background())
	defer app.stopRuntimeEvents()
	app.startupReady.Store(true)
	requestFocus := func(site, proof, origin string, expected int) {
		t.Helper()
		body := `{"site":"` + site + `","proof":"` + proof + `","action":"focus_oauth"}`
		r := httptest.NewRequest(http.MethodPost, mofaToolbarPath, strings.NewReader(body))
		r.Host, r.RemoteAddr = bridge.host, "127.0.0.1:40123"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		bridge.handle(app, w, r)
		if w.Code != expected || strings.Contains(w.Body.String(), proof) {
			t.Fatalf("focus returned status %d, want %d", w.Code, expected)
		}
	}
	requestFocus(site, proof, bridge.origin, http.StatusConflict)
	var shown int
	func() {
		job, finish, err := app.beginMofaImport("first", &mofaToolbarSource{site: site, digest: digest})
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		_, finishOther, err := app.beginMofaImport("other", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer finishOther()
		requestFocus(site, proof, bridge.origin, http.StatusConflict)
		job.showAuthorization = func(context.Context) error {
			shown++
			return nil
		}
		requestFocus(site, proof, "https://other.example.test", http.StatusForbidden)
		requestFocus(site, strings.Repeat("ab", 32), bridge.origin, http.StatusConflict)
		requestFocus("https://other.example.test", proof, bridge.origin, http.StatusConflict)
		requestFocus(site, proof, bridge.origin, http.StatusNoContent)
		if !errors.Is(app.BrowserMofaImportShowAuthorization("other"), errMofaNoAuthorization) {
			t.Fatal("another profile could open the active authorization page")
		}
		if err := app.BrowserMofaImportShowAuthorization("first"); err != nil {
			t.Fatal(err)
		}
		app.BrowserMofaImportCancel("first")
		requestFocus(site, proof, bridge.origin, http.StatusConflict)
	}()
	requestFocus(site, proof, bridge.origin, http.StatusConflict)
	if shown != 2 {
		t.Fatalf("opened the authorization page %d times, want 2", shown)
	}
}

func TestMofaToolbarImportStreamsProgressBeforeResult(t *testing.T) {
	w := httptest.NewRecorder()
	serveMofaToolbarImport(w, true, func(progress func(string)) (map[string]interface{}, error) {
		progress("checking")
		progress(mofaOAuthWaitingPhase)
		progress(mofaOAuthLoginRequiredPhase)
		progress("unknown")
		if !w.Flushed || w.Body.String() != "{\"phase\":\"checking\"}\n{\"phase\":\"waiting_for_oauth\"}\n{\"phase\":\"login_required\"}\n" {
			t.Fatal("the status page did not receive progress during the import")
		}
		return map[string]interface{}{"status": "created", "accountId": 1, "groupId": 2, "inTargetGroup": true, "extra": "omit me"}, nil
	})
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != mofaToolbarStream || strings.Contains(w.Body.String(), "omit me") ||
		!strings.HasSuffix(w.Body.String(), "{\"result\":{\"accountId\":1,\"groupId\":2,\"inTargetGroup\":true,\"status\":\"created\"}}\n") {
		t.Fatalf("unexpected streamed reply: %s", w.Body.String())
	}
	for _, stream := range []bool{false, true} {
		w := httptest.NewRecorder()
		serveMofaToolbarImport(w, stream, func(progress func(string)) (map[string]interface{}, error) {
			if (progress != nil) != stream {
				t.Fatal("only stream requests should subscribe to progress")
			}
			return nil, errors.New("import failed")
		})
		status, media := http.StatusConflict, "application/json"
		if stream {
			status, media = http.StatusOK, mofaToolbarStream
		}
		if w.Code != status || w.Header().Get("Content-Type") != media || w.Body.String() != "{\"error\":\"import failed\"}\n" {
			t.Fatalf("unexpected terminal error response for stream=%t", stream)
		}
	}
}

type disconnectedMofaToolbarWriter struct{ *httptest.ResponseRecorder }

func (w disconnectedMofaToolbarWriter) FlushError() error { return io.ErrClosedPipe }

func TestMofaToolbarImportContinuesAfterStatusPageDisconnects(t *testing.T) {
	w := disconnectedMofaToolbarWriter{httptest.NewRecorder()}
	serveMofaToolbarImport(w, true, func(progress func(string)) (map[string]interface{}, error) {
		progress("checking")
		progress(mofaOAuthLoginRequiredPhase)
		return nil, errors.New("import finished")
	})
	if got := w.Body.String(); got != "{\"phase\":\"checking\"}\n" {
		t.Fatalf("response was written after disconnect: %s", got)
	}
}

func TestMofaToolbarBridgePublishesOnlyLocalAddressAndSite(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	app, db := startPersistenceTestApp(t)
	app.browserMgr.ExtensionDAO = browser.NewSQLiteExtensionDAO(db.GetConn())
	if err := app.browserMgr.SyncBuiltinExtensions(); err != nil {
		t.Fatal(err)
	}
	const site = "https://mofa.example.test"
	if _, err := app.SaveMofaSite(site); err != nil {
		t.Fatal(err)
	}
	if err := app.startMofaToolbarBridge(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.stopMofaToolbarBridge)
	bridge := app.mofaToolbarBridge
	if bridge == nil || !strings.HasPrefix(bridge.host, "127.0.0.1:") || bridge.port <= 0 {
		t.Fatal("toolbar bridge did not use an ephemeral loopback port")
	}
	manifest, err := os.ReadFile(filepath.Join(bridge.extensionDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var version struct{ Version string }
	if err := json.Unmarshal(manifest, &version); err != nil || version.Version != mofaToolbarVersion {
		t.Fatal("toolbar manifest did not match the version verified by the bridge")
	}
	path := filepath.Join(bridge.extensionDir, "site.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil || len(config) != 2 || config["site"] != site || config["bridgePort"] != float64(bridge.port) {
		t.Fatal("unpacked config did not contain only the origin and loopback port")
	}
	app.stopMofaToolbarBridge()
	assertMofaExtensionSite(t, path, site)
	if err := os.WriteFile(filepath.Join(bridge.extensionDir, "page.js"), []byte("// modified installation"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.startMofaToolbarBridge(); err == nil || app.mofaToolbarBridge != nil {
		t.Fatal("a modified unpacked extension was allowed to start the bridge")
	}
}
