package backend

import (
	"strings"

	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/logger"
	"ant-chrome/backend/internal/proxy"
)

func browserNormalizeProxies(proxies []BrowserProxy) []BrowserProxy {
	return proxy.NormalizeBrowserProxies(proxies, generateUUID)
}

// 持久错误文案前缀，便于前端识别与将来统一替换。
const proxyBindClearedPrefix = "代理绑定已清理："

// ProxyReferenceHealReport 一次引用自愈的统计结果。
type ProxyReferenceHealReport struct {
	ProfileBindingsCleared    int `json:"profileBindingsCleared"`
	FetchProxiesCleared       int `json:"fetchProxiesCleared"`
	FetchProxyWarningsChanged int `json:"fetchProxyWarningsChanged"`
	ProfileGroupsCleared      int `json:"profileGroupsCleared"`
}

// loadProxyPoolForHeal 读取用于引用自愈的代理池。
// 读取失败时返回 ok=false：此时"空池"不代表代理真的被删除，
// 绝不能据此清空实例绑定或拉取代理，否则数据库瞬时故障会抹掉全部绑定配置。
func (a *App) loadProxyPoolForHeal() ([]BrowserProxy, bool) {
	if a == nil || a.browserMgr == nil {
		return nil, false
	}
	if a.browserMgr.ProxyDAO != nil {
		list, err := a.browserMgr.ProxyDAO.List()
		if err != nil {
			logger.New("Browser").Error("读取代理目录失败，跳过代理引用自愈", logger.F("error", err.Error()))
			return nil, false
		}
		return list, true
	}
	if a.config == nil {
		return nil, false
	}
	return append([]BrowserProxy(nil), a.config.Browser.Proxies...), true
}

// ReconcileProxyReferences 幂等地把指向已删除对象的引用修复成明确态。
// 不拦截删除、不回退直连、不猜测替代节点；先按快照重绑，失败才清理并写持久错误。
func (a *App) ReconcileProxyReferences() ProxyReferenceHealReport {
	report := ProxyReferenceHealReport{}
	if a == nil || a.browserMgr == nil {
		return report
	}

	pool, ok := a.loadProxyPoolForHeal()
	if !ok {
		return report
	}
	report.ProfileBindingsCleared = a.reconcileProfileProxyBindings(pool)
	if changes := updateFetchProxyWarnings(pool); changes > 0 {
		if err := a.saveProxiesWithoutHeal(pool); err != nil {
			logger.New("Browser").Error("订阅拉取代理修复持久化失败", logger.F("error", err))
		} else {
			report.FetchProxyWarningsChanged = changes
		}
	}
	// 实例业务分组由成员关系的外键和 ChangeGroupDefinition 事务维护。

	if report.ProfileBindingsCleared > 0 || report.FetchProxyWarningsChanged > 0 || report.ProfileGroupsCleared > 0 {
		logger.New("Browser").Info("代理引用自愈完成",
			logger.F("bindings_cleared", report.ProfileBindingsCleared),
			logger.F("fetch_proxy_warnings", report.FetchProxyWarningsChanged),
			logger.F("groups_cleared", report.ProfileGroupsCleared),
		)
		a.emitRuntimeEvent("proxy:references:healed", map[string]interface{}{
			"profileBindingsCleared":    report.ProfileBindingsCleared,
			"fetchProxiesCleared":       report.FetchProxiesCleared,
			"fetchProxyWarningsChanged": report.FetchProxyWarningsChanged,
			"profileGroupsCleared":      report.ProfileGroupsCleared,
		})
	}
	return report
}

// healProfileProxyBindings runs after changes to a proxy or virtual group.
func (a *App) healProfileProxyBindings() int {
	pool, ok := a.loadProxyPoolForHeal()
	if !ok {
		return 0
	}
	return a.reconcileProfileProxyBindings(pool)
}

const missingFetchProxyWarning = "订阅拉取代理已失效，请重新选择拉取代理或明确选择直连"

// Keep the configured ID so refresh cannot silently change its network route.
func updateFetchProxyWarnings(pool []BrowserProxy) int {
	proxyIDs := make(map[string]bool, len(pool))
	for _, row := range pool {
		if strings.TrimSpace(row.ProxyConfig) != "" {
			proxyIDs[strings.ToLower(strings.TrimSpace(row.ProxyId))] = true
		}
	}
	changes := 0
	for i := range pool {
		row := &pool[i]
		id := strings.ToLower(strings.TrimSpace(row.SourceFetchProxyID))
		if id != "" && !proxyIDs[id] {
			if row.SourceLastRefreshError != missingFetchProxyWarning {
				row.SourceLastRefreshError = missingFetchProxyWarning
				changes++
			}
		} else if row.SourceLastRefreshError == missingFetchProxyWarning {
			row.SourceLastRefreshError = ""
			changes++
		}
	}
	return changes
}

// saveProxiesWithoutHeal 保存代理池而不触发 ReconcileProxyReferences，避免自愈内部递归。
func (a *App) saveProxiesWithoutHeal(proxies []BrowserProxy) error {
	normalized := browserNormalizeProxies(proxies)
	if a.browserMgr.ProxyDAO != nil {
		if err := a.browserMgr.ProxyDAO.ReplaceAll(normalized); err != nil {
			return err
		}
	} else if a.config != nil {
		if err := config.SaveProxies(a.resolveAppPath("proxies.yaml"), normalized); err != nil {
			return err
		}
	}
	if a.config != nil {
		a.config.Browser.Proxies = normalized
	}
	return nil
}
