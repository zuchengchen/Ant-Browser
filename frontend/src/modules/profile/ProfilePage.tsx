import {
  Github,
  Globe,
  ExternalLink,
  RefreshCw,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { Badge, Button, Card } from '../../shared/components'
import { profilePageConfig } from '../../config/profile.config'
import { checkForUpdates, loadCurrentAppVersion } from './api'
import type { AppUpdateCheckResult, IconKey } from './types'

const ICON_MAP = {
  globe: Globe,
  github: Github,
  'external-link': ExternalLink,
}

export function ProfilePage() {
  const [appVersion, setAppVersion] = useState('')
  const [updateCheck, setUpdateCheck] = useState<AppUpdateCheckResult | null>(null)
  const [checkingUpdate, setCheckingUpdate] = useState(false)

  useEffect(() => {
    let active = true

    void loadCurrentAppVersion()
      .then((version) => {
        if (active) setAppVersion(version)
      })
      .catch(() => {
        if (active) setAppVersion('')
      })

    return () => {
      active = false
    }
  }, [])

  const openExternal = (url: string) => {
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  const handleCheckForUpdates = async () => {
    if (checkingUpdate) return
    setCheckingUpdate(true)
    try {
      setUpdateCheck(await checkForUpdates())
    } catch (error: any) {
      setUpdateCheck({
        ok: false,
        currentVersion: appVersion,
        latestVersion: '',
        updateAvailable: false,
        releaseUrl: '',
        error: error?.message || '检查更新失败',
      })
    } finally {
      setCheckingUpdate(false)
    }
  }

  const projectInfo = profilePageConfig.project

  return (
    <div className="mx-auto max-w-5xl space-y-4 animate-fade-in">
      <h1 className="text-xl font-semibold text-[var(--color-text-primary)]">关于软件</h1>
      <Card
        className="rounded-[24px]"
        padding="lg"
      >
        <div className="space-y-4 text-[15px] leading-8 text-[var(--color-text-secondary)]">
          <div className="flex flex-col gap-3 border-b border-[var(--color-border-muted)] pb-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="min-w-0">
              <p className="font-semibold text-[var(--color-text-primary)]">
                当前版本 {appVersion || '未知'}
              </p>
              {updateCheck?.ok ? (
                <p className="text-sm text-[var(--color-text-muted)]">
                  {updateCheck.updateAvailable ? `发现新版本 v${updateCheck.latestVersion}` : '已是最新版本'}
                </p>
              ) : updateCheck ? (
                <p className="text-sm text-[var(--color-error)]">{updateCheck.error || '检查更新失败'}</p>
              ) : null}
            </div>
            <div className="flex flex-wrap gap-2 sm:justify-end">
              <Button variant="secondary" size="sm" onClick={() => { void handleCheckForUpdates() }} loading={checkingUpdate}>
                <RefreshCw className="h-4 w-4" />
                检查更新
              </Button>
              {updateCheck?.ok && updateCheck.updateAvailable && updateCheck.releaseUrl ? (
                <Button variant="ghost" size="sm" onClick={() => openExternal(updateCheck.releaseUrl)}>
                  打开下载页
                  <ExternalLink className="h-3.5 w-3.5" />
                </Button>
              ) : null}
            </div>
          </div>
          <p>
            <Badge className="mr-1 rounded-xl px-3 py-1">{projectInfo.introBadge}</Badge>
            {projectInfo.introText}
          </p>
          <div className="flex flex-wrap gap-2">
            {projectInfo.techStack.map((item) => (
              <Badge key={item} className="rounded-xl px-3 py-1">
                {item}
              </Badge>
            ))}
          </div>
          <p>{projectInfo.description}</p>
          <div className="flex flex-wrap items-center gap-3 pt-2">
            {projectInfo.actions.map((action) => {
              const Icon = getIcon(action.icon)
              return (
                <Button
                  key={action.label}
                  variant="ghost"
                  className="h-10 rounded-xl border border-[var(--color-border-default)] px-3 text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-muted)]"
                  onClick={() => openExternal(action.href)}
                >
                  <Icon className="h-4 w-4" />
                  {action.label}
                  <ExternalLink className="h-3 w-3" />
                </Button>
              )
            })}
          </div>
        </div>
      </Card>
    </div>
  )
}

function getIcon(icon?: IconKey) {
  return ICON_MAP[icon || 'globe'] || Globe
}
