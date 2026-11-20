import type { BrowserGroup, BrowserProfile } from '../types'

type GroupProfile = Pick<BrowserProfile, 'groupId' | 'groupIds'>

export function profileGroupIds(profile: GroupProfile): string[] {
  if (Object.prototype.hasOwnProperty.call(profile, 'groupIds')) {
    return Array.isArray(profile.groupIds) ? [...new Set(profile.groupIds.filter(id => typeof id === 'string').map(id => id.trim()).filter(Boolean))] : []
  }
  return profile.groupId?.trim() ? [profile.groupId.trim()] : []
}

export function groupPath(groupId: string, groups: BrowserGroup[]): string {
  const byId = new Map(groups.map(group => [group.groupId, group]))
  const seen = new Set<string>(), parts: string[] = []
  for (let id = groupId; id && !seen.has(id);) {
    seen.add(id)
    const group = byId.get(id)
    if (!group) { parts.unshift('已失效分组'); break }
    parts.unshift(group.groupName)
    id = group.parentId
  }
  return parts.join(' / ')
}

export function descendantGroupIds(groupId: string, groups: BrowserGroup[]): Set<string> {
  const result = new Set<string>()
  if (!groupId) return result
  const children = new Map<string, string[]>()
  for (const group of groups) children.set(group.parentId || '', [...(children.get(group.parentId || '') || []), group.groupId])
  const pending = [groupId]
  while (pending.length) {
    const id = pending.pop()!
    if (result.has(id)) continue
    result.add(id)
    pending.push(...(children.get(id) || []))
  }
  return result
}

export function profileMatchesGroup(profile: GroupProfile, groupId: string, groups: BrowserGroup[]): boolean {
  if (!groupId || groupId === 'all' || groupId === '__all__') return true
  const ids = profileGroupIds(profile)
  if (groupId === 'ungrouped' || groupId === '__ungrouped__') return ids.length === 0
  const subtree = descendantGroupIds(groupId, groups)
  return ids.some(id => subtree.has(id))
}

export function groupInstanceCounts(profiles: BrowserProfile[], groups: BrowserGroup[]): Record<string, number> {
  const result: Record<string, number> = {}
  for (const group of groups) {
    const subtree = descendantGroupIds(group.groupId, groups), ids = new Set<string>()
    for (const profile of profiles) if (!profile.deletedAt && profileGroupIds(profile).some(id => subtree.has(id))) ids.add(profile.profileId)
    result[group.groupId] = ids.size
  }
  return result
}

export function migrateGroupFilter(raw: unknown, groups: BrowserGroup[]): { groupId: string; groupIds: string[] } {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return { groupId: '', groupIds: [] }
  const input = raw as Record<string, unknown>
  const groupId = typeof input.groupId === 'string' ? input.groupId : ''
  const ids = Array.isArray(input.groupIds) ? input.groupIds.filter((id): id is string => typeof id === 'string' && !!id.trim()) : []
  const tags = !Object.prototype.hasOwnProperty.call(input, 'groupIds') && Array.isArray(input.tags) ? input.tags.filter((tag): tag is string => typeof tag === 'string' && !!tag.trim()) : []
  for (const tag of tags) {
    const matches = groups.filter(group => !group.parentId && group.groupName === tag)
    ids.push(matches.length === 1 ? matches[0].groupId : `legacy-group:${encodeURIComponent(tag)}`)
  }
  return { groupId, groupIds: [...new Set(ids)] }
}
