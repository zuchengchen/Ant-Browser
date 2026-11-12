import type { BrowserProfile } from '../../types'

// API profiles contain scalar fields and ordered arrays. Compare every own
// enumerable field, including future additions; unfamiliar nested values must
// retain the incoming snapshot rather than accidentally suppress an update.
function sameProfile(previous: BrowserProfile, incoming: BrowserProfile): boolean {
  if (previous === incoming) return true
  const before = previous as unknown as Record<string, unknown>
  const after = incoming as unknown as Record<string, unknown>
  const keys = Object.keys(after)
  if (Object.keys(before).length !== keys.length) return false
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(before, key)) return false
    const left = before[key], right = after[key]
    if (Object.is(left, right)) continue
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false
    for (let index = 0; index < left.length; index += 1) {
      if (!Object.is(left[index], right[index])) return false
    }
  }
  return true
}

export function reuseProfileSnapshot(previous: BrowserProfile[], incoming: BrowserProfile[]): BrowserProfile[] {
  if (previous === incoming) return previous
  let next: BrowserProfile[] | undefined
  for (let index = 0; index < incoming.length; index += 1) {
    const profile = incoming[index]
    const before = previous[index]
    if (before && sameProfile(before, profile)) {
      next?.push(before)
    } else {
      next ??= previous.slice(0, index)
      next.push(profile)
    }
  }
  // Position-based matching preserves ordering and duplicate IDs exactly.
  return next ?? (previous.length === incoming.length ? previous : previous.slice(0, incoming.length))
}
