import { useEffect, useState } from 'react'
import { Button, Modal, toast } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import type { BrowserGroup } from '../types'
import { changeProfileGroups } from '../api/groups'
import { GroupSelector } from './GroupSelector'

export interface GroupMembershipRequest { mode: 'add' | 'remove' | 'set'; profileIds: string[]; groupIds: string[]; label?: string }
export function InstanceGroupMembershipModal({ request, groups, onClose, onSaved }: { request: GroupMembershipRequest | null; groups: BrowserGroup[]; onClose: () => void; onSaved: () => Promise<unknown> }) {
  const [ids, setIds] = useState<string[]>([]), [saving, setSaving] = useState(false), [error, setError] = useState('')
  useEffect(() => { setIds(request?.groupIds || []); setError('') }, [request])
  const close = () => { if (!saving) onClose() }
  const save = async () => {
    if (!request || saving) return
    setSaving(true); setError('')
    try {
      await changeProfileGroups(request.profileIds, ids, request.mode)
      onClose()
      try { await onSaved() } catch (cause) { toast.error(`归属已保存，刷新失败：${errorMessage(cause, '请点击刷新重试')}`) }
    }
    catch (cause) { setError(errorMessage(cause, '保存实例分组失败')) }
    finally { setSaving(false) }
  }
  const title = request?.mode === 'add' ? '加入分组' : request?.mode === 'remove' ? '移出分组' : '编辑实例分组'
  return <Modal open={!!request} onClose={close} closable={!saving} title={title}
    footer={<><Button variant="secondary" disabled={saving} onClick={close}>取消</Button><Button loading={saving} disabled={!request || (request.mode !== 'set' && ids.length === 0)} onClick={() => { void save() }}>保存归属</Button></>}>
    <p className="mb-3 text-sm text-[var(--color-text-secondary)]">{request?.label || `已选择 ${request?.profileIds.length || 0} 个实例`}{request?.mode === 'set' ? '，可同时选择多个分组。' : '，其他分组归属保持不变。'}</p>
    <GroupSelector groups={groups} value={ids} onChange={setIds} disabled={saving} />
    {request?.mode === 'set' && <p className="mt-2 text-xs text-[var(--color-text-muted)]">取消全部勾选后，实例将成为未分组。</p>}
    {error && <p role="alert" className="mt-3 text-sm text-[var(--color-error)]">{error}</p>}
  </Modal>
}
