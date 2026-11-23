import { getBindings, getGoApp } from './runtime'

export async function importMofaAccount(profileId: string): Promise<string> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImport || !getGoApp()?.BrowserMofaImport) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中导入到 MOFA')
  }
  const result = await bindings.BrowserMofaImport(profileId)
  return typeof result?.status === 'string' ? result.status : ''
}

export async function cancelMofaImport(profileId: string): Promise<void> {
  const bindings = await getBindings()
  if (!bindings?.BrowserMofaImportCancel || !getGoApp()?.BrowserMofaImportCancel) {
    throw new Error('当前桌面应用无法取消 MOFA 导入')
  }
  await bindings.BrowserMofaImportCancel(profileId)
}
