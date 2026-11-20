import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Play, Search } from 'lucide-react'
import { Badge, Button, Modal, toast } from '../../../shared/components'
import { startBrowserInstanceByCode } from '../api'
import type { BrowserProfile } from '../types'
import { resolveActionFeedback } from '../utils/actionErrors'
import { groupInstanceCounts, groupPath, profileGroupIds, profileMatchesGroup } from '../utils/instanceGroups'
import { buildSearchText, GROUP_ALL, GROUP_UNGROUPED, normalizeCode, normalizeText } from './QuickLaunchModal.helpers'
import { useQuickLaunchData } from './useQuickLaunchData'

export function QuickLaunchModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [query, setQuery] = useState('')
  const [groupFilter, setGroupFilter] = useState(GROUP_ALL)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [startingCode, setStartingCode] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const { profiles, groups, loading } = useQuickLaunchData(open, inputRef)
  const counts = useMemo(() => groupInstanceCounts(profiles, groups), [profiles, groups])
  const filtered = useMemo(() => profiles.filter(profile => profileMatchesGroup(profile, groupFilter, groups)
    && (!normalizeText(query) || `${buildSearchText(profile)} ${profileGroupIds(profile).map(id => groupPath(id, groups)).join(' ')}`.toLowerCase().includes(normalizeText(query)))), [profiles, groups, groupFilter, query])

  useEffect(() => { if (open) { setQuery(''); setGroupFilter(GROUP_ALL); setSelectedIndex(0); setStartingCode('') } }, [open])
  useEffect(() => { setSelectedIndex(0) }, [query, groupFilter])
  useEffect(() => { if (selectedIndex >= filtered.length) setSelectedIndex(0) }, [filtered.length, selectedIndex])

  const startByCode = async (code: string) => {
    const normalized = normalizeCode(code)
    if (!normalized || startingCode) return
    setStartingCode(normalized)
    try {
      const profile = await startBrowserInstanceByCode(normalized)
      if (profile?.runtimeWarning) toast.warning(`实例「${profile.profileName || normalized}」已启动：${profile.runtimeWarning}`)
      onClose()
    } catch (error) {
      const feedback = resolveActionFeedback(error, '按 Code 启动失败')
      if (feedback.tone === 'warning') toast.warning(feedback.message)
      else toast.error(feedback.message)
    } finally { setStartingCode('') }
  }
  const startProfile = (profile: BrowserProfile) => {
    if (!profile.launchCode) { toast.error('该实例尚未分配 Code，请先在实例列表设置'); return }
    void startByCode(profile.launchCode)
  }
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') { event.preventDefault(); onClose() }
    if (event.key === 'Enter') {
      event.preventDefault()
      if (filtered[selectedIndex]) startProfile(filtered[selectedIndex])
      else if (query.trim()) void startByCode(query)
    }
    if (filtered.length && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      event.preventDefault()
      setSelectedIndex(index => (index + (event.key === 'ArrowDown' ? 1 : -1) + filtered.length) % filtered.length)
    }
  }
  return <Modal open={open} onClose={onClose} title="快速启动浏览器" width="960px">
    <div className="space-y-4" onKeyDown={onKeyDown}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative flex-1 min-w-48">
          <Search className="absolute left-3 top-2.5 h-4 w-4 text-[var(--color-text-muted)]" />
          <input ref={inputRef} value={query} onChange={event => setQuery(event.target.value)}
            className="w-full rounded-lg border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] py-2 pl-9 pr-3 text-sm"
            placeholder="输入 Code、实例名、分组或关键字" aria-label="搜索启动实例" />
        </div>
        <Button onClick={() => void startByCode(query)} disabled={!query.trim() || !!startingCode}>按 Code 启动</Button>
      </div>
      <div className="flex items-center gap-3 text-sm">
        <label htmlFor="quick-launch-group">实例分组</label>
        <select id="quick-launch-group" value={groupFilter} onChange={event => setGroupFilter(event.target.value)}
          className="min-w-48 rounded border border-[var(--color-border-default)] bg-[var(--color-bg-surface)] p-2">
          <option value={GROUP_ALL}>全部（{profiles.length}）</option>
          <option value={GROUP_UNGROUPED}>未分组（{profiles.filter(profile => !profileGroupIds(profile).length).length}）</option>
          {groups.map(group => <option key={group.groupId} value={group.groupId}>{groupPath(group.groupId, groups)}（{counts[group.groupId] || 0}）</option>)}
        </select>
        <span className="text-[var(--color-text-muted)]">↑↓ 选择 · Enter 启动</span>
      </div>
      <div className="max-h-[55vh] space-y-1 overflow-y-auto" role="list" aria-label="可启动实例">
        {loading ? <p className="py-8 text-center">加载中…</p> : !filtered.length ? <p className="py-8 text-center">没有匹配的实例</p> : filtered.map((profile, index) =>
          <div key={profile.profileId} role="listitem" onMouseEnter={() => setSelectedIndex(index)}
            className={`flex items-center gap-3 rounded-lg border p-3 ${index === selectedIndex ? 'border-[var(--color-accent)] bg-[var(--color-bg-muted)]' : 'border-transparent'}`}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2"><span className="font-medium">{profile.profileName}</span><code className="text-xs">{profile.launchCode}</code>
                <Badge variant={profile.running ? 'success' : 'default'}>{profile.running ? '运行中' : '已停止'}</Badge></div>
              <div className="truncate text-xs text-[var(--color-text-muted)]">{profileGroupIds(profile).map(id => groupPath(id, groups)).join('、') || '未分组'}</div>
            </div>
            <Button size="sm" onClick={() => startProfile(profile)} disabled={!!startingCode} loading={!!profile.launchCode && startingCode === profile.launchCode}>
              <Play className="mr-1 h-3.5 w-3.5" />启动
            </Button>
          </div>)}
      </div>
    </div>
  </Modal>
}
