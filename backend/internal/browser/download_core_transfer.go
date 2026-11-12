package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var coreContentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

func validateCoreContentRange(value string, start, end, total int64) (int64, error) {
	matches := coreContentRangePattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, fmt.Errorf("无效 Content-Range: %q", value)
	}
	values := [3]int64{}
	for i := range values {
		parsed, err := strconv.ParseInt(matches[i+1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("无效 Content-Range: %q", value)
		}
		values[i] = parsed
	}
	if values[0] != start || values[1] != end || values[2] <= end || (total > 0 && values[2] != total) {
		return 0, fmt.Errorf("Content-Range 与请求不符: %q (bytes=%d-%d, total=%d)", value, start, end, total)
	}
	return values[2], nil
}

func doConcurrentDownload(ctx context.Context, client *http.Client, targetUrl string, tempFile *os.File, sendEvent func(string, int, string)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetUrl, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		sendEvent("downloading", 0, "服务器不支持多线程，回退至单流下载...")
		return doSingleThreadDownload(ctx, client, targetUrl, tempFile, resp.ContentLength, sendEvent)
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return fmt.Errorf("HTTP状态码异常: %d", resp.StatusCode)
	}
	totalSize, rangeErr := validateCoreContentRange(resp.Header.Get("Content-Range"), 0, 0, 0)
	if rangeErr != nil {
		resp.Body.Close()
		return rangeErr
	}
	if resp.ContentLength >= 0 && resp.ContentLength != 1 {
		resp.Body.Close()
		return fmt.Errorf("探测响应长度异常: %d", resp.ContentLength)
	}
	probeErr := copyCoreDownloadBody(ctx, io.Discard, resp.Body, 1)
	closeErr := resp.Body.Close()
	if probeErr != nil {
		return probeErr
	}
	if closeErr != nil {
		return closeErr
	}

	sendEvent("downloading", 0, fmt.Sprintf("支持多线程分片下载，总大小 %.2f MB", float64(totalSize)/1024/1024))
	if err := tempFile.Truncate(totalSize); err != nil {
		return err
	}

	numWorkers := 8
	if totalSize < int64(numWorkers) {
		numWorkers = int(totalSize)
	}
	chunkSize := totalSize / int64(numWorkers)
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var downloaded int64
	var lastTick time.Time
	var downloadErr error
	partProgress := make([]int64, numWorkers)

	for i := 0; i < numWorkers; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == numWorkers-1 {
			end = totalSize - 1
		}
		wg.Add(1)
		go func(part int, start, end int64) {
			defer wg.Done()
			progress := func(written int64) {
				mu.Lock()
				defer mu.Unlock()
				// A retry overwrites the same byte range; count its high-water
				// mark once. Reserve 100% for fully validated completion.
				if written > partProgress[part] {
					downloaded += written - partProgress[part]
					partProgress[part] = written
				}
				if time.Since(lastTick) > time.Second {
					percent := min(99, int(float64(downloaded)/float64(totalSize)*100))
					sendEvent("downloading", percent, fmt.Sprintf("并行下载中... %.2f MB / %.2f MB", float64(downloaded)/1024/1024, float64(totalSize)/1024/1024))
					lastTick = time.Now()
				}
			}
			err := retryCoreDownload(downloadCtx, 3, 2*time.Second, func() error {
				return downloadCoreRange(downloadCtx, client, targetUrl, tempFile, start, end, totalSize, progress)
			})
			if err != nil {
				mu.Lock()
				if downloadErr == nil {
					downloadErr = fmt.Errorf("分片 %d 下载失败: %w", part+1, err)
					cancel()
				}
				mu.Unlock()
			}
		}(i, start, end)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if downloadErr != nil {
		return downloadErr
	}
	sendEvent("downloading", 100, "并行下载完成")
	return nil
}

func retryCoreDownload(ctx context.Context, attempts int, delay time.Duration, attempt func() error) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = attempt()
		if lastErr == nil {
			return ctx.Err()
		}
		if i+1 < attempts {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return lastErr
}

func downloadCoreRange(ctx context.Context, client *http.Client, targetURL string, target io.WriterAt, start, end, total int64, progress func(int64)) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
	}()
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("分片 HTTP 状态码异常: %d", resp.StatusCode)
	}
	if _, err := validateCoreContentRange(resp.Header.Get("Content-Range"), start, end, total); err != nil {
		return err
	}
	expected := end - start + 1
	if resp.ContentLength >= 0 && resp.ContentLength != expected {
		return fmt.Errorf("分片响应长度异常: %d，期望 %d", resp.ContentLength, expected)
	}
	var written int64
	writer := &coreDownloadWriter{ctx: ctx, writeFunc: func(p []byte) (int, error) {
		n, err := target.WriteAt(p, start+written)
		written += int64(n)
		if n > 0 {
			progress(written)
		}
		return n, err
	}}
	return copyCoreDownloadBody(ctx, writer, resp.Body, expected)
}

// Limit writes to the advertised size, then require EOF. Checking only EOF
// from io.Copy would accept short bodies, or write an oversized part into its
// neighbour's range. An unknown-length single response uses expected == -1.
func copyCoreDownloadBody(ctx context.Context, dst io.Writer, body io.Reader, expected int64) error {
	var reader io.Reader = body
	if expected >= 0 {
		reader = io.LimitReader(body, expected)
	}
	written, err := io.CopyBuffer(dst, reader, make([]byte, 256*1024))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if expected >= 0 {
		if written != expected {
			return fmt.Errorf("下载长度异常: %d，期望 %d: %w", written, expected, io.ErrUnexpectedEOF)
		}
		var extra [1]byte
		n, readErr := io.ReadFull(body, extra[:])
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n != 0 {
			return fmt.Errorf("下载内容超出预期长度 %d", expected)
		}
		if readErr != io.EOF {
			return readErr
		}
	}
	return nil
}

func doSingleThreadDownload(ctx context.Context, client *http.Client, targetUrl string, tempFile *os.File, totalSize int64, sendEvent func(string, int, string)) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetUrl, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			sendEvent("downloading", 100, "单流下载完成")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP状态码异常: %d", resp.StatusCode)
	}
	// The full response is authoritative; its length may differ from the probe.
	totalSize = resp.ContentLength
	if err := tempFile.Truncate(0); err != nil {
		return err
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var downloaded int64
	var lastTick time.Time
	writer := &coreDownloadWriter{ctx: ctx, writeFunc: func(p []byte) (int, error) {
		n, err := tempFile.Write(p)
		downloaded += int64(n)
		if n > 0 && totalSize > 0 && time.Since(lastTick) > time.Second {
			percent := min(99, int(float64(downloaded)/float64(totalSize)*100))
			sendEvent("downloading", percent, fmt.Sprintf("单流下载中... %.2f MB / %.2f MB", float64(downloaded)/1024/1024, float64(totalSize)/1024/1024))
			lastTick = time.Now()
		}
		return n, err
	}}
	if err := copyCoreDownloadBody(ctx, writer, resp.Body, totalSize); err != nil {
		return err
	}
	return nil
}
