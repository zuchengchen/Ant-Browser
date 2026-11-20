package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/logger"
	"fmt"
	"strings"
	"time"
)

// Reconcile a fresh catalog once, and publish each changed binding only
// after its scoped database update succeeds.
func (a *App) reconcileProfileProxyBindings(pool []BrowserProxy) int {
	log := logger.New("Browser")
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	if len(a.browserMgr.Profiles) == 0 {
		return 0
	}

	var groups []BrowserProxyVirtualGroup
	if dao := a.browserMgr.ProxyVirtualGroupDAO; dao != nil {
		var err error
		groups, err = dao.List()
		if err != nil {
			log.Error("读取虚拟代理分组失败，跳过实例绑定修复", logger.F("error", err))
			return 0
		}
	} else if a.browserMgr.Config != nil {
		groups = a.browserMgr.Config.Browser.ProxyVirtualGroups
	}
	groupIDs := make(map[string]bool, len(groups))
	for _, group := range groups {
		id := strings.ToLower(strings.TrimSpace(group.GroupId))
		if id != "" {
			groupIDs[id] = true
		}
	}

	resolveBinding := browser.NewProfileProxyBindingResolver(pool)
	var saveBinding func(*browser.Profile) error
	if dao := a.browserMgr.ProfileDAO; dao != nil {
		saveBinding = dao.Upsert
		if writer, ok := dao.(interface{ UpdateProxyBinding(*browser.Profile) error }); ok {
			saveBinding = writer.UpdateProxyBinding
		}
	}
	var fileChanges []browser.Profile
	changedCount := 0
	clearedCount := 0
	reboundCount := 0
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		next := *profile
		changed, boundInPool, mode := healProfileProxyBinding(&next, groupIDs, resolveBinding)
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
			if mode == "cleared" {
				clearedCount++
			}
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
		return 0
	}
	if len(fileChanges) > 0 {
		previousConfig := a.browserMgr.Config.Browser.Profiles
		if err := a.browserMgr.SaveProfiles(); err != nil {
			a.browserMgr.Config.Browser.Profiles = previousConfig
			for _, previous := range fileChanges {
				*a.browserMgr.Profiles[previous.ProfileId] = previous
			}
			log.Error("实例代理绑定修复持久化失败", logger.F("error", err.Error()))
			return 0
		}
	}
	log.Info("实例代理绑定修复完成",
		logger.F("changed", changedCount),
		logger.F("rebound", reboundCount),
	)
	return clearedCount
}

// Invalid fixed bindings keep their error until the user selects a new proxy.
// A virtual group owns its ID even when a physical node has a matching name.
func healProfileProxyBinding(profile *browser.Profile, groupIDs map[string]bool, resolve func(*browser.Profile) (bool, bool, string)) (bool, bool, string) {
	id := strings.TrimSpace(profile.ProxyId)
	mode := browser.NormalizeProxyMode(profile.ProxyMode)
	if mode == browser.ProxyModeDynamic || mode == browser.ProxyModeLocal || strings.EqualFold(id, "__local__") || groupIDs[strings.ToLower(id)] {
		changed := profile.ProxyBindError != ""
		profile.ProxyBindError = ""
		return changed, false, ""
	}
	if id == "" && profile.ProxyBindError != "" {
		return false, false, ""
	}
	changed, bound, reason := resolve(profile)
	if bound || id == "" {
		return changed, bound, reason
	}
	name := strings.TrimSpace(profile.ProxyBindName)
	if name == "" {
		name = id
	}
	profile.ProxyId = ""
	profile.ProxyConfig = ""
	profile.ProxyBindError = fmt.Sprintf("%s代理「%s」已删除或不可用，绑定已清理，请重新选择代理（不会自动改为直连）", proxyBindClearedPrefix, name)
	return true, false, "cleared"
}
