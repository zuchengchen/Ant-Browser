package backend

import (
	"context"
)

type browserPasswordAutosaveWorker struct{ running bool }

// One worker follows the current running profiles. It never starts or stops a
// browser, reads a credential, or changes a website's DOM.
func (a *App) ensureBrowserPasswordAutosave() {
	if a == nil || a.browserMgr == nil || !browserPasswordAutosaveSupported() {
		return
	}
	a.runtimeMu.RLock()
	runtimeActive := a.ctx != nil && a.ctx.Err() == nil && !a.runtimeStopped && !a.backgroundTasksBlocked && (a.backgroundTaskCtx == nil || a.backgroundTaskCtx.Err() == nil)
	a.runtimeMu.RUnlock()
	if !runtimeActive {
		return
	}
	a.browserPasswordAutosaveMu.Lock()
	defer a.browserPasswordAutosaveMu.Unlock()
	if a.browserPasswordAutosaveWorker != nil {
		return
	}
	worker := &browserPasswordAutosaveWorker{}
	a.browserPasswordAutosaveWorker = worker
	if err := a.startBackgroundTask(func(ctx context.Context) {
		defer func() {
			a.browserPasswordAutosaveMu.Lock()
			if a.browserPasswordAutosaveWorker == worker {
				a.browserPasswordAutosaveWorker = nil
			}
			a.browserPasswordAutosaveMu.Unlock()
			// Maintenance can resume before an old cancelled task has finished.
			// Retry here after cleanup so that resume cannot strand the watcher.
			// startBackgroundTask rejects this during shutdown/active maintenance.
			a.ensureBrowserPasswordAutosave()
		}()
		a.watchBrowserPasswordAutosave(ctx)
	}); err != nil {
		a.browserPasswordAutosaveWorker = nil
	}
}
