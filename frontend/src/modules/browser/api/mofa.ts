import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { getBindings, getGoApp } from './runtime'

const importStatuses = ['skipped', 'imported', 'reauthorized', 'group_added'] as const
const importPhases = ['checking', 'waiting_for_oauth', 'login_required', 'saving'] as const

export type MofaImportPhase = typeof importPhases[number]

export interface MofaImportResult {
  status: typeof importStatuses[number]
  accountId: number
  groupId: number
  schedulable: boolean | null
}

function subscribeToImportProgress(profileId: string, onProgress: (phase: MofaImportPhase) => void) {
  try {
    const unsubscribe = EventsOn('browser:mofa-import:progress', (event: { profileId?: string; phase?: string } | undefined) => {
      if (event?.profileId !== profileId) return
      const phase = importPhases.find(candidate => candidate === event.phase)
      if (phase) onProgress(phase)
    })
    if (typeof unsubscribe === 'function') return unsubscribe
  } catch {
    // Import stays available when the desktop event runtime is unavailable.
  }
}

export async function importMofaAccount(profileId: string, onProgress: (phase: MofaImportPhase) => void): Promise<MofaImportResult> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImport || !getGoApp()?.BrowserMofaImport) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中导入到 MOFA')
  }
  const unsubscribe = subscribeToImportProgress(profileId, onProgress)
  try {
    const result = await bindings.BrowserMofaImport(profileId)
    const status = importStatuses.find(candidate => candidate === result?.status)
    const accountId = result?.accountId
    const groupId = result?.groupId
    if (!status || typeof accountId !== 'number' || !Number.isSafeInteger(accountId) || accountId <= 0 ||
        typeof groupId !== 'number' || !Number.isSafeInteger(groupId) || groupId <= 0) {
      throw new Error('无法识别 MOFA 导入结果，请重试。')
    }
    const schedulable = typeof result.schedulable === 'boolean' ? result.schedulable : null
    return { status, accountId, groupId, schedulable }
  } finally {
    unsubscribe?.()
  }
}

export async function cancelMofaImport(profileId: string): Promise<void> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImportCancel || !getGoApp()?.BrowserMofaImportCancel) {
    throw new Error('当前桌面应用无法取消 MOFA 导入')
  }
  await bindings.BrowserMofaImportCancel(profileId)
}

export async function showMofaImportAuthorization(profileId: string): Promise<void> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImportShowAuthorization || !getGoApp()?.BrowserMofaImportShowAuthorization) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中切到授权页')
  }
  await bindings.BrowserMofaImportShowAuthorization(profileId)
}
