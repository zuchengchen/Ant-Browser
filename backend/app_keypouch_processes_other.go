//go:build !windows

package backend

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func keyPouchRunningDirs(ctx context.Context) (map[string]browserRuntimeDetection, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	dirs := make(map[string]browserRuntimeDetection)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "--user-data-dir") {
			continue
		}
		pid, cmd, ok := parseProcessListLine(line)
		if !ok {
			continue
		}
		args := splitProcessCommandLine(cmd)
		// ps omits argument quoting on Linux. Read the original boundaries
		// for candidate browsers to preserve spaces in profile paths.
		if runtime.GOOS == "linux" {
			if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline"); err == nil {
				args = keyPouchProcArgs(string(b))
			}
		}
		indexKeyPouchProcess(dirs, pid, args)
	}
	return dirs, nil
}

func keyPouchProcArgs(cmdline string) []string {
	argv := strings.Split(strings.TrimRight(cmdline, "\x00"), "\x00")
	if len(argv) != 1 {
		return argv
	}
	// Some Chromium builds replace argv with a single process title. Ant
	// launches the data directory immediately before the debugging port.
	// Preserve spaces in that path even when Chromium omitted its quotes.
	title := argv[0]
	prefix, rest, hasDir := strings.Cut(title, " --user-data-dir=")
	dir, suffix, hasPort := strings.Cut(rest, " --remote-debugging-port=")
	if hasDir && hasPort && !strings.Contains(dir, " --") {
		args := append(splitProcessCommandLine(prefix), "--user-data-dir="+strings.Trim(dir, `"`))
		return append(args, splitProcessCommandLine("--remote-debugging-port="+suffix)...)
	}
	return splitProcessCommandLine(title)
}

func keyPouchHasVisibleWindow(pid int) bool { return false }
