import { Loader2, Upload } from 'lucide-react'
import { create } from 'zustand'
import { Button, toast } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import { cancelMofaImport, prepareMofaImport, type MofaImportResult } from '../api/mofa'
import type { BrowserProfile } from '../types'

interface PendingImport {
  cancel?: Promise<void>
}

const useMofaImports = create<{ pending: Map<string, PendingImport> }>(() => ({ pending: new Map() }))

function showImportResult(profileName: string, { status, accountId, searchHint, actionOpened, inTargetGroup }: MofaImportResult) {
  const group = '「GPT-不降智」'
  if (status === 'skipped') {
    toast.success(`ChatGPT 账号已在 ${group}，授权有效。`, undefined, { title: `「${profileName}」已跳过` })
    return
  }

  const opened = actionOpened
    ? '已在该实例打开 MOFA 操作窗口。'
    : '已在该实例打开 MOFA「账号管理」。如页面要求登录，请先登录管理员账号。'
  const locateAccount = searchHint ? `搜索「${searchHint}」并核对账号 #${accountId}` : `找到账号 #${accountId}`
  let title: string
  let instructions: string
  switch (status) {
    case 'creation_required': {
      title = '待添加账号'
      const name = searchHint ? `，账号名称可填写「${searchHint}」` : ''
      const action = actionOpened ? '在「添加账号」窗口选择' : '点「添加账号」，选择'
      instructions = `${action} OpenAI 和 OAuth${name}，在「分组」中选中 ${group}，完成授权并创建。`
      break
    }
    case 'reauthorization_required': {
      title = '待重新授权'
      const action = actionOpened ? '在「重新授权账号」窗口完成授权' : `${locateAccount}，点「更多 → 重新授权」并完成授权`
      instructions = `${action}。`
      if (!inTargetGroup) instructions += `随后在「编辑」的「分组」中添加 ${group}，保留原有分组并保存。`
      break
    }
    case 'group_required': {
      title = '待加入分组'
      instructions = actionOpened
        ? `在「编辑账号」窗口的「分组」中选中 ${group}，保留原有分组并保存。`
        : `${locateAccount}，点「编辑」。在「分组」中添加 ${group}，保留原有分组并保存。`
      break
    }
  }
  toast.info(`${opened}${instructions}`, 15_000, { title: `「${profileName}」${title}`, persistent: true })
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
  setPendingImport(profileId, {})
  try {
    showImportResult(profileName, await prepareMofaImport(profileId))
  } catch (error) {
    const message = errorMessage(error, '核对 MOFA 账号失败，请重试。')
    if (message === '已取消 MOFA 导入') {
      toast.info(message, undefined, { title: `「${profileName}」` })
    } else {
      toast.error(message, undefined, {
        title: `「${profileName}」的 MOFA 核对失败`,
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
      <div className="inline-flex h-7 items-center gap-1.5">
        <Loader2 className="h-3.5 w-3.5 animate-spin text-[var(--color-text-muted)]" aria-hidden="true" />
        <span className="text-xs text-[var(--color-text-secondary)]" role="status">{job.cancel ? '取消中' : '核对中'}</span>
        <Button type="button" size="sm" variant="ghost" onClick={() => requestCancel(profile.profileId)} disabled={!!job.cancel} aria-label={`取消「${profile.profileName}」的 MOFA 核对`}>
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
