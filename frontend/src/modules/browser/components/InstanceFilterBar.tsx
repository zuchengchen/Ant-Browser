import { useState } from 'react'
import { ChevronDown, ChevronRight, Filter, X } from 'lucide-react'
import { Input, Select } from '../../../shared/components'
import { groupPath } from '../utils/instanceGroups'
import type { BrowserCore, BrowserProxy, BrowserGroupWithCount } from '../types'

export interface InstanceFilters {
  keyword: string
  status: '' | 'running' | 'stopped'
  proxyId: string
  coreId: string
  groupIds: string[]
  kwSearch: string
  groupId: string   // '' = 全部, '__ungrouped__' = 未分组, 其他 = 具体分组ID
}

export const EMPTY_FILTERS: InstanceFilters = {
  keyword: '',
  status: '',
  proxyId: '',
  coreId: '',
  groupIds: [],
  kwSearch: '',
  groupId: '',
}

export function isFiltersEmpty(f: InstanceFilters) {
  return !f.keyword && !f.status && !f.proxyId && !f.coreId && f.groupIds.length === 0 && !f.kwSearch && !f.groupId
}

interface Props {
  filters: InstanceFilters
  onChange: (f: InstanceFilters) => void
  proxies: BrowserProxy[]
  cores: BrowserCore[]
  groups: BrowserGroupWithCount[]
}

export function InstanceFilterBar({ filters, onChange, proxies, cores, groups }: Props) {
  const [collapsed, setCollapsed] = useState(false)

  const set = <K extends keyof InstanceFilters>(key: K, value: InstanceFilters[K]) =>
    onChange({ ...filters, [key]: value })

  const hasFilter = !isFiltersEmpty(filters)
  const searchValue = filters.keyword || filters.kwSearch
  const activeCount = [searchValue, filters.status, filters.proxyId, filters.coreId, filters.groupId].filter(Boolean).length + filters.groupIds.length

  return (
    <div className="space-y-1.5">
      <div
        className="flex items-center gap-1.5 cursor-pointer select-none text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
        onClick={() => setCollapsed(prev => !prev)}
      >
        {collapsed ? <ChevronRight className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
        <Filter className="w-3.5 h-3.5" />
        <span>筛选</span>
        {collapsed && activeCount > 0 && (
          <span className="ml-1 px-1.5 py-0.5 text-[10px] font-medium bg-[var(--color-accent)]/10 text-[var(--color-accent)] rounded-full">
            {activeCount}
          </span>
        )}
      </div>

      {!collapsed && (
        <>
          <div className="flex items-center gap-1.5 flex-wrap">
            <Input
              value={searchValue}
              onChange={e => onChange({ ...filters, keyword: e.target.value, kwSearch: '' })}
              placeholder="搜索名称/快捷码/关键字..."
              className="flex-1 min-w-[220px]"
            />
            <Select
              value={filters.status}
              onChange={e => set('status', e.target.value as InstanceFilters['status'])}
              options={[
                { value: '', label: '全部状态' },
                { value: 'running', label: '运行中' },
                { value: 'stopped', label: '已停止' },
              ]}
              style={{ width: '120px' }}
            />
            <Select
              value={filters.proxyId}
              onChange={e => set('proxyId', e.target.value)}
              options={[
                { value: '', label: '全部代理' },
                { value: '__none__', label: '无代理' },
                ...proxies.map(p => ({ value: p.proxyId, label: p.proxyName || p.proxyId })),
              ]}
              style={{ width: '150px' }}
            />
            <Select
              value={filters.coreId}
              onChange={e => set('coreId', e.target.value)}
              options={[
                { value: '', label: '全部内核' },
                ...cores.map(c => ({ value: c.coreId, label: c.coreName })),
              ]}
              style={{ width: '140px' }}
            />
            {hasFilter && (
              <button
                onClick={() => onChange({ ...EMPTY_FILTERS, groupIds: [] })}
                className="flex items-center gap-1 px-2 py-1 text-xs text-[var(--color-text-muted)] hover:text-[var(--color-error)] hover:bg-[var(--color-bg-muted)] rounded transition-colors"
              >
                <X className="w-3.5 h-3.5" />
                清除
              </button>
            )}
          </div>
          {(filters.groupId || filters.groupIds.length > 0) && <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-text-secondary)]" aria-label="当前分组筛选">
            {filters.groupId && <span>分组：{filters.groupId === '__ungrouped__' ? '未分组' : groupPath(filters.groupId, groups)}</span>}
            {filters.groupIds.length > 0 && <span>任一分组：{filters.groupIds.map(id => groups.some(group => group.groupId === id) ? groupPath(id, groups) : '待处理的旧分组条件').join('、')}</span>}
            <button type="button" className="text-[var(--color-accent)] hover:underline" onClick={() => onChange({ ...filters, groupId: '', groupIds: [] })}>清除分组条件</button>
          </div>}
        </>
      )}
    </div>
  )
}
