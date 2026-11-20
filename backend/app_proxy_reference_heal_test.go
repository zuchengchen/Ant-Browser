package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ant-chrome/backend/internal/browser"
)

func TestBindProfileToProxyClearsProxyBindError(t *testing.T) {
	profile := &browser.Profile{
		ProfileId:      "p1",
		ProxyMode:      browser.ProxyModeFixed,
		ProxyId:        "gone",
		ProxyBindError: "代理绑定已清理：旧节点已删除",
	}
	proxy := browser.Proxy{ProxyId: "p2", ProxyName: "新节点", ProxyConfig: "socks5://127.0.0.1:1080"}

	changed := browser.BindProfileToProxy(profile, proxy, true)

	if !changed {
		t.Fatal("期望绑定变更")
	}
	if profile.ProxyBindError != "" {
		t.Fatalf("重新绑定后应清除 proxyBindError: %q", profile.ProxyBindError)
	}
	if profile.ProxyId != "p2" {
		t.Fatalf("ProxyId = %q", profile.ProxyId)
	}
}

func TestHealBindErrorPrefix(t *testing.T) {
	if !strings.Contains(proxyBindClearedPrefix, "代理绑定已清理") {
		t.Fatalf("错误文案前缀不正确: %q", proxyBindClearedPrefix)
	}
}

func TestFetchProxyWarningsRetainConfiguredRoute(t *testing.T) {
	pool := []BrowserProxy{
		{
			ProxyId:                "proxy-a",
			ProxyConfig:            "http://127.0.0.1:8080",
			SourceID:               "src-1",
			SourceURL:              "https://sub.example.com/list",
			ProxyName:              "节点A",
			SourceFetchProxyID:     "gone-proxy",
			SourceLastRefreshError: "",
		},
		{
			ProxyId:                "proxy-b",
			SourceID:               "src-1",
			SourceURL:              "https://sub.example.com/list",
			ProxyName:              "节点B",
			SourceFetchProxyID:     "proxy-a",
			SourceLastRefreshError: "",
		},
	}

	if changes := updateFetchProxyWarnings(pool); changes != 1 {
		t.Fatalf("warning changes=%d", changes)
	}
	if pool[0].SourceFetchProxyID != "gone-proxy" || pool[0].SourceLastRefreshError != missingFetchProxyWarning {
		t.Fatalf("missing fetch proxy lost its route or warning: %+v", pool[0])
	}
	if pool[1].SourceFetchProxyID != "proxy-a" || pool[1].SourceLastRefreshError != "" {
		t.Fatalf("unrelated route changed: %+v", pool[1])
	}
	if changes := updateFetchProxyWarnings(pool); changes != 0 {
		t.Fatalf("repeated reconcile changed %d rows", changes)
	}
	pool[0].SourceFetchProxyID = ""
	if changes := updateFetchProxyWarnings(pool); changes != 1 || pool[0].SourceLastRefreshError != "" {
		t.Fatal("explicit choice of direct did not clear the stale warning")
	}
}

func TestClearProfileProxyBindingClearsBindError(t *testing.T) {
	profile := &browser.Profile{
		ProxyMode:      browser.ProxyModeDynamic,
		ProxyBindError: "代理绑定已清理：旧节点已删除",
	}
	if !browser.ClearProfileProxyBinding(profile) {
		t.Fatal("期望清理变更")
	}
	if profile.ProxyBindError != "" {
		t.Fatalf("动态模式切换后应清除历史错误: %q", profile.ProxyBindError)
	}
}

func TestMissingSubscriptionProxyDoesNotDownloadDirectly(t *testing.T) {
	app := newVirtualGroupHealthTestApp(t)
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	row := BrowserProxy{ProxyId: "subscription-row", ProxyName: "fixture", ProxyConfig: "http://localhost:12345",
		SourceID: "fixture-source", SourceURL: target.URL, SourceFetchProxyID: "removed"}
	if err := app.SaveBrowserProxies([]BrowserProxy{row}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.BrowserProxyRefreshSource(row.SourceID, ""); err == nil || !strings.Contains(err.Error(), "拉取代理不存在") {
		t.Fatalf("expected missing fetch proxy, got %v", err)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("subscription endpoint received %d direct requests", n)
	}
	pool, err := app.browserMgr.ProxyDAO.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range pool {
		if saved.ProxyId == row.ProxyId && saved.SourceFetchProxyID == "removed" && saved.SourceLastRefreshError != "" {
			return
		}
	}
	t.Fatalf("configured fetch route and error missing for %s", row.ProxyId)
}
