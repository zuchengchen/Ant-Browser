package backend

import (
	"strings"
	"unicode"

	"ant-chrome/backend/internal/browser"
)

// latestProxiesForSpeedTest narrows only unambiguous, standalone proxy inputs.
// Unsupported DAOs, lookup failures and complex inputs retain the authoritative
// full-directory path, including its logging and refusal to use stale config.
func (a *App) latestProxiesForSpeedTest(proxyID string) []BrowserProxy {
	requestedID := strings.TrimSpace(proxyID)
	if requestedID != "" && !strings.EqualFold(requestedID, "__direct__") && a != nil && a.browserMgr != nil {
		if lookup, ok := a.browserMgr.ProxyDAO.(browser.UnambiguousProxyLookupDAO); ok {
			item, usable, err := lookup.LookupUnambiguousProxy(requestedID)
			if err == nil && usable && isSimpleProxySpeedConfig(item.ProxyConfig) {
				return []BrowserProxy{item}
			}
		}
	}
	return a.getLatestProxies()
}

// isSimpleProxySpeedConfig deliberately covers only standalone standard proxy
// URIs. Chains, YAML, sing-box protocols and other formats keep their old input
// path; this helper does not select a kernel or validate a proxy configuration.
func isSimpleProxySpeedConfig(value string) bool {
	src := strings.ToLower(strings.TrimSpace(value))
	if strings.IndexFunc(src, unicode.IsSpace) >= 0 || strings.Contains(src, "type:") || strings.Contains(src, "proxies:") || strings.Contains(src, "proxy:") {
		return false
	}
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "socks5://")
}
