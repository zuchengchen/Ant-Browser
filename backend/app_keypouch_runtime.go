package backend

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"ant-chrome/backend/internal/keypouch"
)

// A changed site takes effect as soon as the old reporter returns from its
// cancelled request. A new reporter never runs alongside the old one.
func (a *App) connectKeyPouchRuntime(device *keypouch.Device) {
	a.keyPouchRuntimeMu.Lock()
	a.keyPouchRuntimeDevice = device
	if a.keyPouchRuntimeCancel != nil {
		a.keyPouchRuntimeCancel()
	}
	a.keyPouchRuntimeMu.Unlock()
	a.ensureKeyPouchRuntime()
}

func (a *App) ensureKeyPouchRuntime() {
	a.keyPouchRuntimeMu.Lock()
	defer a.keyPouchRuntimeMu.Unlock()
	device := a.keyPouchRuntimeDevice
	if device == nil || a.keyPouchRuntimeActive {
		return
	}
	a.keyPouchRuntimeActive = true
	err := a.startBackgroundTask(func(taskCtx context.Context) {
		ctx, cancel := context.WithCancel(taskCtx)
		a.keyPouchRuntimeMu.Lock()
		a.keyPouchRuntimeCancel = cancel
		if a.keyPouchRuntimeDevice != device {
			// A settings change may precede this goroutine's first instruction.
			cancel()
		}
		a.keyPouchRuntimeMu.Unlock()

		a.runKeyPouchRuntime(ctx, device.Post)
		cancel()
		a.keyPouchRuntimeMu.Lock()
		changed := a.keyPouchRuntimeDevice != device
		a.keyPouchRuntimeCancel = nil
		a.keyPouchRuntimeActive = false
		a.keyPouchRuntimeMu.Unlock()

		// Maintenance can resume before the old task finishes its final HTTP request.
		a.runtimeMu.RLock()
		resumed := a.ctx != nil && a.ctx.Err() == nil && !a.runtimeStopped &&
			!a.backgroundTasksBlocked && a.backgroundTaskCtx != taskCtx
		a.runtimeMu.RUnlock()
		if changed || resumed {
			a.ensureKeyPouchRuntime()
		}
	})
	if err != nil {
		a.keyPouchRuntimeActive = false
	}
}

func (a *App) notifyKeyPouchRuntime() {
	select {
	case a.keyPouchRuntimeChanged <- struct{}{}:
	default:
	}
}

func (a *App) runKeyPouchRuntime(ctx context.Context, post func(context.Context, string, any, any) error) {
	reporter := keypouch.Reporter{
		Post: post,
		Snapshot: func(ctx context.Context, ids []string) (map[string]bool, error) {
			return a.keyPouchRuntimeSnapshot(ctx, ids, keyPouchRunningDirs)
		},
	}
	reporter.Run(ctx, a.keyPouchRuntimeChanged)
}

func keyPouchDirKey(dir string) string {
	dir = normalizeProcessUserDataDir(dir)
	if runtime.GOOS == "windows" {
		return strings.ToLower(dir)
	}
	return dir
}

// A single OS snapshot covers browsers left running across an Ant restart, and
// catches external closes. Only bound IDs and their state go onto the network.
func (a *App) keyPouchRuntimeSnapshot(ctx context.Context, ids []string, runningDirs func(context.Context) (map[string]browserRuntimeDetection, error)) (map[string]bool, error) {
	if a.browserMgr == nil {
		return nil, errors.New("Ant 实例管理器尚未就绪")
	}
	states := make(map[string]bool, len(ids))
	check := make(map[string]string)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.browserMgr.Mutex.Lock()
		p := a.browserMgr.Profiles[id]
		if p == nil || p.DeletedAt != "" {
			a.browserMgr.Mutex.Unlock()
			continue
		}
		running, port := p.Running, p.DebugPort
		dir := keyPouchDirKey(a.browserMgr.ResolveUserDataDir(p))
		a.browserMgr.Mutex.Unlock()
		if running && port > 0 {
			if pages, err := keyPouchPageIDs(ctx, port); err == nil {
				states[id] = len(pages) > 0
				continue
			}
		}
		if dir != "" {
			check[id] = dir
		}
	}
	if len(check) == 0 {
		return states, ctx.Err()
	}
	live, err := runningDirs(ctx)
	if err != nil {
		return states, ctx.Err()
	}
	for id, dir := range check {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		proc, exists := live[dir]
		if !exists {
			states[id] = false
			continue
		}
		// An OS match is not proof that the configured profile owns this
		// process. A shared login directory must not report its alias as live.
		a.browserMgr.Mutex.Lock()
		p := a.browserMgr.Profiles[id]
		owned := p != nil && p.DeletedAt == "" && keyPouchDirKey(a.browserMgr.ResolveUserDataDir(p)) == dir &&
			a.checkGPTBrowserOwnerLocked(p, proc) == nil
		a.browserMgr.Mutex.Unlock()
		if !owned {
			states[id] = false
			continue
		}
		if proc.DebugPort <= 0 {
			proc.DebugPort, _ = readBrowserDebugPortFile(dir)
		}
		if proc.DebugPort > 0 {
			if pages, err := keyPouchPageIDs(ctx, proc.DebugPort); err == nil {
				states[id] = len(pages) > 0
				continue
			}
		}
		if keyPouchHasVisibleWindow(proc.PID) {
			states[id] = true
		}
	}
	return states, ctx.Err()
}

func indexKeyPouchProcess(dirs map[string]browserRuntimeDetection, pid int, args []string) {
	if pid <= 0 {
		return
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--type=") || arg == "--type" {
			return
		}
	}
	if dir := keyPouchDirKey(processCommandLineFlagValue(args, "--user-data-dir")); dir != "" {
		dirs[dir] = browserRuntimeDetection{PID: pid, DebugPort: parseRemoteDebuggingPort(strings.Join(args, " "))}
	}
}
