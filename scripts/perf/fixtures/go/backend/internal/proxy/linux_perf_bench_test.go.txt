package proxy

import (
	"fmt"
	"testing"

	"ant-chrome/backend/internal/config"
)

// BenchmarkLinuxPerfProxyResolve measures the resolver used by startup,
// warmup, speed tests and IP-health requests. The target is the final row, so
// all recorded directory sizes exercise the full existing lookup cost.
func BenchmarkLinuxPerfProxyResolve(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		for _, tc := range []struct {
			name, source, connector, kernel string
		}{
			{"vless_yaml_xray", "name: synthetic\ntype: vless\nserver: 127.0.0.1\nport: 12345\nuuid: 00000000-0000-0000-0000-000000000001\nnetwork: tcp", config.BrowserConnectorXray, ProxyKernelXray},
			{"anytls_yaml_xray", "name: synthetic\ntype: anytls\nserver: 127.0.0.1\nport: 12345\npassword: synthetic-test-password\nsni: localhost", config.BrowserConnectorXray, ProxyKernelSingBox},
			{"vless_yaml_mihomo", "name: synthetic\ntype: vless\nserver: 127.0.0.1\nport: 12345\nuuid: 00000000-0000-0000-0000-000000000001\nnetwork: tcp", config.BrowserConnectorMihomo, ProxyKernelMihomo},
			{"http_auth_uri", "http://synthetic:password@127.0.0.1:12345", config.BrowserConnectorXray, ProxyKernelXray},
		} {
			b.Run(fmt.Sprintf("%s/%d", tc.name, size), func(b *testing.B) {
				proxies := make([]config.BrowserProxy, size)
				for i := range proxies {
					proxies[i] = config.BrowserProxy{ProxyId: fmt.Sprintf("synthetic-%06d", i), ProxyName: "Synthetic", ProxyConfig: tc.source}
				}
				id := proxies[len(proxies)-1].ProxyId
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					resolved, err := ResolveProxyKernelForConnector("", proxies, id, tc.connector)
					if err != nil || resolved.Kernel != tc.kernel {
						b.Fatalf("resolution=%+v err=%v", resolved, err)
					}
				}
			})
		}
	}
}

func BenchmarkLinuxPerfSingBoxOutbound(b *testing.B) {
	source := "name: synthetic\ntype: anytls\nserver: 127.0.0.1\nport: 12345\npassword: synthetic-test-password\nsni: localhost"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		outbound, err := BuildSingBoxOutbound(source)
		if err != nil || outbound["type"] != "anytls" {
			b.Fatalf("outbound=%v err=%v", outbound, err)
		}
	}
}
