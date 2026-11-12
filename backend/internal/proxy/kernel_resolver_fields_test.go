package proxy

import (
	"reflect"
	"testing"

	"ant-chrome/backend/internal/config"
)

func TestResolveProxyKernelForConnectorPreservesDiagnosticFields(t *testing.T) {
	for _, tc := range []struct {
		name, source, connector string
		want                    ProxyKernelResolution
	}{
		{"direct", "direct://", config.BrowserConnectorMihomo, ProxyKernelResolution{Protocol: "direct", PreferredKernel: ProxyKernelNative, Kernel: ProxyKernelNative, SupportedKernels: []string{ProxyKernelNative}, Reason: "直连无需代理内核"}},
		{"empty", "", config.BrowserConnectorXray, ProxyKernelResolution{Protocol: "direct", PreferredKernel: ProxyKernelNative, Kernel: ProxyKernelNative, SupportedKernels: []string{ProxyKernelNative}, Reason: "直连无需代理内核"}},
		{"http-auth", "http://synthetic:password@127.0.0.1:12345", config.BrowserConnectorXray, ProxyKernelResolution{Protocol: "http", PreferredKernel: ProxyKernelXray, Kernel: ProxyKernelXray, SupportedKernels: []string{ProxyKernelXray, ProxyKernelMihomo}, Reason: "使用代理指定内核"}},
		{"anytls-yaml", "type: anytls\nserver: localhost\nport: 12345\npassword: synthetic", config.BrowserConnectorXray, ProxyKernelResolution{Protocol: "anytls", PreferredKernel: ProxyKernelSingBox, Kernel: ProxyKernelSingBox, SupportedKernels: []string{ProxyKernelSingBox, ProxyKernelMihomo}, Reason: "使用代理指定内核"}},
		{"vless-yaml", "type: vless\nserver: localhost\nport: 12345\nuuid: 00000000-0000-0000-0000-000000000001", config.BrowserConnectorMihomo, ProxyKernelResolution{Protocol: "vless", PreferredKernel: ProxyKernelMihomo, Kernel: ProxyKernelMihomo, SupportedKernels: []string{ProxyKernelXray, ProxyKernelMihomo}, Reason: "使用代理指定内核"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveProxyKernelForConnector(tc.source, nil, "", tc.connector)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("diagnostic result=%+v, want=%+v", got, tc.want)
			}
		})
	}
}
