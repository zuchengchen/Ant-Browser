//go:build !windows

package backend

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyPouchIndexesRetainedChromiumProcessTitles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Ant profiles", "account")
	const port = "--remote-debugging-port=9321"
	for _, tc := range []struct {
		name, cmdline string
		live          bool
	}{
		{"original argv", strings.Join([]string{"/bin/chrome", port, "--user-data-dir=" + dir}, "\x00"), true},
		{"flattened Ant launch", "/bin/chrome --user-data-dir=" + dir + " " + port + " --other", true},
		{"quoted flattened directory", "/bin/chrome " + port + ` --user-data-dir="` + dir + `"`, true},
		{"renderer", "/bin/chrome --user-data-dir=" + dir + " " + port + " --type=renderer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := make(map[string]browserRuntimeDetection)
			indexKeyPouchProcess(found, 123, keyPouchProcArgs(tc.cmdline+"\x00"))
			d, ok := found[keyPouchDirKey(dir)]
			if ok != tc.live || ok && (d.PID != 123 || d.DebugPort != 9321) || !ok && len(found) != 0 {
				t.Fatalf("incorrect retained browser index: %+v", found)
			}
		})
	}
}
