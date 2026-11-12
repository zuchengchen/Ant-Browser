//go:build linux

package proxy

import (
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestXrayLaunchCapturesStartupOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		stdout    string
		stderr    string
		message   string
		retryable bool
	}{
		{
			name:    "stdout_nonbind",
			stdout:  "Failed to start: core: controlled-stdout-startup-failure",
			message: "controlled-stdout-startup-failure",
		},
		{
			name:      "stdout_bind",
			stdout:    "Failed to start: listen tcp 127.0.0.1:12345: bind: address already in use",
			message:   "address already in use",
			retryable: true,
		},
		{
			name:    "stderr_nonbind",
			stderr:  "controlled-stderr-startup-failure",
			message: "controlled-stderr-startup-failure",
		},
		{
			name:      "stderr_bind",
			stderr:    "listen tcp 127.0.0.1:12345: bind: address already in use",
			message:   "address already in use",
			retryable: true,
		},
		{
			name:    "both_streams",
			stdout:  "Failed to start: controlled-final-stdout-failure",
			stderr:  "controlled-stderr-detail",
			message: "controlled-final-stdout-failure",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Browser.UserDataRoot = filepath.Join(root, "data")
			cfg.ProxyCheck.BridgeStartTimeoutMs = 1000
			manager := &XrayManager{Config: cfg, AppRoot: root, Bridges: make(map[string]*XrayBridge)}
			const key = "controlled-output-test"
			workdir := manager.resolveWorkdir(key)
			if err := os.MkdirAll(workdir, 0700); err != nil {
				t.Fatal(err)
			}
			const stale = "2000/01/01 00:00:00 [Warning] core: old Xray started\n"
			if err := os.WriteFile(filepath.Join(workdir, "xray-error.log"), []byte(stale), 0600); err != nil {
				t.Fatal(err)
			}
			// Shell builtins keep both preflight and startup as direct children.
			// The startup child writes its own PID/start ticks before exit 255.
			script := `#!/bin/sh
set -eu
if [ "$#" = 4 ] && [ "$1" = run ] && [ "$2" = -test ] && [ "$3" = -c ]; then
    phase=preflight
elif [ "$#" = 3 ] && [ "$1" = run ] && [ "$2" = -c ]; then
    phase=run
else
    exit 91
fi
IFS= read -r identity < "/proc/$$/stat"
printf '%s\n' "$identity" > "helper-$phase.stat"
printf '%s\n' "$phase" >> helper-invocations.log
if [ "$phase" = preflight ]; then
    printf '%s\n' 'controlled-preflight-stdout'
    exit 0
fi
`
			if tc.stderr != "" {
				script += "printf '%s\\n' '" + strings.ReplaceAll(tc.stderr, "'", "'\"'\"'") + "' >&2\n"
			}
			if tc.stdout != "" {
				script += "printf '%s\\n' '" + strings.ReplaceAll(tc.stdout, "'", "'\"'\"'") + "'\n"
			}
			script += "exit 255\n"
			binary := filepath.Join(root, "controlled-xray")
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			outbounds := []interface{}{map[string]interface{}{"protocol": "freedom", "tag": "proxy-out"}}
			url, bridge, err := manager.launchBridgeAttemptContext(ctx, logger.New("XrayOutputTest"), key, binary, outbounds, nil, 0, "", false, 1)
			// Check both real child identities even if another assertion fails.
			for _, phase := range []string{"preflight", "run"} {
				assertXrayOutputChildReaped(t, filepath.Join(workdir, "helper-"+phase+".stat"))
			}
			if ctx.Err() != nil {
				t.Fatalf("startup exceeded test deadline: %v", ctx.Err())
			}
			if err == nil || url != "" || bridge != nil || len(manager.Bridges) != 0 {
				t.Fatalf("startup failure returned url=%q bridge=%v error=%v entries=%d", url, bridge, err, len(manager.Bridges))
			}
			var launchErr *xrayLaunchError
			if !errors.As(err, &launchErr) || launchErr.retryable != tc.retryable {
				t.Fatalf("retry policy = %#v, want retryable=%v; error=%v", launchErr, tc.retryable, err)
			}
			if !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "old Xray started") {
				t.Errorf("startup cause lost or replaced by historical log: %v", err)
			}
			output, readErr := os.ReadFile(filepath.Join(workdir, "xray-stderr.log"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, expected := range []string{tc.stdout, tc.stderr} {
				if expected != "" && !strings.Contains(string(output), expected) {
					t.Errorf("startup output missing %q: %q", expected, output)
				}
			}
			if strings.Contains(string(output), "controlled-preflight-stdout") {
				t.Errorf("preflight stdout leaked into startup output: %q", output)
			}
			calls, readErr := os.ReadFile(filepath.Join(workdir, "helper-invocations.log"))
			if readErr != nil || string(calls) != "preflight\nrun\n" {
				t.Errorf("unexpected child execution count/order: %q, %v", calls, readErr)
			}
		})
	}
}

func assertXrayOutputChildReaped(t *testing.T, path string) {
	t.Helper()
	recorded, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("missing child identity: %v", err)
		return
	}
	text := string(recorded)
	pidText, _, ok := strings.Cut(text, " ")
	pid, pidErr := strconv.Atoi(pidText)
	end := strings.LastIndex(text, ") ")
	if !ok || pidErr != nil || pid <= 0 || end < 0 {
		t.Errorf("invalid recorded child identity %q", text)
		return
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) <= 19 || fields[1] != strconv.Itoa(os.Getpid()) {
		t.Errorf("child identity does not identify our direct child: %q", text)
		return
	}
	start, parseErr := strconv.ParseUint(fields[19], 10, 64)
	if parseErr != nil || start == 0 {
		t.Errorf("invalid child start ticks: %q", text)
		return
	}
	current, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if readErr == nil {
		end := strings.LastIndex(string(current), ") ")
		if end >= 0 {
			currentFields := strings.Fields(string(current)[end+2:])
			if len(currentFields) > 19 {
				currentStart, err := strconv.ParseUint(currentFields[19], 10, 64)
				if err == nil && currentStart > 0 && currentStart != start {
					t.Logf(`XRAY_OUTPUT_CHILD_REAPED:{"pid":%d,"start":%d,"ppid":%d,"phase":%q,"status":"original_gone_pid_reused"}`, pid, start, os.Getpid(), filepath.Base(path))
					return // A different process now owns the PID; the original is gone.
				}
			}
		}
		t.Errorf("controlled child still exists after startup returned: pid=%d start=%d stat=%q", pid, start, current)
		return
	}
	if !os.IsNotExist(readErr) {
		t.Errorf("cannot verify child disappearance: %v", readErr)
		return
	}
	var status syscall.WaitStatus
	got, waitErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if !errors.Is(waitErr, syscall.ECHILD) {
		t.Errorf("product did not already reap child: pid=%d got=%d status=%v error=%v", pid, got, status, waitErr)
		return
	}
	t.Logf(`XRAY_OUTPUT_CHILD_REAPED:{"pid":%d,"start":%d,"ppid":%d,"phase":%q,"status":"gone_waited"}`, pid, start, os.Getpid(), filepath.Base(path))
}
