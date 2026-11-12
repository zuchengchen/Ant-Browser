package backend

import (
	"reflect"
	"testing"
	"time"

	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
)

type proxySpeedLookupReviewDAO struct {
	browser.ProxyDAO
	selected BrowserProxy
	all      []BrowserProxy
	reads    int
}

func (d *proxySpeedLookupReviewDAO) List() ([]browser.Proxy, error) {
	d.reads++
	return append([]browser.Proxy(nil), d.all...), nil
}

func (d *proxySpeedLookupReviewDAO) LookupUnambiguousProxy(string) (browser.Proxy, bool, error) {
	return d.selected, true, nil
}

func TestProxySpeedLookupReviewConfigBoundary(t *testing.T) {
	cases := []struct {
		name   string
		config string
		fast   bool
	}{
		{"http", "http://127.0.0.1:8080", true},
		{"https", "https://localhost:8443", true},
		{"socks5", "socks5://localhost:1080", true},
		{"http_auth", "http://user:pass@localhost:8080", true},
		{"https_auth_ipv6", "https://user:p%40ss@[2001:db8::1]:8443", true},
		{"socks_auth", "socks5://user:p%3Ass@localhost:1080", true},
		{"upper_scheme", "HTTP://LOCALHOST:8080", true},
		{"outer_unicode_space", "\u2003https://localhost:8443\u00a0", true},
		{"escaped_space", "http://user:pass%20word@localhost:8080", true},
		{"query_and_fragment", "http://localhost:8080?fixture=1#fixture", true},
		{"blank", " \t\n", false},
		{"bare_host", "localhost:8080", false},
		{"wrong_scheme", "httpx://localhost:8080", false},
		{"missing_slashes", "http:localhost:8080", false},
		{"socks_alias", "socks://localhost:1080", false},
		{"socks_remote_dns", "socks5h://localhost:1080", false},
		{"direct", "direct://", false},
		{"chain", "chain+socks5://%7B%22first%22%3A%7B%7D%7D", false},
		{"clash", "clash://encoded", false},
		{"vmess", "vmess://encoded", false},
		{"vless", "vless://user@localhost:443", false},
		{"trojan", "trojan://pass@localhost:443", false},
		{"shadowsocks", "ss://encoded@localhost:443", false},
		{"hysteria", "hysteria://localhost:443", false},
		{"hysteria2", "hysteria2://pass@localhost:443", false},
		{"tuic", "tuic://id:pass@localhost:443", false},
		{"anytls", "anytls://pass@localhost:443", false},
		{"yaml", "type: http\nserver: localhost\nport: 8080", false},
		{"inline_yaml", "{type:http,server:localhost,port:8080}", false},
		{"internal_ascii_space", "http://local host:8080", false},
		{"internal_nbsp", "http://local\u00a0host:8080", false},
		{"internal_em_space", "http://local\u2003host:8080", false},
		{"internal_line_separator", "http://localhost:8080\u2028socks5://other:1080", false},
		{"multiple_lines", "http://localhost:8080\nsocks5://other:1080", false},
		{"yaml_marker_after_uri", "http://localhost:8080#proxies:[]", false},
		{"type_marker_after_uri", "http://localhost:8080#type:http", false},
		{"proxy_marker_after_uri", "http://localhost:8080#proxy:node", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSimpleProxySpeedConfig(tc.config); got != tc.fast {
				t.Fatalf("isSimpleProxySpeedConfig(%q)=%t, want %t", tc.config, got, tc.fast)
			}
			target := BrowserProxy{
				ProxyId: "node", ProxyConfig: tc.config, PreferredKernel: "mihomo", DnsServers: "9.9.9.9",
				LastLatencyMs: 31, LastTestOk: true, LastTestEngine: "native", LastTestConnectorType: "mihomo",
				LastTestedAt: time.Now().Format(time.RFC3339), LastTestFingerprint: "fixture",
			}
			full := []BrowserProxy{
				{ProxyId: "__direct__", ProxyConfig: "direct://"},
				{ProxyId: "dependency", ProxyConfig: "http://other.invalid:8080"},
				target,
			}
			dao := &proxySpeedLookupReviewDAO{selected: target, all: full}
			app := NewApp(t.TempDir())
			app.config = config.DefaultConfig()
			app.browserMgr = &browser.Manager{ProxyDAO: dao}
			got := app.latestProxiesForSpeedTest(" NODE ")
			if tc.fast {
				if dao.reads != 0 || !reflect.DeepEqual(got, []BrowserProxy{target}) {
					t.Fatalf("standalone config lost original fields: got=%#v full reads=%d", got, dao.reads)
				}
				oldCache, oldOK := app.readFreshProxySpeedResult("NODE", "mihomo", full)
				newCache, newOK := app.readFreshProxySpeedResult("NODE", "mihomo", got)
				if !oldOK || !newOK || !reflect.DeepEqual(newCache, oldCache) {
					t.Fatalf("single-row cache differs from full directory: old=%#v/%t new=%#v/%t", oldCache, oldOK, newCache, newOK)
				}
			} else if dao.reads != 1 || !reflect.DeepEqual(got, full) {
				t.Fatalf("complex/other protocol lost directory dependencies/order: got=%#v full reads=%d", got, dao.reads)
			}
		})
	}
}

func TestProxySpeedLookupReviewDirectAliasKeepsSyntheticDirectFirst(t *testing.T) {
	alias := BrowserProxy{ProxyId: "__DIRECT__", ProxyName: "Stored alias", ProxyConfig: "http://alias.invalid:8080"}
	dao := &proxySpeedLookupReviewDAO{selected: alias, all: []BrowserProxy{alias}}
	app := NewApp(t.TempDir())
	app.config = config.DefaultConfig()
	app.browserMgr = &browser.Manager{ProxyDAO: dao}
	got := app.latestProxiesForSpeedTest(" __DIRECT__ ")
	if dao.reads != 1 || len(got) != 2 || got[0].ProxyId != "__direct__" || got[0].ProxyConfig != "direct://" || got[1] != alias {
		t.Fatalf("synthetic direct precedence changed: got=%#v full reads=%d", got, dao.reads)
	}
}
