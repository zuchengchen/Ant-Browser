import { useEffect, useState, type RefObject } from 'react'
import { errorMessage } from '../../../utils/errors'
import { toast } from '../../../shared/components'
import { fetchBrowserProfiles, fetchGroups } from '../api'
import type { BrowserGroupWithCount, BrowserProfile } from '../types'
import { sortProfiles } from './QuickLaunchModal.helpers'

export function useQuickLaunchData(open: boolean, inputRef: RefObject<HTMLInputElement | null>) {
  const [profiles, setProfiles] = useState<BrowserProfile[]>([])
  const [groups, setGroups] = useState<BrowserGroupWithCount[]>([])
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return

    let alive = true
    setLoading(true)

    Promise.allSettled([fetchBrowserProfiles(), fetchGroups()])
      .then(([profilesResult, groupsResult]) => {
        if (!alive) return

        if (profilesResult.status === 'fulfilled') {
          setProfiles((profilesResult.value || []).slice().sort(sortProfiles))
        } else {
          toast.error(errorMessage(profilesResult.reason, '加载实例列表失败'))
          setProfiles([])
        }

        if (groupsResult.status === 'rejected') toast.error(errorMessage(groupsResult.reason, '加载实例分组失败'))
        setGroups(groupsResult.status === 'fulfilled' ? groupsResult.value || [] : [])
      })
      .finally(() => {
        if (!alive) return
        setLoading(false)
        setTimeout(() => inputRef.current?.focus(), 0)
      })

    return () => {
      alive = false
    }
  }, [inputRef, open])

  return { profiles, groups, loading }
}
