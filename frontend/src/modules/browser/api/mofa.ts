import { getBindings, getGoApp } from './runtime'

const importStatuses = ['skipped', 'reauthorization_required', 'creation_required', 'group_required'] as const

export interface MofaImportResult {
  status: typeof importStatuses[number]
  accountId: number
  groupId: number
  searchHint: string
  actionOpened: boolean
  inTargetGroup: boolean
}

export async function prepareMofaImport(profileId: string): Promise<MofaImportResult> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImport || !getGoApp()?.BrowserMofaImport) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中导入到 MOFA')
  }
  const result = await bindings.BrowserMofaImport(profileId)
  const status = importStatuses.find(candidate => candidate === result?.status)
  const accountId = result?.accountId
  const groupId = result?.groupId
  if (!status || typeof accountId !== 'number' || !Number.isSafeInteger(accountId) ||
      (status === 'creation_required' ? accountId !== 0 : accountId <= 0) ||
      typeof groupId !== 'number' || !Number.isSafeInteger(groupId) || groupId <= 0) {
    throw new Error('无法识别 MOFA 核对结果，请重试。')
  }
  return {
    status,
    accountId,
    groupId,
    searchHint: typeof result.searchHint === 'string' ? result.searchHint.trim() : '',
    actionOpened: result.actionOpened === true,
    inTargetGroup: result.inTargetGroup === true,
  }
}

export async function cancelMofaImport(profileId: string): Promise<void> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImportCancel || !getGoApp()?.BrowserMofaImportCancel) {
    throw new Error('当前桌面应用无法取消 MOFA 导入')
  }
  await bindings.BrowserMofaImportCancel(profileId)
}
