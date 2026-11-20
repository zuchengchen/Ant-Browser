import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Loader2, RefreshCw } from 'lucide-react'
import { Button, Modal } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import type { BrowserProfile } from '../types'
import { getSavedChatGPTAccount, readChatGPTAccount, type ChatGPTAccountInfo, type ChatGPTUsageWindow } from '../api/chatgpt'

function AccountRow({ label, children }: { label: string; children: ReactNode }) {
  return <div className="grid grid-cols-[7rem_1fr] gap-4 py-2.5">
    <dt className="text-[var(--color-text-secondary)]">{label}</dt>
    <dd className="min-w-0 break-words text-[var(--color-text-primary)]">{children}</dd>
  </div>
}

function UsageValue({ value }: { value: ChatGPTUsageWindow }) {
  return <>{value.remainingPercent === null ? '页面未提供' : <strong className="font-medium">剩余 {value.remainingPercent}%</strong>}
    {value.resetText && <span className="mt-0.5 block text-xs text-[var(--color-text-muted)]">{value.resetText}</span>}</>
}

export function ChatGPTAccountModal({ profile, fetchOnOpen, onClose, onUpdated }: {
  profile: BrowserProfile; fetchOnOpen: boolean; onClose: () => void; onUpdated: () => void
}) {
  const [data, setData] = useState<ChatGPTAccountInfo | null>(null)
  const [attempt, setAttempt] = useState<ChatGPTAccountInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [polling, setPolling] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const updateRef = useRef(onUpdated)
  updateRef.current = onUpdated

  useEffect(() => {
    const profileId = profile.profileId
    let active = true
    let timer: number | undefined
    const deadline = Date.now() + 10 * 60 * 1000
    setError(''); setAttempt(null); setSaved(false); setPolling(false)
    const read = async () => {
      setBusy(true)
      try {
        const result = await readChatGPTAccount(profileId)
        if (!active) return
        setAttempt(result)
        if (result.savedAt) { setData(result); setSaved(true); setError('') }
        updateRef.current()
        if (result.status === 'login_required' || result.status === 'verification_required') {
          if (Date.now() < deadline) {
            setPolling(true)
            timer = window.setTimeout(() => { void read() }, 5000)
          } else { setPolling(false); setError('等待登录已结束。完成登录后可再次点击“获取 GPT 账户信息”。') }
        } else setPolling(false)
      } catch (cause) {
        if (active) { setPolling(false); setError(errorMessage(cause, '获取 GPT 账户信息失败')) }
      } finally { if (active) setBusy(false) }
    }
    const open = async () => {
      setLoading(true)
      try {
        const stored = await getSavedChatGPTAccount(profileId)
        if (active) setData(stored)
      } catch (cause) {
        if (active) setError(errorMessage(cause, '读取已保存的信息失败'))
      } finally { if (active) setLoading(false) }
      if (active && (fetchOnOpen || refresh > 0)) await read()
    }
    void open()
    return () => { active = false; window.clearTimeout(timer) }
  }, [profile.profileId, fetchOnOpen, refresh])

  const waiting = attempt?.status === 'login_required' || attempt?.status === 'verification_required'
  return <Modal open onClose={onClose} title="GPT 账户信息" width="620px"
    footer={<><Button variant="secondary" onClick={onClose}>关闭</Button><Button disabled={loading || busy || polling} onClick={() => setRefresh(value => value + 1)}>
      <RefreshCw className="h-4 w-4" />获取 GPT 账户信息</Button></>}>
    <p className="mb-3 break-all text-sm text-[var(--color-text-secondary)]">{profile.profileName}</p>
    <div aria-live="polite">
      {loading && <p role="status" className="py-3 text-sm text-[var(--color-text-secondary)]">正在读取已保存的信息…</p>}
      {busy && !waiting && <p role="status" className="flex items-center gap-2 py-3 text-sm text-[var(--color-text-secondary)]"><Loader2 className="h-4 w-4 animate-spin" />正在打开实例、检查登录并读取账户信息…</p>}
      {waiting && <p role="status" className="rounded-lg bg-[var(--color-bg-muted)] p-3 text-sm text-[var(--color-text-primary)]">
        {attempt.warnings[0]} 浏览器窗口会保持打开，之前保存的信息保留。</p>}
      {attempt?.status === 'account_mismatch' && <p role="alert" className="my-3 text-sm text-[var(--color-warning)]">{attempt.warnings.join(' ')} 本次结果未保存。</p>}
      {error && <p role="alert" className="my-3 text-sm text-[var(--color-error)]">{error}</p>}
      {saved && <p role="status" className="mb-3 text-sm text-[var(--color-success)]">{data?.status === 'partial' ? '已保存，部分信息未读取到。' : '已获取并保存。'}</p>}
      {!loading && !busy && !waiting && !data && <p className="py-6 text-sm text-[var(--color-text-secondary)]">尚未保存此实例的 GPT 账户信息。点击“获取 GPT 账户信息”开始获取。</p>}
      {data && <>
        <p className="mb-2 text-xs text-[var(--color-text-muted)]">上次保存：{new Date(data.savedAt).toLocaleString('zh-CN')}</p>
        <dl className="divide-y divide-[var(--color-border-muted)] text-sm">
          <AccountRow label="显示名">{data.displayName || '页面未提供'}</AccountRow>
          <AccountRow label="用户名">{data.username || '页面未提供'}</AccountRow>
          <AccountRow label="登录邮箱">{data.email || '页面未提供'}</AccountRow>
          <AccountRow label="订阅类型">{data.plan || '页面未提供'}</AccountRow>
          <AccountRow label="订阅到期">{data.expiryDate || (data.renewalDate ? '未显示到期日；已显示下次续费日期' : '页面未提供')}</AccountRow>
          {data.renewalDate && <AccountRow label="下次续费">{data.renewalDate}</AccountRow>}
          <AccountRow label="本周额度"><UsageValue value={data.weekly} /></AccountRow>
          <AccountRow label="5 小时额度"><UsageValue value={data.fiveHour} /></AccountRow>
          <AccountRow label="积分">{data.credits === null ? '页面未提供' : `${data.credits > 0 ? '有' : '无'}（${data.credits.toLocaleString()} 积分）`}</AccountRow>
          <AccountRow label="可用重置卡">{data.resetCards === null ? '页面未提供' : `${data.resetCards > 0 ? '有' : '无'}（${data.resetCards} 张）`}</AccountRow>
        </dl>
        {data.usageScope && <p className="mt-3 text-xs text-[var(--color-text-secondary)]">{data.usageScope}。</p>}
        {data.subscriptionDetail && <details className="mt-3 text-xs text-[var(--color-text-secondary)]"><summary className="cursor-pointer">网页订阅说明</summary><p className="mt-2 whitespace-pre-line">{data.subscriptionDetail}</p></details>}
        {data.warnings?.length > 0 && <ul role="status" className="mt-3 list-inside list-disc space-y-1 text-sm text-[var(--color-warning)]">{data.warnings.map(warning => <li key={warning}>{warning}</li>)}</ul>}
        <p className="mt-3 text-xs text-[var(--color-text-muted)]">获取时间：{new Date(data.checkedAt).toLocaleString('zh-CN')} · 额度和重置倒计时为获取时的记录，再次获取可更新。</p>
      </>}
    </div>
  </Modal>
}
