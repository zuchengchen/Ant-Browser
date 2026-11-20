package backend

import (
	"os/exec"
	"strings"
)

// Enable the native accessibility bridge for this managed Chromium process.
// GTK/AT-SPI reads this override without changing desktop or screen-reader prefs.
func configureBrowserPasswordEnvironment(cmd *exec.Cmd) {
	if cmd == nil || !browserPasswordAutosaveSupported() {
		return
	}
	environment := cmd.Environ()
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "ACCESSIBILITY_ENABLED=") {
			filtered = append(filtered, entry)
		}
	}
	cmd.Env = append(filtered, "ACCESSIBILITY_ENABLED=1")
}
