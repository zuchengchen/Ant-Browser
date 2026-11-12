//go:build linux && linuxperf

package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/testutil"
)

// TestLinuxPerfProxyLocalPaths exercises actual shipped kernels and a pinned
// Mihomo release against loopback-only fixtures. It intentionally does not skip
// unavailable kernels. linuxperf opts this infrastructure-dependent test into
// measurement runs; normal product test runs need no extra runtime download.
func TestLinuxPerfProxyLocalPaths(t *testing.T) {
	samples := 30
	if raw := os.Getenv("LINUXPERF_PROXY_SAMPLES"); raw != "" {
		var err error
		samples, err = strconv.Atoi(raw)
		if err != nil || samples < 1 {
			t.Fatalf("invalid LINUXPERF_PROXY_SAMPLES: %q", raw)
		}
	}
	root := t.TempDir()
	xrayBinary := testutil.CopyBundledRuntime(t, "xray")
	singboxBinary := testutil.CopyBundledRuntime(t, "sing-box")
	mihomoBinary := linuxPerfCopyRuntime(t, os.Getenv("LINUXPERF_MIHOMO_BINARY"), "mihomo")
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	cfg.Browser.XrayBinaryPath = xrayBinary
	cfg.Browser.SingBoxBinaryPath = singboxBinary
	cfg.Browser.ClashBinaryPath = mihomoBinary
	// Avoid a caller's ambient Xray override taking precedence over the fixed
	// runtime selected from this exact checkout.
	t.Setenv("XRAY_BINARY_PATH", xrayBinary)
	t.Setenv("SINGBOX_BINARY_PATH", singboxBinary)

	const payload = "ant-browser-linux-perf-loopback-v1\n"
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(target.Close)
	targetAddress := strings.TrimPrefix(target.URL, "http://")
	httpProxy := linuxPerfAuthHTTPProxy(t, targetAddress)
	certPath, keyPath, caPath := linuxPerfTLS(t, root)
	t.Setenv("SSL_CERT_FILE", caPath)
	caDir := filepath.Join(root, "empty-ca-dir")
	if err := os.Mkdir(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_DIR", caDir)
	anytlsPort := linuxPerfAnyTLSServer(t, singboxBinary, root, certPath, keyPath)
	anytlsSrc := fmt.Sprintf("anytls://linux-perf-password@127.0.0.1:%d?sni=localhost", anytlsPort)
	outbound, err := BuildSingBoxOutbound(anytlsSrc)
	if err != nil || outbound["tls"].(map[string]interface{})["insecure"] != false {
		t.Fatalf("AnyTLS fixture must validate TLS, outbound=%v err=%v", outbound, err)
	}

	metrics := make(map[string][]float64)
	for _, tc := range []struct {
		name, connector, src string
	}{
		{ProxyKernelXray, config.BrowserConnectorXray, httpProxy},
		{ProxyKernelSingBox, config.BrowserConnectorXray, anytlsSrc},
		{ProxyKernelMihomo, config.BrowserConnectorMihomo, httpProxy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var xm *XrayManager
			var sm *SingBoxManager
			var mm *ClashManager
			switch tc.name {
			case ProxyKernelXray:
				xm = NewXrayManager(cfg, root)
				t.Cleanup(xm.StopAll)
			case ProxyKernelSingBox:
				sm = NewSingBoxManager(cfg, root)
				t.Cleanup(sm.StopAll)
			case ProxyKernelMihomo:
				mm = NewClashManager(cfg, root)
				t.Cleanup(mm.StopAll)
			}
			resolution, err := ResolveProxyKernelForConnector(tc.src, nil, "", tc.connector)
			if err != nil || resolution.Kernel != tc.name {
				t.Fatalf("wrong connector resolution: %+v, %v", resolution, err)
			}
			newClient := func(src string) (*http.Client, func()) {
				t.Helper()
				client, release, err := buildProbeHTTPClientContext(context.Background(), src, "", nil, xm, sm, mm, nil, tc.connector, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				return client, release
			}
			snapshot := func() (int, <-chan struct{}, int, int, *exec.Cmd) {
				t.Helper()
				if xm != nil {
					xm.mu.Lock()
					defer xm.mu.Unlock()
					for _, bridge := range xm.Bridges {
						return bridge.Pid, bridge.ExitDone, bridge.RefCount, len(xm.Bridges), bridge.Cmd
					}
				}
				if sm != nil {
					sm.mu.Lock()
					defer sm.mu.Unlock()
					for _, bridge := range sm.Bridges {
						return bridge.Pid, bridge.ExitDone, bridge.RefCount, len(sm.Bridges), bridge.Cmd
					}
				}
				if mm != nil {
					mm.mu.Lock()
					defer mm.mu.Unlock()
					for _, bridge := range mm.NodeBridges {
						return bridge.Pid, bridge.ExitDone, bridge.RefCount, len(mm.NodeBridges), bridge.Cmd
					}
				}
				return 0, nil, 0, 0, nil
			}
			request := func(client *http.Client) {
				t.Helper()
				response, err := client.Get(target.URL)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				closeErr := response.Body.Close()
				if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(body) != payload {
					t.Fatalf("proxy response: status=%d body=%q read=%v close=%v", response.StatusCode, body, readErr, closeErr)
				}
			}
			waitReclaimed := func(done <-chan struct{}) {
				t.Helper()
				if done == nil {
					t.Fatal("missing kernel process completion signal")
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("released kernel was not reaped")
				}
				if _, _, refs, total, _ := snapshot(); refs != 0 || total != 0 {
					t.Fatalf("released bridges remain: refs=%d total=%d", refs, total)
				}
			}
			for i := 0; i < samples; i++ {
				parentCPUStarted := linuxPerfSelfCPU(t)
				start := time.Now()
				client, release := newClient(tc.src)
				bridgeReady := time.Since(start)
				pid, done, refs, total, process := snapshot()
				if pid <= 0 || refs != 1 || total != 1 {
					release()
					t.Fatalf("cold bridge state: pid=%d refs=%d total=%d", pid, refs, total)
				}
				request(client)
				elapsed := time.Since(start)
				kernelPSS := linuxPerfProcessPSS(t, pid)
				release()
				waitReclaimed(done)
				parentCPU := linuxPerfSelfCPU(t) - parentCPUStarted
				if process.ProcessState == nil {
					t.Fatal("reaped kernel is missing process resource usage")
				}
				kernelCPU := process.ProcessState.UserTime() + process.ProcessState.SystemTime()
				metrics[tc.name+".cold_parent_cpu_ms"] = append(metrics[tc.name+".cold_parent_cpu_ms"], parentCPU)
				metrics[tc.name+".cold_kernel_cpu_ms"] = append(metrics[tc.name+".cold_kernel_cpu_ms"], float64(kernelCPU)/float64(time.Millisecond))
				metrics[tc.name+".cold_kernel_pss_kib"] = append(metrics[tc.name+".cold_kernel_pss_kib"], kernelPSS)
				metrics[tc.name+".cold_ready_ms"] = append(metrics[tc.name+".cold_ready_ms"], float64(bridgeReady)/float64(time.Millisecond))
				metrics[tc.name+".cold_request_ms"] = append(metrics[tc.name+".cold_request_ms"], float64(elapsed)/float64(time.Millisecond))
			}
			_, releasePinned := newClient(tc.src)
			pid, done, refs, total, _ := snapshot()
			if pid <= 0 || refs != 1 || total != 1 {
				t.Fatalf("pinned bridge state: pid=%d refs=%d total=%d", pid, refs, total)
			}
			defer releasePinned()
			for i := 0; i < samples; i++ {
				start := time.Now()
				client, release := newClient(tc.src)
				bridgeReady := time.Since(start)
				request(client)
				elapsed := time.Since(start)
				release()
				actualPID, _, actualRefs, actualTotal, _ := snapshot()
				if actualPID != pid || actualRefs != 1 || actualTotal != 1 {
					t.Fatalf("bridge was not reused or refcount changed: pid=%d/%d refs=%d total=%d", actualPID, pid, actualRefs, actualTotal)
				}
				metrics[tc.name+".reuse_ready_ms"] = append(metrics[tc.name+".reuse_ready_ms"], float64(bridgeReady)/float64(time.Millisecond))
				metrics[tc.name+".reuse_request_ms"] = append(metrics[tc.name+".reuse_request_ms"], float64(elapsed)/float64(time.Millisecond))
			}
			releasePinned()
			waitReclaimed(done)
			if sm != nil {
				badSrc := strings.Replace(tc.src, "sni=localhost", "sni=untrusted.invalid", 1)
				client, release := newClient(badSrc)
				_, done, _, _, _ := snapshot()
				response, requestErr := client.Get(target.URL)
				if response != nil {
					response.Body.Close()
				}
				release()
				waitReclaimed(done)
				if requestErr == nil {
					t.Fatal("AnyTLS accepted a certificate for the wrong hostname; TLS verification is not active")
				}
			}
		})
	}
	if t.Failed() {
		return
	}
	if got, want := requests.Load(), int64(samples*6); got != want {
		t.Fatalf("target requests=%d, want exactly %d", got, want)
	}
	report := map[string]interface{}{
		"schema_version": 1, "suite": "proxy_local_paths", "unit": "ms", "samples": samples,
		"metrics": metrics, "metric_units": linuxPerfProxyMetricUnits(metrics),
		"evidence": map[string]interface{}{
			"target_requests": requests.Load(), "loopback_only": true,
			"tls_verification": "temporary CA, insecure=false, wrong hostname rejected",
			"cpu_measurement":  "parent getrusage RUSAGE_SELF and reaped kernel wait4 user+system; reported in ms at microsecond resolution",
			"pss_measurement":  "kernel /proc/PID/smaps_rollup Pss, in KiB, after successful response before reclaim; excludes fixture server",
			"xray_sha256":      linuxPerfRuntimeHash(t, xrayBinary),
			"singbox_sha256":   linuxPerfRuntimeHash(t, singboxBinary),
			"mihomo_sha256":    linuxPerfRuntimeHash(t, mihomoBinary),
		},
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LINUXPERF_JSON:%s", encoded)
}

func linuxPerfCopyRuntime(t *testing.T, source, name string) string {
	t.Helper()
	if !filepath.IsAbs(source) {
		t.Fatalf("LINUXPERF_%s_BINARY must be an absolute path to the pinned executable", strings.ToUpper(name))
	}
	data, err := os.ReadFile(source)
	if err != nil || len(data) < 4 || string(data[:4]) != "\x7fELF" {
		t.Fatalf("missing or invalid %s ELF runtime: %v", name, err)
	}
	dest := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dest, data, 0700); err != nil {
		t.Fatal(err)
	}
	return dest
}

func linuxPerfRuntimeHash(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func linuxPerfAuthHTTPProxy(t *testing.T, target string) string {
	t.Helper()
	var mu sync.Mutex
	var connections []net.Conn
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("linux-perf:password")) {
			w.Header().Set("Proxy-Authenticate", "Basic realm=linux-perf")
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.Method != http.MethodConnect || r.Host != target {
			http.Error(w, "fixture accepts CONNECT to its own target only", http.StatusForbidden)
			return
		}
		upstream, err := net.DialTimeout("tcp", target, time.Second)
		if err != nil {
			http.Error(w, "local target unavailable", http.StatusBadGateway)
			return
		}
		downstream, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		mu.Lock()
		connections = append(connections, upstream, downstream)
		mu.Unlock()
		defer downstream.Close()
		defer upstream.Close()
		if _, err := buffered.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.Close()
			close(done)
		}()
		_, _ = io.Copy(downstream, upstream)
		_ = downstream.Close()
		<-done
	}))
	t.Cleanup(func() {
		server.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	return "http://linux-perf:password@" + strings.TrimPrefix(server.URL, "http://")
}

func linuxPerfTLS(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Ant Browser isolated performance CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	certDER, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, caPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"), filepath.Join(dir, "ca.crt")
	for path, block := range map[string]*pem.Block{certPath: {Type: "CERTIFICATE", Bytes: certDER}, keyPath: {Type: "PRIVATE KEY", Bytes: keyDER}, caPath: {Type: "CERTIFICATE", Bytes: caDER}} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath, caPath
}

func linuxPerfAnyTLSServer(t *testing.T, binary, dir, certPath, keyPath string) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{
		"log":       map[string]interface{}{"level": "error"},
		"inbounds":  []interface{}{map[string]interface{}{"type": "anytls", "listen": "127.0.0.1", "listen_port": port, "users": []interface{}{map[string]interface{}{"name": "linux-perf", "password": "linux-perf-password"}}, "tls": map[string]interface{}{"enabled": true, "certificate_path": certPath, "key_path": keyPath}}},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct"}},
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "anytls-server.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("AnyTLS fixture config: %v: %s", err, output)
	}
	logPath := filepath.Join(dir, "anytls-server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
	})
	if err := waitTCPPortReadyContext(context.Background(), "127.0.0.1", port, 5*time.Second); err != nil {
		data, _ := os.ReadFile(logPath)
		t.Fatalf("AnyTLS server did not start: %v: %s", err, data)
	}
	return port
}

// CPU excludes wall-clock sleep. RUSAGE_SELF includes the in-process loopback
// HTTP fixture; the long-lived AnyTLS server is a separate fixture process.
func linuxPerfSelfCPU(t *testing.T) float64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec)*1000 + float64(usage.Utime.Usec+usage.Stime.Usec)/1000
}

func linuxPerfProcessPSS(t *testing.T, pid int) float64 {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "Pss:" && fields[2] == "kB" {
			value, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || value <= 0 {
				t.Fatalf("invalid kernel PSS: %q", line)
			}
			return value
		}
	}
	t.Fatal("kernel smaps_rollup missing Pss")
	return 0
}

func linuxPerfProxyMetricUnits(metrics map[string][]float64) map[string]string {
	units := make(map[string]string, len(metrics))
	for name := range metrics {
		unit := "ms"
		if strings.HasSuffix(name, "_kib") {
			unit = "KiB"
		}
		units[name] = unit
	}
	return units
}
