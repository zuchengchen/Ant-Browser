package backend

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"ant-chrome/backend/internal/keypouch"
)

// Pairing and startup use the same authenticated client. Backup maintenance
// pauses the worker through Ant's ordinary background task lifecycle.
func (a *App) connectKeyPouchRuntime(post func(context.Context, string, any, any) error) {
	a.keyPouchRuntimeMu.Lock()
	a.keyPouchRuntimePost = post
	active := a.keyPouchRuntimeActive
	a.keyPouchRuntimeMu.Unlock()
	if active {
		a.notifyKeyPouchRuntime()
	} else {
		a.ensureKeyPouchRuntime()
	}
}

func (a *App) ensureKeyPouchRuntime() {
	a.keyPouchRuntimeMu.Lock()
	defer a.keyPouchRuntimeMu.Unlock()
	post := a.keyPouchRuntimePost
	if post == nil || a.keyPouchRuntimeActive {
		return
	}
	a.keyPouchRuntimeActive = true
	err := a.startBackgroundTask(func(ctx context.Context) {
		a.runKeyPouchRuntime(ctx, post)
		a.keyPouchRuntimeMu.Lock()
		a.keyPouchRuntimeActive = false
		a.keyPouchRuntimeMu.Unlock()
		// A backup restore can resume before this worker finishes. Restart only
		// for a new task context, never for a cancelled application context.
		a.runtimeMu.RLock()
		resumed := a.ctx != nil && a.ctx.Err() == nil && !a.runtimeStopped &&
			!a.backgroundTasksBlocked && a.backgroundTaskCtx != ctx
		a.runtimeMu.RUnlock()
		if resumed {
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
