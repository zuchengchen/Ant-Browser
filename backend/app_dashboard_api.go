package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"
	"fmt"
	goruntime "runtime"
	"strings"
)

func (a *App) GetDashboardStats() map[string]interface{} {
	profiles := []BrowserProfile{}
	if a.browserMgr != nil {
		profiles = a.browserMgr.List()
	}
	stats := browser.BuildDashboardStats(profiles, a.config)

	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	memUsedMB := float64(mem.Alloc) / 1024 / 1024

	return map[string]interface{}{
		"totalInstances":   stats.TotalInstances,
		"runningInstances": stats.RunningInstances,
		"proxyCount":       stats.ProxyCount,
		"coreCount":        stats.CoreCount,
		"memUsedMB":        int(memUsedMB),
		"appVersion":       a.appVersion(),
	}
}

func (a *App) GetAppConfig() map[string]interface{} {
	return map[string]interface{}{
		"name":    a.appName(),
		"version": a.appVersion(),
	}
}

func (a *App) GetMemoryStats() map[string]interface{} {
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	return map[string]interface{}{
		"alloc_mb":       float64(mem.Alloc) / 1024 / 1024,
		"total_alloc_mb": float64(mem.TotalAlloc) / 1024 / 1024,
		"sys_mb":         float64(mem.Sys) / 1024 / 1024,
		"num_gc":         mem.NumGC,
		"limit_mb":       a.config.Runtime.MaxMemoryMB,
		"gc_percent":     a.config.Runtime.GCPercent,
	}
}

func (a *App) TriggerGC()               { goruntime.GC() }
func (a *App) SetLogLevel(level string) { logger.SetGlobalLevelString(level) }
func (a *App) GetLogLevel() string      { return logger.New("App").GetLevel().String() }

func (a *App) SaveLogLevel(level string) error {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("未知的日志级别")
	}
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()
	if a.config == nil {
		return fmt.Errorf("应用配置未初始化")
	}

	unlock := config.LockConfigWrite()
	defer unlock()
	next := *a.config
	next.Logging.Level = level
	if err := next.SaveLocked(a.resolveAppPath("config.yaml")); err != nil {
		return err
	}
	a.config.Logging.Level = level
	logger.SetGlobalLevelString(level)
	return nil
}

// GetAppLogs 获取内存缓冲日志
func (a *App) GetAppLogs() []logger.MemoryLogEntry {
	return logger.GetMemoryWriter().GetEntries()
}

// ClearAppLogs 清空内存缓冲日志
func (a *App) ClearAppLogs() {
	logger.GetMemoryWriter().Clear()
}

// GetRunningInstances 获取运行中实例的详细信息
func (a *App) GetRunningInstances() []BrowserProfile {
	return browser.RunningProfiles(a.browserMgr.List())
}
