import type { BrowserProfile } from '../types'

export const GROUP_ALL = '__all__'
export const GROUP_UNGROUPED = '__ungrouped__'

export function normalizeText(v?: string): string {
  return (v || '').trim().toLowerCase()
}

export function normalizeCode(v?: string): string {
  return normalizeText(v).toUpperCase()
}

export function buildSearchText(profile: BrowserProfile): string {
  return [
    profile.profileName,
    profile.launchCode || '',
    ...(profile.tags || []),
    ...(profile.keywords || []),
  ]
    .join(' ')
    .toLowerCase()
}

export function sortProfiles(a: BrowserProfile, b: BrowserProfile): number {
  if (a.running !== b.running) {
    return a.running ? -1 : 1
  }
  return a.profileName.localeCompare(b.profileName, 'zh-CN')
}
