package backend

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
)

type proxySpeedLookupLegacyDAO struct {
	browser.ProxyDAO
	items     []browser.Proxy
	listErr   error
	listCalls int
	writes    []ProxyTestResult
}

func (d *proxySpeedLookupLegacyDAO) List() ([]browser.Proxy, error) {
	d.listCalls++
	return append([]browser.Proxy(nil), d.items...), d.listErr
}

func (d *proxySpeedLookupLegacyDAO) UpdateSpeedResultWithProvenance(id string, ok bool, latency int64, at, engine, connector, fingerprint string) error {
	d.writes = append(d.writes, ProxyTestResult{
		ProxyId: id, Ok: ok, LatencyMs: latency, TestedAt: at, Engine: engine,
		connectorType: connector, fingerprint: fingerprint,
	})
	return nil
}

type proxySpeedLookupTestDAO struct {
	*proxySpeedLookupLegacyDAO
	item      browser.Proxy
	usable    bool
	lookupErr error
	lookupIDs []string
}

func (d *proxySpeedLookupTestDAO) LookupUnambiguousProxy(id string) (browser.Proxy, bool, error) {
	d.lookupIDs = append(d.lookupIDs, id)
	return d.item, d.usable, d.lookupErr
}

func newProxySpeedLookupApp(t *testing.T, dao browser.ProxyDAO) *App {
	t.Helper()
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	app.config.Browser.Proxies = []BrowserProxy{{ProxyId: "stale-config", ProxyConfig: "http://127.0.0.1:1"}}
	app.config.ProxyCheck.SpeedTargetID = "lookup-local"
	app.config.ProxyCheck.Targets = []config.ProxyCheckTarget{{
		ID: "lookup-local", Type: "speed", URL: "http://lookup-regression.invalid/generate_204",
		TimeoutMs: 1000, ExpectedStatus: []int{http.StatusNoContent},
	}}
	app.browserMgr = browser.NewManager(app.config, app.appRoot)
	app.browserMgr.ProxyDAO = dao
	return app
}

func TestProxySpeedLookupOptionalCapabilityAndFallback(t *testing.T) {
	full := BrowserProxy{ProxyId: "from-full", ProxyConfig: "http://127.0.0.1:2"}
	for _, tc := range []struct {
		name      string
		id        string
		value     string
		usable    bool
		lookupErr error
		listErr   error
		wantFast  bool
		wantCalls int
	}{
		{name: "usable", id: " node ", value: "http://127.0.0.1:3", usable: true, wantFast: true, wantCalls: 1},
		{name: "needs-full-path", id: "node", value: "http://127.0.0.1:3", wantCalls: 1},
		{name: "query-error", id: "node", value: "http://127.0.0.1:3", usable: true, lookupErr: errors.New("lookup failed"), wantCalls: 1},
		{name: "query-and-list-errors", id: "node", value: "http://127.0.0.1:3", lookupErr: errors.New("lookup failed"), listErr: errors.New("list failed"), wantCalls: 1},
		{name: "chain", id: "node", value: "chain+socks5://encoded", usable: true, wantCalls: 1},
		{name: "yaml", id: "node", value: "type: http\nserver: 127.0.0.1\nport: 3", usable: true, wantCalls: 1},
		{name: "sing-box", id: "node", value: "anytls://test@127.0.0.1:3", usable: true, wantCalls: 1},
		{name: "empty-id", value: "http://127.0.0.1:3", usable: true},
		{name: "direct-alias", id: " __DIRECT__ ", value: "http://127.0.0.1:3", usable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &proxySpeedLookupLegacyDAO{items: []browser.Proxy{full}, listErr: tc.listErr}
			dao := &proxySpeedLookupTestDAO{proxySpeedLookupLegacyDAO: base, item: browser.Proxy{ProxyId: "node", ProxyConfig: tc.value}, usable: tc.usable, lookupErr: tc.lookupErr}
			app := newProxySpeedLookupApp(t, dao)
			got := app.latestProxiesForSpeedTest(tc.id)
			if len(dao.lookupIDs) != tc.wantCalls {
				t.Fatalf("lookup calls = %d, want %d", len(dao.lookupIDs), tc.wantCalls)
			}
			if tc.wantFast {
				if !reflect.DeepEqual(got, []BrowserProxy{dao.item}) || base.listCalls != 0 {
					t.Fatalf("single row = %#v, List calls = %d", got, base.listCalls)
				}
				return
			}
			if base.listCalls != 1 || len(got) == 0 || got[0].ProxyId != "__direct__" {
				t.Fatalf("full path = %#v, List calls = %d", got, base.listCalls)
			}
			if tc.listErr != nil {
				if len(got) != 1 {
					t.Fatalf("failed DAO reused stale/config rows: %#v", got)
				}
			} else if len(got) != 2 || got[1] != full {
				t.Fatalf("full-directory rows changed: %#v", got)
			}
		})
	}
	legacy := &proxySpeedLookupLegacyDAO{items: []browser.Proxy{full}}
	got := newProxySpeedLookupApp(t, legacy).latestProxiesForSpeedTest("node")
	if legacy.listCalls != 1 || len(got) != 2 || got[1] != full {
		t.Fatalf("custom DAO path changed: %#v, List calls = %d", got, legacy.listCalls)
	}
}

func TestProxySpeedLookupPreservesRequestIDAndProvenance(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != "lookup-regression.invalid" {
			http.Error(w, "expected local HTTP proxy request", http.StatusBadRequest)
			return
		}
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, connector := range []string{config.BrowserConnectorXray, config.BrowserConnectorMihomo} {
		t.Run(connector, func(t *testing.T) {
			base := &proxySpeedLookupLegacyDAO{}
			dao := &proxySpeedLookupTestDAO{proxySpeedLookupLegacyDAO: base, item: browser.Proxy{ProxyId: "node", ProxyConfig: server.URL}, usable: true}
			app := newProxySpeedLookupApp(t, dao)
			app.config.Browser.DefaultConnectorType = connector
			result := app.testSharedProxySpeed(" NODE ", true)
			if !result.Ok || result.ProxyId != "NODE" || result.Engine != "native" || result.connectorType != connector {
				t.Fatalf("speed result changed: %#v", result)
			}
			if base.listCalls != 0 || len(base.writes) != 1 || base.writes[0].ProxyId != "NODE" || base.writes[0].connectorType != connector || base.writes[0].TestedAt == "" {
				t.Fatalf("write provenance changed: %#v, List calls = %d", base.writes, base.listCalls)
			}
			if len(dao.lookupIDs) != 1 || dao.lookupIDs[0] != "NODE" {
				t.Fatalf("lookup request ID changed: %#v", dao.lookupIDs)
			}
		})
	}
	// Each speed test keeps the existing warm-up plus measured request.
	if hits.Load() != 4 {
		t.Fatalf("local proxy request count = %d, want 4 (two probes with warm-up)", hits.Load())
	}
}

func TestProxySpeedLookupKeepsCacheAndCrossStackRejection(t *testing.T) {
	base := &proxySpeedLookupLegacyDAO{}
	dao := &proxySpeedLookupTestDAO{proxySpeedLookupLegacyDAO: base, usable: true, item: browser.Proxy{
		ProxyId: "node", ProxyConfig: "http://127.0.0.1:1", LastTestOk: true, LastLatencyMs: 17,
		LastTestedAt: time.Now().Format(time.RFC3339), LastTestEngine: "native", LastTestConnectorType: config.BrowserConnectorXray,
	}}
	app := newProxySpeedLookupApp(t, dao)
	result := app.testSharedProxySpeed("NODE", false)
	if !result.Ok || result.LatencyMs != 17 || result.ProxyId != "NODE" || len(base.writes) != 0 {
		t.Fatalf("cached result changed or was persisted again: %#v, writes=%#v", result, base.writes)
	}
	dao.item.PreferredKernel = "mihomo"
	result = app.testSharedProxySpeed("node", true)
	if result.Ok || !strings.Contains(result.Error, "不属于当前 xray 连接栈") || len(base.writes) != 1 {
		t.Fatalf("cross-stack preference was not rejected through old path: %#v, writes=%#v", result, base.writes)
	}
}

func newSQLiteProxySpeedLookupApp(t *testing.T) *App {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "proxy-speed-lookup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	app := newProxySpeedLookupApp(t, browser.NewSQLiteProxyDAO(db.GetConn()))
	app.browserMgr.ProxyVirtualGroupDAO = browser.NewSQLiteProxyVirtualGroupDAO(db.GetConn())
	app.browserMgr.ProxyVirtualGroupMemberStateDAO = browser.NewSQLiteProxyVirtualGroupMemberStateDAO(db.GetConn())
	return app
}

func TestProxySpeedLookupReadsCommittedChangesAndDeletion(t *testing.T) {
	app := newSQLiteProxySpeedLookupApp(t)
	first := BrowserProxy{ProxyId: "node", ProxyConfig: "http://127.0.0.1:1", DnsServers: "1.1.1.1"}
	if err := app.browserMgr.ProxyDAO.Upsert(first); err != nil {
		t.Fatal(err)
	}
	got := app.latestProxiesForSpeedTest("node")
	if len(got) != 1 || got[0].ProxyConfig != first.ProxyConfig || got[0].DnsServers != first.DnsServers {
		t.Fatalf("initial row = %#v", got)
	}
	second := first
	second.ProxyConfig = "socks5://test:password@127.0.0.1:2"
	second.DnsServers = "9.9.9.9"
	second.PreferredKernel = "xray"
	if err := app.browserMgr.ProxyDAO.Upsert(second); err != nil {
		t.Fatal(err)
	}
	got = app.latestProxiesForSpeedTest("node")
	if len(got) != 1 || got[0].ProxyConfig != second.ProxyConfig || got[0].DnsServers != second.DnsServers || got[0].PreferredKernel != second.PreferredKernel {
		t.Fatalf("edited row was stale: %#v", got)
	}
	if err := app.browserMgr.ProxyDAO.Delete("node"); err != nil {
		t.Fatal(err)
	}
	result := app.testSharedProxySpeed("node", true)
	if result.Ok || result.Error != "代理配置为空" {
		t.Fatalf("deleted proxy reused an old row or direct connection: %#v", result)
	}
	if err := app.browserMgr.ProxyDAO.ReplaceAll([]BrowserProxy{first}); err != nil {
		t.Fatal(err)
	}
	got = app.latestProxiesForSpeedTest("node")
	if len(got) != 1 || got[0].ProxyConfig != first.ProxyConfig || got[0].PreferredKernel != "" {
		t.Fatalf("replacement row was stale: %#v", got)
	}
}

func TestProxySpeedLookupKeepsGroupResolutionAndMemberHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	app := newSQLiteProxySpeedLookupApp(t)
	if err := app.browserMgr.ProxyDAO.Upsert(BrowserProxy{ProxyId: "node", ProxyConfig: server.URL}); err != nil {
		t.Fatal(err)
	}
	group := browser.ProxyVirtualGroup{GroupId: "group", GroupName: "group", Strategy: browser.ProxyVirtualGroupStrategyRoundRobin, MemberProxyIds: []string{"node"}}
	if err := app.browserMgr.ProxyVirtualGroupDAO.Upsert(group); err != nil {
		t.Fatal(err)
	}
	before, err := app.browserMgr.ProxyVirtualGroupDAO.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"node", "group"} {
		result := app.testSharedProxySpeed(id, true)
		if !result.Ok || result.ProxyId != "node" {
			t.Fatalf("probe %q = %#v", id, result)
		}
	}
	states, err := app.browserMgr.ProxyVirtualGroupMemberStateDAO.ListByGroup("group")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ProxyId != "node" || states[0].ConsecutiveSuccesses != 2 || states[0].LastConnectorType != config.BrowserConnectorXray || states[0].LastEngine != "native" {
		t.Fatalf("member health was skipped or changed: %#v", states)
	}
	after, err := app.browserMgr.ProxyVirtualGroupDAO.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("probe committed a virtual-group selection: before=%#v after=%#v", before, after)
	}
}

func TestProxySpeedLookupFallbackKeepsFirstCaseFoldRow(t *testing.T) {
	var firstHits, secondHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer second.Close()
	app := newSQLiteProxySpeedLookupApp(t)
	if err := app.browserMgr.ProxyDAO.ReplaceAll([]BrowserProxy{
		{ProxyId: "NODE", ProxyConfig: first.URL, SortOrder: 0},
		{ProxyId: "node", ProxyConfig: second.URL, SortOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	result := app.testSharedProxySpeed("node", true)
	if !result.Ok || result.ProxyId != "node" || firstHits.Load() != 2 || secondHits.Load() != 0 {
		t.Fatalf("case-fold precedence changed: result=%#v requests=%d/%d", result, firstHits.Load(), secondHits.Load())
	}
}
