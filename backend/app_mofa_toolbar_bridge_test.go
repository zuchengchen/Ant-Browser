package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
