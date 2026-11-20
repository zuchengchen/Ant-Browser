import { useEffect, useRef, useState } from 'react'
import { toast } from '../../../../shared/components'
import type { BrowserGroupWithCount, BrowserProfile, BrowserProxy, BrowserProxyVirtualGroup } from '../../types'
import { fetchBrowserProfiles, fetchBrowserProxies, fetchBrowserProxyVirtualGroups, fetchGroups } from '../../api'
import { EventsOn } from '../../../../wailsjs/runtime/runtime'
import { reuseProfileSnapshot } from './profileSnapshot'
import { waitForAppReady } from '../../../../utils/appReady'
import { errorMessage } from '../../../../utils/errors'

const PROFILE_LIST_FALLBACK_REFRESH_MS = 15_000

interface UseBrowserListDataOptions {
  loadCores: () => Promise<void>
}

export function useBrowserListData({ loadCores }: UseBrowserListDataOptions) {
  const [profiles, setProfiles] = useState<BrowserProfile[]>([])
  const [loading, setLoading] = useState(true)
  const [proxies, setProxies] = useState<BrowserProxy[]>([])
  const [proxyVirtualGroups, setProxyVirtualGroups] = useState<BrowserProxyVirtualGroup[]>([])
  const [groups, setGroups] = useState<BrowserGroupWithCount[]>([])
  const [groupsLoaded, setGroupsLoaded] = useState(false)
  const [groupsError, setGroupsError] = useState('')
  const groupRequestRef = useRef(0)
  const profileRequestRef = useRef(0)
  const [startingIds, setStartingIds] = useState<Set<string>>(new Set())
  const [stoppingIds, setStoppingIds] = useState<Set<string>>(new Set())
  const profilesRef = useRef<BrowserProfile[]>([])
  const silentRefreshInFlightRef = useRef(false)

  const updatePendingIds = (
    setter: React.Dispatch<React.SetStateAction<Set<string>>>,
    profileId: string,
    active: boolean
  ) => {
    setter(prev => {
      const next = new Set(prev)
      if (active) {
        next.add(profileId)
      } else {
        next.delete(profileId)
      }
      return next
    })
  }

  const replaceProfilesState = (items: BrowserProfile[]) => {
    const next = reuseProfileSnapshot(profilesRef.current, items)
    if (next === profilesRef.current) return
    profilesRef.current = next
    setProfiles(next)
  }

  const updateProfilesState = (updater: (items: BrowserProfile[]) => BrowserProfile[]) => {
    const next = updater(profilesRef.current)
    profilesRef.current = next
    setProfiles(next)
  }

  const mergeProfileState = (profile: BrowserProfile | null | undefined) => {
    if (!profile) return
    updateProfilesState(prev => prev.map(item => (
      item.profileId === profile.profileId ? { ...item, ...profile } : item
    )))
  }

  const syncProfiles = (items: BrowserProfile[], syncRuntimeState: boolean) => {
    if (syncRuntimeState) {
      const previousById = new Map(profilesRef.current.map(item => [item.profileId, item]))
      const newlyRunning = items.find(item => item.running && !previousById.get(item.profileId)?.running)
      if (newlyRunning) {
        updatePendingIds(setStartingIds, newlyRunning.profileId, false)
        updatePendingIds(setStoppingIds, newlyRunning.profileId, false)
      }
      items.forEach(item => {
        if (!item.running && previousById.get(item.profileId)?.running) {
          updatePendingIds(setStartingIds, item.profileId, false)
          updatePendingIds(setStoppingIds, item.profileId, false)
        }
      })
    }
    replaceProfilesState(items)
  }

  const loadProfiles = async ({ silent = false, syncRuntimeState = false }: { silent?: boolean; syncRuntimeState?: boolean } = {}) => {
    if (silent && silentRefreshInFlightRef.current) {
      return profilesRef.current
    }
    if (!silent) {
      setLoading(true)
    } else {
      silentRefreshInFlightRef.current = true
    }
    const request = ++profileRequestRef.current
    try {
      const [items] = await Promise.all([fetchBrowserProfiles(), loadGroups()])
      if (request === profileRequestRef.current) syncProfiles(items, syncRuntimeState)
      return items
    } finally {
      if (silent) {
        silentRefreshInFlightRef.current = false
      } else {
        setLoading(false)
      }
    }
  }

  const loadGroups = async () => {
    const request = ++groupRequestRef.current
    try {
      const items = await fetchGroups()
      if (request !== groupRequestRef.current) return
      setGroups(items); setGroupsLoaded(true); setGroupsError('')
    } catch (error) {
      if (request === groupRequestRef.current) setGroupsError(errorMessage(error, '加载实例分组失败'))
    }
  }

  const updateProxiesState = (items: BrowserProxy[]) => {
    setProxies(items)
  }

  const loadProxyCatalog = async () => {
    const [proxyResult, virtualGroupResult] = await Promise.allSettled([
      fetchBrowserProxies(),
      fetchBrowserProxyVirtualGroups(),
    ])
    if (proxyResult.status === 'fulfilled') {
      setProxies(proxyResult.value)
    } else {
      setProxies([])
      toast.error(errorMessage(proxyResult.reason, '加载代理目录失败'))
    }
    if (virtualGroupResult.status === 'fulfilled') {
      setProxyVirtualGroups(virtualGroupResult.value)
    } else {
      setProxyVirtualGroups([])
    }
  }

  useEffect(() => {
    let disposed = false
    let backendReady = false
    let initialLoadInFlight = false
    let initialLoadQueued = false
    let runtimeRetryTimer: number | null = null
    let runtimeSubscriptionFailed = false
    const runtimeUnsubscribers: Array<() => void> = []

    const loadInitialData = () => {
      if (disposed) return
      if (initialLoadInFlight) {
        initialLoadQueued = true
        return
      }

      initialLoadQueued = false
      initialLoadInFlight = true
      void waitForAppReady().then(() => {
        if (disposed) return
        backendReady = true
        return Promise.allSettled([
          loadProfiles(),
          loadProxyCatalog(),
          loadCores(),
        ])
      }).catch((error) => {
        if (disposed) return
        setLoading(false)
        toast.error(errorMessage(error, '等待应用初始化失败'))
      }).finally(() => {
        initialLoadInFlight = false
        if (!disposed && initialLoadQueued) {
          initialLoadQueued = false
          loadInitialData()
        }
      })
    }

    const clearPending = (payload: any) => {
      const profileId = typeof payload === 'string' ? payload : payload?.profileId
      if (profileId) {
        updatePendingIds(setStartingIds, profileId, false)
        updatePendingIds(setStoppingIds, profileId, false)
      }
    }

    loadInitialData()

    const subscribeRuntimeEvents = () => {
      if (disposed) return
      const subscriptions: Array<() => void> = []
      try {
        const subscribe = (eventName: string, handler: (...data: any[]) => void) => {
          const unsubscribe = EventsOn(eventName, handler)
          if (typeof unsubscribe !== 'function') {
            throw new Error('runtime event subscription unavailable')
          }
          subscriptions.push(unsubscribe)
        }

        subscribe('browser:instance:started', (payload: any) => {
          clearPending(payload)
          void loadProfiles({ silent: true, syncRuntimeState: true })
        })
        subscribe('browser:instance:updated', (payload: any) => {
          clearPending(payload)
          void loadProfiles({ silent: true, syncRuntimeState: true })
        })
        subscribe('browser:instance:stopped', (payload: any) => {
          clearPending(payload)
          void loadProfiles({ silent: true, syncRuntimeState: true })
        })
        subscribe('browser:instance:crashed', (payload: any) => {
          clearPending(payload)
          void loadProfiles({ silent: true, syncRuntimeState: true })
        })
        runtimeUnsubscribers.push(...subscriptions)
        if (runtimeSubscriptionFailed) {
          runtimeSubscriptionFailed = false
          loadInitialData()
        }
      } catch {
        subscriptions.forEach(unsubscribe => unsubscribe())
        runtimeSubscriptionFailed = true
        if (runtimeRetryTimer === null) {
          runtimeRetryTimer = window.setTimeout(() => {
            runtimeRetryTimer = null
            subscribeRuntimeEvents()
          }, 250)
        }
      }
    }

    subscribeRuntimeEvents()

    const timer = window.setInterval(() => {
      if (!backendReady || document.visibilityState !== 'visible') return
      void loadProfiles({ silent: true, syncRuntimeState: true })
    }, PROFILE_LIST_FALLBACK_REFRESH_MS)

    return () => {
      disposed = true
      window.clearInterval(timer)
      if (runtimeRetryTimer !== null) {
        window.clearTimeout(runtimeRetryTimer)
      }
      runtimeUnsubscribers.forEach(unsubscribe => unsubscribe())
    }
  }, [])

  return {
    profiles,
    loading,
    proxies,
    proxyVirtualGroups,
    groups,
    groupsLoaded,
    groupsError,
    startingIds,
    stoppingIds,
    setStartingIds,
    setStoppingIds,
    updatePendingIds,
    updateProfilesState,
    mergeProfileState,
    updateProxiesState,
    loadProfiles,
  }
}
