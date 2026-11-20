package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/logger"
	"time"
)

// reconcileProfileProxyBindings 对实例代理绑定执行幂等修复：
// 1. 同步已存在 proxyId 的绑定快照；
// 2. 当 proxyId 失效时按绑定快照/配置执行自动重关联；
// 3. 仅在有变更时持久化。
func (a *App) reconcileProfileProxyBindings() {
	if a == nil || a.browserMgr == nil {
		return
	}

	log := logger.New("Browser")
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	if len(a.browserMgr.Profiles) == 0 {
		return
	}

	resolveBinding := a.browserMgr.NewProfileProxyBindingResolver()
	var saveBinding func(*browser.Profile) error
	if dao := a.browserMgr.ProfileDAO; dao != nil {
		saveBinding = dao.Upsert
		if writer, ok := dao.(interface{ UpdateProxyBinding(*browser.Profile) error }); ok {
			saveBinding = writer.UpdateProxyBinding
		}
	}
	var fileChanges []browser.Profile
	changedCount := 0
	reboundCount := 0
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		next := *profile
		changed, boundInPool, mode := resolveBinding(&next)
		if changed {
			next.UpdatedAt = time.Now().Format(time.RFC3339)
			if saveBinding != nil {
				if err := saveBinding(&next); err != nil {
					log.Error("实例代理绑定修复持久化失败", logger.F("profile_id", profile.ProfileId), logger.F("error", err.Error()))
					continue
				}
			} else {
				fileChanges = append(fileChanges, *profile)
			}
			*profile = next
			changedCount++
		}
		if boundInPool && mode != "" && mode != "proxy_id" {
			reboundCount++
			log.Info("实例代理重关联成功",
				logger.F("profile_id", profile.ProfileId),
				logger.F("profile_name", profile.ProfileName),
				logger.F("proxy_id", profile.ProxyId),
				logger.F("mode", mode),
			)
		}
	}

	if changedCount == 0 {
		return
	}
	if len(fileChanges) > 0 {
		previousConfig := a.browserMgr.Config.Browser.Profiles
		if err := a.browserMgr.SaveProfiles(); err != nil {
			a.browserMgr.Config.Browser.Profiles = previousConfig
			for _, previous := range fileChanges {
				*a.browserMgr.Profiles[previous.ProfileId] = previous
			}
			log.Error("实例代理绑定修复持久化失败", logger.F("error", err.Error()))
			return
		}
	}
	log.Info("实例代理绑定修复完成",
		logger.F("changed", changedCount),
		logger.F("rebound", reboundCount),
	)
}
