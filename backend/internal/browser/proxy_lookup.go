package browser

import (
	"fmt"
	"strings"
)

// UnambiguousProxyLookupDAO is an optional capability, not a ProxyDAO
// requirement. usable=true returns the complete current row; usable=false with
// no error requires the original directory lookup. A database failure is an
// error, never a missing row. Callers must preserve their original error path.
type UnambiguousProxyLookupDAO interface {
	LookupUnambiguousProxy(proxyID string) (item Proxy, usable bool, err error)
}

// LookupUnambiguousProxy reads one complete row only when every existing ID
// lookup would identify that same row. SQLite's primary key is case-sensitive,
// while callers use Go's Unicode EqualFold, sometimes after TrimSpace. Scanning
// just IDs preserves those rules without decoding and sorting every full row.
// This remains a linear ID scan; it does not cache the directory or add an index.
func (d *SQLiteProxyDAO) LookupUnambiguousProxy(proxyID string) (Proxy, bool, error) {
	proxyID = strings.TrimSpace(proxyID)
	if proxyID == "" || strings.EqualFold(proxyID, directProxyID) {
		return Proxy{}, false, nil
	}
	if d == nil || d.db == nil {
		return Proxy{}, false, fmt.Errorf("代理查询数据库不可用")
	}

	// Both reads must see one committed snapshot. Release the transaction before
	// returning: the application uses a single DB connection and will write the
	// speed result after probing, so no reader may survive this method.
	tx, err := d.db.Begin()
	if err != nil {
		return Proxy{}, false, fmt.Errorf("开始代理查询失败: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT proxy_id FROM browser_proxies`)
	if err != nil {
		return Proxy{}, false, fmt.Errorf("查询代理 ID 失败: %w", err)
	}
	matchedID := ""
	matches := 0
	for rows.Next() {
		var storedID string
		if err := rows.Scan(&storedID); err != nil {
			rows.Close()
			return Proxy{}, false, fmt.Errorf("读取代理 ID 失败: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(storedID), proxyID) {
			matchedID = storedID
			matches++
			if matches > 1 {
				break
			}
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return Proxy{}, false, fmt.Errorf("读取代理 ID 失败: %w", readErr)
	}
	if closeErr != nil {
		return Proxy{}, false, fmt.Errorf("关闭代理 ID 查询失败: %w", closeErr)
	}
	if matches != 1 || matchedID != strings.TrimSpace(matchedID) {
		return Proxy{}, false, nil
	}

	// Keep the projection and scanProxies decoding identical to List. In
	// particular, PreferredKernel, DNS and speed provenance are not optional.
	rows, err = tx.Query(`
		SELECT proxy_id, proxy_name, proxy_config, COALESCE(preferred_kernel, ''), dns_servers, COALESCE(group_name, ''),
		       COALESCE(source_id, ''), COALESCE(source_url, ''), COALESCE(source_name_prefix, ''),
		       COALESCE(source_auto_refresh, 0), COALESCE(source_refresh_interval_m, 0), COALESCE(source_last_refresh_at, ''),
		       COALESCE(source_fetch_proxy_id, ''), COALESCE(source_last_refresh_error, ''),
		       COALESCE(source_missing, 0), COALESCE(source_missing_since, ''),
		       COALESCE(last_latency_ms, -1), COALESCE(last_test_ok, 0), COALESCE(last_tested_at, ''),
		       COALESCE(last_test_engine, ''), COALESCE(last_test_connector_type, ''), COALESCE(last_test_fingerprint, ''),
		       COALESCE(last_ip_health_json, ''),
		       sort_order
		FROM browser_proxies WHERE proxy_id = ?`, matchedID)
	if err != nil {
		return Proxy{}, false, fmt.Errorf("查询单个代理失败: %w", err)
	}
	items, readErr := scanProxies(rows)
	closeErr = rows.Close()
	if readErr != nil {
		return Proxy{}, false, readErr
	}
	if closeErr != nil {
		return Proxy{}, false, fmt.Errorf("关闭单个代理查询失败: %w", closeErr)
	}
	if len(items) != 1 {
		return Proxy{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return Proxy{}, false, fmt.Errorf("完成代理查询失败: %w", err)
	}
	return items[0], true, nil
}
