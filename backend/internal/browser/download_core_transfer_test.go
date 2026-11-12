package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type coreDownloadTestTransport func(*http.Request) (*http.Response, error)

func (f coreDownloadTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func coreDownloadTestResponse(status int, contentRange string, body io.Reader, size int64) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Range": {contentRange}}, Body: io.NopCloser(body), ContentLength: size}
}

func coreDownloadTestFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "download"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func coreDownloadTestRange(t *testing.T, req *http.Request) (int64, int64) {
	t.Helper()
	var start, end int64
	if _, err := fmt.Sscanf(req.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start {
		t.Errorf("invalid requested range %q: %v", req.Header.Get("Range"), err)
		return 0, 0
	}
	return start, end
}

func assertCoreDownloadContents(t *testing.T, file *os.File, want []byte) {
	t.Helper()
	got, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("download bytes differ: got %d bytes, want %d", len(got), len(want))
	}
}

func assertCoreDownloadProgress(t *testing.T, events []int, success bool) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no download progress was emitted")
	}
	last := 0
	for _, value := range events {
		if value < last || value > 100 || (!success && value == 100) {
			t.Fatalf("invalid progress for success=%v: %v", success, events)
		}
		last = value
	}
	if success && last != 100 {
		t.Fatalf("successful download did not finish at 100%%: %v", events)
	}
}

func TestCoreDownloadParallelAndSmallFiles(t *testing.T) {
	for _, size := range []int{1, 3, 7, 8, 32771, 3*1024*1024 + 7} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i * 31)
			}
			var requests atomic.Int32
			var mu sync.Mutex
			var ranges [][2]int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				start, end := coreDownloadTestRange(t, req)
				if end >= int64(len(data)) {
					t.Errorf("range exceeds data: %d-%d", start, end)
					http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
					return
				}
				if req.Header.Get("Accept-Encoding") != "identity" {
					t.Error("range request must use the uncompressed representation")
				}
				if requests.Add(1) > 1 {
					mu.Lock()
					ranges = append(ranges, [2]int64{start, end})
					mu.Unlock()
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(data[start : end+1])
			}))
			defer server.Close()
			file := coreDownloadTestFile(t)
			if _, err := file.Write(make([]byte, size+100)); err != nil {
				t.Fatal(err)
			}
			var events []int
			if err := doConcurrentDownload(context.Background(), server.Client(), server.URL, file, func(_ string, progress int, _ string) { events = append(events, progress) }); err != nil {
				t.Fatal(err)
			}
			assertCoreDownloadContents(t, file, data)
			assertCoreDownloadProgress(t, events, true)
			if got, want := int(requests.Load()), 1+min(8, size); got != want {
				t.Fatalf("request count = %d, want %d", got, want)
			}
			sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
			var next int64
			for _, part := range ranges {
				if part[0] != next {
					t.Fatalf("gap or overlap in ranges: %v", ranges)
				}
				next = part[1] + 1
			}
			if next != int64(size) {
				t.Fatalf("incomplete ranges: %v", ranges)
			}
		})
	}
}

func TestCoreDownloadRejectsInvalidProbe(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		contentRange string
		body         string
		length       int64
	}{
		{"http_503", 503, "", "", 0},
		{"missing_range", 206, "", "x", 1},
		{"wrong_unit", 206, "items 0-0/64", "x", 1},
		{"wrong_start", 206, "bytes 1-1/64", "x", 1},
		{"wrong_end", 206, "bytes 0-1/64", "x", 1},
		{"empty_total", 206, "bytes 0-0/0", "x", 1},
		{"overflow_total", 206, "bytes 0-0/9999999999999999999999", "x", 1},
		{"trailing_junk", 206, "bytes 0-0/64suffix", "x", 1},
		{"wrong_length_header", 206, "bytes 0-0/64", "xx", 2},
		{"short_body", 206, "bytes 0-0/64", "", -1},
		{"long_body", 206, "bytes 0-0/64", "xx", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return coreDownloadTestResponse(tc.status, tc.contentRange, strings.NewReader(tc.body), tc.length), nil
			})}
			file := coreDownloadTestFile(t)
			if _, err := file.WriteString("original"); err != nil {
				t.Fatal(err)
			}
			if err := doConcurrentDownload(context.Background(), client, "http://example.test/core", file, func(string, int, string) {}); err == nil {
				t.Fatal("invalid range probe was accepted")
			}
			if calls.Load() != 1 {
				t.Fatalf("invalid probe triggered %d requests", calls.Load())
			}
			assertCoreDownloadContents(t, file, []byte("original"))
		})
	}
}

func TestCoreDownloadRejectsInvalidPartsAndRetryExhaustion(t *testing.T) {
	for _, failure := range []string{"http_503", "http_200", "missing_range", "wrong_start", "wrong_end", "wrong_total", "wrong_length_header", "short_body", "long_body", "network_error"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			networkErr := errors.New("test transport failure")
			var requests atomic.Int32
			var mu sync.Mutex
			attempts := map[string]int{}
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					return coreDownloadTestResponse(206, "bytes 0-0/64", strings.NewReader("x"), 1), nil
				}
				mu.Lock()
				attempts[req.Header.Get("Range")]++
				mu.Unlock()
				start, end := coreDownloadTestRange(t, req)
				status := 206
				contentRange := fmt.Sprintf("bytes %d-%d/64", start, end)
				body := "abcdefgh"
				length := int64(len(body))
				switch failure {
				case "http_503":
					status = 503
				case "http_200":
					status = 200
				case "missing_range":
					contentRange = ""
				case "wrong_start":
					contentRange = fmt.Sprintf("bytes %d-%d/64", start+1, end)
				case "wrong_end":
					contentRange = fmt.Sprintf("bytes %d-%d/64", start, end+1)
				case "wrong_total":
					contentRange = fmt.Sprintf("bytes %d-%d/65", start, end)
				case "wrong_length_header":
					length++
				case "short_body":
					body = "abc"
				case "long_body":
					body += "EXTRA"
					length = -1
				case "network_error":
					return nil, networkErr
				}
				return coreDownloadTestResponse(status, contentRange, strings.NewReader(body), length), nil
			})}
			file := coreDownloadTestFile(t)
			var events []int
			err := doConcurrentDownload(context.Background(), client, "http://example.test/core", file, func(_ string, progress int, _ string) { events = append(events, progress) })
			if err == nil {
				t.Fatal("failed parts reported a successful download")
			}
			if failure == "network_error" && !errors.Is(err, networkErr) {
				t.Fatalf("network cause lost: %v", err)
			}
			assertCoreDownloadProgress(t, events, false)
			maxAttempts := 0
			for _, count := range attempts {
				maxAttempts = max(maxAttempts, count)
			}
			if maxAttempts != 3 {
				t.Fatalf("retry exhaustion not exercised: %v", attempts)
			}
			info, statErr := file.Stat()
			if statErr != nil || info.Size() != 64 {
				t.Fatalf("part wrote outside allocated file: info=%v err=%v", info, statErr)
			}
		})
	}
}

type coreDownloadErrorReader struct {
	data []byte
}

func (r *coreDownloadErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.ErrUnexpectedEOF
}

func TestCoreDownloadRetriesPartialResponsesWithoutDoubleCounting(t *testing.T) {
	data := []byte(strings.Repeat("0123456789abcdef", 64))
	var requests atomic.Int32
	var mu sync.Mutex
	attempts := map[string]int{}
	client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			return coreDownloadTestResponse(206, fmt.Sprintf("bytes 0-0/%d", len(data)), bytes.NewReader(data[:1]), 1), nil
		}
		start, end := coreDownloadTestRange(t, req)
		mu.Lock()
		attempts[req.Header.Get("Range")]++
		attempt := attempts[req.Header.Get("Range")]
		mu.Unlock()
		part := data[start : end+1]
		var body io.Reader = bytes.NewReader(part)
		if attempt == 1 {
			body = &coreDownloadErrorReader{data: part[:len(part)/2]}
		}
		return coreDownloadTestResponse(206, fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)), body, int64(len(part))), nil
	})}
	file := coreDownloadTestFile(t)
	var events []int
	if err := doConcurrentDownload(context.Background(), client, "http://example.test/core", file, func(_ string, progress int, _ string) { events = append(events, progress) }); err != nil {
		t.Fatal(err)
	}
	assertCoreDownloadContents(t, file, data)
	assertCoreDownloadProgress(t, events, true)
	for part, count := range attempts {
		if count != 2 {
			t.Fatalf("part %s attempted %d times, want 2", part, count)
		}
	}
}

type coreDownloadTestWriterAt func([]byte, int64) (int, error)

func (f coreDownloadTestWriterAt) WriteAt(p []byte, offset int64) (int, error) {
	return f(p, offset)
}

func TestCoreDownloadPropagatesWriteErrorsAndShortWrites(t *testing.T) {
	for _, failure := range []string{"write_error", "short_write"} {
		t.Run(failure, func(t *testing.T) {
			writeErr := errors.New("test file write failed")
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				return coreDownloadTestResponse(206, "bytes 8-15/32", strings.NewReader("12345678"), 8), nil
			})}
			calls := 0
			target := coreDownloadTestWriterAt(func(p []byte, offset int64) (int, error) {
				calls++
				if offset != 8 || len(p) != 8 {
					t.Errorf("wrong file write: offset=%d length=%d", offset, len(p))
				}
				if failure == "short_write" {
					return len(p) - 1, nil
				}
				return 0, writeErr
			})
			err := downloadCoreRange(context.Background(), client, "http://example.test/core", target, 8, 15, 32, func(int64) {})
			want := writeErr
			if failure == "short_write" {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || calls != 1 {
				t.Fatalf("write result err=%v calls=%d, want %v and one call", err, calls, want)
			}
		})
	}
}

func TestCoreDownloadOverlongPartDoesNotOverwriteNeighbour(t *testing.T) {
	client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
		return coreDownloadTestResponse(206, "bytes 8-15/32", strings.NewReader("12345678EXTRA"), -1), nil
	})}
	file := coreDownloadTestFile(t)
	if _, err := file.WriteString(strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	if err := downloadCoreRange(context.Background(), client, "http://example.test/core", file, 8, 15, 32, func(int64) {}); err == nil {
		t.Fatal("overlong part accepted")
	}
	assertCoreDownloadContents(t, file, []byte("xxxxxxxx12345678xxxxxxxxxxxxxxxx"))
}

func TestCoreDownloadCancellationInterruptsRequestAndBackoff(t *testing.T) {
	for _, phase := range []string{"request", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			started := make(chan struct{}, 8)
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					return coreDownloadTestResponse(206, "bytes 0-0/64", strings.NewReader("x"), 1), nil
				}
				started <- struct{}{}
				if phase == "request" {
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
				return nil, errors.New("retryable transport failure")
			})}
			file := coreDownloadTestFile(t)
			done := make(chan error, 1)
			go func() {
				done <- doConcurrentDownload(ctx, client, "http://example.test/core", file, func(string, int, string) {})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("range download never started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation waited for the two-second retry backoff")
			}
		})
	}
}

func TestCoreDownloadSingleStreamFallback(t *testing.T) {
	for _, kind := range []string{"known_length", "unknown_length", "empty"} {
		t.Run(kind, func(t *testing.T) {
			data := "single stream body"
			length := int64(len(data))
			if kind == "unknown_length" {
				length = -1
			} else if kind == "empty" {
				data, length = "", 0
			}
			calls := 0
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return coreDownloadTestResponse(200, "", strings.NewReader("probe"), 999), nil
				}
				if req.Header.Get("Range") != "" {
					t.Error("single-stream fallback still requested a range")
				}
				return coreDownloadTestResponse(200, "", strings.NewReader(data), length), nil
			})}
			file := coreDownloadTestFile(t)
			if _, err := file.WriteString(strings.Repeat("stale", 100)); err != nil {
				t.Fatal(err)
			}
			var events []int
			if err := doConcurrentDownload(context.Background(), client, "http://example.test/core", file, func(_ string, progress int, _ string) { events = append(events, progress) }); err != nil {
				t.Fatal(err)
			}
			assertCoreDownloadContents(t, file, []byte(data))
			assertCoreDownloadProgress(t, events, true)
			if calls != 2 {
				t.Fatalf("fallback made %d requests, want 2", calls)
			}
		})
	}
}

func TestCoreDownloadSingleStreamRejectsIncompleteAndOverlongBody(t *testing.T) {
	for _, body := range []string{"short", "overlong body"} {
		t.Run(body, func(t *testing.T) {
			client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
				return coreDownloadTestResponse(200, "", strings.NewReader(body), 8), nil
			})}
			var events []int
			err := doSingleThreadDownload(context.Background(), client, "http://example.test/core", coreDownloadTestFile(t), 8, func(_ string, progress int, _ string) { events = append(events, progress) })
			if err == nil {
				t.Fatal("invalid single response reported success")
			}
			assertCoreDownloadProgress(t, events, false)
		})
	}
}

type coreDownloadTestReadCloser struct {
	io.Reader
	closeErr error
}

func (r *coreDownloadTestReadCloser) Close() error { return r.closeErr }

func TestCoreDownloadSingleStreamCloseFailureDoesNotReportCompletion(t *testing.T) {
	closeErr := errors.New("test response close failed")
	client := &http.Client{Transport: coreDownloadTestTransport(func(req *http.Request) (*http.Response, error) {
		response := coreDownloadTestResponse(200, "", strings.NewReader("body"), 4)
		response.Body = &coreDownloadTestReadCloser{Reader: strings.NewReader("body"), closeErr: closeErr}
		return response, nil
	})}
	var events []int
	err := doSingleThreadDownload(context.Background(), client, "http://example.test/core", coreDownloadTestFile(t), 4, func(_ string, progress int, _ string) { events = append(events, progress) })
	if !errors.Is(err, closeErr) {
		t.Fatalf("response close failure lost: %v", err)
	}
	assertCoreDownloadProgress(t, events, false)
}
