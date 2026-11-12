import { useCallback, useMemo } from 'react'

import { selectUsableProxies, useProxySpeedStore, type ProxySpeedResult } from '../../proxySpeedStore'
import type { BrowserProxy } from '../../types'
import { toLatencyValue } from './storage'

type Results = Record<string, ProxySpeedResult>
type TestingIds = Record<string, true>
interface SpeedState { results: Results; testingIds: TestingIds }

const EMPTY_RESULTS: Results = {}
const EMPTY_TESTING: TestingIds = {}
const TESTING = Symbol('proxy-pool-testing')

export function deriveProxyPoolSpeedMaps(results: Results, testingIds: TestingIds) {
  const latencyMap: Record<string, number> = {}
  const latencyEngineMap: Record<string, string> = {}
  const latencyErrorMap: Record<string, string> = {}
  Object.entries(results).forEach(([proxyId, result]) => {
    latencyMap[proxyId] = toLatencyValue(result.ok, result.latencyMs, result.error)
    if (result.engine) latencyEngineMap[proxyId] = result.engine
    if (result.error) latencyErrorMap[proxyId] = result.error
  })
  Object.keys(testingIds).forEach(proxyId => { latencyMap[proxyId] = -1 })
  return { latencyMap, latencyEngineMap, latencyErrorMap }
}

// Keep the hook order fixed when sorting/filtering is enabled at runtime.
// The default preserves useProxyChecks' existing virtual-group-page consumer.
export function useProxyPoolSpeedMaps(aggregate = true) {
  const results = useProxySpeedStore(useCallback(
    (state: SpeedState) => aggregate ? state.results : EMPTY_RESULTS,
    [aggregate],
  ))
  const testingIds = useProxySpeedStore(useCallback(
    (state: SpeedState) => aggregate ? state.testingIds : EMPTY_TESTING,
    [aggregate],
  ))
  return useMemo(() => deriveProxyPoolSpeedMaps(results, testingIds), [results, testingIds])
}

// Proxy-pool rows have always used exact record keys, unlike profile aliases.
// While testing, neither the old result nor its engine/error is visible. A
// stable sentinel avoids commits for results arriving before testing finishes.
export function useProxyPoolSpeedValue(proxyId: string | undefined) {
  const selection = useProxySpeedStore(useCallback(
    (state: SpeedState) => proxyId === undefined ? undefined
      : state.testingIds[proxyId] ? TESTING : state.results[proxyId],
    [proxyId],
  ))
  if (selection === TESTING) return { latency: -1, engine: '', error: '' }
  return {
    latency: selection ? toLatencyValue(selection.ok, selection.latencyMs, selection.error) : undefined,
    engine: selection?.engine || '',
    error: selection?.error || '',
  }
}

export function deriveFetchProxyOptions(proxies: BrowserProxy[], results: Results) {
  return selectUsableProxies(proxies, results).map(proxy => ({
    ...proxy,
    lastLatencyMs: results[proxy.proxyId]?.latencyMs ?? proxy.lastLatencyMs,
  }))
}

export function deriveDownloadProxyOptions(proxies: BrowserProxy[], latencyMap: Record<string, number>) {
  return proxies
    .filter(proxy => {
      if (!proxy.proxyId || proxy.proxyConfig === 'direct://') return false
      const latency = latencyMap[proxy.proxyId]
      return typeof latency === 'number' && latency > 0
    })
    .sort((a, b) => (latencyMap[a.proxyId] || 0) - (latencyMap[b.proxyId] || 0))
    .slice(0, 20)
    .map(proxy => ({ value: proxy.proxyConfig, label: `${proxy.proxyName} · ${latencyMap[proxy.proxyId]}ms` }))
}

export function useFetchProxyOptions(proxies: BrowserProxy[]) {
  const results = useProxySpeedStore(state => state.results)
  return useMemo(() => deriveFetchProxyOptions(proxies, results), [proxies, results])
}

export function useDownloadProxyOptions(proxies: BrowserProxy[]) {
  const { latencyMap } = useProxyPoolSpeedMaps()
  return useMemo(() => deriveDownloadProxyOptions(proxies, latencyMap), [proxies, latencyMap])
}
