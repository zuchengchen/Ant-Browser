import type { AppUpdateCheckResult } from './types'

const getBindings = async () => {
  try {
    return await import('../../wailsjs/go/main/App')
  } catch {
    return null
  }
}

export async function loadCurrentAppVersion(): Promise<string> {
  const bindings: any = await getBindings()
  const goApp = (window as any).go?.main?.App
  if (bindings?.GetAppConfig) {
    const config = await bindings.GetAppConfig()
    return typeof config?.version === 'string' ? config.version.trim() : ''
  }
  if (goApp?.GetAppConfig) {
    const config = await goApp.GetAppConfig()
    return typeof config?.version === 'string' ? config.version.trim() : ''
  }
  return ''
}

export async function checkForUpdates(): Promise<AppUpdateCheckResult> {
  const fallback: AppUpdateCheckResult = {
    ok: false,
    currentVersion: '',
    latestVersion: '',
    updateAvailable: false,
    releaseUrl: '',
    error: '当前环境不支持更新检查',
  }
  const bindings: any = await getBindings()
  const goApp = (window as any).go?.main?.App
  let rawResult: any
  if (bindings?.CheckForUpdates) {
    rawResult = await bindings.CheckForUpdates()
  } else if (goApp?.CheckForUpdates) {
    rawResult = await goApp.CheckForUpdates()
  } else {
    return fallback
  }

  return {
    ok: rawResult?.ok === true,
    currentVersion: typeof rawResult?.currentVersion === 'string' ? rawResult.currentVersion.trim() : '',
    latestVersion: typeof rawResult?.latestVersion === 'string' ? rawResult.latestVersion.trim() : '',
    updateAvailable: rawResult?.updateAvailable === true,
    releaseUrl: typeof rawResult?.releaseUrl === 'string' ? rawResult.releaseUrl.trim() : '',
    error: typeof rawResult?.error === 'string' ? rawResult.error.trim() : '',
  }
}
