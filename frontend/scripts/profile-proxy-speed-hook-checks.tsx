import { memo, useLayoutEffect, useState } from 'react'
import { flushSync } from 'react-dom'
import { createRoot } from 'react-dom/client'

import { useProfileProxySpeedResult } from '../src/modules/browser/profileProxySpeed'
import { useProxySpeedStore, type ProxySpeedResult } from '../src/modules/browser/proxySpeedStore'

// Bundle this test entry separately and run it in a real browser. It mounts the
// production hook against the real Zustand/React subscription implementation;
// it does not alter application data, call Wails APIs, or measure performance.
interface RowSpec {
  id: string
  target: string
  members?: Set<string>
  now: number
}

export async function runProfileProxySpeedHookChecks() {
  const now = Date.parse('2026-11-10T00:00:00.000Z')
  const maxAge = 10_000
  const checks: string[] = []
  const commits = new Map<string, number>()
  const observed = new Map<string, { result: ProxySpeedResult | null; mount: number }>()
  const reactErrors: string[] = []
  const originalConsoleError = console.error
  let mounts = 0
  const makeResult = (proxyId: string, fields: Partial<ProxySpeedResult> = {}): ProxySpeedResult => ({
    proxyId, ok: true, latencyMs: 20, error: '', testedAt: new Date(now).toISOString(), ...fields,
  })
  const check = (condition: unknown, message: string) => {
    if (!condition) throw new Error(message)
  }
  const text = (value: ProxySpeedResult | null) => value ? (value.ok ? `${value.latencyMs}ms` : '失败') : '未测速'
  const host = document.createElement('div')
  host.hidden = true
  document.body.appendChild(host)
  const root = createRoot(host)
  const Row = memo(function Row({ row }: { row: RowSpec }) {
    const [mount] = useState(() => ++mounts)
    const result = useProfileProxySpeedResult(row.target, row.members, row.now, maxAge)
    useLayoutEffect(() => {
      commits.set(row.id, (commits.get(row.id) || 0) + 1)
      observed.set(row.id, { result, mount })
    })
    return <span data-profile-speed-probe={row.id}>{text(result)}</span>
  })
  let rows: RowSpec[] = [
    { id: 'exact', target: 'Alpha', now },
    { id: 'alias', target: 'alpha', now },
    { id: 'missing', target: 'late', now },
    { id: 'direct', target: '__direct__', now },
    { id: 'chain', target: 'chain-id', now },
    { id: 'custom', target: '', now },
    { id: 'group', target: 'virtual-id', members: new Set(['alpha', 'beta']), now },
    { id: 'empty-group', target: 'Alpha', members: new Set(), now },
    { id: 'mode', target: 'Alpha', now },
    { id: 'clock', target: 'Alpha', now },
    { id: 'collision', target: 'target', now },
    { id: 'failure', target: 'failed', now },
  ]
  const paint = () => root.render(<>{rows.map(row => <Row key={row.id} row={row} />)}</>)
  const step = async (action: () => void) => {
    flushSync(action)
    // Let React finish any passive subscription setup before the next update.
    await new Promise<void>(resolve => window.setTimeout(resolve, 0))
  }
  const selected = (id: string) => observed.get(id)?.result
  const count = (id: string) => commits.get(id) || 0
  const checkDOM = (id: string, expected: string) => {
    check(host.querySelector(`[data-profile-speed-probe="${id}"]`)?.textContent === expected, `${id}: expected ${expected}`)
  }
  const updateRow = async (id: string, fields: Partial<RowSpec>) => {
    rows = rows.map(row => row.id === id ? { ...row, ...fields } : row)
    await step(paint)
  }

  try {
    console.error = (...args: unknown[]) => {
      reactErrors.push(args.map(String).join(' '))
      originalConsoleError(...args)
    }
    useProxySpeedStore.getState().reset()
    const alpha = makeResult('Alpha')
    const beta = makeResult('Beta', { latencyMs: 30 })
    const direct = makeResult('__direct__', { latencyMs: 2 })
    const chain = makeResult('chain-id', { latencyMs: 40 })
    useProxySpeedStore.getState().setManyResults([alpha, beta, direct, chain])
    await step(paint)
    check(selected('exact') === alpha && selected('alias') === alpha, 'initial selection preserves original objects')
    checkDOM('exact', '20ms')
    checkDOM('group', '20ms')
    checkDOM('missing', '未测速')
    checkDOM('empty-group', '未测速')
    checkDOM('custom', '未测速')
    checks.push('initial-exact-alias-missing-and-empty-group-display')

    const before = new Map(commits)
    await step(() => useProxySpeedStore.getState().setResult(makeResult('unrelated')))
    for (const id of ['exact', 'alias', 'missing', 'direct', 'chain', 'custom', 'mode', 'clock', 'collision', 'failure']) {
      check(count(id) === before.get(id), `${id}: unrelated result caused a React commit`)
    }
    check(count('group') > (before.get('group') || 0), 'virtual groups must retain the full-map subscription')
    check(count('empty-group') > (before.get('empty-group') || 0), 'an empty virtual group remains a group')
    checks.push('unrelated-event-does-not-commit-ordinary-rows')

    const beforeTesting = new Map(commits)
    await step(() => useProxySpeedStore.getState().setManyTesting(['Alpha', 'unrelated'], true))
    for (const [id, previous] of beforeTesting) check(count(id) === previous, `${id}: testing-only update changed the result selection`)
    checks.push('testing-only-updates-retain-all-selections')

    const nextAlpha = makeResult('Alpha', { latencyMs: 12 })
    const beforeRelevant = new Map(commits)
    await step(() => useProxySpeedStore.getState().setResult(nextAlpha))
    check(selected('exact') === nextAlpha && selected('alias') === nextAlpha, 'bound aliases missed a new result object')
    for (const id of ['exact', 'alias', 'group', 'mode', 'clock']) check(count(id) > (beforeRelevant.get(id) || 0), `${id}: relevant result did not commit`)
    for (const id of ['missing', 'direct', 'chain', 'custom']) check(count(id) === beforeRelevant.get(id), `${id}: unrelated bound result committed`)
    checkDOM('alias', '12ms')
    checks.push('relevant-results-update-bound-rows-and-group')

    const late = makeResult('late', { latencyMs: 61 })
    await step(() => useProxySpeedStore.getState().setResult(late))
    check(selected('missing') === late, 'a cached absence hid a newly arriving result')
    checkDOM('missing', '61ms')
    checks.push('missing-result-arrival-invalidates-snapshot-cache')

    const mount = observed.get('mode')?.mount
    await updateRow('mode', { target: 'Beta' })
    check(selected('mode') === beta, 'changing the effective dynamic/fixed target retained the previous result')
    await updateRow('mode', { target: 'virtual-id', members: new Set(['alpha', 'beta']) })
    check(selected('mode') === nextAlpha, 'switching into a virtual group failed')
    await updateRow('mode', { target: 'Alpha', members: new Set() })
    check(selected('mode') === null, 'empty virtual groups must not use the target result')
    await updateRow('mode', { target: '', members: undefined })
    checkDOM('mode', '未测速')
    await updateRow('mode', { target: 'chain-id' })
    check(selected('mode') === chain, 'chain target lost its own result')
    await updateRow('mode', { target: '__direct__' })
    check(selected('mode') === direct, 'direct target lost its own result')
    check(observed.get('mode')?.mount === mount, 'proxy/mode changes remounted the row or changed hook state')
    checks.push('proxy-and-mode-transitions-preserve-hook-state')

    const snapshotBeforeTick = useProxySpeedStore.getState()
    await updateRow('clock', { now: now + maxAge + 1 })
    checkDOM('clock', '未测速')
    await updateRow('clock', { now })
    checkDOM('clock', '12ms')
    check(useProxySpeedStore.getState() === snapshotBeforeTick, 'clock test unexpectedly wrote the store')
    checks.push('clock-ticks-recheck-freshness-without-store-events')

    const staleExact = makeResult('target', { testedAt: new Date(now - maxAge - 1).toISOString() })
    await step(() => useProxySpeedStore.getState().setManyResults([makeResult(' Target '), staleExact]))
    checkDOM('collision', '未测速')
    const failure = makeResult('failed', { ok: false, latencyMs: -1 })
    await step(() => useProxySpeedStore.getState().setResult(failure))
    check(selected('failure') === failure, 'ordinary failure result must remain visible')
    checkDOM('failure', '失败')
    checks.push('stale-exact-priority-and-failure-display')

    await step(() => useProxySpeedStore.getState().setResult(makeResult('Alpha', { ok: false, latencyMs: -1 })))
    checkDOM('exact', '失败')
    check(selected('group') === beta, 'virtual group did not switch after its winner failed')
    await step(() => useProxySpeedStore.getState().prune(new Set(['Beta'])))
    checkDOM('exact', '未测速')
    checkDOM('missing', '未测速')
    check(selected('group') === beta, 'pruning unrelated members changed the remaining winner')
    await step(() => useProxySpeedStore.getState().reset())
    for (const row of rows) checkDOM(row.id, '未测速')
    checks.push('winner-failure-prune-and-reset-refresh-all-affected-selections')
    check(reactErrors.length === 0, `React emitted errors: ${reactErrors.join(' | ')}`)
    return { passed: true, checks, reactErrors }
  } finally {
    flushSync(() => root.unmount())
    useProxySpeedStore.getState().reset()
    host.remove()
    console.error = originalConsoleError
  }
}
