package backend

import (
	"context"
	"sort"

	"ant-chrome/backend/internal/logger"
)

// Restore existing windows once after startup, without launching a browser or
// changing its proxy. This also resumes window names after an app-only restart.
func (a *App) startupRestoreBrowserWindows() {
	if a == nil || a.browserMgr == nil {
		return
	}
	if err := a.startBackgroundTask(func(ctx context.Context) {
		a.restoreRunningBrowserProfiles(ctx, detectBrowserRuntimeByUserDataDir)
	}); err != nil {
		logger.New("Browser").Warn("恢复已打开实例的窗口标识失败", logger.F("error", err))
	}
}

func (a *App) restoreRunningBrowserProfiles(ctx context.Context, detect func(string) (browserRuntimeDetection, bool)) {
	if a == nil || a.browserMgr == nil || ctx == nil || detect == nil || ctx.Err() != nil {
		return
	}
	type candidate struct {
		id, userDataDir, lastStartAt, lastStopAt string
		profile                                  *BrowserProfile
	}
	manager := a.browserMgr
	manager.Mutex.Lock()
	candidates := make([]candidate, 0, len(manager.Profiles))
	seen := make(map[string]struct{}, len(manager.Profiles))
	for id, profile := range manager.Profiles {
		if profile == nil || profile.DeletedAt != "" {
			continue
		}
		if profile.Running || manager.BrowserProcesses[id] != nil {
			seen[manager.ResolveUserDataDir(profile)] = struct{}{}
			continue
		}
		candidates = append(candidates, candidate{
			id: id, userDataDir: manager.ResolveUserDataDir(profile),
			lastStartAt: profile.LastStartAt, lastStopAt: profile.LastStopAt, profile: profile,
		})
	}
	manager.Mutex.Unlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })

	// A shared user-data directory represents one Chrome process, even if an old
	// configuration happens to reference it twice. Never apply competing markers.
	for _, item := range candidates {
		if ctx.Err() != nil {
			return
		}
		if item.userDataDir == "" {
			continue
		}
		if _, exists := seen[item.userDataDir]; exists {
			continue
		}
		seen[item.userDataDir] = struct{}{}
		detection, exists := detect(item.userDataDir)
		if !exists || !detection.DebugReady || detection.DebugPort <= 0 || ctx.Err() != nil {
			continue
		}

		manager.Mutex.Lock()
		profile := manager.Profiles[item.id]
		// Detection runs outside the manager lock. Respect any start, stop,
		// deletion or directory edit made while that operation was in progress.
		if ctx.Err() != nil || profile != item.profile || profile.Running || profile.DeletedAt != "" ||
			manager.BrowserProcesses[item.id] != nil || profile.LastStartAt != item.lastStartAt ||
			profile.LastStopAt != item.lastStopAt || manager.ResolveUserDataDir(profile) != item.userDataDir {
			manager.Mutex.Unlock()
			continue
		}
		a.markProfileRunningLocked(item.id, profile, nil, detection.PID, detection.DebugPort, true, "")
		snapshot := copyBrowserProfileSnapshot(profile)
		manager.Mutex.Unlock()
		a.emitBrowserInstanceStarted(snapshot, true)
	}
}
