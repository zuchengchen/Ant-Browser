package backend

import (
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"
	"ant-chrome/backend/internal/proxy"
	"fmt"
	"strings"
)

func (a *App) SaveBrowserProxies(proxies []BrowserProxy) error {
	a.proxySourceRefreshMu.Lock()
	defer a.proxySourceRefreshMu.Unlock()
	return a.saveBrowserProxiesLocked(proxies)
}

// BrowserProxyApplyChanges saves additions, edits and deletions made to a
// proxy snapshot while retaining updates the user did not edit.
func (a *App) BrowserProxyApplyChanges(base, edited []BrowserProxy, sourceID string) error {
	a.proxySourceRefreshMu.Lock()
	defer a.proxySourceRefreshMu.Unlock()
	latest, err := a.BrowserProxyList()
	if err != nil {
		return err
	}
	if sourceID = strings.TrimSpace(sourceID); sourceID != "" && proxySourcePreviewChanged(base, latest, sourceID) {
		return fmt.Errorf("订阅源已有更新，请重新加载代理池和订阅内容")
	}
	merged, changed, err := mergeBrowserProxyEdits(base, edited, latest)
	if err != nil || !changed {
		return err
	}
	return a.saveBrowserProxiesLocked(merged)
}

func mergeBrowserProxyEdits(base, edited, latest []BrowserProxy) ([]BrowserProxy, bool, error) {
	baseByID := proxyRowsByID(base)
	editedByID := proxyRowsByID(edited)
	latestByID := proxyRowsByID(latest)
	merged := make([]BrowserProxy, 0, len(latest))
	changed := false
	for _, current := range latest {
		id := proxyRowID(current)
		before, inBase := baseByID[id]
		after, inEdit := editedByID[id]
		if inBase && !inEdit && id != "__direct__" {
			changed = true
			continue
		}
		if inBase && inEdit {
			updated := applyBrowserProxyFields(current, before, after)
			changed = changed || updated != current
			current = updated
		}
		merged = append(merged, current)
	}
	for _, after := range edited {
		id := proxyRowID(after)
		before, inBase := baseByID[id]
		_, exists := latestByID[id]
		if inBase {
			if !exists && applyBrowserProxyFields(before, before, after) != before {
				return nil, false, fmt.Errorf("代理「%s」已删除，请重新加载代理池", before.ProxyName)
			}
			continue
		}
		if exists {
			return nil, false, fmt.Errorf("代理 ID 已存在，请重新加载代理池")
		}
		changed = true
		merged = append(merged, after)
	}
	return merged, changed, nil
}

func proxyRowID(row BrowserProxy) string {
	return strings.ToLower(strings.TrimSpace(row.ProxyId))
}

func proxyRowsByID(rows []BrowserProxy) map[string]BrowserProxy {
	index := make(map[string]BrowserProxy, len(rows))
	for _, row := range rows {
		if id := proxyRowID(row); id != "" {
			index[id] = row
		}
	}
	return index
}

func proxySourcePreviewChanged(base, latest []BrowserProxy, sourceID string) bool {
	original := make(map[string]BrowserProxy)
	for _, row := range base {
		if proxySourceKey(row) == sourceID {
			original[proxyRowID(row)] = row
		}
	}
	for _, row := range latest {
		if proxySourceKey(row) != sourceID {
			continue
		}
		id := proxyRowID(row)
		before, exists := original[id]
		if !exists || applyBrowserProxyFields(before, before, row) != before {
			return true
		}
		delete(original, id)
	}
	return len(original) > 0
}

func applyBrowserProxyFields(current, base, edited BrowserProxy) BrowserProxy {
	applyProxyField(&current.ProxyName, base.ProxyName, edited.ProxyName)
	applyProxyField(&current.ProxyConfig, base.ProxyConfig, edited.ProxyConfig)
	applyProxyField(&current.PreferredKernel, proxy.NormalizePreferredKernel(base.PreferredKernel), proxy.NormalizePreferredKernel(edited.PreferredKernel))
	applyProxyField(&current.DnsServers, base.DnsServers, edited.DnsServers)
	applyProxyField(&current.GroupName, base.GroupName, edited.GroupName)
	applyProxyField(&current.SourceID, base.SourceID, edited.SourceID)
	applyProxyField(&current.SourceURL, base.SourceURL, edited.SourceURL)
	applyProxyField(&current.SourceNamePrefix, base.SourceNamePrefix, edited.SourceNamePrefix)
	applyProxyField(&current.SourceAutoRefresh, base.SourceAutoRefresh, edited.SourceAutoRefresh)
	applyProxyField(&current.SourceRefreshIntervalM, base.SourceRefreshIntervalM, edited.SourceRefreshIntervalM)
	applyProxyField(&current.SourceFetchProxyID, base.SourceFetchProxyID, edited.SourceFetchProxyID)
	// 手动对齐可更新刷新状态。并发刷新的较新状态优先。
	if current.SourceLastRefreshAt == base.SourceLastRefreshAt &&
		current.SourceLastRefreshError == base.SourceLastRefreshError &&
		current.SourceMissing == base.SourceMissing &&
		current.SourceMissingSince == base.SourceMissingSince {
		applyProxyField(&current.SourceLastRefreshAt, base.SourceLastRefreshAt, edited.SourceLastRefreshAt)
		applyProxyField(&current.SourceLastRefreshError, base.SourceLastRefreshError, edited.SourceLastRefreshError)
		applyProxyField(&current.SourceMissing, base.SourceMissing, edited.SourceMissing)
		applyProxyField(&current.SourceMissingSince, base.SourceMissingSince, edited.SourceMissingSince)
	}
	return current
}

func applyProxyField[T comparable](field *T, base, edited T) {
	if base != edited {
		*field = edited
	}
}

func (a *App) saveBrowserProxiesLocked(proxies []BrowserProxy) error {
	if a.browserMgr == nil || a.config == nil {
		return fmt.Errorf("代理数据保存上下文不可用")
	}
	log := logger.New("Browser")
	normalized := proxy.NormalizeBrowserProxies(proxies, generateUUID)
	// 在持久化前完成拉取代理自愈：一次 ReplaceAll 写回，避免二次全量写与竞态窗口。
	updateFetchProxyWarnings(normalized)

	if a.browserMgr.ProxyDAO != nil {
		if err := a.browserMgr.ProxyDAO.ReplaceAll(normalized); err != nil {
			log.Error("代理列表事务保存失败", logger.F("error", err))
			return err
		}
		log.Info("代理列表已保存到数据库", logger.F("count", len(normalized)))
	} else {
		if err := config.SaveProxies(a.resolveAppPath("proxies.yaml"), normalized); err != nil {
			log.Error("代理列表保存失败", logger.F("error", err))
			return err
		}
	}
	unlock := config.LockConfigWrite()
	a.config.Browser.Proxies = normalized
	unlock()
	a.healProfileProxyBindings()
	if err := a.pruneProxyVirtualGroupMembers(normalized); err != nil {
		return err
	}
	return nil
}

func (a *App) pruneProxyVirtualGroupMembers(proxies []BrowserProxy) error {
	if a == nil || a.browserMgr == nil {
		return nil
	}
	validProxyIDs := make(map[string]string, len(proxies))
	for _, item := range proxies {
		proxyID := strings.TrimSpace(item.ProxyId)
		if proxyID == "" || strings.EqualFold(proxyID, "__direct__") || strings.EqualFold(strings.TrimSpace(item.ProxyConfig), "direct://") {
			continue
		}
		validProxyIDs[strings.ToLower(proxyID)] = proxyID
	}

	groups := a.BrowserProxyVirtualGroupList()
	if len(groups) == 0 {
		return nil
	}
	configGroups := make([]BrowserProxyVirtualGroup, 0, len(groups))
	configChanged := false
	for _, group := range groups {
		group = browser.NormalizeProxyVirtualGroup(group)
		// 动态分组（MemberGroupNames）的成员由代理池实时展开，不能把展开结果写回
		// MemberProxyIds，否则下次刷新时动态组会退化成静态快照。
		isDynamicMembers := len(group.MemberGroupNames) > 0
		filteredIDs := make([]string, 0, len(group.MemberProxyIds))
		for _, memberProxyID := range group.MemberProxyIds {
			if canonicalID, exists := validProxyIDs[strings.ToLower(strings.TrimSpace(memberProxyID))]; exists {
				filteredIDs = append(filteredIDs, canonicalID)
			}
		}
		selectedProxyID := strings.ToLower(strings.TrimSpace(group.SelectedProxyId))
		_, selectedExists := validProxyIDs[selectedProxyID]
		preferredProxyID := strings.ToLower(strings.TrimSpace(group.PreferredProxyId))
		_, preferredExists := validProxyIDs[preferredProxyID]
		changed := len(filteredIDs) != len(group.MemberProxyIds) ||
			(selectedProxyID != "" && !selectedExists) ||
			(preferredProxyID != "" && !preferredExists)
		if !changed {
			// 动态组即使成员列表未变，也要按展开结果清理孤儿状态行。
			if isDynamicMembers {
				effective := browser.ExpandProxyVirtualGroup(group, proxies)
				stateGroup := group
				stateGroup.MemberProxyIds = make([]string, 0, len(effective))
				for _, item := range effective {
					stateGroup.MemberProxyIds = append(stateGroup.MemberProxyIds, item.ProxyId)
				}
				a.pruneVirtualGroupMemberStates(stateGroup)
			}
			configGroups = append(configGroups, group)
			continue
		}
		if isDynamicMembers {
			// 保留空 MemberProxyIds，让 ResolveProxyVirtualGroup 继续按组名动态展开。
			group.MemberProxyIds = nil
		} else {
			group.MemberProxyIds = filteredIDs
		}
		if !selectedExists {
			group.SelectedProxyId = ""
		}
		if !preferredExists {
			group.PreferredProxyId = ""
		}
		configGroups = append(configGroups, group)
		a.resetProxyVirtualGroupCursorForGroup(group)
		if a.browserMgr.ProxyVirtualGroupDAO != nil {
			if err := a.browserMgr.ProxyVirtualGroupDAO.Upsert(group); err != nil {
				return fmt.Errorf("清理虚拟代理分组「%s」失效关联失败: %w", group.GroupName, err)
			}
			stateGroup := group
			if isDynamicMembers {
				effective := browser.ExpandProxyVirtualGroup(group, proxies)
				stateGroup.MemberProxyIds = make([]string, 0, len(effective))
				for _, item := range effective {
					stateGroup.MemberProxyIds = append(stateGroup.MemberProxyIds, item.ProxyId)
				}
			}
			a.pruneVirtualGroupMemberStates(stateGroup)
		} else {
			configChanged = true
		}
	}
	if configChanged && a.config != nil {
		a.config.Browser.ProxyVirtualGroups = configGroups
		if err := a.config.Save(a.resolveAppPath("config.yaml")); err != nil {
			return fmt.Errorf("保存清理后的虚拟代理分组失败: %w", err)
		}
	}
	a.invalidateVirtualGroupSnapshot()
	return nil
}
