package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ant-chrome/backend/internal/browser"
	"golang.org/x/sync/errgroup"
)

const (
	mofaToolbarPath    = "/mofa/import"
	mofaToolbarVersion = "1.0.8"
)

type mofaToolbarSource struct {
	site, profileDir, socket string
	port                     int
}

type mofaToolbarBridge struct {
	server       *http.Server
	port         int
	host         string
	origin       string
	extensionID  string
	extensionDir string
	claims       chan struct{}
}

func (a *App) startMofaToolbarBridge() error {
	dir := a.resolveAppPath(filepath.Join("data", mofaExtensionDirectory))
	if err := browser.VerifyMofaImportFiles(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // The bundled extension may have been replaced.
		}
		return err
	}
	extID, err := browser.UnpackedExtensionID(dir)
	if err != nil {
		return err
	}
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	if a.mofaToolbarBridge != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := listener.Addr().(*net.TCPAddr)
	bridge := &mofaToolbarBridge{
		port: address.Port, host: address.String(), origin: "chrome-extension://" + extID,
		extensionID: extID, extensionDir: dir, claims: make(chan struct{}, 4),
	}
	bridge.server = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { bridge.handle(a, w, r) }),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: mofaImportTimeout + 30*time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4 << 10,
	}
	a.mofaToolbarBridge = bridge
	if err := a.syncSavedMofaExtensionSiteLocked(); err != nil {
		a.mofaToolbarBridge = nil
		_ = listener.Close()
		return err
	}
	go func() { _ = bridge.server.Serve(listener) }()
	return nil
}

func (a *App) stopMofaToolbarBridge() {
	a.mofaSiteMu.Lock()
	defer a.mofaSiteMu.Unlock()
	if bridge := a.mofaToolbarBridge; bridge != nil {
		a.mofaToolbarBridge = nil
		// Core jobs are tracked by the app, independently of the HTTP client.
		_ = bridge.server.Close()
		_ = a.syncSavedMofaExtensionSiteLocked()
	}
}

func (b *mofaToolbarBridge) handle(a *App, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.RequestURI != mofaToolbarPath {
		http.NotFound(w, r)
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	origins := r.Header.Values("Origin")
	if err != nil || peer != "127.0.0.1" || r.Host != b.host || len(origins) != 1 || origins[0] != b.origin {
		writeMofaToolbarReply(w, http.StatusForbidden, mofaToolbarReply{Error: "请从当前 Ant 实例的 MOFA 工具栏重新发起。"})
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", b.origin)
	w.Header().Set("Vary", "Origin")
	if r.Method == http.MethodOptions {
		if r.Header.Get("Access-Control-Request-Method") != http.MethodPost ||
			!strings.EqualFold(strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers")), "content-type") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", http.MethodPost)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	request, digest, valid := readMofaToolbarRequest(w, r)
	if !valid {
		writeMofaToolbarReply(w, http.StatusBadRequest, mofaToolbarReply{Error: "MOFA 工具栏请求无效，请重新发起。"})
		return
	}
	if !a.IsAppReady() {
		writeMofaToolbarReply(w, http.StatusServiceUnavailable, mofaToolbarReply{Error: "请等待 Ant 浏览器启动完成后重试。"})
		return
	}
	select {
	case b.claims <- struct{}{}:
	default:
		writeMofaToolbarReply(w, http.StatusTooManyRequests, mofaToolbarReply{Error: "MOFA 工具栏正在验证实例，请稍候重试。"})
		return
	}
	profileID, source := b.claim(r.Context(), a, request.Site, digest)
	<-b.claims
	if profileID == "" || r.Context().Err() != nil {
		writeMofaToolbarReply(w, http.StatusForbidden, mofaToolbarReply{Error: "无法确认 MOFA 工具栏所属的运行实例，请重开工具栏后重试。"})
		return
	}
	result, err := a.browserMofaImport(profileID, &source)
	if err != nil {
		writeMofaToolbarReply(w, http.StatusConflict, mofaToolbarReply{Error: err.Error()})
		return
	}
	response := map[string]interface{}{
		"status": result["status"], "accountId": result["accountId"], "groupId": result["groupId"],
		"inTargetGroup": result["inTargetGroup"],
	}
	if schedulable, ok := result["schedulable"].(bool); ok {
		response["schedulable"] = schedulable
	}
	writeMofaToolbarReply(w, http.StatusOK, mofaToolbarReply{Result: response})
}

type mofaToolbarRequest struct {
	Site  string `json:"site"`
	Proof string `json:"proof"`
}

func readMofaToolbarRequest(w http.ResponseWriter, r *http.Request) (mofaToolbarRequest, string, bool) {
	var request mofaToolbarRequest
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return request, "", false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || len(request.Proof) != 64 {
		return request, "", false
	}
	site, err := normalizeMofaSite(request.Site)
	if err != nil || site == "" || site != request.Site {
		return request, "", false
	}
	raw, err := hex.DecodeString(request.Proof)
	if err != nil {
		return request, "", false
	}
	digest := sha256.Sum256(raw)
	return request, hex.EncodeToString(digest[:]), true
}

type mofaToolbarReply struct {
	Result map[string]interface{} `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

func writeMofaToolbarReply(w http.ResponseWriter, status int, reply mofaToolbarReply) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(reply)
}

type mofaToolbarTarget struct {
	profileID, profileDir, tabID string
	port                         int
}

func (a *App) mofaToolbarTargets() []mofaToolbarTarget {
	manager := a.browserMgr
	if manager == nil {
		return nil
	}
	manager.Mutex.Lock()
	defer manager.Mutex.Unlock()
	var targets []mofaToolbarTarget
	for id, profile := range manager.Profiles {
		if profile != nil && profile.DeletedAt == "" && profile.Running && profile.DebugReady && profile.DebugPort > 0 {
			targets = append(targets, mofaToolbarTarget{profileID: id, profileDir: manager.ResolveUserDataDir(profile), port: profile.DebugPort})
		}
	}
	return targets
}

func (b *mofaToolbarBridge) claim(parent context.Context, a *App, site, digest string) (string, mofaToolbarSource) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := browser.VerifyMofaImportFiles(b.extensionDir); err != nil {
		return "", mofaToolbarSource{}
	}
	// Only the digest is visible in the tab URL. The random preimage travels
	// over loopback and authorization consumes the toolbar-issued challenge.
	tabURL := b.origin + "/import.html#ant-mofa-" + digest
	var workers errgroup.Group
	workers.SetLimit(8)
	var mu sync.Mutex
	var matches []mofaToolbarTarget
	for _, target := range a.mofaToolbarTargets() {
		if ctx.Err() != nil {
			break
		}
		workers.Go(func() error {
			var tabs []struct{ ID, URL, Type, Subtype string }
			if fetchBrowserDebugJSONContext(ctx, keyPouchDebugClient, target.port, "/json/list", &tabs) == nil {
				for _, tab := range tabs {
					if tab.Type == "page" && tab.Subtype == "" && tab.URL == tabURL && mofaTargetID.MatchString(tab.ID) {
						mu.Lock()
						target.tabID = tab.ID
						matches = append(matches, target)
						mu.Unlock()
					}
				}
			}
			return nil
		})
	}
	_ = workers.Wait()
	if ctx.Err() != nil || len(matches) != 1 {
		return "", mofaToolbarSource{}
	}
	target := matches[0]
	instance, err := connectMofaBrowser(ctx, target.port)
	if err != nil || !b.claimDocument(ctx, instance, target.tabID, tabURL, digest) {
		return "", mofaToolbarSource{}
	}
	return target.profileID, mofaToolbarSource{site: site, port: target.port, profileDir: target.profileDir, socket: instance.socket}
}

func (b *mofaToolbarBridge) claimDocument(ctx context.Context, instance mofaBrowser, tabID, tabURL, digest string) bool {
	control, closeControl, err := chatGPTCDP(ctx, chatGPTPageTarget{Socket: instance.socket})
	if err != nil {
		return false
	}
	defer closeControl()
	target, err := control("Target.getTargetInfo", map[string]any{"targetId": tabID})
	info, _ := target["targetInfo"].(map[string]any)
	if err != nil || info["targetId"] != tabID || info["url"] != tabURL || info["type"] != "page" || info["subtype"] != nil {
		return false
	}
	extensions, err := control(cdpMethodExtensionsGet, nil)
	paths := extensions["extensions"]
	if err != nil {
		// Older engines expose unpacked paths through the built-in manager.
		tab, closeTab, err := instance.openBackgroundTab(ctx)
		if err != nil {
			return false
		}
		defer closeTab()
		page, disconnect, err := chatGPTCDP(ctx, tab)
		if err != nil {
			return false
		}
		defer disconnect()
		paths, err = readBrowserExtensionPaths(ctx, page)
		if err != nil {
			return false
		}
	}
	if gptExtensionAt(paths, b.extensionDir) != b.extensionID {
		return false
	}
	page, closePage, err := chatGPTCDP(ctx, chatGPTPageTarget{Socket: "ws://" + instance.address + "/devtools/page/" + tabID})
	if err != nil {
		return false
	}
	defer closePage()
	expr := fmt.Sprintf(`window === window.top && location.href === %q && chrome.runtime?.id === %q &&
		chrome.runtime.getManifest().version === %q && chrome.extension?.inIncognitoContext === false &&
		globalThis.antMofaClaimBridge?.(%q) === true`, tabURL, b.extensionID, mofaToolbarVersion, digest)
	response, err := page("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	value, _ := response["result"].(map[string]any)
	return err == nil && value["value"] == true
}
