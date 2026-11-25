package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"

	xproxy "golang.org/x/net/proxy"
)

var ErrProxyConfigUnavailable = errors.New("代理不存在或配置为空")

func buildProbeHTTPClientContext(
	ctx context.Context,
	src string,
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	clashMgr *ClashManager,
	mieruMgr *MieruManager,
	connectorType string,
	timeout time.Duration,
) (*http.Client, func(), error) {
	return AcquireProxyHTTPClient(ctx, src, proxyId, proxies, xrayMgr, singboxMgr, clashMgr, mieruMgr, connectorType, timeout)
}

// AcquireProxyHTTPClient holds a bridge reference until release is called after
// all requests and response bodies have finished.
func AcquireProxyHTTPClient(
	ctx context.Context,
	src string,
	proxyId string,
	proxies []config.BrowserProxy,
	xrayMgr *XrayManager,
	singboxMgr *SingBoxManager,
	clashMgr *ClashManager,
	mieruMgr *MieruManager,
	connectorType string,
	timeout time.Duration,
) (*http.Client, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	proxyId = strings.TrimSpace(proxyId)
	src = resolveProxyConfig(src, proxies, proxyId)
	if src == "" && proxyId != "" {
		return nil, nil, fmt.Errorf("%w: %s", ErrProxyConfigUnavailable, proxyId)
	}
	log := logger.New("ProxyHTTPClient")
	resolution, err := ResolveProxyKernelForConnector(src, proxies, proxyId, connectorType)
	if err != nil {
		log.Warn("代理内核解析失败",
			logger.F("proxy_id", proxyId),
			logger.F("error", err.Error()),
		)
		return nil, nil, err
	}
	log.Info("代理 HTTP 客户端内核选择",
		logger.F("proxy_id", proxyId),
		logger.F("protocol", resolution.Protocol),
		logger.F("kernel", resolution.Kernel),
		logger.F("preferred_kernel", resolution.PreferredKernel),
		logger.F("supported_kernels", strings.Join(resolution.SupportedKernels, ",")),
		logger.F("reason", resolution.Reason),
	)
	var client *http.Client
	var releaseBridge func()
	switch resolution.Kernel {
	case ProxyKernelNative:
		client, err = buildNativeProxyHTTPClient(src, resolution.Protocol, timeout)
	case ProxyKernelMieru:
		if mieruMgr == nil {
			return nil, nil, fmt.Errorf("mieru 管理器未初始化")
		}
		socks5Addr, key, bridgeErr := mieruMgr.ensureBridgeContext(ctx, src, proxies, proxyId, true)
		if bridgeErr != nil {
			return nil, nil, fmt.Errorf("mieru 桥接启动失败: %w", bridgeErr)
		}
		client, err = buildSocks5HTTPClient(strings.TrimPrefix(socks5Addr, "socks5://"), timeout)
		releaseBridge = func() {
			mieruMgr.ReleaseBridge(key)
			mieruMgr.ReclaimBridgeIfIdle(key)
		}
	case ProxyKernelMihomo:
		if clashMgr == nil {
			return nil, nil, fmt.Errorf("Mihomo 管理器未初始化")
		}
		proxyAddr, key, bridgeErr := clashMgr.ensureNodeBridgeContext(ctx, src, proxies, proxyId, true)
		if bridgeErr != nil {
			return nil, nil, fmt.Errorf("Mihomo 桥接启动失败: %w", bridgeErr)
		}
		client, err = buildHTTPProxyClient(proxyAddr, timeout)
		releaseBridge = func() {
			clashMgr.ReleaseNodeBridge(key)
			clashMgr.ReclaimBridgeIfIdle(key)
		}
	case ProxyKernelSingBox:
		if singboxMgr == nil {
			return nil, nil, fmt.Errorf("sing-box 管理器未初始化")
		}
		socks5Addr, key, bridgeErr := singboxMgr.ensureBridgeContext(ctx, src, proxies, proxyId, true)
		if bridgeErr != nil {
			return nil, nil, fmt.Errorf("sing-box 桥接启动失败: %w", bridgeErr)
		}
		client, err = buildSocks5HTTPClient(strings.TrimPrefix(socks5Addr, "socks5://"), timeout)
		releaseBridge = func() {
			singboxMgr.ReleaseBridge(key)
			singboxMgr.ReclaimBridgeIfIdle(key)
		}
	case ProxyKernelXray:
		if xrayMgr == nil {
			return nil, nil, fmt.Errorf("xray 管理器未初始化")
		}
		socks5Addr, key, bridgeErr := xrayMgr.ensureBridgeContext(ctx, src, proxies, proxyId, true)
		if bridgeErr != nil {
			return nil, nil, fmt.Errorf("xray 桥接启动失败: %w", bridgeErr)
		}
		client, err = buildSocks5HTTPClient(strings.TrimPrefix(socks5Addr, "socks5://"), timeout)
		releaseBridge = func() {
			xrayMgr.ReleaseBridge(key)
			xrayMgr.ReclaimBridgeIfIdle(key)
		}
	default:
		return nil, nil, fmt.Errorf("无法为协议 %s 选择代理内核", resolution.Protocol)
	}
	if err != nil {
		if releaseBridge != nil {
			releaseBridge()
		}
		return nil, nil, err
	}
	var once sync.Once
	return client, func() {
		once.Do(func() {
			client.CloseIdleConnections()
			if releaseBridge != nil {
				releaseBridge()
			}
		})
	}, nil
}

func buildNativeProxyHTTPClient(src, protocol string, timeout time.Duration) (*http.Client, error) {
	if protocol == "direct" {
		return &http.Client{Timeout: timeout, Transport: &http.Transport{}}, nil
	}
	address, _, err := ParseProxyNode(src)
	if err != nil || address == "" {
		return nil, fmt.Errorf("代理地址解析失败")
	}
	switch protocol {
	case "socks5":
		u, err := url.Parse(address)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 地址解析失败")
		}
		var auth *xproxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: pass}
		}
		return newSocks5HTTPClient(u.Host, auth, timeout)
	case "http", "https":
		return buildHTTPProxyClient(address, timeout)
	default:
		return nil, fmt.Errorf("不支持的直连协议: %s", protocol)
	}
}

func buildHTTPProxyClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("HTTP 代理地址解析失败")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func buildSocks5HTTPClient(socks5Host string, timeout time.Duration) (*http.Client, error) {
	return newSocks5HTTPClient(socks5Host, nil, timeout)
}

func newSocks5HTTPClient(socks5Host string, auth *xproxy.Auth, timeout time.Duration) (*http.Client, error) {
	dialer, err := xproxy.SOCKS5("tcp", socks5Host, auth, xproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 dialer 创建失败: %w", err)
	}
	contextDialer, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer 不支持 ContextDialer")
	}
	transport := &http.Transport{DialContext: contextDialer.DialContext}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
