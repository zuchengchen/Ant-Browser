package browser

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ant-chrome/backend/internal/database"
)

func proxyScanScratchDB(t *testing.T) (*database.DB, *SQLiteProxyDAO) {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db, NewSQLiteProxyDAO(db.GetConn())
}

func proxyScanScratchFull() Proxy {
	return Proxy{
		ProxyId: "full", ProxyName: "完整代理K", ProxyConfig: "https://user:p%40ss@[::1]:8443",
		PreferredKernel: "mihomo", DnsServers: "1.1.1.1,2001:4860:4860::8888", GroupName: "组 A", SortOrder: -7,
		SourceID: "source-1", SourceURL: "https://subscription.invalid/nodes?fixture=1", SourceNamePrefix: "订阅/",
		SourceAutoRefresh: true, SourceRefreshIntervalM: 73, SourceLastRefreshAt: "2026-11-09T01:02:03Z",
		SourceFetchProxyID: "fetch-proxy", SourceLastRefreshError: "last refresh failed", SourceMissing: true,
		SourceMissingSince: "2026-11-08T01:02:03Z", LastLatencyMs: 9876543210, LastTestOk: true,
		LastTestedAt: "2026-11-09T04:05:06Z", LastTestEngine: "mihomo", LastTestConnectorType: "mihomo",
		LastTestFingerprint: "fingerprint-完整", LastIPHealthJSON: `{"ip":"2001:db8::1","score":87}`,
	}
}

func proxyScanScratchPut(t *testing.T, dao *SQLiteProxyDAO, item Proxy) {
	t.Helper()
	if err := dao.Upsert(item); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateSpeedResultWithProvenance(item.ProxyId, item.LastTestOk, item.LastLatencyMs,
		item.LastTestedAt, item.LastTestEngine, item.LastTestConnectorType, item.LastTestFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateIPHealthResult(item.ProxyId, item.LastIPHealthJSON); err != nil {
		t.Fatal(err)
	}
}

func TestProxyScanScratchAllPersistedFieldsAndIndependentRows(t *testing.T) {
	db, dao := proxyScanScratchDB(t)
	full := proxyScanScratchFull()
	empty := Proxy{ProxyId: "empty", ProxyName: "空值代理", ProxyConfig: "direct://", GroupName: full.GroupName, LastLatencyMs: -1, SortOrder: 3}
	third := full
	third.ProxyId, third.ProxyName, third.SortOrder = "third", "最后代理", 3
	for _, item := range []Proxy{third, empty, full} {
		proxyScanScratchPut(t, dao, item)
	}
	// Ties must retain the existing created_at order, not ID or insertion order.
	if _, err := db.GetConn().Exec(`UPDATE browser_proxies SET created_at=CASE proxy_id WHEN 'third' THEN '2026-01-03' WHEN 'empty' THEN '2026-01-02' ELSE '2026-01-01' END`); err != nil {
		t.Fatal(err)
	}
	want := []Proxy{full, empty, third}
	for _, query := range []struct {
		name string
		run  func() ([]Proxy, error)
	}{
		{"list", dao.List}, {"group", func() ([]Proxy, error) { return dao.ListByGroup(full.GroupName) }},
	} {
		t.Run(query.name, func(t *testing.T) {
			got, err := query.run()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got=%#v err=%v want=%#v", got, err, want)
			}
			got[1].ProxyName = "caller mutation"
			if got[0] != full || got[2] != third {
				t.Fatal("rows alias each other")
			}
			again, err := query.run()
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatalf("caller mutation leaked: %#v %v", again, err)
			}
		})
	}
	got, usable, err := dao.LookupUnambiguousProxy(" full ")
	if err != nil || !usable || got != full {
		t.Fatalf("single-row scanner changed: %#v usable=%t err=%v", got, usable, err)
	}
}

func TestProxyScanScratchNullDefaultsAndOnlyOneMeansTrue(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nullable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE browser_proxies (
		proxy_id TEXT PRIMARY KEY, proxy_name TEXT, proxy_config TEXT, preferred_kernel TEXT,
		dns_servers TEXT, group_name TEXT, source_id TEXT, source_url TEXT, source_name_prefix TEXT,
		source_auto_refresh INTEGER, source_refresh_interval_m INTEGER, source_last_refresh_at TEXT,
		source_fetch_proxy_id TEXT, source_last_refresh_error TEXT, source_missing INTEGER, source_missing_since TEXT,
		last_latency_ms INTEGER, last_test_ok INTEGER, last_tested_at TEXT, last_test_engine TEXT,
		last_test_connector_type TEXT, last_test_fingerprint TEXT, last_ip_health_json TEXT,
		sort_order INTEGER, created_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config,dns_servers,sort_order,created_at)
		VALUES ('nulls','Nulls','http://localhost:80','',1,'2026-01-02'),('ones','Ones','http://localhost:81','',0,'2026-01-01'),('other','Other','http://localhost:82','',2,'2026-01-03')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE browser_proxies SET last_test_ok=1,source_auto_refresh=1,source_missing=1 WHERE proxy_id='ones'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE browser_proxies SET last_test_ok=2,source_auto_refresh=-1,source_missing=2 WHERE proxy_id='other'`); err != nil {
		t.Fatal(err)
	}
	dao := NewSQLiteProxyDAO(db)
	got, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []Proxy{
		{ProxyId: "ones", ProxyName: "Ones", ProxyConfig: "http://localhost:81", LastLatencyMs: -1, LastTestOk: true, SourceAutoRefresh: true, SourceMissing: true},
		{ProxyId: "nulls", ProxyName: "Nulls", ProxyConfig: "http://localhost:80", LastLatencyMs: -1, SortOrder: 1},
		{ProxyId: "other", ProxyName: "Other", ProxyConfig: "http://localhost:82", LastLatencyMs: -1, SortOrder: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nullable/non-one values leaked across rows: got=%#v want=%#v", got, want)
	}
}

func TestProxyScanScratchScanFailureDoesNotReturnPartialOrDirtyRow(t *testing.T) {
	db, dao := proxyScanScratchDB(t)
	for _, item := range []Proxy{{ProxyId: "valid", ProxyName: "Valid", ProxyConfig: "http://localhost:1", SortOrder: 0},
		{ProxyId: "invalid", ProxyName: "Invalid", ProxyConfig: "http://localhost:2", SortOrder: 1}} {
		proxyScanScratchPut(t, dao, item)
	}
	if _, err := db.GetConn().Exec(`UPDATE browser_proxies SET last_latency_ms='broken-integer' WHERE proxy_id='invalid'`); err != nil {
		t.Fatal(err)
	}
	got, err := dao.List()
	if err == nil || got != nil || !strings.Contains(err.Error(), "读取代理行失败") {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if _, err := db.GetConn().Exec(`UPDATE browser_proxies SET last_latency_ms=42 WHERE proxy_id='invalid'`); err != nil {
		t.Fatal(err)
	}
	got, err = dao.List()
	if err != nil || len(got) != 2 || got[1].LastLatencyMs != 42 {
		t.Fatalf("failed scan retained DB connection or stale row: %#v %v", got, err)
	}
}

type proxyScanScratchConnector struct{ rows *proxyScanScratchRows }

func (c proxyScanScratchConnector) Connect(context.Context) (driver.Conn, error) {
	return proxyScanScratchConn{c.rows}, nil
}
func (c proxyScanScratchConnector) Driver() driver.Driver { return proxyScanScratchDriver{} }

type proxyScanScratchDriver struct{}

func (proxyScanScratchDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector only")
}

type proxyScanScratchConn struct{ rows *proxyScanScratchRows }

func (proxyScanScratchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (proxyScanScratchConn) Close() error              { return nil }
func (proxyScanScratchConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }
func (c proxyScanScratchConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, nil
}

type proxyScanScratchRows struct {
	emitted, closed bool
	terminal        error
}

func (*proxyScanScratchRows) Columns() []string {
	return strings.Split("id,name,config,kernel,dns,group,source,url,prefix,auto,interval,refresh,fetch,refreshError,missing,missingSince,latency,ok,tested,engine,connector,fingerprint,health,sort", ",")
}
func (r *proxyScanScratchRows) Close() error { r.closed = true; return nil }
func (r *proxyScanScratchRows) Next(dest []driver.Value) error {
	if r.emitted {
		if r.terminal != nil {
			return r.terminal
		}
		return io.EOF
	}
	r.emitted = true
	for i := range dest {
		dest[i] = ""
	}
	for _, i := range []int{9, 10, 14, 16, 17, 23} {
		dest[i] = int64(0)
	}
	dest[0], dest[1], dest[2] = "first", "First", "http://localhost:1"
	return nil
}

func TestProxyScanScratchRowsErrorRetainsOnlyCompletedRows(t *testing.T) {
	sentinel := errors.New("synthetic stream failure")
	stream := &proxyScanScratchRows{terminal: sentinel}
	db := sql.OpenDB(proxyScanScratchConnector{stream})
	t.Cleanup(func() { _ = db.Close() })
	rows, err := db.QueryContext(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanProxies(rows)
	want := []Proxy{{ProxyId: "first", ProxyName: "First", ProxyConfig: "http://localhost:1"}}
	if !errors.Is(err, sentinel) || !reflect.DeepEqual(got, want) || !stream.closed {
		t.Fatalf("got=%#v err=%v closed=%t", got, err, stream.closed)
	}
}

func TestProxyScanScratchEmptyFreshEditsDeletesReplaceAllAndConcurrentReads(t *testing.T) {
	_, dao := proxyScanScratchDB(t)
	got, err := dao.List()
	if err != nil || got != nil {
		t.Fatalf("empty result changed: %#v %v", got, err)
	}
	item := proxyScanScratchFull()
	proxyScanScratchPut(t, dao, item)
	old, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	item.ProxyName = "Edited"
	item.ProxyConfig = "http://new.invalid:80"
	proxyScanScratchPut(t, dao, item)
	fresh, err := dao.List()
	if err != nil || len(fresh) != 1 || fresh[0] != item || old[0].ProxyName == "Edited" {
		t.Fatalf("stale or shared result: %#v %#v %v", old, fresh, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			list, err := dao.List()
			if err != nil || len(list) != 1 || list[0] != item {
				t.Errorf("concurrent scan: %#v %v", list, err)
			}
		}()
	}
	wg.Wait()
	if err := dao.Delete(item.ProxyId); err != nil {
		t.Fatal(err)
	}
	got, err = dao.List()
	if err != nil || got != nil {
		t.Fatalf("deleted result remains: %#v %v", got, err)
	}
	newItem := Proxy{ProxyId: "replacement", ProxyName: "Replacement", ProxyConfig: "http://replacement:80", LastLatencyMs: -1}
	if err := dao.ReplaceAll([]Proxy{newItem}); err != nil {
		t.Fatal(err)
	}
	got, err = dao.List()
	if err != nil || !reflect.DeepEqual(got, []Proxy{newItem}) {
		t.Fatalf("replacement result: %#v %v", got, err)
	}
}
