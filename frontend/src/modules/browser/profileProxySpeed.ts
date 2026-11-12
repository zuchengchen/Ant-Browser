import { useCallback, useMemo } from 'react'

import { isProxySpeedResultFresh } from './pages/proxyPool/storage'
import { useProxySpeedStore, type ProxySpeedResult } from './proxySpeedStore'

type ProxySpeedResults = Record<string, ProxySpeedResult>

const normalizeProxyKey = (value?: string) => (value || '').trim().toLowerCase()

// Store writes replace the results map and leave existing result objects intact.
// Share fallback lookup work across rows, without retaining obsolete snapshots.
const normalizedResults = new WeakMap<ProxySpeedResults, Map<string, ProxySpeedResult>>()

export function findProfileProxySpeedResult(results: ProxySpeedResults, proxyId: string): ProxySpeedResult | null {
  const key = normalizeProxyKey(proxyId)
  if (!key) return null
  const exact = results[proxyId]
  if (exact) return exact

  let byProxyId = normalizedResults.get(results)
  if (!byProxyId) {
    byProxyId = new Map()
    // Preserve Object.values order and the first matching result.proxyId, even
    // when a record key differs from its result ID or aliases share an ID.
    for (const result of Object.values(results)) {
      const resultKey = normalizeProxyKey(result.proxyId)
      if (!byProxyId.has(resultKey)) byProxyId.set(resultKey, result)
    }
    normalizedResults.set(results, byProxyId)
  }
  return byProxyId.get(key) ?? null
}

export function selectVirtualGroupSpeedResult(
  results: ProxySpeedResults,
  memberIds: Set<string>,
  now: number,
  maxAgeMs: number,
): ProxySpeedResult | null {
  let best: ProxySpeedResult | null = null
  memberIds.forEach(memberKey => {
    const result = Object.values(results).find(item => normalizeProxyKey(item.proxyId) === memberKey)
    if (!result || !result.ok) return
    if (!isProxySpeedResultFresh(result.testedAt, now, maxAgeMs)) return
    if (!best || result.latencyMs < best.latencyMs) best = result
  })
  return best
}

export function useProfileProxySpeedResult(
  proxyId: string,
  virtualGroupMemberIds: Set<string> | undefined,
  now: number,
  maxAgeMs: number,
): ProxySpeedResult | null {
  // Keep one unconditional subscription when a profile changes proxy or mode.
  // Virtual groups still depend on the complete map; ordinary rows select the
  // original result object so unrelated events retain Object.is equality.
  const selection = useProxySpeedStore(useCallback(
    (state: { results: ProxySpeedResults }) => virtualGroupMemberIds
      ? state.results
      : findProfileProxySpeedResult(state.results, proxyId),
    [proxyId, virtualGroupMemberIds],
  ))

  return useMemo(() => {
    if (virtualGroupMemberIds) {
      return selectVirtualGroupSpeedResult(selection as ProxySpeedResults, virtualGroupMemberIds, now, maxAgeMs)
    }
    const result = selection as ProxySpeedResult | null
    // Freshness is deliberately outside the snapshot cache: the parent's clock
    // tick must expire results even when the store has received no new events.
    return result && isProxySpeedResultFresh(result.testedAt, now, maxAgeMs) ? result : null
  }, [selection, virtualGroupMemberIds, now, maxAgeMs])
}
