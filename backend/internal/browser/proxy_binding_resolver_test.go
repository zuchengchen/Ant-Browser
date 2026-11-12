package browser

import (
	"ant-chrome/backend/internal/config"
	"reflect"
	"testing"
)

func TestProfileProxyBindingResolverPreservesMatchingRules(t *testing.T) {
	a := Proxy{ProxyId: "node-a", ProxyName: "Shared", ProxyConfig: "http://a:8080", SourceID: "source-a", SourceURL: "https://a.example/sub"}
	b := Proxy{ProxyId: "node-b", ProxyName: "Shared", ProxyConfig: "http://b:8080", SourceID: "source-b", SourceURL: "https://b.example/sub"}
	tests := []struct {
		name    string
		proxies []Proxy
		profile Profile
		wantID  string
		mode    string
	}{
		{"id-precedes-snapshot", []Proxy{a, b}, Profile{ProxyId: " NODE-A ", ProxyBindName: b.ProxyName, ProxyBindSourceID: b.SourceID}, "node-a", "proxy_id"},
		{"first-duplicate-id", []Proxy{a, {ProxyId: " NODE-A ", ProxyName: "Later", ProxyConfig: "http://later:8080"}}, Profile{ProxyId: "node-A"}, "node-a", "proxy_id"},
		{"source-id-name", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyBindName: " shared ", ProxyBindSourceID: " SOURCE-A "}, "node-a", "source_id+name"},
		{"source-url-name", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyBindName: "SHARED", ProxyBindSourceURL: " HTTPS://B.EXAMPLE/SUB "}, "node-b", "source_url+name"},
		{"unique-name", []Proxy{a}, Profile{ProxyId: "gone", ProxyBindName: "shared"}, "node-a", "name"},
		{"source-id-config", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyBindName: "ambiguous-or-gone", ProxyBindSourceID: "source-b", ProxyConfig: b.ProxyConfig}, "node-b", "source_id+config"},
		{"source-url-config", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyBindSourceURL: a.SourceURL, ProxyConfig: a.ProxyConfig}, "node-a", "source_url+config"},
		{"config", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyConfig: b.ProxyConfig}, "node-b", "config"},
		{"ambiguous-name", []Proxy{a, b}, Profile{ProxyId: "gone", ProxyBindName: "shared"}, "", ""},
		{"ambiguous-config", []Proxy{a, {ProxyId: "duplicate-config", ProxyConfig: a.ProxyConfig}}, Profile{ProxyId: "gone", ProxyConfig: a.ProxyConfig}, "", ""},
		{"local-config-stays-unbound", []Proxy{a}, Profile{ProxyConfig: a.ProxyConfig}, "", ""},
		{"missing-stays-bound-to-missing-id", []Proxy{a}, Profile{ProxyId: "gone"}, "", ""},
		{"blank-id-not-a-match", []Proxy{{ProxyId: " ", ProxyName: "Invalid", ProxyConfig: "http://invalid:8080"}}, Profile{}, "", ""},
		{"builtin-direct", nil, Profile{ProxyId: " __DIRECT__ "}, directProxyID, "proxy_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(&config.Config{Browser: config.BrowserConfig{Proxies: test.proxies}}, "")
			profile := test.profile
			resolver := manager.NewProfileProxyBindingResolver()
			changed, bound, mode := resolver(&profile)
			if test.wantID == "" {
				if changed || bound || mode != "" || !reflect.DeepEqual(profile, test.profile) {
					t.Fatalf("unresolved binding changed: changed=%v bound=%v mode=%q profile=%+v", changed, bound, mode, profile)
				}
				return
			}
			if !changed || !bound || mode != test.mode || profile.ProxyId != test.wantID {
				t.Fatalf("binding: changed=%v bound=%v mode=%q profile=%+v", changed, bound, mode, profile)
			}
			if test.name == "first-duplicate-id" && profile.ProxyConfig != a.ProxyConfig {
				t.Fatalf("duplicate ID selected later catalog entry: %+v", profile)
			}
			before := profile
			if changed, bound, mode = resolver(&profile); changed || !bound || mode != "proxy_id" || !reflect.DeepEqual(profile, before) {
				t.Fatalf("second resolution not idempotent: changed=%v bound=%v mode=%q profile=%+v", changed, bound, mode, profile)
			}
		})
	}
	manager := NewManager(config.DefaultConfig(), "")
	if changed, bound, mode := manager.NewProfileProxyBindingResolver()(nil); changed || bound || mode != "" {
		t.Fatalf("nil profile: %v %v %q", changed, bound, mode)
	}
}
