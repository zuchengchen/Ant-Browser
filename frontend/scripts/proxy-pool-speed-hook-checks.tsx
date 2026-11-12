import { Profiler, useLayoutEffect, type ComponentProps } from 'react'
import { flushSync } from 'react-dom'
import { createRoot } from 'react-dom/client'

import { ProxyPoolTableCard } from '../src/modules/browser/pages/proxyPool/ProxyPoolTableCard'
import { useProxyChecks } from '../src/modules/browser/pages/proxyPool/useProxyChecks'
import { useProxyPoolFilter } from '../src/modules/browser/pages/proxyPool/useProxyPoolFilter'
import { useProxySpeedStore, type ProxySpeedResult } from '../src/modules/browser/proxySpeedStore'
import { ConnectedProxyCoreDownloadModal, ConnectedProxyPoolImportModal, ConnectedProxySourceRefreshModal } from '../src/modules/browser/pages/proxyPool/ProxyPoolSpeedOptions'
import { useFetchProxyOptions, useDownloadProxyOptions, useProxyPoolSpeedValue } from '../src/modules/browser/pages/proxyPool/proxyPoolSpeed'
import type { ProxyDisplayInfo } from '../src/modules/browser/pages/proxyPool/helpers'
import { createInitialChainImportForm, INITIAL_DIRECT_IMPORT_FORM } from '../src/modules/browser/pages/proxyPool/helpers'
import type { BrowserProxy, BrowserProxyVirtualGroup, ProxyIPHealthResult } from '../src/modules/browser/types'
import type { SortOrder } from '../src/shared/components/Table'

// Browser-only behavior entry. It uses the real hooks, store, table, Button,
// Modal and filter. API-triggering table callbacks are recorded, not executed.
// Run in the existing isolated Wails/WebKit iframe with production CSS.
function check(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
const delay = (ms: number) => new Promise<void>(resolve => window.setTimeout(resolve, ms))
async function waitFor(predicate: () => boolean, label: string, timeout = 3000) {
  const start = performance.now()
  while (!predicate()) {
    if (performance.now() - start > timeout) throw new Error('Deadline: ' + label)
    await delay(10)
  }
}
async function settle() {
  await delay(0)
  await new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error('Two frame callbacks missing')), 2000)
    requestAnimationFrame(() => requestAnimationFrame(() => { clearTimeout(timer); resolve() }))
  })
}
function result(proxyId: string, extra: Partial<ProxySpeedResult> = {}): ProxySpeedResult {
  return { proxyId, ok: true, latencyMs: 20, error: '', testedAt: '2026-11-10T00:00:00Z', engine: 'xray', ...extra }
}
function record(proxyId: string, extra: Partial<ProxyDisplayInfo> = {}): ProxyDisplayInfo {
  return { proxyId, proxyName: proxyId, proxyConfig: 'http://127.0.0.1:1',
    preferredKernel: 'auto', groupName: '', sourceId: '', sourceUrl: '',
    sourceAutoRefresh: false, sourceRefreshIntervalM: 60, sourceLastRefreshAt: '',
    type: 'http', server: proxyId, port: 1, ...extra }
}

export async function runProxyPoolSpeedHookChecks() {
  const checks: string[] = []
  const reactErrors: string[] = []
  const originalError = console.error
  // The actual generated runtime wrapper calls this narrow transport boundary.
  // Keep the test's real speed/bridge listeners independent of the host app.
  const runtimeView = window as unknown as { runtime?: Record<string, unknown> }
  const originalRuntime = runtimeView.runtime
  const eventListeners = new Map<string, Set<(...args: unknown[]) => void>>()
  runtimeView.runtime = { EventsOnMultiple: (name: string, callback: (...args: unknown[]) => void, count: number) => {
    check(count === -1, 'Unexpected event transport contract')
    let listeners = eventListeners.get(name)
    if (!listeners) { listeners = new Set(); eventListeners.set(name, listeners) }
    listeners.add(callback)
    return () => listeners!.delete(callback)
  } }
  const emit = (name: string, value: unknown) => {
    const callbacks = eventListeners.get(name)
    check(callbacks?.size, 'Production event listener was not registered: ' + name)
    for (const callback of callbacks) callback(value)
  }
  const selected = new Set<string>()
  const actionIds: string[] = []
  const storageBefore = new Map<string, string>()
  for (let i = 0; i < localStorage.length; i += 1) {
    const key = localStorage.key(i)!
    storageBefore.set(key, localStorage.getItem(key)!)
  }
  console.error = (...args: unknown[]) => { reactErrors.push(args.map(String).join(' ')); originalError(...args) }
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  let displayList = [record('A'), record('B'), record('C'), record('D'), record('E'), record('F'),
    record('direct', { proxyConfig: 'direct://' }),
    record('virtual', { isVirtual: true, type: 'virtual', virtualMemberProxyIds: ['B'], virtualMemberCount: 1 })]
  let sortColumn = ''
  let sortOrder: SortOrder = undefined
  let available = false
  let keyword = ''
  let filterGroup = 'all'
  let virtualGroups: BrowserProxyVirtualGroup[] = []
  let legacyAggregate = false
  let pageCommits = 0
  let subtreeCommits = 0
  const selectedCommits: Record<string, number> = {}
  let currentMaps: ReturnType<typeof useProxyChecks> | undefined
  let shownIds: string[] = []
  const noop = () => {}
  function SubscriptionObserver({ id }: { id: string }) {
    useProxyPoolSpeedValue(id)
    useLayoutEffect(() => { selectedCommits[id] = (selectedCommits[id] || 0) + 1 })
    return null
  }
  function Harness() {
    const state = useProxyChecks(legacyAggregate ? { proxies: displayList } : {
      proxies: displayList,
      aggregateSpeed: available || (sortColumn === 'latency' && sortOrder !== undefined),
    })
    const filtered = useProxyPoolFilter({
      displayList, filterProtocol: 'all', filterKeyword: keyword, filterGroup,
      filterAvailableOnly: available, virtualGroups, sortColumn, sortOrder,
      latencyMap: state.latencyMap, ipHealthMap: state.ipHealthMap,
    })
    useLayoutEffect(() => {
      pageCommits += 1
      currentMaps = state
      shownIds = filtered.filteredList.map(item => item.proxyId)
    })
    return <><SubscriptionObserver id="A" /><SubscriptionObserver id="B" />
    <Profiler id="pool" onRender={() => { subtreeCommits += 1 }}><ProxyPoolTableCard
      allFilteredSelected={false} someFilteredSelected={false} selectedCount={selected.size} selectedIds={selected}
      checkingIPHealthIds={state.checkingIPHealthIds} ipHealthMap={state.ipHealthMap}
      data={filtered.filteredList} loading={false} filterProtocol="all" filterGroup={filterGroup}
      filterKeyword={keyword} filterAvailableOnly={available} groups={[]} virtualGroupOptions={[]}
      protocolOptions={filtered.protocolOptions} refreshingSourceIds={new Set()}
      sortColumn={sortColumn} sortOrder={sortOrder}
      onSort={value => { sortColumn = value.column; sortOrder = value.order; paint() }}
      onFilterAvailableOnlyChange={value => { available = value; paint() }}
      onFilterKeywordChange={value => { keyword = value; paint() }}
      onFilterGroupChange={noop} onFilterProtocolChange={noop} onClearFilters={noop}
      onCheckOneIPHealth={noop} onDelete={noop} onEdit={noop} onOpenBatchDelete={noop}
      onOpenIPHealthDetail={noop} onManageVirtualGroup={noop} onRefreshSingleSource={noop}
      onOpenVirtualGroupMembers={row => actionIds.push('group:' + row.proxyId)}
      onTestOne={row => actionIds.push('test:' + row.proxyId)}
      onToggleAll={noop} onToggleOne={id => { selected.add(id); paint() }}
    /></Profiler></>
  }
  function paint() { root.render(<Harness />) }
  function rows() { return Array.from(host.querySelectorAll<HTMLTableRowElement>('tbody tr')).filter(row => row.cells.length === 11) }
  function row(id: string) {
    const found = rows().find(item => item.cells[1].textContent === id)
    check(found, 'Missing full table row ' + id)
    return found
  }
  async function step(action: () => void) { flushSync(action); await settle() }

  let optionsRoot: ReturnType<typeof createRoot> | undefined
  let optionsHost: HTMLDivElement | undefined
  const optionEvidence: Array<Record<string, unknown>> = []
  try {
    useProxySpeedStore.getState().reset()
    await step(paint)
    await waitFor(() => rows().length === displayList.length, 'initial complete 11-column table')
    check(rows().every(item => item.cells.length === 11), 'Column count changed')
    check(row('direct').cells[7].textContent === '不适用', 'Direct latency changed')
    check(row('virtual').cells[7].textContent === '按成员节点', 'Virtual latency changed')
    check(row('virtual').cells[8].textContent === '虚拟分组', 'Virtual engine changed')
    checks.push('complete_dom_direct_and_virtual_labels')

    const parentBefore = pageCommits
    const unrelatedBefore = subtreeCommits
    const selectedBefore = { ...selectedCommits }
    const nodes = rows()
    await step(() => useProxySpeedStore.getState().setResult(result('unrelated')))
    check(pageCommits === parentBefore && subtreeCommits === unrelatedBefore, 'Unrelated result rendered the page/table subtree')
    check(selectedCommits.A === selectedBefore.A && selectedCommits.B === selectedBefore.B, 'Unrelated result committed single-ID subscribers')
    await step(() => emit('proxy:speed:result', result('A', { latencyMs: 41 })))
    check(row('A').cells[7].textContent === '41 ms' && row('A').cells[8].textContent === 'xray', 'Relevant result was not displayed immediately')
    check(pageCommits === parentBefore, 'Default page rerendered for a relevant row result')
    check(selectedCommits.A > selectedBefore.A && selectedCommits.B === selectedBefore.B, 'Relevant result failed to isolate the unchanged selector reference')
    check(rows().every((item, i) => item === nodes[i]), 'Result update replaced table row DOM')
    await step(() => useProxySpeedStore.getState().setResult(result('a', { latencyMs: 1 })))
    check(row('A').cells[7].textContent === '41 ms', 'Pool introduced case-insensitive alias matching')
    checks.push('default_subscriptions_keep_page_and_unrelated_rows_stable')

    await step(() => useProxySpeedStore.getState().setTesting('A', true))
    check(row('A').cells[7].textContent === '测试中...' && row('A').cells[8].textContent === '-', 'Testing did not override old result')
    check(row('A').querySelector<HTMLButtonElement>('button[title="测速"]')!.disabled, 'Testing button was not disabled')
    const testingCommits = subtreeCommits
    await step(() => useProxySpeedStore.getState().setResult(result('A', { ok: false, error: 'first refusal', engine: 'mihomo' })))
    check(row('A').cells[7].textContent === '测试中...' && subtreeCommits === testingCommits, 'Hidden result interrupted testing state or caused a needless row commit')
    await step(() => useProxySpeedStore.getState().setTesting('A', false))
    check(row('A').cells[7].textContent === '失败' && row('A').cells[7].querySelector('span')?.title === 'first refusal', 'Testing completion did not reveal latest failure')
    check(row('A').cells[8].textContent === 'mihomo', 'Engine result stayed stale')
    await step(() => useProxySpeedStore.getState().setResult(result('A', { ok: false, error: 'second refusal', engine: 'mihomo' })))
    check(row('A').cells[7].querySelector('span')?.title === 'second refusal', 'Same failure class retained stale error tooltip')
    check(pageCommits === parentBefore, 'Testing/failure updates reached default page render')
    checks.push('testing_latest_result_error_and_button_semantics')

    await step(() => {
      useProxySpeedStore.getState().reset()
      useProxySpeedStore.getState().setManyResults([
        result('A', { latencyMs: 20 }), result('B', { latencyMs: 0 }),
        result('D', { ok: false, error: 'timeout' }), result('E', { ok: false, error: '协议不支持' }),
        result('F', { ok: false, error: 'refused' }),
      ])
      useProxySpeedStore.getState().setTesting('C', true)
      sortColumn = 'latency'; sortOrder = 'asc'; paint()
    })
    check(shownIds.join(',') === 'B,A,C,D,E,F,direct,virtual', 'Latency rank/tie ascending order changed')
    await step(() => { sortOrder = 'desc'; paint() })
    check(shownIds.join(',') === 'virtual,direct,F,E,D,C,A,B', 'Latency descending did not reverse all ranks')
    await step(() => { sortOrder = undefined; sortColumn = ''; available = true; paint() })
    check(shownIds.join(',') === 'A,B,virtual', 'Available filter lost zero latency or virtual member')
    await step(() => currentMaps!.setIPHealthMap({ F: { proxyId: 'F', ok: true } as ProxyIPHealthResult }))
    check(shownIds.join(',') === 'A,B,F,virtual', 'IP-only availability did not update list')
    await step(() => useProxySpeedStore.getState().setResult(result('B', { ok: false, error: 'refused' })))
    check(shownIds.join(',') === 'A,F', 'Virtual group stayed available after sole member failed')
    checks.push('latency_sort_available_zero_ip_health_and_virtual_members')

    await step(() => {
      available = false; legacyAggregate = true; paint()
    })
    const legacyBefore = pageCommits
    await step(() => useProxySpeedStore.getState().setResult(result('D', { latencyMs: 99 })))
    check(pageCommits > legacyBefore && currentMaps!.latencyMap.D === 99, 'Default aggregate=true compatibility lost')
    await step(() => { legacyAggregate = false; paint() })
    const offBefore = pageCommits
    await step(() => useProxySpeedStore.getState().setResult(result('D', { latencyMs: 100 })))
    check(pageCommits === offBefore && row('D').cells[7].textContent === '100 ms', 'Disabling aggregate broke leaf freshness or parent isolation')
    await step(() => { keyword = 'B'; paint() })
    check(shownIds.join(',') === 'B', 'Name filter changed')
    await step(() => { keyword = ''; paint() })
    check(rows().length === displayList.length, 'Restore omitted complete rows')
    flushSync(() => row('A').querySelector<HTMLButtonElement>('button[title="测速"]')!.click())
    flushSync(() => row('virtual').click())
    check(actionIds.join(',') === 'test:A,group:virtual', 'Actions used stale/wrong record')
    checks.push('aggregate_toggle_default_compatibility_filter_restore_and_actions')

    // Replacing a record/proxy ID changes the selected exact key immediately.
    await step(() => { displayList = [record('NEW')]; paint() })
    await step(() => useProxySpeedStore.getState().setResult(result('NEW', { latencyMs: 73 })))
    check(row('NEW').cells[7].textContent === '73 ms', 'Replacement record kept previous subscription')
    await step(() => useProxySpeedStore.getState().prune(new Set(['unrelated'])))
    check(row('NEW').cells[7].textContent === '未测速', 'Prune did not refresh selected result')
    await step(() => useProxySpeedStore.getState().setResult(result('NEW', { latencyMs: 74 })))
    await step(() => useProxySpeedStore.getState().reset())
    check(row('NEW').cells[7].textContent === '未测速', 'Reset did not refresh selected result')
    checks.push('record_change_prune_and_reset')

    // Existing availability aliases trim/lowercase and let the final catalog
    // duplicate win. Row rendering itself must still use exact store keys.
    await step(() => {
      displayList = [record(' Alpha ', { groupName: 'North' }), record('ALPHA', { groupName: 'South' }),
        record('third', { groupName: ' North ' }), record('direct', { proxyConfig: 'direct://', groupName: 'North' }),
        record('virtual', { isVirtual: true, type: 'virtual', virtualMemberProxyIds: [' alpha '] })]
      available = true; paint()
    })
    await step(() => useProxySpeedStore.getState().setManyResults([
      result(' Alpha ', { latencyMs: 8 }), result('ALPHA', { ok: false, error: 'refused' }),
    ]))
    check(shownIds.length === 0, 'Normalized availability stopped selecting the last duplicate catalog ID')
    await step(() => useProxySpeedStore.getState().setResult(result('ALPHA', { latencyMs: 0 })))
    check(shownIds.join('|') === ' Alpha |ALPHA|virtual', 'Normalized virtual member lost zero-latency availability')
    check(row(' Alpha ').cells[7].textContent === '8 ms' && row('ALPHA').cells[7].textContent === '0 ms', 'Row selectors accidentally normalized exact result keys')
    await step(() => {
      available = false
      virtualGroups = [{ groupId: 'vg', groupName: 'vg', strategy: 'round_robin',
        memberProxyIds: [' alpha '], memberGroupNames: [' north '] }]
      filterGroup = '__virtual_proxy_group__:vg'; paint()
    })
    check(shownIds.join('|') === ' Alpha |ALPHA|third', 'Virtual group ID/group-name filter changed or admitted direct')
    checks.push('exact_rows_normalized_virtual_members_and_group_filters')

    // Option updates live below a parent that does not subscribe to the store.
    // The actual download Modal also proves its original 680ms exit remains.
    optionsHost = document.createElement('div'); document.body.appendChild(optionsHost)
    optionsRoot = createRoot(optionsHost)
    const catalog = [{ ...record('Q'), lastLatencyMs: 1 }] as unknown as BrowserProxy[]
    let optionsParentCommits = 0
    let optionsOpen = false
    let importOpen = false
    let sourceOpen = false
    let fetchIds = ''
    let downloads = ''
    const savedSources: string[] = []
    const sources = [{ sourceId: 'source', sourceUrl: 'http://127.0.0.1:1/subscription',
      sourceNamePrefix: '', sourceGroupName: '', sourceDnsServers: '', sourceAutoRefresh: true,
      sourceRefreshIntervalM: 60, sourceLastRefreshAt: '', sourceFetchProxyId: 'Q', sourceLastRefreshError: '' }]
    function OptionsSink() {
      const fetch = useFetchProxyOptions(catalog)
      const download = useDownloadProxyOptions(catalog)
      useLayoutEffect(() => {
        fetchIds = fetch.map(p => p.proxyId + ':' + p.lastLatencyMs).join(',')
        downloads = download.map(p => p.label).join(',')
      })
      return null
    }
    function OptionsParent() {
      useLayoutEffect(() => { optionsParentCommits += 1 })
      const props: ComponentProps<typeof ConnectedProxyCoreDownloadModal> = {
        proxies: catalog, open: optionsOpen, core: 'xray', goos: 'linux', goarch: 'amd64',
        downloadProxy: catalog[0].proxyConfig, progress: null, status: null, statusLoading: false,
        onCoreChange: noop, onGOOSChange: noop, onGOARCHChange: noop, onDownloadProxyChange: noop,
        onClose: noop, onStart: noop,
      }
      return <><OptionsSink /><ConnectedProxyCoreDownloadModal {...props} />
        <ConnectedProxyPoolImportModal proxies={catalog} open={importOpen} groups={[]} importMode="clash"
          importUrl="http://127.0.0.1:1/retained" importFetchProxyId="Q" importAutoRefresh={false}
          importRefreshIntervalM={60} importResolvedUrl="" importText="retained draft" importDnsServers=""
          importNamePrefix="" importGroupName="" chainImportText="" directImportText=""
          chainImportForm={createInitialChainImportForm()} directImportForm={INITIAL_DIRECT_IMPORT_FORM}
          fetchingImportUrl={false} canParseImport={true} onClose={noop} onParse={noop} onFetchImportUrl={noop}
          onImportModeChange={noop} onImportUrlChange={noop} onImportFetchProxyIdChange={noop}
          onImportAutoRefreshChange={noop} onImportRefreshIntervalMChange={noop} onImportTextChange={noop}
          onImportDnsServersChange={noop} onImportNamePrefixChange={noop} onImportGroupNameChange={noop}
          onChainImportTextChange={noop} onDirectImportTextChange={noop} onApplyChainJSON={noop}
          onApplyDirectText={noop} onChainImportFormChange={noop} onChainImportHopChange={noop}
          onFillChainTemplate={noop} onCopyChainTemplate={noop} onFillDirectTemplate={noop}
          onCopyDirectTemplate={noop} onDirectImportFormChange={noop} />
        <ConnectedProxySourceRefreshModal proxies={catalog} open={sourceOpen} sources={sources}
          refreshingAll={false} refreshingSourceIds={new Set()} onClose={noop} onRefreshAll={noop}
          onRefreshOne={noop} onReloadProxies={async () => {}}
          onSaveSourceConfig={async (id, auto, interval, fetchId) => { savedSources.push(`${id}:${auto}:${interval}:${fetchId}`) }} />
      </>
    }
    const paintOptions = () => optionsRoot!.render(<OptionsParent />)
    await step(paintOptions)
    const optionsBefore = optionsParentCommits
    await step(() => useProxySpeedStore.getState().setResult(result('Q', { latencyMs: 12 })))
    check(fetchIds === 'Q:12' && downloads === 'Q · 12ms' && optionsParentCommits === optionsBefore, 'Closed option consumers updated their parent or lost latest data')
    await step(() => { optionsOpen = true; paintOptions() })
    const surface = () => Array.from(document.querySelectorAll<HTMLElement>('.modal-surface'))
      .find(element => element.querySelector('h3')?.textContent === '下载代理内核')
    await waitFor(() => !!surface(), 'download Modal first open')
    check(surface()!.textContent!.includes('Q · 12ms'), 'First open options were stale')
    const sameSurface = surface()
    const closingStarted = performance.now()
    await step(() => { optionsOpen = false; paintOptions() })
    await waitFor(() => !!surface()?.classList.contains('animate-modal-out'), 'download Modal exit commit', 500)
    await step(() => useProxySpeedStore.getState().setResult(result('Q', { latencyMs: 15 })))
    check(surface() === sameSurface && surface()!.textContent!.includes('Q · 15ms'), 'Exit erased options or remounted Modal')
    optionEvidence.push({ duringExitMs: performance.now() - closingStarted, sameSurface: true, currentOption: 'Q · 15ms' })
    await waitFor(() => !surface(), 'download Modal exits normally')
    check(performance.now() - closingStarted >= 650, 'Download exit was truncated')
    await step(() => { optionsOpen = true; paintOptions() })
    await waitFor(() => !!surface(), 'download Modal reopens')
    check(surface()!.textContent!.includes('Q · 15ms'), 'Reopened options stale')
    await step(() => { optionsOpen = false; paintOptions() })
    await waitFor(() => !!surface()?.classList.contains('animate-modal-out'), 'download interrupted exit commit', 500)
    const reopenSurface = surface()
    await step(() => { optionsOpen = true; paintOptions() })
    await delay(750)
    check(surface() === reopenSurface && !surface()!.classList.contains('animate-modal-out'), 'Old closing timer removed/replaced reopened Modal')
    await step(() => { optionsOpen = false; paintOptions() })
    await waitFor(() => !surface(), 'download closes before import check')

    const surfaceFor = (title: string) => Array.from(document.querySelectorAll<HTMLElement>('.modal-surface'))
      .find(element => element.querySelector('h3')?.textContent === title)
    await step(() => { importOpen = true; paintOptions() })
    await waitFor(() => !!surfaceFor('导入代理配置'), 'import first open')
    const importSurface = surfaceFor('导入代理配置')!
    const importSelect = () => importSurface.querySelector<HTMLSelectElement>('select')!
    check(importSelect().value === 'Q' && importSelect().textContent!.includes('Q · 15ms'), 'Import option/selected ID stale on first open')
    await step(() => useProxySpeedStore.getState().setResult(result('Q', { latencyMs: 17 })))
    check(importSelect().value === 'Q' && importSelect().textContent!.includes('Q · 17ms'), 'Import live option update lost current selection')
    await step(() => { importOpen = false; paintOptions() })
    await waitFor(() => importSurface.classList.contains('animate-modal-out'), 'import exit commit', 500)
    await step(() => useProxySpeedStore.getState().setResult(result('Q', { latencyMs: 19 })))
    check(surfaceFor('导入代理配置') === importSurface && importSelect().textContent!.includes('Q · 19ms')
      && importSurface.querySelector<HTMLTextAreaElement>('textarea')!.value === 'retained draft', 'Import exit lost live options, draft or Modal identity')
    await waitFor(() => !surfaceFor('导入代理配置'), 'import closes normally')

    await step(() => { sourceOpen = true; paintOptions() })
    await waitFor(() => !!surfaceFor('订阅更新管理'), 'source first open')
    const sourceSurface = surfaceFor('订阅更新管理')!
    const edit = Array.from(sourceSurface.querySelectorAll('button')).find(button => button.textContent === '编辑')
    check(edit, 'Source edit action missing')
    await step(() => edit.click())
    const sourceSelect = () => sourceSurface.querySelector<HTMLSelectElement>('select')!
    check(sourceSelect().value === 'Q' && sourceSelect().textContent!.includes('Q · 19ms'), 'Source options stale entering draft')
    await step(() => useProxySpeedStore.getState().setResult(result('Q', { latencyMs: 21 })))
    check(sourceSelect().value === 'Q' && sourceSelect().textContent!.includes('Q · 21ms'), 'Source option update reset edited selection')
    const save = Array.from(sourceSurface.querySelectorAll('button')).find(button => button.textContent === '保存')
    check(save, 'Source save action missing')
    await step(() => save.click())
    check(savedSources.join(',') === 'source:true:60:Q', 'Source draft saved stale/wrong values')
    await step(() => { sourceOpen = false; paintOptions() })
    await waitFor(() => !!surfaceFor('订阅更新管理')?.classList.contains('animate-modal-out'), 'source exit commit', 500)
    await waitFor(() => !surfaceFor('订阅更新管理'), 'source closes normally')
    checks.push('modal_options_isolate_parent_and_preserve_exit_updates')

    check(reactErrors.length === 0, 'React emitted errors: ' + reactErrors.join(' | '))
    return { passed: true, diagnostic: true, formal_samples: false, checks,
      pageCommits, subtreeCommits, selectedCommits, optionEvidence, reactErrors,
      limitations: ['Table/download/source-save API callbacks are controlled.', 'No backend or trusted-native input correctness/performance claim.'] }
  } finally {
    if (optionsRoot) flushSync(() => optionsRoot!.unmount())
    optionsHost?.remove()
    flushSync(() => root.unmount())
    host.remove()
    useProxySpeedStore.getState().reset()
    localStorage.clear()
    for (const [key, value] of storageBefore) localStorage.setItem(key, value)
    console.error = originalError
    runtimeView.runtime = originalRuntime
  }
}
