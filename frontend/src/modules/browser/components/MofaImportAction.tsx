import { Loader2, Upload } from 'lucide-react'
import { create } from 'zustand'
import { Button, toast } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import { cancelMofaImport, importMofaAccount } from '../api/mofa'
import type { BrowserProfile } from '../types'

interface PendingImport {
  cancel?: Promise<void>
}

const useMofaImports = create<{ pending: Map<string, PendingImport> }>(() => ({ pending: new Map() }))
const resultMessages = new Map([
  ['skipped', '的 ChatGPT 账号已在 GPT-不降智，授权有效，已跳过。'],
  ['group_added', '的 ChatGPT 账号已加入 GPT-不降智。'],
  ['imported', '的 ChatGPT 账号已导入 GPT-不降智，新账号的调用已关闭。'],
  ['reauthorized', '的 ChatGPT 账号已重新授权。'],
])

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
  setPendingImport(profileId, {})
  try {
    const status = await importMofaAccount(profileId)
    const message = resultMessages.get(status)
    if (!message) throw new Error('MOFA 返回了未知结果，请在后台账号列表中核对。')
    toast.success(`「${profileName}」${message}`, undefined, { title: 'MOFA 导入完成' })
  } catch (error) {
    const message = errorMessage(error, 'MOFA 导入失败，请重试。')
    if (message === '已取消 MOFA 导入') {
      toast.info(message, undefined, { title: `「${profileName}」` })
    } else {
      toast.error(message, undefined, {
        title: `「${profileName}」导入到 MOFA 失败`,
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
    setPendingImport(profileId, {})
    toast.error(errorMessage(error, '发送 MOFA 导入取消请求失败，请重试。'))
  })
  setPendingImport(profileId, { cancel })
}

export function MofaImportAction({ profile, disabled }: { profile: BrowserProfile; disabled: boolean }) {
  const job = useMofaImports(state => state.pending.get(profile.profileId))
  if (job) {
    return (
      <div className="inline-flex flex-col items-start">
        <div className="flex h-7 items-center gap-1.5">
          <Loader2 className="h-3.5 w-3.5 animate-spin text-[var(--color-text-muted)]" aria-hidden="true" />
          <span className="text-xs text-[var(--color-text-secondary)]" role="status">{job.cancel ? '取消中' : '导入中'}</span>
          <Button type="button" size="sm" variant="ghost" onClick={() => requestCancel(profile.profileId)} disabled={!!job.cancel} aria-label={`取消「${profile.profileName}」的 MOFA 导入`}>
            取消
          </Button>
        </div>
        {!job.cancel && <p className="max-w-36 whitespace-normal text-xs text-[var(--color-text-muted)]">若打开 OpenAI 授权页，请在实例内确认。</p>}
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
