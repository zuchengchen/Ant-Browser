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
				args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
			}
		}
		indexKeyPouchProcess(dirs, pid, args)
	}
	return dirs, nil
}

func keyPouchHasVisibleWindow(pid int) bool { return false }
