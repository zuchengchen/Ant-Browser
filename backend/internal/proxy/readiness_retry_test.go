package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitSocks5ReadyRetriesUntilProtocolHandshakeSucceeds(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var probes atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			request := make([]byte, 3)
			if _, err := io.ReadFull(connection, request); err == nil {
				if probes.Add(1) < 3 {
					_, _ = connection.Write([]byte{5, 255})
				} else {
					_, _ = connection.Write([]byte{5, 0})
				}
			}
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	port := listener.Addr().(*net.TCPAddr).Port
	if err := waitSocks5ReadyContext(context.Background(), "127.0.0.1", port, time.Second); err != nil {
		t.Fatalf("valid SOCKS handshake after transient failures: %v", err)
	}
	if got := probes.Load(); got != 3 {
		t.Fatalf("readiness passed without retrying invalid SOCKS responses: probes=%d", got)
	}
}

func TestWaitSocks5ReadyKeepsDeadlineForNonSocksService(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	start := time.Now()
	err = waitSocks5ReadyContext(context.Background(), "127.0.0.1", listener.Addr().(*net.TCPAddr).Port, 40*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "socks5") {
		t.Fatalf("non-SOCKS service accepted as ready: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("readiness exceeded bounded timeout: %v", elapsed)
	}
}

func TestWaitProxyReadyCancellationAndTimeout(t *testing.T) {
	for name, wait := range map[string]func(context.Context, string, int, time.Duration) error{
		"socks": waitSocks5ReadyContext,
		"tcp":   waitTCPPortReadyContext,
	} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- wait(ctx, "127.0.0.1", port, 5*time.Second) }()
			// Cancellation is deliberately delivered while retries are active.
			time.Sleep(15 * time.Millisecond)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled readiness did not return")
			}
			start := time.Now()
			if err := wait(context.Background(), "127.0.0.1", port, 5*time.Millisecond); err == nil {
				t.Fatal("unavailable endpoint reported ready")
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("short timeout was not honored: %v", elapsed)
			}
		})
	}
}
