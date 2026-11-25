package backend

import (
	"ant-chrome/backend/internal/proxy"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *App) unifiedProxyCoreHTTPClient(ctx context.Context, timeout time.Duration, proxyConfig string) (*http.Client, func(), string, error) {
	if a == nil {
		return nil, nil, "", fmt.Errorf("应用实例为空")
	}
	rawProxyConfig := strings.TrimSpace(proxyConfig)
	if rawProxyConfig == "" || rawProxyConfig == "__direct__" || strings.EqualFold(rawProxyConfig, "direct://") {
		client := &http.Client{Timeout: timeout, Transport: proxyCoreDirectTransport()}
		return client, client.CloseIdleConnections, "直连", nil
	}
	if parsed, err := url.Parse(rawProxyConfig); err == nil && isBadLocalHTTPSProxy(parsed) {
		return nil, nil, "", fmt.Errorf("下载代理不能填 %s，127.0.0.1:443 通常不是本机代理端口；请改成真实代理端口，如 socks5://127.0.0.1:7890，或留空直连", parsed.Host)
	}

	var proxies []BrowserProxy
	if a.browserMgr != nil {
		proxies = a.getLatestProxies()
	} else {
		proxies = a.proxyConfigFallback()
	}
	client, release, err := proxy.AcquireProxyHTTPClient(ctx, rawProxyConfig, "", proxies, a.xrayMgr, a.singboxMgr, a.clashMgr, a.mieruMgr, a.defaultProxyConnectorType(), timeout)
	if err != nil {
		return nil, nil, "", err
	}
	return client, release, "统一代理连接链路", nil
}
