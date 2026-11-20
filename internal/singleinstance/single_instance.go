package singleinstance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const singleInstanceLockFile = "app-instance.lock"

type singleInstanceLockInfo struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
}

type Guard struct {
	lockPath   string
	lock       *singleInstanceFileLock
	listener   net.Listener
	activation chan ActivationRequest
}

type ActivationRequest struct {
	URI  string
	done chan struct{}
}

func (r ActivationRequest) Complete() {
	if r.done != nil {
		close(r.done)
	}
}

func (g *Guard) Activations() <-chan ActivationRequest {
	if g == nil {
		return nil
	}
	return g.activation
}

func Acquire(appRoot, uri string) (*Guard, bool, error) {
	stateRoot := singleInstanceStateRoot(appRoot)
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		return nil, false, fmt.Errorf("准备单实例状态目录失败: %w", err)
	}

	lockPath := filepath.Join(stateRoot, singleInstanceLockFile)
	for attempt := 0; attempt < 3; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, false, fmt.Errorf("启动单实例监听失败: %w", err)
		}

		lock, acquired, err := tryLockSingleInstanceFile(lockPath)
		if err != nil {
			_ = listener.Close()
			return nil, false, fmt.Errorf("创建单实例锁失败: %w", err)
		}
		if acquired {
			info := singleInstanceLockInfo{PID: os.Getpid(), Addr: listener.Addr().String()}
			if encodeErr := writeSingleInstanceLockInfo(lock.file, info); encodeErr != nil {
				_ = lock.Close()
				_ = listener.Close()
				return nil, false, fmt.Errorf("写入单实例锁失败: %w", encodeErr)
			}
			guard := &Guard{
				lockPath:   lockPath,
				lock:       lock,
				listener:   listener,
				activation: make(chan ActivationRequest, 8),
			}
			go guard.serve()
			return guard, true, nil
		}

		_ = listener.Close()
		if signalExistingSingleInstance(lockPath, uri) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("单实例锁被占用且无法唤醒已有应用")
	}

	return nil, false, fmt.Errorf("单实例锁被占用且无法唤醒已有应用")
}

func writeSingleInstanceLockInfo(file *os.File, info singleInstanceLockInfo) error {
	if file == nil {
		return fmt.Errorf("单实例锁文件未打开")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(info); err != nil {
		return err
	}
	return file.Sync()
}

func signalExistingSingleInstance(lockPath, uri string) bool {
	for attempt := 0; attempt < 5; attempt++ {
		info, err := readSingleInstanceLock(lockPath)
		host, _, addrErr := net.SplitHostPort(info.Addr)
		if err == nil && addrErr == nil && net.ParseIP(host).IsLoopback() {
			grantExistingSingleInstanceForeground(info.PID)
			conn, dialErr := net.DialTimeout("tcp", info.Addr, 350*time.Millisecond)
			if dialErr == nil {
				_ = conn.SetDeadline(time.Now().Add(1200 * time.Millisecond))
				command := "activate"
				if uri != "" {
					command += " " + uri
				}
				_, sendErr := fmt.Fprintln(conn, command)
				reply, readErr := bufio.NewReader(io.LimitReader(conn, 32)).ReadString('\n')
				_ = conn.Close()
				if uri == "" {
					ActivateExistingWindow(info.PID)
					return true
				}
				if sendErr == nil && readErr == nil && reply == "ok\n" {
					return true
				}
			}
			ActivateExistingWindow(info.PID)
		}
		time.Sleep(120 * time.Millisecond)
	}
	return false
}

func readSingleInstanceLock(lockPath string) (singleInstanceLockInfo, error) {
	var info singleInstanceLockInfo
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

func (g *Guard) serve() {
	for {
		conn, err := g.listener.Accept()
		if err != nil {
			return
		}
		go g.handleConn(conn)
	}
}

func (g *Guard) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	message, err := bufio.NewReader(io.LimitReader(conn, 256)).ReadString('\n')
	command, uri, _ := strings.Cut(strings.TrimSuffix(message, "\n"), " ")
	if err != nil || command != "activate" || strings.ContainsAny(uri, " \t\r\n") {
		return
	}
	done := make(chan struct{})
	select {
	case g.activation <- ActivationRequest{URI: uri, done: done}:
		// A ticket is queued once even if Ant is still starting. Waiting for
		// its window here would make the second process resend the same link.
		if uri == "" {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	default:
		return
	}
	_, _ = conn.Write([]byte("ok\n"))
}

func (g *Guard) Close() {
	if g == nil {
		return
	}
	if err := g.listener.Close(); err != nil {
		log.Printf("关闭单实例监听失败: %v", err)
	}
	if g.lock != nil {
		if err := g.lock.Close(); err != nil {
			log.Printf("释放单实例锁失败: %v", err)
		}
	}
	_ = os.Remove(g.lockPath)
}
