import { useEffect, useMemo, useState } from 'react'
import type { BrowserCore, BrowserProfile, BrowserGroup } from '../../types'
import { EMPTY_FILTERS, type InstanceFilters } from '../../components/InstanceFilterBar'
import { migrateGroupFilter, profileMatchesGroup, groupPath, profileGroupIds } from '../../utils/instanceGroups'
import type { BrowserViewMode } from '../../components/BrowserListLayout'

export const resolveProfileStatus = (running: boolean, debugReady: boolean, starting: boolean, stopping: boolean, lastError = '') => {
  if (starting) {
    return { variant: 'default' as const, label: '启动中' }
  }
  if (stopping) {
    return { variant: 'default' as const, label: '停止中' }
  }
  if (!running && lastError.trim()) {
    return { variant: 'error' as const, label: '异常' }
  }
  if (running && !debugReady) {
    return { variant: 'info' as const, label: '运行中（待就绪）' }
  }
  if (running) {
    return { variant: 'success' as const, label: '运行中' }
  }
  return { variant: 'default' as const, label: '已停止' }
}

export function useBrowserListViewState(groups: BrowserGroup[], groupsLoaded: boolean) {
  const [savedFilters] = useState<Record<string, unknown>>(() => {
    try { const value = JSON.parse(localStorage.getItem('browser:filters') || '{}'); return value && typeof value === 'object' && !Array.isArray(value) ? value : {} }
    catch { return {} }
  })
  const [filtersMigrated, setFiltersMigrated] = useState(false)
  const [viewMode, setViewMode] = useState<BrowserViewMode>(() => {
    return (localStorage.getItem('browser:viewMode') as BrowserViewMode) || 'table'
  })
  const [filters, setFilters] = useState<InstanceFilters>(() => ({
    ...EMPTY_FILTERS,
    keyword: typeof savedFilters.keyword === 'string' ? savedFilters.keyword : '',
    kwSearch: typeof savedFilters.kwSearch === 'string' ? savedFilters.kwSearch : '',
    status: savedFilters.status === 'running' || savedFilters.status === 'stopped' ? savedFilters.status : '',
    proxyId: typeof savedFilters.proxyId === 'string' ? savedFilters.proxyId : '',
    coreId: typeof savedFilters.coreId === 'string' ? savedFilters.coreId : '',
    ...migrateGroupFilter(savedFilters, []),
  }))
  useEffect(() => {
    if (!groupsLoaded || filtersMigrated) return
    const initial = migrateGroupFilter(savedFilters, [])
    setFilters(previous => previous.groupId === initial.groupId && JSON.stringify(previous.groupIds) === JSON.stringify(initial.groupIds)
      ? { ...previous, ...migrateGroupFilter(savedFilters, groups) } : previous)
    setFiltersMigrated(true)
  }, [groups, groupsLoaded, filtersMigrated, savedFilters])
  const [headerCollapsed, setHeaderCollapsed] = useState(() => {
    return localStorage.getItem('browser:headerCollapsed') === 'true'
  })

  useEffect(() => {
    if (filtersMigrated) localStorage.setItem('browser:filters', JSON.stringify({ version: 2, ...filters }))
  }, [filters, filtersMigrated])

  useEffect(() => {
    localStorage.setItem('browser:viewMode', viewMode)
  }, [viewMode])

  useEffect(() => {
    localStorage.setItem('browser:headerCollapsed', String(headerCollapsed))
  }, [headerCollapsed])

  return {
    viewMode,
    setViewMode,
    filters,
    setFilters,
    headerCollapsed,
    setHeaderCollapsed,
  }
}

export function useBrowserListDerived(
  profiles: BrowserProfile[],
  cores: BrowserCore[],
  groups: BrowserGroup[],
  filters: InstanceFilters,
  startingIds: Set<string>,
  stoppingIds: Set<string>
) {
  const runningCount = useMemo(() => profiles.filter(profile => profile.running).length, [profiles])
  const defaultCore = useMemo(() => {
    return cores.find(core => core.isDefault) || cores[0] || null
  }, [cores])

  const resolveProfileCore = (profile: BrowserProfile) => {
    const coreId = (profile.coreId || '').trim()
    if (coreId && !/^default$/i.test(coreId)) {
      return cores.find(core => core.coreId === coreId) || null
    }
    return defaultCore
  }

  const getProfileCoreLabel = (profile: BrowserProfile) => {
    const resolvedCore = resolveProfileCore(profile)
    if (resolvedCore) {
      return resolvedCore.coreName
    }

    const coreId = (profile.coreId || '').trim()
    if (!coreId || /^default$/i.test(coreId)) {
      return '使用默认内核'
    }
    return coreId
  }

  const isProfileStarting = (profileId: string) => startingIds.has(profileId)
  const isProfileStopping = (profileId: string) => stoppingIds.has(profileId)
  const isProfileBusy = (profileId: string) => isProfileStarting(profileId) || isProfileStopping(profileId)

  const getProfileStatus = (profile: BrowserProfile) => (
    resolveProfileStatus(profile.running, profile.debugReady, isProfileStarting(profile.profileId), isProfileStopping(profile.profileId), profile.lastError)
  )

  const filteredProfiles = useMemo(() => {
    const unifiedKeyword = filters.keyword || filters.kwSearch
    return profiles.filter(profile => {
      if (!profileMatchesGroup(profile, filters.groupId, groups)) return false
      if (filters.groupIds.length && !filters.groupIds.some(id => profileMatchesGroup(profile, id, groups))) return false
      if (unifiedKeyword && !matchesProfileKeyword(profile, unifiedKeyword, groups)) return false
      if (filters.status === 'running' && !profile.running) return false
      if (filters.status === 'stopped' && profile.running) return false
      if (filters.proxyId === '__none__' && (profile.proxyId || profile.proxyConfig)) return false
      if (filters.proxyId && filters.proxyId !== '__none__' && profile.proxyId !== filters.proxyId) return false
      if (filters.coreId) {
        const effectiveCore = resolveProfileCore(profile)
        if (!effectiveCore || effectiveCore.coreId !== filters.coreId) return false
      }
      return true
    }).sort((a, b) => naturalCompare(a.profileName, b.profileName))
  }, [profiles, filters, defaultCore, cores, groups])

  return {
    runningCount,
    filteredProfiles,
    resolveProfileCore,
    getProfileCoreLabel,
    isProfileStarting,
    isProfileStopping,
    isProfileBusy,
    getProfileStatus,
  }
}

function normalizeSearchText(value: string): string {
  return value.trim().toLowerCase()
}

function normalizeCompactSearchText(value: string): string {
  return normalizeSearchText(value).replace(/[\s_-]+/g, '')
}

function matchesProfileKeyword(profile: BrowserProfile, keyword: string, groups: BrowserGroup[]): boolean {
  const query = normalizeSearchText(keyword)
  if (!query) return true

  const compactQuery = normalizeCompactSearchText(query)
  const values = [
    profile.profileName,
    profile.launchCode,
    profile.profileId,
    profile.userDataDir,
    profile.proxyId,
    profile.proxyBindName,
    profile.proxyBindSourceUrl,
    ...profileGroupIds(profile).map(id => groupPath(id, groups)),
    ...(profile.keywords || []),
  ]

  return values.some(value => {
    const text = normalizeSearchText(String(value || ''))
    if (!text) return false
    return text.includes(query) || normalizeCompactSearchText(text).includes(compactQuery)
  })
}

function naturalCompare(a: string, b: string): number {
  const re = /(\d+)|(\D+)/g
  const partsA = a.match(re) || []
  const partsB = b.match(re) || []
  for (let index = 0; index < Math.max(partsA.length, partsB.length); index++) {
    if (index >= partsA.length) return -1
    if (index >= partsB.length) return 1
    const partA = partsA[index]
    const partB = partsB[index]
    const numberA = Number(partA)
    const numberB = Number(partB)
    if (!Number.isNaN(numberA) && !Number.isNaN(numberB)) {
      if (numberA !== numberB) return numberA - numberB
    } else {
      const compared = partA.localeCompare(partB, 'zh-CN')
      if (compared !== 0) return compared
    }
  }
  return 0
}
