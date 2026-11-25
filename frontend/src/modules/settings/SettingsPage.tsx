import { useEffect, useRef, useState } from 'react'
import { RotateCcw, Save } from 'lucide-react'

import { Button, Card, ThemeSwitcher, toast } from '../../shared/components'
import { useTheme } from '../../shared/theme'
import {
  isNotificationHistoryEnabled,
  resetNotificationHistory,
  saveNotificationHistory,
} from '../../store/notificationStore'
import { errorMessage } from '../../utils/errors'
import {
  automationProbeSystemNode,
  automationRuntimeSelfCheck,
  defaultAutomationState,
  fetchAutomationState,
  fetchKeyPouchSettings,
  fetchLaunchServerSettings,
  fetchLogLevel,
  fetchMofaSettings,
  installAutomationRuntime,
  resetSettings,
  saveAutomationRuntimeSettings,
  saveAutomationScriptPackageSettings,
  saveAutomationSettings,
  saveKeyPouchSettings,
  saveLaunchServerSettings,
  saveLogLevel,
  saveMofaSettings,
} from './api'
import type { AutomationNodeSource, AutomationRuntimeCheck, AutomationState, AutomationSystemNodeProbe, KeyPouchSettings, MofaSettings } from './api'
import { AutomationSettingsCard } from './components/AutomationSettingsCard'
import type { MofaSettingsDraft } from './components/MofaSettingsFields'
import { useSettingsProgressEffects } from './hooks/useSettingsProgressEffects'
import { SettingsBasicCard, SettingsDiagnosticsCard } from './components/SettingsGeneralCards'
import type { LogLevel } from './types'
import type { AutomationRuntimeProgress } from './progress'

type AutomationBusyState = 'none' | 'toggle' | 'probe' | 'runtime' | 'package' | 'install' | 'check'

function mofaSettingsDraft(settings: MofaSettings | null): MofaSettingsDraft {
  return {
    siteUrl: settings?.siteUrl ?? '',
    username: settings?.username ?? '',
    password: '',
    totpSecret: '',
    clearCredentials: false,
  }
}

function mofaOrigin(siteUrl: string) {
  try {
    return new URL(siteUrl.trim()).origin
  } catch {
    return ''
  }
}

export function SettingsPage() {
  const { setTheme } = useTheme()
  const [enableNotifications, setEnableNotifications] = useState(isNotificationHistoryEnabled)
  const [savedEnableNotifications, setSavedEnableNotifications] = useState(enableNotifications)
  const [logLevel, setLogLevel] = useState<LogLevel | null>(null)
  const [savedLogLevel, setSavedLogLevel] = useState<LogLevel | null>(null)
  const [logLevelError, setLogLevelError] = useState('')
  const logLevelRequest = useRef(0)
  const [keyPouch, setKeyPouch] = useState<KeyPouchSettings | null | undefined>(undefined)
  const [siteUrlDraft, setSiteUrlDraft] = useState('')
  const [siteError, setSiteError] = useState('')
  const [mofaSettings, setMofaSettings] = useState<MofaSettings | null | undefined>(undefined)
  const [mofaDraft, setMofaDraft] = useState(() => mofaSettingsDraft(null))
  const [mofaError, setMofaError] = useState('')
  const [automationState, setAutomationState] = useState<AutomationState>(defaultAutomationState)
  const [automationProgress, setAutomationProgress] = useState<AutomationRuntimeProgress | null>(null)
  const [automationBusy, setAutomationBusy] = useState<AutomationBusyState>('none')
  const [automationCheck, setAutomationCheck] = useState<AutomationRuntimeCheck | null>(null)
  const [automationProbe, setAutomationProbe] = useState<AutomationSystemNodeProbe | null>(null)
  const [automationNodeSourceDraft, setAutomationNodeSourceDraft] = useState<AutomationNodeSource>('auto')
  const [automationSystemNodePathDraft, setAutomationSystemNodePathDraft] = useState('')
  const [automationRuntimeDirty, setAutomationRuntimeDirty] = useState(false)
  const [launchServerPortDraft, setLaunchServerPortDraft] = useState('19876')
  const [launchServerBaseUrl, setLaunchServerBaseUrl] = useState('')
  const [launchServerReady, setLaunchServerReady] = useState(false)
  const [launchServerSaving, setLaunchServerSaving] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const notificationHistoryChanged = enableNotifications !== savedEnableNotifications
  const logLevelChanged = logLevel !== null && logLevel !== savedLogLevel
  const siteChanged = keyPouch !== undefined && siteUrlDraft.trim() !== (keyPouch?.siteUrl ?? '')
  const canReuseMofaCredentials = !!mofaSettings?.siteUrl && mofaOrigin(mofaDraft.siteUrl) === mofaSettings.siteUrl &&
    mofaDraft.username.trim() === mofaSettings.username
  const mofaChanged = mofaSettings !== undefined && (
    mofaDraft.siteUrl.trim() !== (mofaSettings?.siteUrl ?? '') ||
    mofaDraft.username.trim() !== (mofaSettings?.username ?? '') ||
    Boolean(mofaDraft.password || mofaDraft.totpSecret || mofaDraft.clearCredentials)
  )

  useEffect(() => {
    void loadSettings()
    void loadLogLevel()
  }, [])

  useSettingsProgressEffects({
    setAutomationProgress,
    setAutomationState,
  })

  useEffect(() => {
    setAutomationNodeSourceDraft((automationState.settings.nodeSource || 'auto') as AutomationNodeSource)
    setAutomationSystemNodePathDraft(automationState.settings.systemNodePath || '')
    setAutomationProbe(null)
    setAutomationRuntimeDirty(false)
  }, [automationState.settings.nodeSource, automationState.settings.systemNodePath])

  const loadKeyPouch = async () => {
    setKeyPouch(undefined)
    try {
      const result = await fetchKeyPouchSettings()
      setSiteUrlDraft(result.siteUrl)
      setKeyPouch(result)
      setSiteError('')
    } catch (error) {
      setKeyPouch(null)
      setSiteUrlDraft('')
      setSiteError(`${errorMessage(error, '读取 KeyPouch 站点地址失败')}。可填写新的 HTTPS 地址后保存。`)
    }
  }

  const loadMofaSettings = async () => {
    setMofaSettings(undefined)
    try {
      const result = await fetchMofaSettings()
      setMofaDraft(mofaSettingsDraft(result))
      setMofaSettings(result)
      setMofaError('')
    } catch (error) {
      setMofaSettings(null)
      setMofaDraft(mofaSettingsDraft(null))
      setMofaError(errorMessage(error, '读取 MOFA 设置失败'))
    }
  }

  const loadLogLevel = async () => {
    const request = ++logLevelRequest.current
    setLogLevel(null)
    setLogLevelError('')
    try {
      const current = await fetchLogLevel()
      if (request !== logLevelRequest.current) return
      setLogLevel(current)
      setSavedLogLevel(current)
    } catch (error) {
      if (request === logLevelRequest.current) {
        setLogLevelError(errorMessage(error, '读取日志级别失败'))
      }
    }
  }

  const loadSettings = async () => {
    setLoading(true)
    try {
      const [automation, launchServer] = await Promise.all([
        fetchAutomationState(),
        fetchLaunchServerSettings(),
        loadKeyPouch(),
        loadMofaSettings(),
      ])
      setAutomationState(automation)
      setLaunchServerPortDraft(String(launchServer.preferredPort || launchServer.port || 19876))
      setLaunchServerBaseUrl(launchServer.baseUrl)
      setLaunchServerReady(launchServer.ready)
    } catch (error) {
      toast.error(errorMessage(error, '系统设置加载失败'))
    } finally {
      setLoading(false)
    }
  }

  const handleMofaChange = (update: Partial<MofaSettingsDraft>) => {
    setMofaDraft(previous => {
      const next = { ...previous, ...update }
      if (next.clearCredentials || mofaOrigin(next.siteUrl) !== mofaOrigin(previous.siteUrl) || next.username.trim() !== previous.username.trim()) {
        next.password = ''
        next.totpSecret = ''
      }
      return next
    })
    setMofaError('')
  }

  const handleSave = async () => {
    const mofaSiteUrl = mofaDraft.siteUrl.trim()
    const mofaUsername = mofaDraft.username.trim()
    const clearMofaCredentials = !mofaSiteUrl || mofaDraft.clearCredentials
    if (mofaChanged && !clearMofaCredentials) {
      let error = ''
      if (!mofaUsername && (mofaSettings?.username || mofaDraft.password || mofaDraft.totpSecret.trim())) {
        error = '请输入 MOFA 管理员用户名，或选择清除管理员凭据。'
      } else if (mofaUsername && !mofaDraft.password && !(canReuseMofaCredentials && mofaSettings?.passwordConfigured)) {
        error = '请输入当前 MOFA 管理员的密码。'
      }
      if (error) {
        setMofaError(error)
        toast.error(error)
        return
      }
    }

    setSaving(true)
    try {
      if (siteChanged) {
        try {
          const result = await saveKeyPouchSettings(siteUrlDraft.trim())
          setKeyPouch(result)
          setSiteUrlDraft(result.siteUrl)
          setSiteError('')
        } catch (error) {
          const message = errorMessage(error, 'KeyPouch 站点地址保存失败')
          setSiteError(message)
          toast.error(message)
          return
        }
      }
      if (mofaChanged) {
        try {
          const input: Record<string, string> = { siteUrl: mofaSiteUrl, username: clearMofaCredentials ? '' : mofaUsername }
          if (clearMofaCredentials) input.clearCredentials = 'true'
          else {
            if (mofaDraft.password) input.password = mofaDraft.password
            if (mofaDraft.totpSecret.trim()) input.totpSecret = mofaDraft.totpSecret.trim()
          }
          const result = await saveMofaSettings(input)
          setMofaSettings(result)
          setMofaDraft(mofaSettingsDraft(result))
          setMofaError('')
        } catch (error) {
          const message = errorMessage(error, 'MOFA 设置保存失败')
          setMofaError(message)
          toast.error(message)
          return
        }
      }
      if (logLevelChanged) {
        setLogLevelError('')
        try {
          await saveLogLevel(logLevel)
          setSavedLogLevel(logLevel)
        } catch (error) {
          const message = errorMessage(error, '日志级别保存失败，请重试')
          setLogLevelError(message)
          toast.error(message)
          return
        }
      }
      if (notificationHistoryChanged) {
        saveNotificationHistory(enableNotifications)
        setSavedEnableNotifications(enableNotifications)
      }
      toast.success('设置已保存')
    } catch (error) {
      toast.error(errorMessage(error, '通知历史设置保存失败，请重试'))
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    if (!confirm('确定要重置所有设置吗？将重置主题、日志级别、通知历史开关、KeyPouch 地址、MOFA 地址及管理员凭据、自动化、备份渠道和 Launch Server。浏览器实例、代理和内核数据会保留。')) return

    setLoading(true)
    try {
      await resetSettings()
      let localResetFailed = false
      try {
        resetNotificationHistory()
      } catch (error) {
        toast.error(errorMessage(error, '通知历史设置重置失败'))
        localResetFailed = true
      }
      try {
        setTheme('light')
      } catch (error) {
        toast.error(errorMessage(error, '主题设置重置失败'))
        localResetFailed = true
      }
      const historyEnabled = isNotificationHistoryEnabled()
      setEnableNotifications(historyEnabled)
      setSavedEnableNotifications(historyEnabled)
      const [automation, launchServer] = await Promise.all([
        fetchAutomationState(),
        fetchLaunchServerSettings(),
        loadKeyPouch(),
        loadMofaSettings(),
        loadLogLevel(),
      ])
      setAutomationState(automation)
      setAutomationProgress(null)
      setAutomationCheck(null)
      setAutomationProbe(null)
      setAutomationNodeSourceDraft((automation.settings.nodeSource || 'auto') as AutomationNodeSource)
      setAutomationSystemNodePathDraft(automation.settings.systemNodePath || '')
      setAutomationRuntimeDirty(false)
      setLaunchServerPortDraft(String(launchServer.preferredPort || launchServer.port || 19876))
      setLaunchServerBaseUrl(launchServer.baseUrl)
      setLaunchServerReady(launchServer.ready)
      if (!localResetFailed) toast.success('设置已重置')
    } catch (error) {
      toast.error(errorMessage(error, '设置重置失败'))
    } finally {
      setLoading(false)
    }
  }

  const handleAutomationEnabledChange = async (enabled: boolean) => {
    setAutomationBusy('toggle')
    setAutomationCheck(null)
    try {
      const next = await saveAutomationSettings(enabled, automationState.settings.headlessDefault)
      setAutomationState(next)
      if (!enabled) {
        setAutomationProgress(null)
        toast.success('自动化支持已关闭')
        return
      }
      if (!next.status.ready) {
        setAutomationProgress({ phase: 'checking', progress: 0, message: '已开启自动化支持，正在准备运行时...' })
        toast.success('自动化支持已开启，正在准备运行时')
        return
      }
      toast.success('自动化支持已开启')
    } catch (error: any) {
      toast.error(error?.message || '自动化配置保存失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationHeadlessChange = async (headlessDefault: boolean) => {
    setAutomationBusy('toggle')
    try {
      const next = await saveAutomationSettings(automationState.settings.enabled, headlessDefault)
      setAutomationState(next)
      toast.success(headlessDefault ? '默认无头模式已开启' : '默认无头模式已关闭')
    } catch (error: any) {
      toast.error(error?.message || '自动化配置保存失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationRuntimeSettingsSave = async () => {
    setAutomationBusy('runtime')
    setAutomationCheck(null)
    try {
      const next = await saveAutomationRuntimeSettings(automationNodeSourceDraft, automationSystemNodePathDraft)
      setAutomationState(next)
      setAutomationRuntimeDirty(false)
      if (next.settings.enabled && next.status.installing) {
        setAutomationProgress({ phase: 'checking', progress: 0, message: '运行时策略已保存，正在重新检查自动化运行时...' })
        toast.success('运行时策略已保存，正在重新检查')
        return
      }
      toast.success('运行时策略已保存')
    } catch (error: any) {
      toast.error(error?.message || '运行时策略保存失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationTypeScriptBuildChange = async (allowTypeScriptBuild: boolean) => {
    setAutomationBusy('package')
    try {
      const next = await saveAutomationScriptPackageSettings(allowTypeScriptBuild)
      setAutomationState(next)
      toast.success(allowTypeScriptBuild ? 'TypeScript 导入构建已开启' : 'TypeScript 导入构建已关闭')
    } catch (error: any) {
      toast.error(error?.message || '脚本包配置保存失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationProbeSystemNode = async () => {
    setAutomationBusy('probe')
    try {
      const result = await automationProbeSystemNode(automationSystemNodePathDraft)
      setAutomationProbe(result)
      toast.success(`系统 Node 可用：${result.version}`)
    } catch (error: any) {
      setAutomationProbe(null)
      toast.error(error?.message || '系统 Node 检测失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationInstall = async () => {
    setAutomationBusy('install')
    try {
      const next = await installAutomationRuntime()
      setAutomationState(next)
      setAutomationProgress({ phase: 'checking', progress: 0, message: '正在准备自动化运行时...' })
      toast.success('已开始准备自动化运行时')
    } catch (error: any) {
      toast.error(error?.message || '启动自动化运行时安装失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleAutomationSelfCheck = async () => {
    setAutomationBusy('check')
    try {
      const result = await automationRuntimeSelfCheck()
      setAutomationCheck(result)
      if (result.ok) {
        toast.success(`自检通过：Node ${result.nodeVersion} / playwright-core ${result.playwrightVersion}`)
      } else {
        toast.warning('自检未通过')
      }
    } catch (error: any) {
      setAutomationCheck(null)
      toast.error(error?.message || '自动化运行时自检失败')
    } finally {
      setAutomationBusy('none')
    }
  }

  const handleLaunchServerPortSave = async () => {
    const port = Number(launchServerPortDraft)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      toast.error('端口必须在 1-65535 之间')
      return
    }

    setLaunchServerSaving(true)
    try {
      const next = await saveLaunchServerSettings(port)
      setLaunchServerPortDraft(String(next.preferredPort || next.port || port))
      setLaunchServerBaseUrl(next.baseUrl)
      setLaunchServerReady(next.ready)
      toast.success(`本地 API 端口已保存：${next.port}`)
    } catch (error: any) {
      toast.error(error?.message || '本地 API 端口保存失败')
    } finally {
      setLaunchServerSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <div className="h-6 w-6 animate-spin rounded-full border-2 border-[var(--color-border-default)] border-t-[var(--color-accent)]" />
      </div>
    )
  }

  return (
    <div className="w-full space-y-4 animate-fade-in">
      <Card padding="none" className="shadow-[var(--shadow-sm)]">
        <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
          <h1 className="text-xl font-semibold text-[var(--color-text-primary)]">系统设置</h1>
          <div className="flex flex-wrap gap-2 sm:justify-end">
            <Button variant="secondary" size="sm" onClick={handleReset} disabled={saving}>
              <RotateCcw className="h-4 w-4" />
              重置
            </Button>
            <Button variant="danger" size="sm" onClick={handleSave} loading={saving} disabled={!notificationHistoryChanged && !logLevelChanged && !siteChanged && !mofaChanged}>
              <Save className="h-4 w-4" />
              保存
            </Button>
          </div>
        </div>
      </Card>

      <Card title="主题">
        <ThemeSwitcher />
      </Card>
      <SettingsBasicCard
        siteUrl={siteUrlDraft}
        fallbackUrl={keyPouch?.fallbackUrl ?? ''}
        siteDisabled={keyPouch === undefined || saving}
        siteError={siteError}
        onSiteUrlChange={value => {
          setSiteUrlDraft(value)
          setSiteError('')
        }}
        mofa={{
          draft: mofaDraft,
          saved: mofaSettings,
          canReuseCredentials: canReuseMofaCredentials,
          disabled: mofaSettings === undefined || saving,
          error: mofaError,
          onChange: handleMofaChange,
        }}
      />
      <AutomationSettingsCard
        automationState={automationState}
        automationProgress={automationProgress}
        automationBusy={automationBusy}
        automationCheck={automationCheck}
        automationProbe={automationProbe}
        automationNodeSourceDraft={automationNodeSourceDraft}
        automationSystemNodePathDraft={automationSystemNodePathDraft}
        automationRuntimeDirty={automationRuntimeDirty}
        launchServerPortDraft={launchServerPortDraft}
        launchServerBaseUrl={launchServerBaseUrl}
        launchServerReady={launchServerReady}
        launchServerSaving={launchServerSaving}
        onEnabledChange={handleAutomationEnabledChange}
        onHeadlessChange={handleAutomationHeadlessChange}
        onNodeSourceDraftChange={(value) => {
          setAutomationNodeSourceDraft(value)
          setAutomationProbe(null)
          setAutomationRuntimeDirty(true)
        }}
        onSystemNodePathDraftChange={(value) => {
          setAutomationSystemNodePathDraft(value)
          setAutomationProbe(null)
          setAutomationRuntimeDirty(true)
        }}
        onLaunchServerPortDraftChange={setLaunchServerPortDraft}
        onSaveLaunchServerPort={() => { void handleLaunchServerPortSave() }}
        onTypeScriptBuildChange={handleAutomationTypeScriptBuildChange}
        onProbeSystemNode={() => { void handleAutomationProbeSystemNode() }}
        onSaveRuntimeSettings={() => { void handleAutomationRuntimeSettingsSave() }}
        onInstall={() => { void handleAutomationInstall() }}
        onSelfCheck={() => { void handleAutomationSelfCheck() }}
      />
      <SettingsDiagnosticsCard
        enableNotifications={enableNotifications}
        onNotificationHistoryChange={setEnableNotifications}
        logLevel={logLevel}
        onLogLevelChange={value => {
          setLogLevel(value)
          setLogLevelError('')
        }}
        logLevelError={logLevelError}
        onLogLevelRetry={() => { void loadLogLevel() }}
        disabled={saving}
      />
    </div>
  )
}
