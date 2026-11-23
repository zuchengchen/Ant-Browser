import { Loader2, Upload } from 'lucide-react'
import { create } from 'zustand'
import { Button, toast } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import { cancelMofaImport, importMofaAccount, type MofaImportPhase, type MofaImportResult } from '../api/mofa'
import type { BrowserProfile } from '../types'

interface PendingImport {
  phase: MofaImportPhase
  cancel?: Promise<void>
}

const useMofaImports = create<{ pending: Map<string, PendingImport> }>(() => ({ pending: new Map() }))

const progressLabels: Record<MofaImportPhase, string> = {
  checking: '正在核对账号',
  waiting_for_oauth: '等待授权，如有提示请在该实例的 OpenAI 页面确认',
  saving: '正在保存到 MOFA',
}

function showImportResult(profileName: string, { status, accountId, schedulable }: MofaImportResult) {
  const group = '「GPT-不降智」'
  const outcomes = {
    skipped: ['已跳过', `MOFA 账号 #${accountId} 已在${group}，授权有效。`],
    imported: ['已导入到 MOFA', `已创建账号 #${accountId} 并加入${group}。`],
    reauthorized: ['已重新授权', `已更新账号 #${accountId} 的 OpenAI 授权，账号位于${group}。`],
    group_added: ['已加入分组', `已将账号 #${accountId} 加入${group}。`],
  }
  const [title, message] = outcomes[status]
  const scheduling = schedulable === false ? '该账号的 MOFA 调度当前已关闭。' : ''
  toast.success(message + scheduling, undefined, { title: `「${profileName}」${title}` })
}

function setPendingImport(profileId: string, job?: PendingImport) {
  useMofaImports.setState(({ pending }) => {
    const next = new Map(pending)
    if (job) next.set(profileId, job)
    else next.delete(profileId)
    return { pending: next }
  })
}

async function startImport(profile: BrowserProfile) {
  const { profileId, profileName } = profile
  if (!profile.running || useMofaImports.getState().pending.has(profileId)) return
  setPendingImport(profileId, { phase: 'checking' })
  try {
    const result = await importMofaAccount(profileId, phase => {
      const job = useMofaImports.getState().pending.get(profileId)
      if (job && job.phase !== phase) setPendingImport(profileId, { ...job, phase })
    })
    showImportResult(profileName, result)
  } catch (error) {
    const message = errorMessage(error, '导入到 MOFA 失败，请重试。')
    if (message === '已取消 MOFA 导入') {
      toast.info(message, undefined, { title: `「${profileName}」` })
    } else {
      toast.error(message, undefined, {
        title: `「${profileName}」的 MOFA 导入失败`,
        action: message.includes('系统设置') ? { type: 'navigate', label: '打开系统设置', path: '/settings' } : undefined,
      })
    }
  } finally {
    // Keep this profile busy until a cancellation request for this import has returned.
    const cancellation = useMofaImports.getState().pending.get(profileId)?.cancel
    if (cancellation) await cancellation
    setPendingImport(profileId)
  }
}

function requestCancel(profileId: string) {
  const job = useMofaImports.getState().pending.get(profileId)
  if (!job || job.cancel) return
  const cancel = cancelMofaImport(profileId).catch(error => {
    const current = useMofaImports.getState().pending.get(profileId)
    if (current) setPendingImport(profileId, { phase: current.phase })
    toast.error(errorMessage(error, '发送 MOFA 导入取消请求失败，请重试。'))
  })
  setPendingImport(profileId, { ...job, cancel })
}

export function MofaImportAction({ profile, disabled }: { profile: BrowserProfile; disabled: boolean }) {
  const job = useMofaImports(state => state.pending.get(profile.profileId))
  if (job) {
    return (
      <div className="inline-flex min-h-7 max-w-full items-center gap-1.5 whitespace-normal">
        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-[var(--color-text-muted)]" aria-hidden="true" />
        <span className="text-xs text-[var(--color-text-secondary)]" role="status">{job.cancel ? '取消中' : progressLabels[job.phase]}</span>
        <Button type="button" size="sm" variant="ghost" className="shrink-0" onClick={() => requestCancel(profile.profileId)} disabled={!!job.cancel} aria-label={`取消「${profile.profileName}」的 MOFA 导入`}>
          取消
        </Button>
      </div>
    )
  }
  if (!profile.running) return null
  return (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      onClick={() => { void startImport(profile) }}
      disabled={disabled || !profile.debugReady}
      title={profile.debugReady ? undefined : '请等待实例就绪后导入'}
      aria-label={`导入到 MOFA，实例「${profile.profileName}」`}
    >
      <Upload className="h-3.5 w-3.5" aria-hidden="true" />导入到 MOFA
    </Button>
  )
}
