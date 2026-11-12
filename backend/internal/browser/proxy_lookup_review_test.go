package browser

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ant-chrome/backend/internal/database"
	"modernc.org/sqlite"
)

func proxyLookupReviewDB(t *testing.T, path string) (*database.DB, *SQLiteProxyDAO) {
	t.Helper()
	db, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db, NewSQLiteProxyDAO(db.GetConn())
}

func proxyLookupReviewPut(t *testing.T, dao *SQLiteProxyDAO, item Proxy) {
	t.Helper()
	if err := dao.Upsert(item); err != nil {
		t.Fatal(err)
	}
}

func proxyLookupReviewAssertCurrent(t *testing.T, dao *SQLiteProxyDAO, requestedID, storedID string) Proxy {
	t.Helper()
	list, err := dao.List()
	if err != nil {
		t.Fatal(err)
	}
	var expected *Proxy
	for i := range list {
		if list[i].ProxyId == storedID {
			expected = &list[i]
			break
		}
	}
	if expected == nil {
		t.Fatalf("test fixture has no exact stored ID %q", storedID)
	}
	got, usable, err := dao.LookupUnambiguousProxy(requestedID)
	if err != nil || !usable {
		t.Fatalf("LookupUnambiguousProxy(%q): usable=%t err=%v", requestedID, usable, err)
	}
	if !reflect.DeepEqual(got, *expected) {
		t.Fatalf("lookup differs from List row\ngot  %#v\nwant %#v", got, *expected)
	}
	return got
}

func TestProxyLookupReviewCompletePersistedRow(t *testing.T) {
	_, dao := proxyLookupReviewDB(t, filepath.Join(t.TempDir(), "complete.db"))
	want := Proxy{
		ProxyId: "MiXeD", ProxyName: "完整节点", ProxyConfig: "https://user:p%40ss@[::1]:8443",
		PreferredKernel: "mihomo", DnsServers: "1.1.1.1,2001:4860:4860::8888", GroupName: "组 A", SortOrder: -7,
		SourceID: "source-1", SourceURL: "https://subscription.invalid/nodes?token=fixture",
		SourceNamePrefix: "订阅/", SourceAutoRefresh: true, SourceRefreshIntervalM: 73,
		SourceLastRefreshAt: "2026-11-09T01:02:03Z", SourceFetchProxyID: "fetch-proxy",
		SourceLastRefreshError: "last refresh failed", SourceMissing: true, SourceMissingSince: "2026-11-08T01:02:03Z",
		LastLatencyMs: 9876543210, LastTestOk: true, LastTestedAt: "2026-11-09T04:05:06Z",
		LastTestEngine: "mihomo", LastTestConnectorType: "mihomo", LastTestFingerprint: "fingerprint-完整",
		LastIPHealthJSON: `{"ip":"2001:db8::1","score":87,"tags":["test"]}`,
	}
	proxyLookupReviewPut(t, dao, Proxy{ProxyId: "other", ProxyName: "Other", ProxyConfig: "http://other.invalid:80", SortOrder: -99})
	proxyLookupReviewPut(t, dao, want)
	if err := dao.UpdateSpeedResultWithProvenance(want.ProxyId, want.LastTestOk, want.LastLatencyMs, want.LastTestedAt, want.LastTestEngine, want.LastTestConnectorType, want.LastTestFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := dao.UpdateIPHealthResult(want.ProxyId, want.LastIPHealthJSON); err != nil {
		t.Fatal(err)
	}
	got := proxyLookupReviewAssertCurrent(t, dao, "\u2003mIxEd\t", want.ProxyId)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("complete persisted row changed\ngot  %#v\nwant %#v", got, want)
	}
}

func TestProxyLookupReviewIdentityBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		ids        []string
		request    string
		wantID     string
		wantUsable bool
	}{
		{"unique_ascii_fold", []string{"Alpha"}, "aLPHA", "Alpha", true},
		{"trim_request", []string{"Alpha"}, "\u2003Alpha\u00a0", "Alpha", true},
		{"ascii_collision", []string{"Alpha", "ALPHA"}, "alpha", "", false},
		{"ascii_collision_reversed", []string{"ALPHA", "Alpha"}, "alpha", "", false},
		{"padded_only", []string{" Alpha "}, "Alpha", "", false},
		{"unicode_padded_only", []string{"\u2003Alpha\u00a0"}, "Alpha", "", false},
		{"padded_collision", []string{" Alpha ", "Alpha"}, "Alpha", "", false},
		{"kelvin_unique", []string{"K"}, "k", "K", true},
		{"kelvin_collision", []string{"K", "K"}, "k", "", false},
		{"long_s_unique", []string{"ſ"}, "S", "ſ", true},
		{"long_s_collision", []string{"ſ", "s"}, "S", "", false},
		{"dotted_i_is_distinct", []string{"i", "İ"}, "I", "i", true},
		{"dotted_i_exact", []string{"i", "İ"}, "İ", "İ", true},
		{"dotted_i_not_ascii_i", []string{"i"}, "İ", "", false},
		{"dotless_i_is_distinct", []string{"i", "ı"}, "I", "i", true},
		{"missing", []string{"one", "two"}, "missing", "", false},
		{"empty_request", []string{""}, "", "", false},
		{"whitespace_request", []string{""}, "\u2003\t", "", false},
		{"builtin_direct", []string{"__direct__"}, "__direct__", "", false},
		{"direct_case_variant", []string{"__DIRECT__"}, " __DiReCt__ ", "", false},
		{"direct_collision", []string{"__DIRECT__", "__direct__"}, "__DIRECT__", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, dao := proxyLookupReviewDB(t, filepath.Join(t.TempDir(), "identity.db"))
			for i, id := range tc.ids {
				proxyLookupReviewPut(t, dao, Proxy{ProxyId: id, ProxyName: id, ProxyConfig: "http://127.0.0.1:8080", SortOrder: len(tc.ids) - i})
			}
			// List order intentionally differs from insertion and primary-key order.
			if _, err := db.GetConn().Exec(`UPDATE browser_proxies SET created_at='2026-01-01T00:00:00Z'`); err != nil {
				t.Fatal(err)
			}
			before, err := dao.List()
			if err != nil {
				t.Fatal(err)
			}
			got, usable, err := dao.LookupUnambiguousProxy(tc.request)
			if err != nil || usable != tc.wantUsable || got.ProxyId != tc.wantID {
				t.Fatalf("lookup(%q) = (%#v, %t, %v), want ID=%q usable=%t", tc.request, got, usable, err, tc.wantID, tc.wantUsable)
			}
			if !usable && !reflect.DeepEqual(got, Proxy{}) {
				t.Fatalf("unusable result exposes a partial row: %#v", got)
			}
			after, err := dao.List()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("lookup mutated directory/order: before=%#v after=%#v err=%v", before, after, err)
			}
		})
	}
}

func TestProxyLookupReviewListDecodingAndNullDefaults(t *testing.T) {
	// Legacy/imported nullable columns must follow List's COALESCE and bool
	// conversion rules. This table is deliberately independent of migrations.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "nullable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
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
	if _, err := db.Exec(`INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config,dns_servers,sort_order,created_at)
		VALUES ('nullable','Nullable','http://localhost:8080','',0,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	dao := NewSQLiteProxyDAO(db)
	got := proxyLookupReviewAssertCurrent(t, dao, "nullable", "nullable")
	if got.LastLatencyMs != -1 || got.LastTestOk || got.SourceAutoRefresh || got.SourceMissing {
		t.Fatalf("nullable defaults changed: %#v", got)
	}
	if _, err := db.Exec(`UPDATE browser_proxies SET last_test_ok=2,source_auto_refresh=-1,source_missing=2`); err != nil {
		t.Fatal(err)
	}
	got = proxyLookupReviewAssertCurrent(t, dao, "nullable", "nullable")
	if got.LastTestOk || got.SourceAutoRefresh || got.SourceMissing {
		t.Fatalf("non-1 booleans differ from List: %#v", got)
	}
}

func TestProxyLookupReviewCommittedEditsDeletesAndReplaceAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "freshness.db")
	_, reader := proxyLookupReviewDB(t, path)
	writerDB, err := database.NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writerDB.Close() })
	writer := NewSQLiteProxyDAO(writerDB.GetConn())
	item := Proxy{ProxyId: "node", ProxyName: "Before", ProxyConfig: "http://old.invalid:80", PreferredKernel: "xray", DnsServers: "1.1.1.1"}
	proxyLookupReviewPut(t, writer, item)
	proxyLookupReviewAssertCurrent(t, reader, "NODE", "node")

	item.ProxyName, item.ProxyConfig = "After", "https://new.invalid:443"
	item.PreferredKernel, item.DnsServers, item.SourceID = "mihomo", "8.8.8.8", "new-source"
	proxyLookupReviewPut(t, writer, item)
	if err := writer.UpdateSpeedResultWithProvenance("node", false, 91, "2026-11-10T00:00:00Z", "mihomo", "mihomo", "new-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if err := writer.UpdateIPHealthResult("node", `{"revision":2}`); err != nil {
		t.Fatal(err)
	}
	got := proxyLookupReviewAssertCurrent(t, reader, "node", "node")
	if got.ProxyConfig != item.ProxyConfig || got.PreferredKernel != "mihomo" || got.LastTestFingerprint != "new-fingerprint" || got.LastIPHealthJSON != `{"revision":2}` {
		t.Fatalf("external committed edit was stale: %#v", got)
	}

	if err := writer.Delete("node"); err != nil {
		t.Fatal(err)
	}
	if got, usable, err := reader.LookupUnambiguousProxy("node"); err != nil || usable || !reflect.DeepEqual(got, Proxy{}) {
		t.Fatalf("deleted row remained available: %#v usable=%t err=%v", got, usable, err)
	}
	proxyLookupReviewPut(t, writer, item)
	if err := writer.UpdateSpeedResultWithProvenance("node", true, 17, "2026-11-10T01:00:00Z", "native", "xray", "replacement-fingerprint"); err != nil {
		t.Fatal(err)
	}
	item.ProxyName, item.ProxyConfig = "Replacement", "socks5://replacement.invalid:1080"
	if err := writer.ReplaceAll([]Proxy{item, {ProxyId: "other", ProxyName: "Other", ProxyConfig: "http://other.invalid:80"}}); err != nil {
		t.Fatal(err)
	}
	got = proxyLookupReviewAssertCurrent(t, reader, "node", "node")
	if got.ProxyConfig != item.ProxyConfig || got.LastLatencyMs != 17 || got.LastTestFingerprint != "replacement-fingerprint" {
		t.Fatalf("ReplaceAll current config/preserved results changed: %#v", got)
	}
	proxyLookupReviewPut(t, writer, Proxy{ProxyId: "NODE", ProxyName: "Collision", ProxyConfig: "http://collision.invalid:80"})
	if _, usable, err := reader.LookupUnambiguousProxy("node"); err != nil || usable {
		t.Fatalf("new case collision did not force fallback: usable=%t err=%v", usable, err)
	}
	if err := writer.Delete("NODE"); err != nil {
		t.Fatal(err)
	}
	proxyLookupReviewAssertCurrent(t, reader, "node", "node")
	if err := writer.ReplaceAll(nil); err != nil {
		t.Fatal(err)
	}
	if _, usable, err := reader.LookupUnambiguousProxy("node"); err != nil || usable {
		t.Fatalf("empty replacement remained cached: usable=%t err=%v", usable, err)
	}
}

type proxyLookupReviewConnector struct {
	path        string
	afterIDRead func() error
}

func (c *proxyLookupReviewConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (c *proxyLookupReviewConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.path)
	if err != nil {
		return nil, err
	}
	return &proxyLookupReviewConn{Conn: conn, afterIDRead: c.afterIDRead}, nil
}

type proxyLookupReviewConn struct {
	driver.Conn
	afterIDRead func() error
}

func (c *proxyLookupReviewConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	querier, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := querier.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "SELECT proxy_id FROM browser_proxies" {
		return &proxyLookupReviewRows{Rows: rows, afterIDRead: c.afterIDRead}, nil
	}
	return rows, nil
}

type proxyLookupReviewRows struct {
	driver.Rows
	afterIDRead func() error
	once        sync.Once
	closeErr    error
}

func (r *proxyLookupReviewRows) Close() error {
	r.once.Do(func() {
		r.closeErr = r.Rows.Close()
		if r.closeErr == nil {
			r.closeErr = r.afterIDRead()
		}
	})
	return r.closeErr
}

func TestProxyLookupReviewTwoReadsUseOneCommittedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	_, writer := proxyLookupReviewDB(t, path)
	original := Proxy{ProxyId: "node", ProxyName: "Before", ProxyConfig: "http://before.invalid:8080", DnsServers: "1.1.1.1"}
	proxyLookupReviewPut(t, writer, original)
	original = proxyLookupReviewAssertCurrent(t, writer, "node", "node")
	var once sync.Once
	var mutationErr error
	mutated := false
	readerDB := sql.OpenDB(&proxyLookupReviewConnector{path: path, afterIDRead: func() error {
		once.Do(func() {
			// This executes after the ID cursor closes and before the target-row
			// query. WAL permits the second connection to commit while the reader
			// retains its snapshot. No sleeps or racing timing are required.
			mutated = true
			mutationErr = writer.ReplaceAll([]Proxy{
				{ProxyId: "node", ProxyName: "After", ProxyConfig: "https://after.invalid:443", DnsServers: "9.9.9.9"},
				{ProxyId: "NODE", ProxyName: "New collision", ProxyConfig: "http://collision.invalid:8080"},
			})
		})
		return mutationErr
	}})
	readerDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = readerDB.Close() })
	reader := NewSQLiteProxyDAO(readerDB)
	got, usable, err := reader.LookupUnambiguousProxy("node")
	if !mutated || mutationErr != nil {
		t.Fatalf("concurrent committed replacement was not exercised: mutated=%t err=%v", mutated, mutationErr)
	}
	if err != nil || !usable || !reflect.DeepEqual(got, original) {
		t.Fatalf("ID scan and row read mixed snapshots: got=%#v usable=%t err=%v want=%#v", got, usable, err, original)
	}
	if _, usable, err := reader.LookupUnambiguousProxy("node"); err != nil || usable {
		t.Fatalf("next lookup did not see committed collision: usable=%t err=%v", usable, err)
	}
}

func TestProxyLookupReviewSQLAndDecodeErrors(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"missing_table", `DROP TABLE browser_proxies`},
		{"missing_projection_column", `ALTER TABLE browser_proxies DROP COLUMN last_test_engine`},
		{"bad_target_number", `UPDATE browser_proxies SET last_latency_ms='not-an-integer' WHERE proxy_id='target'`},
		{"null_id", `INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config) VALUES(NULL,'Bad ID','http://invalid:80')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, dao := proxyLookupReviewDB(t, filepath.Join(t.TempDir(), "error.db"))
			proxyLookupReviewPut(t, dao, Proxy{ProxyId: "target", ProxyName: "Target", ProxyConfig: "http://127.0.0.1:8080"})
			if _, err := db.GetConn().Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			got, usable, err := dao.LookupUnambiguousProxy("target")
			if err == nil || usable || !reflect.DeepEqual(got, Proxy{}) {
				t.Fatalf("database error became a usable/missing row: %#v usable=%t err=%v", got, usable, err)
			}
		})
	}
	t.Run("closed_connection", func(t *testing.T) {
		db, dao := proxyLookupReviewDB(t, filepath.Join(t.TempDir(), "closed.db"))
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, usable, err := dao.LookupUnambiguousProxy("target"); err == nil || usable {
			t.Fatalf("closed DB became a missing row: usable=%t err=%v", usable, err)
		}
	})
	t.Run("nil_dao", func(t *testing.T) {
		var dao *SQLiteProxyDAO
		if _, usable, err := dao.LookupUnambiguousProxy("target"); err == nil || usable {
			t.Fatalf("nil DAO became a missing row: usable=%t err=%v", usable, err)
		}
	})
}

type proxyLookupReviewCompletion struct {
	Marker string   `json:"marker"`
	Count  int      `json:"count"`
	Cases  []string `json:"cases"`
}

const proxyLookupReviewCompletionMarker = "proxy-lookup-single-connection-complete-v1"

func proxyLookupReviewExpectedCompletion() proxyLookupReviewCompletion {
	return proxyLookupReviewCompletion{
		Marker: proxyLookupReviewCompletionMarker,
		Count:  10,
		Cases: []string{
			"hit", "missing", "direct", "empty", "ambiguous", "padded",
			"bad_number", "null_id", "missing_column", "missing_table",
		},
	}
}

func validateProxyLookupReviewCompletion(data []byte) error {
	expected, err := json.Marshal(proxyLookupReviewExpectedCompletion())
	if err != nil {
		return err
	}
	// The child writes this exact canonical JSON format. Exact comparison also
	// rejects duplicate fields/cases, extra output and superficially benign PASS
	// messages; exit zero alone does not prove that any required case executed.
	if !bytes.Equal(bytes.TrimSpace(data), expected) {
		return fmt.Errorf("missing or invalid single-connection completion record: got %q, want %s", data, expected)
	}
	return nil
}

func TestProxyLookupReviewCompletionEvidence(t *testing.T) {
	valid, err := json.Marshal(proxyLookupReviewExpectedCompletion())
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{valid, append(append([]byte(nil), valid...), '\n')} {
		if err := validateProxyLookupReviewCompletion(data); err != nil {
			t.Fatalf("complete positive evidence was rejected: %v", err)
		}
	}
	missingCase := proxyLookupReviewExpectedCompletion()
	missingCase.Cases = missingCase.Cases[:9]
	missingCase.Count = len(missingCase.Cases)
	duplicateCase := proxyLookupReviewExpectedCompletion()
	duplicateCase.Cases[9] = duplicateCase.Cases[0]
	extraCase := proxyLookupReviewExpectedCompletion()
	extraCase.Cases = append(extraCase.Cases, "extra")
	extraCase.Count++
	wrongMarker := proxyLookupReviewExpectedCompletion()
	wrongMarker.Marker += "-benign"
	wrongCount := proxyLookupReviewExpectedCompletion()
	wrongCount.Count = 0
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"missing", nil},
		{"empty", []byte(" \n")},
		{"null", []byte("null")},
		{"truncated", valid[:len(valid)-1]},
		{"pass_only", []byte("PASS\n")},
		{"no_tests_warning", []byte("testing: warning: no tests to run\nPASS\n")},
		{"skipped_child", []byte("--- SKIP: TestProxyLookupReviewSingleConnectionChild (0.00s)\nPASS\n")},
		{"marker_in_benign_log", []byte("benign log: " + string(valid))},
		{"trailing_pass_log", []byte(string(valid) + "\nPASS\n")},
		{"duplicate_record", []byte(string(valid) + "\n" + string(valid))},
		{"duplicate_json_field", []byte(strings.TrimSuffix(string(valid), "}") + `,"count":10}`)},
		{"unknown_json_field", []byte(strings.TrimSuffix(string(valid), "}") + `,"benign":true}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateProxyLookupReviewCompletion(tc.data); err == nil {
				t.Fatalf("incomplete or misleading evidence was accepted: %q", tc.data)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		record proxyLookupReviewCompletion
	}{
		{"missing_case", missingCase},
		{"duplicate_case", duplicateCase},
		{"extra_case", extraCase},
		{"wrong_marker", wrongMarker},
		{"wrong_count", wrongCount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.record)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateProxyLookupReviewCompletion(data); err == nil {
				t.Fatalf("incorrect execution inventory was accepted: %s", data)
			}
		})
	}
}

func TestProxyLookupReviewReleasesSingleConnection(t *testing.T) {
	// A leaked reader/transaction can block the lookup itself or a subsequent
	// writer forever. Isolate the entire sequence so a regression leaves no
	// blocked goroutine in the parent test process.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	childDir := t.TempDir()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProxyLookupReviewSingleConnectionChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ANT_PROXY_LOOKUP_REVIEW_CHILD=1", "ANT_PROXY_LOOKUP_REVIEW_DIR="+childDir)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("lookup did not release its sole DB connection: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("single-connection subprocess failed: %v\n%s", err, output)
	}
	completion, err := os.ReadFile(filepath.Join(childDir, "completion.json"))
	if err != nil {
		t.Fatalf("single-connection subprocess produced no completion record: %v\n%s", err, output)
	}
	if err := validateProxyLookupReviewCompletion(completion); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	t.Logf("PROXY_LOOKUP_COMPLETION_JSON:%s", bytes.TrimSpace(completion))
}

func TestProxyLookupReviewRejectsIncompleteChildExecution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filter  string
		enabled string
	}{
		{"misspelled_filter", "^TestProxyLookupReviewIntentionallyMissing$", "1"},
		{"skipped_entry", "^TestProxyLookupReviewSingleConnectionChild$", "0"},
		{"one_filtered_case", "^TestProxyLookupReviewSingleConnectionChild$/^hit$", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			childDir := t.TempDir()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+tc.filter, "-test.count=1")
			cmd.Env = append(os.Environ(), "ANT_PROXY_LOOKUP_REVIEW_CHILD="+tc.enabled, "ANT_PROXY_LOOKUP_REVIEW_DIR="+childDir)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("calibration child must exit zero to exercise false-positive rejection: %v\n%s", err, output)
			}
			completion, err := os.ReadFile(filepath.Join(childDir, "completion.json"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := validateProxyLookupReviewCompletion(completion); err == nil {
				t.Fatalf("zero-exit incomplete execution was accepted: %s\n%s", completion, output)
			}
		})
	}
}

func TestProxyLookupReviewSingleConnectionChild(t *testing.T) {
	if os.Getenv("ANT_PROXY_LOOKUP_REVIEW_CHILD") != "1" {
		t.Skip("launched only by the bounded parent regression")
	}
	childDir := os.Getenv("ANT_PROXY_LOOKUP_REVIEW_DIR")
	if childDir == "" {
		t.Fatal("single-connection child has no isolated output directory")
	}
	cases := []struct {
		name    string
		request string
		mutate  string
	}{
		{"hit", "target", ""},
		{"missing", "absent", ""},
		{"direct", "__DIRECT__", ""},
		{"empty", "\u2003", ""},
		{"ambiguous", "target", `INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config) VALUES('TARGET','Collision','http://other:80')`},
		{"padded", "target", `UPDATE browser_proxies SET proxy_id=' target '`},
		{"bad_number", "target", `UPDATE browser_proxies SET last_latency_ms='broken'`},
		{"null_id", "target", `INSERT INTO browser_proxies(proxy_id,proxy_name,proxy_config) VALUES(NULL,'Invalid','http://other:80')`},
		{"missing_column", "target", `ALTER TABLE browser_proxies DROP COLUMN last_test_engine`},
		{"missing_table", "target", `DROP TABLE browser_proxies`},
	}
	completed := make([]string, 0, len(cases))
	for _, tc := range cases {
		if !t.Run(tc.name, func(t *testing.T) {
			db, dao := proxyLookupReviewDB(t, filepath.Join(childDir, tc.name+".db"))
			db.GetConn().SetMaxOpenConns(1)
			proxyLookupReviewPut(t, dao, Proxy{ProxyId: "target", ProxyName: "Target", ProxyConfig: "http://127.0.0.1:8080"})
			if tc.mutate != "" {
				if _, err := db.GetConn().Exec(tc.mutate); err != nil {
					t.Fatal(err)
				}
			}
			_, _, _ = dao.LookupUnambiguousProxy(tc.request)
			if inUse := db.GetConn().Stats().InUse; inUse != 0 {
				t.Fatalf("lookup left %d DB connections in use", inUse)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := db.GetConn().ExecContext(ctx, `CREATE TABLE after_lookup (id INTEGER)`); err != nil {
				t.Fatalf("write after lookup failed: %v", err)
			}
			// Append only after all required checks execute. Filtered or skipped
			// subtests cannot appear in the completion inventory.
			completed = append(completed, tc.name)
		}) {
			return
		}
	}
	completion, err := json.Marshal(proxyLookupReviewCompletion{
		Marker: proxyLookupReviewCompletionMarker, Count: len(completed), Cases: completed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "completion.json"), append(completion, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
