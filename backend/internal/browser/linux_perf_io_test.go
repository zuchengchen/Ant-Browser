//go:build linux && linuxperf

package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestLinuxPerfCoreDownload measures the actual range and stream download paths.
// The loopback endpoint does not use an external proxy or change connector policy.
func TestLinuxPerfCoreDownload(t *testing.T) {
	samples := 30
	if raw := os.Getenv("LINUXPERF_IO_SAMPLES"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			t.Fatalf("invalid LINUXPERF_IO_SAMPLES: %q", raw)
		}
		samples = value
	}
	root := t.TempDir()
	metrics := map[string][]float64{}
	units := map[string]string{}
	hashes := map[string]string{}
	requests := map[string]int64{}
	for _, compressible := range []bool{true, false} {
		name := "incompressible"
		if compressible {
			name = "compressible"
		}
		payload := make([]byte, 8*1024*1024+13)
		if compressible {
			for index := range payload {
				payload[index] = byte(index % 31)
			}
		} else {
			if _, err := rand.New(rand.NewSource(7102026)).Read(payload); err != nil {
				t.Fatal(err)
			}
		}
		wantHash := sha256.Sum256(payload)
		hashes[name] = hex.EncodeToString(wantHash[:])
		for _, ranged := range []bool{true, false} {
			mode := "stream"
			if ranged {
				mode = "range"
			}
			key := "download_" + name + "_" + mode
			t.Run(key, func(t *testing.T) {
				var count atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					count.Add(1)
					if request.Header.Get("Accept-Encoding") != "identity" {
						t.Error("download must request the identity representation")
					}
					if ranged {
						http.ServeContent(w, request, "fixed-core.bin", time.Unix(1700000000, 0), bytes.NewReader(payload))
						return
					}
					w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
					_, _ = w.Write(payload) // The initial unsupported-range probe intentionally closes early.
				}))
				defer server.Close()
				transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConnsPerHost: 8}
				defer transport.CloseIdleConnections()
				counter := &linuxPerfDownloadTransport{transport: transport}
				client := &http.Client{Transport: counter, Timeout: 15 * time.Second}
				for sample := 0; sample < samples; sample++ {
					file, err := os.CreateTemp(root, key+"-*")
					if err != nil {
						t.Fatal(err)
					}
					var progress []int
					var progressMu sync.Mutex
					var beforeMem, afterMem runtime.MemStats
					runtime.ReadMemStats(&beforeMem)
					beforeCPU := linuxPerfDownloadCPU(t)
					started := time.Now()
					err = doConcurrentDownload(context.Background(), client, server.URL, file, func(_ string, value int, _ string) {
						progressMu.Lock()
						progress = append(progress, value)
						progressMu.Unlock()
					})
					elapsed := float64(time.Since(started)) / float64(time.Millisecond)
					cpu := linuxPerfDownloadCPU(t) - beforeCPU
					runtime.ReadMemStats(&afterMem)
					closeErr := file.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("download/close failed: %v / %v", err, closeErr)
					}
					result, err := os.ReadFile(file.Name())
					if err != nil || len(result) != len(payload) || sha256.Sum256(result) != wantHash {
						t.Fatalf("downloaded payload hash or size mismatch: %v", err)
					}
					if len(progress) == 0 || progress[len(progress)-1] != 100 {
						t.Fatalf("download did not positively report completion: %v", progress)
					}
					for index, value := range progress {
						if value < 0 || value > 100 || (index > 0 && value < progress[index-1]) {
							t.Fatalf("invalid progress: %v", progress)
						}
					}
					if counter.active.Load() != 0 {
						t.Fatalf("response bodies remain open: %d", counter.active.Load())
					}
					linuxPerfDownloadNoFilesOpen(t, root)
					for metric, value := range map[string]float64{
						"wall_ms": elapsed, "cpu_ms": cpu, "alloc_bytes": float64(afterMem.TotalAlloc - beforeMem.TotalAlloc),
					} {
						metricKey := key + "_" + metric
						metrics[metricKey] = append(metrics[metricKey], value)
						units[metricKey] = "ms"
						if metric == "alloc_bytes" {
							units[metricKey] = "bytes"
						}
					}
					if err := os.Remove(file.Name()); err != nil {
						t.Fatal(err)
					}
				}
				perSample := int64(2)
				if ranged {
					perSample = 9 // one validated probe followed by eight exact ranges
				}
				if got, want := count.Load(), int64(samples)*perSample; got != want {
					t.Fatalf("request count=%d, want %d", got, want)
				}
				requests[key] = count.Load()
			})
		}
	}
	t.Run("error_and_cancellation", linuxPerfDownloadErrorAndCancellation)
	if t.Failed() {
		return
	}
	report, err := json.Marshal(map[string]interface{}{
		"schema_version": 1, "suite": "file_io", "component": "core_download", "samples": samples, "diagnostic": samples < 30,
		"metrics": metrics, "metric_units": units,
		"evidence": map[string]interface{}{
			"payload_bytes": 8*1024*1024 + 13, "payload_sha256": hashes, "requests": requests,
			"hash_verified_each_sample": true, "closed_response_bodies_each_sample": true, "no_fixture_file_descriptors_each_sample": true,
			"error_and_cancellation": "HTTP 503, truncated body, in-flight context cancellation and request cleanup positively asserted",
			"cpu_measurement":        "getrusage RUSAGE_SELF user+system; includes loopback fixture server in same process",
			"allocation_measurement": "runtime.TotalAlloc delta during operation; includes loopback fixture server",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LINUXPERF_JSON:%s", report)
}

type linuxPerfDownloadTransport struct {
	transport *http.Transport
	active    atomic.Int64
}

func (transport *linuxPerfDownloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.transport.RoundTrip(request)
	if err == nil {
		transport.active.Add(1)
		response.Body = &linuxPerfDownloadBody{ReadCloser: response.Body, active: &transport.active}
	}
	return response, err
}

type linuxPerfDownloadBody struct {
	io.ReadCloser
	active *atomic.Int64
	once   sync.Once
}

func (body *linuxPerfDownloadBody) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(func() { body.active.Add(-1) })
	return err
}

func linuxPerfDownloadCPU(t *testing.T) float64 {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return float64(usage.Utime.Sec+usage.Stime.Sec)*1000 + float64(usage.Utime.Usec+usage.Stime.Usec)/1000
}

func linuxPerfDownloadNoFilesOpen(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if os.IsNotExist(err) { // /proc entry for the just-closed ReadDir descriptor
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(target, root+string(os.PathSeparator)) {
			t.Fatalf("fixture file descriptor leaked: %s -> %s", entry.Name(), target)
		}
	}
}

func linuxPerfDownloadErrorAndCancellation(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/error":
			http.Error(w, "intentional fixture failure", http.StatusServiceUnavailable)
		case "/short":
			w.Header().Set("Content-Length", "32")
			_, _ = io.WriteString(w, "short")
		case "/cancel":
			w.Header().Set("Content-Length", "1000000")
			_, _ = io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			close(started)
			<-request.Context().Done()
			close(cancelled)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	counter := &linuxPerfDownloadTransport{transport: transport}
	client := &http.Client{Transport: counter, Timeout: 5 * time.Second}
	root := t.TempDir()
	for _, path := range []string{"/error", "/short", "/cancel"} {
		file, err := os.CreateTemp(root, "failure-*")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var completed atomic.Bool
		done := make(chan error, 1)
		go func() {
			done <- doSingleThreadDownload(ctx, client, server.URL+path, file, -1, func(_ string, progress int, _ string) {
				if progress == 100 {
					completed.Store(true)
				}
			})
		}()
		if path == "/cancel" {
			select {
			case <-started:
				cancel()
			case <-time.After(5 * time.Second):
				cancel()
				t.Fatal("cancellation request never reached loopback server")
			}
		}
		select {
		case err = <-done:
		case <-time.After(6 * time.Second):
			cancel()
			t.Fatal("download did not return after failure/cancellation")
		}
		cancel()
		closeErr := file.Close()
		if err == nil || closeErr != nil || completed.Load() || counter.active.Load() != 0 {
			t.Fatalf("%s: err=%v close=%v completion=%v open-bodies=%d", path, err, closeErr, completed.Load(), counter.active.Load())
		}
		if path == "/cancel" {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error lost: %v", err)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("cancelled request remains open at server")
			}
		}
		linuxPerfDownloadNoFilesOpen(t, root)
	}
}
