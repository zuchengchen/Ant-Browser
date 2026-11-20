import type { ProfileIconKey } from '../../config/profile.config'

export type IconKey = ProfileIconKey

export interface AppUpdateCheckResult {
  ok: boolean
  currentVersion: string
  latestVersion: string
  updateAvailable: boolean
  releaseUrl: string
  error: string
}
