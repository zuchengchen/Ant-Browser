package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ant-chrome/backend/internal/config"
)

func TestPageProbeDoesNotUseDirectForUnknownProxy(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := &BrowserPageProbeConfig{URLs: []string{server.URL}, Concurrency: 1, Timeout: time.Second}
	proxies := []config.BrowserProxy{{ProxyId: "__direct__", ProxyConfig: "direct://"}}
	for _, id := range []string{"missing", ""} {
		result := ProbeBrowserPageConnectivity(id, proxies, nil, nil, nil, nil, cfg)
		if result.Ok || result.Error == "" || requests.Load() != 0 {
			t.Fatalf("invalid proxy %q: result = %+v, requests = %d", id, result, requests.Load())
		}
	}
	if result := ProbeBrowserPageConnectivity("__direct__", proxies, nil, nil, nil, nil, cfg); !result.Ok || requests.Load() != 1 {
		t.Fatalf("explicit direct: result = %+v, requests = %d", result, requests.Load())
	}
}

func TestIPHealthRejectsNullResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "null")
	}))
	defer server.Close()
	proxies := []config.BrowserProxy{{ProxyId: "__direct__", ProxyConfig: "direct://"}}
	result, err := FetchIPHealthInfo("__direct__", proxies, nil, nil, nil, nil, config.BrowserConnectorXray, &IPHealthConfig{URL: server.URL})
	if err == nil || result["error"] != "IP 健康检测响应必须是 JSON 对象" || result["_targetUrl"] != server.URL {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestProxyClientKeepsDownloadBridgeWhileProbeReleases(t *testing.T) {
	binaryPath := filepath.Join("..", "..", "..", "bin", runtime.GOOS+"-"+runtime.GOARCH, "xray")
	if runtime.GOOS == "windows" {
		binaryPath = filepath.Join("..", "..", "..", "bin", "xray.exe")
	}
	stat, err := os.Stat(binaryPath)
	if os.IsNotExist(err) {
		t.Skip("bundled Xray is unavailable on this platform")
	}
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm()&0o111 == 0 {
		t.Skip("bundled Xray is not marked executable")
	}
	binary, err := filepath.Abs(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Browser.XrayBinaryPath = binary
	manager := NewXrayManager(cfg, t.TempDir())
	t.Cleanup(manager.StopAll)

	mayFinish := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(mayFinish) }) }
	// The local HTTP proxy accepts CONNECT and serves the tunneled requests itself.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusBadRequest)
			return
		}
		conn, tunnel, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(tunnel, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if tunnel.Flush() != nil {
			return
		}
		req, err := http.ReadRequest(tunnel.Reader)
		if err != nil {
			return
		}
		if req.URL.Path == "/probe" {
			fmt.Fprint(tunnel, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
		} else {
			fmt.Fprint(tunnel, "HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhead")
			if tunnel.Flush() != nil {
				return
			}
			<-mayFinish
			fmt.Fprint(tunnel, "tail")
		}
		_ = tunnel.Flush()
	}))
	defer upstream.Close()
	defer finish()

	addr, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	addr.User = url.UserPassword("fixture", "fixture")
	proxies := []config.BrowserProxy{{ProxyId: "p", ProxyConfig: addr.String()}}
	client, release, err := AcquireProxyHTTPClient(context.Background(), addr.String(), "", proxies, manager, nil, nil, nil, config.BrowserConnectorXray, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	target := "http://download.example.test"
	resp, err := client.Get(target + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, head); err != nil || string(head) != "head" {
		t.Fatalf("download prefix = %q, err = %v", head, err)
	}
	probe := &BrowserPageProbeConfig{URLs: []string{target + "/probe"}, Timeout: 2 * time.Second, Concurrency: 1}
	if result := ProbeBrowserPageConnectivity("p", proxies, manager, nil, nil, nil, probe); !result.Ok {
		t.Fatalf("concurrent probe failed: %+v", result)
	}
	manager.mu.Lock()
	for _, bridge := range manager.Bridges {
		bridge.LastUsedAt = time.Now().Add(-2 * xrayBridgeIdleTTL)
	}
	manager.mu.Unlock()
	manager.recycleIdleBridges()

	finish()
	tail, err := io.ReadAll(resp.Body)
	if err != nil || string(tail) != "tail" {
		t.Fatalf("download suffix = %q, err = %v", tail, err)
	}
	release()
	manager.mu.Lock()
	remaining := len(manager.Bridges)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d bridges remain after the last release", remaining)
	}
}

func TestPageProbeRoutesStandardClashNodeThroughItsProxy(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")
	host, port, _ := strings.Cut(hostPort, ":")
	node := fmt.Sprintf("- type: http\n  server: %s\n  port: %s\n", host, port)
	proxies := []config.BrowserProxy{{ProxyId: "p", ProxyConfig: node}}
	target := "http://proxy-test.example.test/probe"
	cfg := &BrowserPageProbeConfig{URLs: []string{target}, Concurrency: 1, Timeout: time.Second}
	if result := ProbeBrowserPageConnectivity("p", proxies, nil, nil, nil, nil, cfg); !result.Ok {
		t.Fatalf("probe = %+v", result)
	}
	select {
	case path := <-requests:
		if path != target {
			t.Fatalf("upstream URL = %q, want %q", path, target)
		}
	default:
		t.Fatal("proxy did not receive the request")
	}
}
