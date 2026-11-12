package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ant-chrome/backend/internal/browser"
)

// Use the same SQLite-backed proxy/group/state DAOs as the application, with
// three local HTTP members whose responses identify the selected member.
func newVirtualGroupPreviewTestApp(t *testing.T, strategy string) (*App, BrowserProxyVirtualGroup, map[string]string) {
	t.Helper()
	app := newVirtualGroupHealthTestApp(t)
	configs := make(map[string]string)
	proxies := make([]BrowserProxy, 0, 3)
	for _, id := range []string{"proxy-a", "proxy-b", "proxy-c"} {
		id := id
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !r.URL.IsAbs() {
				http.Error(w, "expected HTTP proxy request", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, id)
		}))
		t.Cleanup(server.Close)
		configs[id] = server.URL
		proxies = append(proxies, BrowserProxy{ProxyId: id, ProxyName: id, ProxyConfig: server.URL})
	}
	if err := app.browserMgr.ProxyDAO.ReplaceAll(proxies); err != nil {
		t.Fatalf("seed proxies: %v", err)
	}
	for _, proxy := range proxies {
		seedHealthyProxy(t, app, proxy.ProxyId)
	}
	group := BrowserProxyVirtualGroup{
		GroupId: "vg-preview", GroupName: "preview-group", Strategy: strategy,
		MemberProxyIds: []string{"proxy-a", "proxy-b", "proxy-c"},
	}
	if strategy == browser.ProxyVirtualGroupStrategySticky {
		group.SelectedProxyId = "proxy-b"
		group.PreferredProxyId = "proxy-b"
	}
	if err := app.SaveBrowserProxyVirtualGroup(group); err != nil {
		t.Fatalf("save virtual group: %v", err)
	}
	return app, group, configs
}

func virtualGroupPreviewStartInput(group BrowserProxyVirtualGroup, mode string) (BrowserProfile, browserStartInput) {
	profile := BrowserProfile{ProfileId: "profile-preview", ProxyMode: browser.ProxyModeFixed, ProxyId: group.GroupId}
	input := browserStartInput{ProfileID: profile.ProfileId}
	switch mode {
	case "temporary":
		profile.ProxyId = "proxy-c"
		input.TemporaryProxyID = group.GroupId
	case "dynamic":
		profile.ProxyMode = browser.ProxyModeDynamic
		profile.ProxyId = ""
		profile.ProxyGroupName = group.GroupName
	}
	return profile, input
}

func assertVirtualGroupPreflight(t *testing.T, app *App, groupID string) {
	t.Helper()
	if result := app.ValidateProxyConfig("", groupID); !result.Supported {
		t.Fatalf("validation failed: %s", result.ErrorMsg)
	}
	if result := app.BrowserProxyWarmupBridgeWithConfig(groupID, ""); !result.Ok || result.ProxyId != groupID {
		t.Fatalf("warmup failed or changed group ID: %+v", result)
	}
}

func assertVirtualGroupProxyTraffic(t *testing.T, proxyURL, wantURL, wantID string) {
	t.Helper()
	if proxyURL != wantURL {
		t.Fatalf("selected proxy = %q, want %q (%s)", proxyURL, wantURL, wantID)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get("http://virtual-group.invalid/member")
	if err != nil {
		t.Fatalf("request through selected local member: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != wantID {
		t.Fatalf("member response = (%d, %q, %v), want (200, %q, nil)", resp.StatusCode, body, err, wantID)
	}
}

// The UI validates and warms a group before each start. Those lookups must not
// consume the cursor; direct, temporary and dynamic starts must still advance it.
func TestVirtualGroupPreviewDoesNotConsumeRoundRobin(t *testing.T) {
	for _, mode := range []string{"fixed", "temporary", "dynamic"} {
		for _, preflight := range []bool{false, true} {
			name := mode + "/direct_start"
			if preflight {
				name = mode + "/ui_preflight"
			}
			t.Run(name, func(t *testing.T) {
				app, group, configs := newVirtualGroupPreviewTestApp(t, browser.ProxyVirtualGroupStrategyRoundRobin)
				profile, input := virtualGroupPreviewStartInput(group, mode)
				for i := 0; i < 6; i++ {
					if preflight {
						assertVirtualGroupPreflight(t, app, group.GroupId)
					}
					proxyURL, ref, release, err := app.resolveBrowserStartProxy(input, &profile)
					if err != nil || ref.valid() || release {
						t.Fatalf("start %d = (%+v, %v, %v), want native proxy", i, ref, release, err)
					}
					wantID := group.MemberProxyIds[i%len(group.MemberProxyIds)]
					assertVirtualGroupProxyTraffic(t, proxyURL, configs[wantID], wantID)
				}
			})
		}
	}
}

func TestVirtualGroupPreviewKeepsStickySelectionUntilStart(t *testing.T) {
	for _, mode := range []string{"fixed", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			app, group, configs := newVirtualGroupPreviewTestApp(t, browser.ProxyVirtualGroupStrategySticky)
			profile, input := virtualGroupPreviewStartInput(group, mode)
			seedMemberState(t, app, group.GroupId, "proxy-b", browser.MemberStateDegraded)
			assertVirtualGroupPreflight(t, app, group.GroupId)
			if target, ok := app.resolveVirtualGroupSpeedTestTarget(group.GroupId, app.getLatestProxies()); !ok || target != "proxy-c" {
				t.Fatalf("speed probe target = (%q, %v), want proxy-c", target, ok)
			}
			stored, ok := app.findProxyVirtualGroupByID(group.GroupId)
			if !ok || stored.SelectedProxyId != "proxy-b" || stored.PreferredProxyId != "proxy-b" {
				t.Fatalf("preview changed stored sticky selection: %+v", stored)
			}
			proxyURL, _, _, err := app.resolveBrowserStartProxy(input, &profile)
			if err != nil {
				t.Fatal(err)
			}
			assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-c"], "proxy-c")
			stored, ok = app.findProxyVirtualGroupByID(group.GroupId)
			if !ok || stored.SelectedProxyId != "proxy-c" || stored.PreferredProxyId != "proxy-b" {
				t.Fatalf("successful fallback did not preserve sticky preference: %+v", stored)
			}

			seedMemberState(t, app, group.GroupId, "proxy-b", browser.MemberStateHealthy)
			assertVirtualGroupPreflight(t, app, group.GroupId)
			stored, ok = app.findProxyVirtualGroupByID(group.GroupId)
			if !ok || stored.SelectedProxyId != "proxy-c" {
				t.Fatalf("preview committed recovered member before a start: %+v", stored)
			}
			for i := 0; i < 2; i++ {
				proxyURL, _, _, err = app.resolveBrowserStartProxy(input, &profile)
				if err != nil {
					t.Fatal(err)
				}
				assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-b"], "proxy-b")
			}
		})
	}
}

func TestVirtualGroupPreviewPreservesHealthFiltering(t *testing.T) {
	for _, mode := range []string{"fixed", "temporary", "dynamic"} {
		t.Run(mode, func(t *testing.T) {
			app, group, configs := newVirtualGroupPreviewTestApp(t, browser.ProxyVirtualGroupStrategyRoundRobin)
			profile, input := virtualGroupPreviewStartInput(group, mode)
			seedMemberState(t, app, group.GroupId, "proxy-a", browser.MemberStateDegraded)
			seedMemberState(t, app, group.GroupId, "proxy-b", browser.MemberStateRecovering)
			seedMemberState(t, app, group.GroupId, "proxy-c", browser.MemberStateHealthy)
			for i := 0; i < 2; i++ {
				assertVirtualGroupPreflight(t, app, group.GroupId)
				proxyURL, _, _, err := app.resolveBrowserStartProxy(input, &profile)
				if err != nil {
					t.Fatal(err)
				}
				assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-c"], "proxy-c")
			}
			seedMemberState(t, app, group.GroupId, "proxy-c", browser.MemberStateDegraded)
			// Probes remain available to recover degraded nodes, but must never
			// permit the actual start path to bypass the degraded-state gate.
			if _, ok := app.resolveVirtualGroupSpeedTestTarget(group.GroupId, app.getLatestProxies()); !ok {
				t.Fatal("all-degraded group cannot be manually probed")
			}
			if _, _, _, err := app.resolveBrowserStartProxy(input, &profile); err == nil || !strings.Contains(err.Error(), "降级") {
				t.Fatalf("all-degraded start error = %v, want explicit rejection", err)
			}
		})
	}
}

func TestVirtualGroupStartCommitsOnlySuccessfulMember(t *testing.T) {
	for _, mode := range []string{"fixed", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			app, group, configs := newVirtualGroupPreviewTestApp(t, browser.ProxyVirtualGroupStrategyRoundRobin)
			profile, input := virtualGroupPreviewStartInput(group, mode)
			proxies := app.getLatestProxies()
			for i := range proxies {
				proxies[i].ProxyConfig = "invalid://not-a-proxy"
			}
			if err := app.browserMgr.ProxyDAO.ReplaceAll(proxies); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, _, _, err := app.resolveBrowserStartProxy(input, &profile); err == nil {
					t.Fatal("invalid group unexpectedly started")
				}
			}
			for i := range proxies {
				proxies[i].ProxyConfig = configs[proxies[i].ProxyId]
			}
			if err := app.browserMgr.ProxyDAO.ReplaceAll(proxies); err != nil {
				t.Fatal(err)
			}
			proxyURL, _, _, err := app.resolveBrowserStartProxy(input, &profile)
			if err != nil {
				t.Fatal(err)
			}
			assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-a"], "proxy-a")

			// B fails; C succeeds. Commit C so the following start wraps to A.
			for i := range proxies {
				if proxies[i].ProxyId == "proxy-b" {
					proxies[i].ProxyConfig = "invalid://not-a-proxy"
				}
			}
			if err := app.browserMgr.ProxyDAO.ReplaceAll(proxies); err != nil {
				t.Fatal(err)
			}
			for _, wantID := range []string{"proxy-c", "proxy-a"} {
				proxyURL, _, _, err = app.resolveBrowserStartProxy(input, &profile)
				if err != nil {
					t.Fatal(err)
				}
				assertVirtualGroupProxyTraffic(t, proxyURL, configs[wantID], wantID)
			}
		})
	}
}

func TestVirtualGroupTemporaryOverridesKeepExistingSemantics(t *testing.T) {
	app, group, configs := newVirtualGroupPreviewTestApp(t, browser.ProxyVirtualGroupStrategyRoundRobin)
	profile, input := virtualGroupPreviewStartInput(group, "temporary")
	input.ForceDirectProxy = true
	if proxyURL, _, _, err := app.resolveBrowserStartProxy(input, &profile); err != nil || proxyURL != "direct://" {
		t.Fatalf("force direct = (%q, %v), want direct://", proxyURL, err)
	}
	input.ForceDirectProxy = false
	input.TemporaryProxyConfig = configs["proxy-c"]
	if _, _, _, err := app.resolveBrowserStartProxy(input, &profile); err == nil || !strings.Contains(err.Error(), "proxy id not found") {
		t.Fatalf("explicit config with non-entity ID error = %v, want missing entity ID", err)
	}
	input.TemporaryProxyID = ""
	proxyURL, _, _, err := app.resolveBrowserStartProxy(input, &profile)
	if err != nil {
		t.Fatal(err)
	}
	assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-c"], "proxy-c")
	input.TemporaryProxyID = "proxy-b"
	proxyURL, _, _, err = app.resolveBrowserStartProxy(input, &profile)
	if err != nil {
		t.Fatal(err)
	}
	assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-b"], "proxy-b")
	// All bypass/override operations above must leave the group cursor alone.
	input.TemporaryProxyID = group.GroupId
	input.TemporaryProxyConfig = ""
	proxyURL, _, _, err = app.resolveBrowserStartProxy(input, &profile)
	if err != nil {
		t.Fatal(err)
	}
	assertVirtualGroupProxyTraffic(t, proxyURL, configs["proxy-a"], "proxy-a")
}
