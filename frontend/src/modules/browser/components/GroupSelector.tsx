import type { BrowserGroup } from '../types'
import { groupPath } from '../utils/instanceGroups'

interface GroupSelectorProps {
  groups: BrowserGroup[]
  value: string[]
  onChange: (groupIds: string[]) => void
  className?: string
  disabled?: boolean
}

export function GroupSelector({ groups, value, onChange, className = '', disabled = false }: GroupSelectorProps) {
  const sorted = [...groups].sort((a, b) => groupPath(a.groupId, groups).localeCompare(groupPath(b.groupId, groups), 'zh-CN', { numeric: true }))
  return (
    <div className={`max-h-60 overflow-auto rounded-lg border border-[var(--color-border)] p-2 ${className}`} role="group" aria-label="实例分组选择">
      {sorted.length === 0 && <p className="p-2 text-sm text-[var(--color-text-muted)]">暂无分组，请先在实例列表中新建。</p>}
      {sorted.map(group => (
        <label key={group.groupId} className="flex cursor-pointer items-center gap-2 rounded px-2 py-2 text-sm hover:bg-[var(--color-bg-muted)]">
          <input type="checkbox" disabled={disabled} checked={value.includes(group.groupId)} aria-label={groupPath(group.groupId, groups)}
            onChange={event => onChange(event.target.checked ? [...new Set([...value, group.groupId])] : value.filter(id => id !== group.groupId))}
            className="h-4 w-4 accent-[var(--color-accent)]" />
          <span>{groupPath(group.groupId, groups)}</span>
        </label>
      ))}
    </div>
  )
}
