import { useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { ChevronDown, ChevronRight, Folder, FolderPlus, PanelLeftClose, PanelLeftOpen, Pencil, Plus, Trash2 } from 'lucide-react'
import { Button, FormItem, Input, Modal, Select, toast } from '../../../shared/components'
import { errorMessage } from '../../../utils/errors'
import { createGroup, deleteGroup, updateGroup } from '../api/groups'
import type { BrowserGroup, BrowserGroupWithCount } from '../types'
import { descendantGroupIds, groupPath } from '../utils/instanceGroups'

type Editor = { mode: 'create' | 'edit' | 'delete'; group?: BrowserGroup; name: string; parentId: string }
interface Props {
  groups: BrowserGroupWithCount[]
  selectedGroupId: string
  totalCount: number
  ungroupedCount: number
  error?: string
  ready: boolean
  onSelect: (id: string) => void
  onChanged: () => Promise<unknown>
}

export function GroupTreeNav({ groups, selectedGroupId, totalCount, ungroupedCount, error, ready, onSelect, onChanged }: Props) {
  const location = useLocation()
  const [collapsed, setCollapsed] = useState(() => new URLSearchParams(location.search).get('groups') !== 'open' && localStorage.getItem('browser:groupTreeCollapsed') === 'true')
  useEffect(() => { if (new URLSearchParams(location.search).get('groups') === 'open') setCollapsed(false) }, [location.search])
  const [folded, setFolded] = useState<Set<string>>(new Set())
  const [editor, setEditor] = useState<Editor | null>(null)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState('')
  const toggleCollapsed = () => setCollapsed(value => { localStorage.setItem('browser:groupTreeCollapsed', String(!value)); return !value })
  const openEditor = (next: Editor) => { setFormError(''); setEditor(next) }
  const closeEditor = () => { if (!saving) setEditor(null) }
  const save = async () => {
    if (!editor || saving) return
    setSaving(true); setFormError('')
    try {
      if (editor.mode === 'delete') {
        await deleteGroup(editor.group!.groupId)
        if (selectedGroupId === editor.group!.groupId) onSelect('')
      } else {
        const input = { groupName: editor.name.trim(), parentId: editor.parentId, sortOrder: editor.group?.sortOrder || 0 }
        if (editor.mode === 'create') await createGroup(input)
        else await updateGroup(editor.group!.groupId, input)
      }
      setEditor(null)
      try { await onChanged() } catch (cause) { toast.error(`分组已保存，刷新失败：${errorMessage(cause, '请点击刷新重试')}`) }
    } catch (cause) { setFormError(errorMessage(cause, '分组操作失败')) }
    finally { setSaving(false) }
  }
  const rowClass = (id: string) => `flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm transition-colors ${selectedGroupId === id ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]' : 'hover:bg-[var(--color-bg-muted)]'}`
  const renderBranch = (group: BrowserGroupWithCount, ancestors: Set<string>): React.ReactNode => {
    if (ancestors.has(group.groupId)) return null
    const path = new Set([...ancestors, group.groupId])
    const children = groups.filter(item => item.parentId === group.groupId)
    return <div key={group.groupId}>
      <div className="group flex items-center gap-0.5" style={{ paddingLeft: ancestors.size * 12 }}>
        {children.length ? <button type="button" className="p-1" aria-label={`${folded.has(group.groupId) ? '展开' : '收起'}子分组：${group.groupName}`} aria-expanded={!folded.has(group.groupId)}
          onClick={() => setFolded(previous => { const next = new Set(previous); if (next.has(group.groupId)) next.delete(group.groupId); else next.add(group.groupId); return next })}>
          {folded.has(group.groupId) ? <ChevronRight size={14} /> : <ChevronDown size={14} />}
        </button> : <span className="w-[22px] shrink-0" />}
        <button type="button" className={`${rowClass(group.groupId)} min-w-0 flex-1`} title={groupPath(group.groupId, groups)} aria-current={selectedGroupId === group.groupId ? 'true' : undefined} onClick={() => onSelect(group.groupId)}>
          <Folder size={14} className="shrink-0" /><span className="truncate">{group.groupName}</span><span className="ml-auto text-xs tabular-nums">{group.instanceCount}</span>
        </button>
        <div className="flex shrink-0">
          <button type="button" title={`新建子分组：${group.groupName}`} aria-label={`新建子分组：${group.groupName}`} className="rounded p-1 hover:bg-[var(--color-bg-muted)]" disabled={!ready || !!error} onClick={() => openEditor({ mode: 'create', name: '', parentId: group.groupId })}><Plus size={13} /></button>
          <button type="button" title={`重命名或移动：${group.groupName}`} aria-label={`重命名分组：${group.groupName}`} className="rounded p-1 hover:bg-[var(--color-bg-muted)]" disabled={!ready || !!error} onClick={() => openEditor({ mode: 'edit', group, name: group.groupName, parentId: group.parentId })}><Pencil size={13} /></button>
          <button type="button" title={`删除分组：${group.groupName}`} aria-label={`删除分组：${group.groupName}`} className="rounded p-1 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-error)]" disabled={!ready || !!error} onClick={() => openEditor({ mode: 'delete', group, name: group.groupName, parentId: group.parentId })}><Trash2 size={13} /></button>
        </div>
      </div>
      {!folded.has(group.groupId) && children.map(child => renderBranch(child, path))}
    </div>
  }
  const forbiddenParents = editor?.group ? descendantGroupIds(editor.group.groupId, groups) : new Set<string>()
  return <>
    <aside aria-label="实例分组" className={`shrink-0 rounded-lg border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-2 ${collapsed ? 'lg:w-12' : 'w-full lg:w-72'}`}>
      <div className="flex items-center gap-2 px-1 py-1">
        {!collapsed && <span className="flex-1 text-sm font-semibold">实例分组</span>}
        {!collapsed && <button type="button" title="新建分组" aria-label="新建分组" className="rounded p-1.5 hover:bg-[var(--color-bg-muted)]" disabled={!ready || !!error} onClick={() => openEditor({ mode: 'create', name: '', parentId: '' })}><FolderPlus size={16} /></button>}
        <button type="button" title={collapsed ? '展开实例分组' : '收起实例分组'} aria-label={collapsed ? '展开实例分组' : '收起实例分组'} onClick={toggleCollapsed} className="rounded p-1.5 hover:bg-[var(--color-bg-muted)]">{collapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}</button>
      </div>
      {!collapsed && <div className="mt-2 max-h-[65vh] overflow-auto">
        <button type="button" className={rowClass('')} onClick={() => onSelect('')}><span>全部实例</span><span className="ml-auto text-xs tabular-nums">{totalCount}</span></button>
        <button type="button" className={rowClass('__ungrouped__')} onClick={() => onSelect('__ungrouped__')}><span>未分组</span><span className="ml-auto text-xs tabular-nums">{ungroupedCount}</span></button>
        <div className="my-2 border-t border-[var(--color-border-default)]" />
        {error ? <p role="alert" className="p-2 text-sm text-[var(--color-error)]">{error}</p> : !ready ? <p className="p-2 text-sm text-[var(--color-text-muted)]">正在加载分组…</p> : groups.length === 0 ? <p className="p-2 text-sm text-[var(--color-text-muted)]">点击上方按钮新建分组。</p> : groups.filter(group => !group.parentId || !groups.some(parent => parent.groupId === group.parentId)).map(group => renderBranch(group, new Set()))}
      </div>}
    </aside>
    <Modal open={!!editor} onClose={closeEditor} closable={!saving} title={editor?.mode === 'delete' ? '删除实例分组' : editor?.mode === 'edit' ? '重命名或移动分组' : '新建实例分组'}
      footer={<><Button variant="secondary" disabled={saving} onClick={closeEditor}>取消</Button><Button variant={editor?.mode === 'delete' ? 'danger' : 'primary'} loading={saving} disabled={!editor || (editor.mode !== 'delete' && !editor.name.trim())} onClick={() => { void save() }}>{editor?.mode === 'delete' ? '删除分组' : '保存分组'}</Button></>}>
      {editor?.mode === 'delete' ? <p className="text-sm leading-6">删除“{editor.name}”后，实例及其其他分组归属会保留；直接子分组提升一级。同级重名时需要先重命名冲突分组。</p> : <div className="space-y-4">
        <FormItem label="分组名称"><Input aria-label="分组名称" autoFocus value={editor?.name || ''} onChange={event => setEditor(value => value && { ...value, name: event.target.value })} onKeyDown={event => { if (event.key === 'Enter' && editor?.name.trim()) void save() }} disabled={saving} /></FormItem>
        <FormItem label="父分组"><Select aria-label="父分组" value={editor?.parentId || ''} disabled={saving} onChange={event => setEditor(value => value && { ...value, parentId: event.target.value })} options={[{ value: '', label: '无（根分组）' }, ...groups.filter(group => !forbiddenParents.has(group.groupId)).map(group => ({ value: group.groupId, label: groupPath(group.groupId, groups) }))]} /></FormItem>
      </div>}
      {formError && <p role="alert" className="mt-3 text-sm text-[var(--color-error)]">{formError}</p>}
    </Modal>
  </>
}
