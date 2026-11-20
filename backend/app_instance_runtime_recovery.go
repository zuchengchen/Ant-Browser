package backend

import (
	"context"

	"ant-chrome/backend/internal/logger"
)

var listBrowserProcessesForStartup = keyPouchRunningDirs

// Restore detached browsers before app:ready so retained windows occupy launch slots.
func (a *App) recoverBrowserRuntimeStates() {
	if a == nil {
		return
	}
	a.runtimeMu.RLock()
	ctx := a.backgroundTaskCtx
	stopped := a.runtimeStopped
	a.runtimeMu.RUnlock()
	if stopped {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var scanned bool
	var live map[string]browserRuntimeDetection
	a.restoreRunningBrowserProfiles(ctx, func(dir string) (browserRuntimeDetection, bool) {
		if detection, ok := detectBrowserRuntimeByActivePort(dir); ok {
			return detection, true
		}
		// The OS process list is shared by every remaining profile, including
		// profiles started with a fixed debugging port and no DevToolsActivePort.
		if !scanned {
			scanned = true
			var err error
			live, err = listBrowserProcessesForStartup(ctx)
			if err != nil && ctx.Err() == nil {
				logger.New("Browser").Warn("启动时无法读取浏览器进程列表", logger.F("error", err))
			}
		}
		proc, exists := live[keyPouchDirKey(dir)]
		if !exists || proc.DebugPort <= 0 || ctx.Err() != nil || probeBrowserDebugPort(proc.DebugPort, browserDebugProbeTimeout) != nil {
			return browserRuntimeDetection{}, false
		}
		proc.DebugReady = true
		return proc, true
	})
}
