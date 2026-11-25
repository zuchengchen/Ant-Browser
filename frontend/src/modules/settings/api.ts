import { waitForAppReady } from '../../utils/appReady'
import { logLevels, type LogLevel } from './types'
import { GetLogLevel, SaveLogLevel } from '../../wailsjs/go/main/App'

const getBindings = async () => {
  try {
    return await import('../../wailsjs/go/main/App')
  } catch {
    return null
  }
}

export interface AutomationSettings {
  enabled: boolean
  installPolicy: string
  runtimeVersion: string
  headlessDefault: boolean
  keepRuntimeOnDisable: boolean
  allowTypeScriptBuild: boolean
  artifactsDir: string
  nodeSource: string
  systemNodePath: string
  nodeVersion: string
  playwrightVersion: string
}

export interface AutomationRuntimeStatus {
  installed: boolean
  ready: boolean
  installing: boolean
  lastError: string
  runtimeDir: string
  nodePath: string
  nodeSource: string
  nodeResolution: string
  systemNodeDetected: boolean
  systemNodePath: string
  systemNodeError: string
  nodeVersion: string
  playwrightVersion: string
}

export interface AutomationState {
  settings: AutomationSettings
  status: AutomationRuntimeStatus
}

export type AutomationNodeSource = 'auto' | 'system' | 'bundled'

export interface AutomationRuntimeCheck {
  ok: boolean
  nodeSource: string
  nodeVersion: string
  playwrightVersion: string
}

export interface AutomationSystemNodeProbe {
  ok: boolean
  path: string
  version: string
}

export interface LaunchServerSettings {
  host: string
  port: number
  preferredPort: number
  baseUrl: string
  ready: boolean
}

export interface KeyPouchSettings {
  siteUrl: string
  fallbackUrl: string
}

export async function fetchKeyPouchSettings(): Promise<KeyPouchSettings> {
  const bindings: any = await getBindings()
  if (!bindings?.GetKeyPouchSettings) throw new Error('当前环境不支持 KeyPouch 站点设置')
  return bindings.GetKeyPouchSettings()
}

export async function saveKeyPouchSettings(siteUrl: string): Promise<KeyPouchSettings> {
  const bindings: any = await getBindings()
  if (!bindings?.SaveKeyPouchSettings) throw new Error('当前环境不支持 KeyPouch 站点设置')
  return bindings.SaveKeyPouchSettings(siteUrl)
}

export interface MofaSettings {
  siteUrl: string
  username: string
  passwordConfigured: boolean
  totpConfigured: boolean
}

function parseMofaSettings(value: Record<string, string>): MofaSettings {
  return {
    siteUrl: value.siteUrl || '',
    username: value.username || '',
    passwordConfigured: value.passwordConfigured === 'true',
    totpConfigured: value.totpConfigured === 'true',
  }
}

export async function fetchMofaSettings(): Promise<MofaSettings> {
  const bindings = await getBindings()
  if (!bindings?.GetMofaSettings || !(globalThis as any).go?.main?.App?.GetMofaSettings) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中配置 MOFA 导入')
  }
  return parseMofaSettings(await bindings.GetMofaSettings())
}

export async function saveMofaSettings(input: Record<string, string>): Promise<MofaSettings> {
  const bindings = await getBindings()
  if (!bindings?.SaveMofaSettings || !(globalThis as any).go?.main?.App?.SaveMofaSettings) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中配置 MOFA 导入')
  }
  return parseMofaSettings(await bindings.SaveMofaSettings(input))
}

export const defaultAutomationState: AutomationState = {
  settings: {
    enabled: false,
    installPolicy: 'on_demand',
    runtimeVersion: 'node-22.15.1-playwright-core-1.59.0',
    headlessDefault: false,
    keepRuntimeOnDisable: true,
    allowTypeScriptBuild: false,
    artifactsDir: 'data/automation/artifacts',
    nodeSource: 'auto',
    systemNodePath: '',
    nodeVersion: '22.15.1',
    playwrightVersion: '1.59.0',
  },
  status: {
    installed: false,
    ready: false,
    installing: false,
    lastError: '',
    runtimeDir: '',
    nodePath: '',
    nodeSource: 'auto',
    nodeResolution: '',
    systemNodeDetected: false,
    systemNodePath: '',
    systemNodeError: '',
    nodeVersion: '22.15.1',
    playwrightVersion: '1.59.0',
  },
}

async function waitForLogLevelBindings() {
  const app = (globalThis as { go?: { main?: { App?: Record<string, unknown> } } }).go?.main?.App
  if (typeof app?.GetLogLevel !== 'function' || typeof app.SaveLogLevel !== 'function') {
    throw new Error('请在更新后的 Ant Browser 桌面应用中设置日志级别')
  }
  await waitForAppReady()
}

export async function fetchLogLevel(): Promise<LogLevel> {
  await waitForLogLevelBindings()
  const current = (await GetLogLevel()).trim().toLowerCase()
  const level = logLevels.find(value => value === current)
  if (!level) throw new Error('应用返回了无法识别的日志级别')
  return level
}

export async function saveLogLevel(level: LogLevel): Promise<void> {
  await waitForLogLevelBindings()
  await SaveLogLevel(level)
}

// 重置设置
export async function resetSettings(): Promise<void> {
  const bindings: any = await getBindings()
  if (!bindings?.ResetManagedSettings) {
    throw new Error('当前环境不支持重置所有设置')
  }
  await bindings.ResetManagedSettings()
}


export async function fetchAutomationState(): Promise<AutomationState> {
  const bindings: any = await getBindings()
  if (!bindings?.GetAutomationState) {
    return defaultAutomationState
  }
  const raw = (await bindings.GetAutomationState()) || {}
  return {
    settings: {
      ...defaultAutomationState.settings,
      ...(raw.settings || {}),
    },
    status: {
      ...defaultAutomationState.status,
      ...(raw.status || {}),
    },
  }
}

export async function saveAutomationSettings(enabled: boolean, headlessDefault: boolean): Promise<AutomationState> {
  const bindings: any = await getBindings()
  if (!bindings?.SaveAutomationSettings) {
    return {
      ...defaultAutomationState,
      settings: {
        ...defaultAutomationState.settings,
        enabled,
        headlessDefault,
      },
    }
  }
  const raw = (await bindings.SaveAutomationSettings(enabled, headlessDefault)) || {}
  return {
    settings: {
      ...defaultAutomationState.settings,
      ...(raw.settings || {}),
    },
    status: {
      ...defaultAutomationState.status,
      ...(raw.status || {}),
    },
  }
}

export async function saveAutomationRuntimeSettings(
  nodeSource: AutomationNodeSource | string,
  systemNodePath: string
): Promise<AutomationState> {
  const bindings: any = await getBindings()
  if (!bindings?.SaveAutomationRuntimeSettings) {
    return {
      ...defaultAutomationState,
      settings: {
        ...defaultAutomationState.settings,
        nodeSource: String(nodeSource || defaultAutomationState.settings.nodeSource),
        systemNodePath: String(systemNodePath || '').trim(),
      },
    }
  }
  const raw = (await bindings.SaveAutomationRuntimeSettings(nodeSource, systemNodePath)) || {}
  return {
    settings: {
      ...defaultAutomationState.settings,
      ...(raw.settings || {}),
    },
    status: {
      ...defaultAutomationState.status,
      ...(raw.status || {}),
    },
  }
}

export async function saveAutomationScriptPackageSettings(
  allowTypeScriptBuild: boolean
): Promise<AutomationState> {
  const bindings: any = await getBindings()
  if (!bindings?.SaveAutomationScriptPackageSettings) {
    return {
      ...defaultAutomationState,
      settings: {
        ...defaultAutomationState.settings,
        allowTypeScriptBuild,
      },
    }
  }
  const raw = (await bindings.SaveAutomationScriptPackageSettings(allowTypeScriptBuild)) || {}
  return {
    settings: {
      ...defaultAutomationState.settings,
      ...(raw.settings || {}),
    },
    status: {
      ...defaultAutomationState.status,
      ...(raw.status || {}),
    },
  }
}

export async function installAutomationRuntime(): Promise<AutomationState> {
  const bindings: any = await getBindings()
  if (!bindings?.InstallAutomationRuntime) {
    return defaultAutomationState
  }
  const raw = (await bindings.InstallAutomationRuntime()) || {}
  return {
    settings: {
      ...defaultAutomationState.settings,
      ...(raw.settings || {}),
    },
    status: {
      ...defaultAutomationState.status,
      ...(raw.status || {}),
    },
  }
}

export async function automationProbeSystemNode(systemNodePath: string): Promise<AutomationSystemNodeProbe> {
  const bindings: any = await getBindings()
  if (!bindings?.AutomationProbeSystemNode) {
    return { ok: false, path: '', version: '' }
  }
  return (await bindings.AutomationProbeSystemNode(systemNodePath)) || { ok: false, path: '', version: '' }
}

export async function automationRuntimeSelfCheck(): Promise<AutomationRuntimeCheck> {
  const bindings: any = await getBindings()
  if (!bindings?.AutomationRuntimeSelfCheck) {
    return { ok: false, nodeSource: '', nodeVersion: '', playwrightVersion: '' }
  }
  return (await bindings.AutomationRuntimeSelfCheck()) || { ok: false, nodeSource: '', nodeVersion: '', playwrightVersion: '' }
}

function normalizeLaunchServerSettings(payload: any): LaunchServerSettings {
  const host = String(payload?.host || '127.0.0.1')
  const port = Number(payload?.port) || 0
  const preferredPort = Number(payload?.preferredPort) || port || 19876
  const effectivePort = port > 0 ? port : preferredPort
  return {
    host,
    port: effectivePort,
    preferredPort,
    baseUrl: String(payload?.baseUrl || (effectivePort > 0 ? `http://${host}:${effectivePort}` : '')),
    ready: !!payload?.ready && port > 0,
  }
}

export async function fetchLaunchServerSettings(): Promise<LaunchServerSettings> {
  const bindings: any = await getBindings()
  if (!bindings?.GetLaunchServerInfo) {
    return normalizeLaunchServerSettings(null)
  }
  return normalizeLaunchServerSettings(await bindings.GetLaunchServerInfo())
}

export async function saveLaunchServerSettings(port: number): Promise<LaunchServerSettings> {
  const bindings: any = await getBindings()
  if (!bindings?.SaveLaunchServerSettings) {
    return normalizeLaunchServerSettings({ port, preferredPort: port, ready: false })
  }
  return normalizeLaunchServerSettings(await bindings.SaveLaunchServerSettings(port))
}
