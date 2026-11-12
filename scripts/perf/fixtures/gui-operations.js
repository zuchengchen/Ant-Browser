// Inject only into the test Wails process. This never substitutes product APIs.
(() => {
  const profileSelector = 'input[placeholder="搜索名称/快捷码/关键字..."]'
  const proxySelector = 'input[placeholder="搜索名称或服务器..."]'
  let proxyCatalog = new Map()
  let preparedKeyword = null
  function requireElement(selector, root = document) {
    const element = root.querySelector(selector)
    if (!element) throw new Error(`Missing required element: ${selector}`)
    return element
  }

  function setInput(input, value) {
    if (!input.isConnected || input.disabled) throw new Error('Input is disconnected or disabled')
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
    let count = 0
    const capture = event => { if (event.target === input) count++ }
    input.addEventListener('input', capture, true)
    setter.call(input, value)
    try { input.dispatchEvent(new Event('input', { bubbles: true })) }
    finally { input.removeEventListener('input', capture, true) }
    if (count !== 1) throw new Error('Expected exactly one input event')
  }

  function exactButton(text, root = document) {
    const found = [...root.querySelectorAll('button')].filter(button => button.textContent.trim() === text)
    if (found.length !== 1) throw new Error(`Expected one ${text} button; got ${found.length}`)
    if (found[0].disabled) throw new Error(`Disabled ${text} button`)
    return found[0]
  }

  function profileRows() {
    return [...document.querySelectorAll('tbody a[href^="/browser/detail/"]')]
      .map(link => ({ id: decodeURIComponent(link.getAttribute('href').slice('/browser/detail/'.length)), name: link.textContent.trim() }))
  }

  function proxyNames() {
    return [...document.querySelectorAll('tbody tr td:nth-child(2) span[title]')]
      .map(span => span.getAttribute('title'))
  }

  function sameList(actual, expected) {
    if (actual.length !== expected.length) return false
    if (new Set(actual).size !== actual.length || new Set(expected).size !== expected.length) return false
    const actualSorted = [...actual].sort()
    const expectedSorted = [...expected].sort()
    return actualSorted.every((value, index) => value === expectedSorted[index])
  }

  function nextFrame(deadline) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { cancelAnimationFrame(frame); reject(new Error('Rendered frame deadline exceeded')) }, Math.max(0, deadline - performance.now()))
      const frame = requestAnimationFrame(rafMs => {
        clearTimeout(timer)
        resolve({ raf_ms: rafMs, callback_ms: performance.now() })
      })
    })
  }

  async function waitRendered(predicate, description, timeoutMs = 1800000) {
    const deadline = performance.now() + timeoutMs
    while (performance.now() < deadline) {
      if (predicate()) {
        const predicateMs = performance.now()
        const attempts = []
        const invalidFrame = message => new Error(`${message}: ${description}; ${JSON.stringify({
          predicate_ms: predicateMs, deadline_ms: deadline, attempt_count: attempts.length,
          first: attempts[0], previous: attempts.at(-2), last: attempts.at(-1),
        })}`)
        do {
          let frame
          try { frame = await nextFrame(deadline) }
          catch (error) { throw invalidFrame(error.message) }
          const previous = attempts.at(-1)
          attempts.push(frame)
          if (![frame.raf_ms, frame.callback_ms].every(value => typeof value === 'number' && Number.isFinite(value) && value >= 0)) {
            throw invalidFrame('Invalid rendered-frame clock')
          }
          if (frame.callback_ms < (previous?.callback_ms ?? predicateMs) || (previous && frame.raf_ms < previous.raf_ms)) {
            throw invalidFrame('Rendered-frame clock moved backwards')
          }
          if (performance.now() >= deadline) throw invalidFrame('Rendered result exceeded deadline')
          // Callback execution clocks may be coarsened. Distinct native rAF
          // timestamps prove two frames; repeated stamps keep the original deadline.
        } while (attempts.length < 2 || attempts.at(-1).raf_ms === attempts[0].raf_ms)
        if (performance.now() >= deadline) throw new Error(`Rendered result exceeded deadline: ${description}`)
        if (predicate()) {
          const completedMs = performance.now()
          if (completedMs >= deadline) throw invalidFrame('Rendered result exceeded deadline')
          if (completedMs < attempts.at(-1).callback_ms) throw invalidFrame('Rendered completion clock moved backwards')
          return { render_proof_version: 2, frame_count: 2, predicate_before: true, predicate_after: true,
            predicate_ms: predicateMs, first_frame_ms: attempts[0].callback_ms,
            second_frame_ms: attempts.at(-1).callback_ms, completed_ms: completedMs,
            deadline_ms: deadline, frame_attempts: attempts }
        }
      }
      await new Promise(resolve => setTimeout(resolve, 10))
    }
    throw new Error(`Timed out waiting for actual rendered result: ${description}`)
  }

  function controlled(input, selector, value, persisted = false) {
    const matching = document.querySelectorAll(selector)
    if (!input || !input.isConnected || matching.length !== 1 || matching[0] !== input || input.value !== value || input.defaultValue !== value) return false
    if (persisted && JSON.parse(localStorage.getItem('browser:filters') || 'null')?.keyword !== value) return false
    return document.visibilityState === 'visible'
  }

  function exactProfileRows(expectedIDs) {
    const rows = profileRows()
    return sameList(rows.map(row => row.id), expectedIDs) && rows.every(row =>
      /^perf-profile-\d{5}$/.test(row.id) && row.name === `性能实例 ${row.id.slice(-5)}`)
  }

  function proxyRows() {
    return [...document.querySelectorAll('tbody tr')].flatMap(row => {
      const cells = row.querySelectorAll('td')
      if (cells.length < 7) return []
      const labels = cells[1].querySelectorAll('span[title]')
      if (labels.length !== 1) throw new Error('Ambiguous proxy row name')
      const name = labels[0].getAttribute('title'), proxy = proxyCatalog.get(name)
      if (!proxy) throw new Error(`Rendered proxy is absent from verified API catalog: ${name}`)
      if (proxy.proxyId !== '__direct__') {
        const port = String(20000 + Number(proxy.proxyId.slice(-5)))
        if (cells[2].textContent.trim() !== proxy.groupName || cells[5].textContent.trim() !== '127.0.0.1' || cells[6].textContent.trim() !== port) throw new Error('Rendered proxy fields do not match its unique API identity')
      }
      return [{ id: proxy.proxyId, name }]
    })
  }

  async function catalogAPI(kind, size, proxies = true) {
    if (!Number.isInteger(size) || size < 1) throw new Error('Invalid catalog size')
    const ids = Array.from({ length: size }, (_, i) => `perf-profile-${String(i).padStart(5, '0')}`)
    if (kind === 'profiles') {
      const items = await window.go.main.App.BrowserProfileList()
      if (!sameList(items.map(item => item.profileId), ids) || !items.every(item =>
        item.profileName === `性能实例 ${item.profileId.slice(-5)}` && item.proxyId === (proxies ? `perf-proxy-${item.profileId.slice(-5)}` : '__direct__'))) throw new Error('Profile API exact IDs, names or bindings differ')
      return { kind, ids: items.map(item => item.profileId), names: items.map(item => item.profileName), count: items.length }
    }
    if (kind === 'proxies') {
      const items = await window.go.main.App.BrowserProxyList()
      const expected = proxies ? ids.map(id => id.replace('profile', 'proxy')) : []
      expected.push('__direct__')
      if (!sameList(items.map(item => item.proxyId), expected) || !items.every(item => {
        if (item.proxyId === '__direct__') return item.proxyName === '直连（不走代理）' && item.proxyConfig === 'direct://'
        const index = Number(item.proxyId.slice(-5))
        return item.proxyName === `性能代理 ${item.proxyId.slice(-5)}` && item.proxyConfig === `http://127.0.0.1:${20000 + index}` && item.groupName === `性能分组 ${index % 10}`
      })) throw new Error('Proxy API exact IDs, names, addresses or builtin-direct exception differ')
      if (new Set(items.map(item => item.proxyName)).size !== items.length) throw new Error('Ambiguous proxy API names')
      proxyCatalog = new Map(items.map(item => [item.proxyName, item]))
      return { kind, ids: items.map(item => item.proxyId), names: items.map(item => item.proxyName), count: items.length, builtin_direct: '__direct__' }
    }
    if (kind === 'groups') {
      const items = await window.go.main.App.ListGroups()
      // The fixed seed has no instance groups; proxy group names are separate.
      if (!Array.isArray(items) || items.length !== 0) throw new Error('Instance group API differs from the empty fixed seed')
      return { kind, ids: [], names: [], count: 0 }
    }
    if (kind === 'config') {
      const settings = await window.go.main.App.GetBrowserSettings()
      if (settings.userDataRoot !== 'data/profiles' || settings.defaultConnectorType !== 'xray') throw new Error('Browser config API differs from the fixed local seed')
      return { kind, settings }
    }
    throw new Error(`Unregistered catalog API: ${kind}`)
  }

  async function initialReady(size, proxies = true) {
    const ids = Array.from({ length: size }, (_, i) => `perf-profile-${String(i).padStart(5, '0')}`)
    const predicate = () => location.pathname === '/browser/list' && exactProfileRows(ids)
      && controlled(document.querySelector(profileSelector), profileSelector, '', true)
    await waitRendered(predicate, 'initial complete profile IDs and names')
    const profiles = await catalogAPI('profiles', size, proxies)
    const proxyResult = await catalogAPI('proxies', size, proxies)
    const render = await waitRendered(predicate, 'initial verified catalog')
    return { rows: profileRows(), profiles, proxies: proxyResult, render }
  }

  async function filterProfiles(value, expectedIDs, timeoutMs = 1800000) {
    if (location.pathname !== '/browser/list') throw new Error('Wrong route for profile filter')
    const input = requireElement(profileSelector)
    const started = performance.now()
    setInput(input, value)
    const render = await waitRendered(() => controlled(input, profileSelector, value, true) && exactProfileRows(expectedIDs), 'profile filter IDs, names and controlled input', timeoutMs)
    return { elapsedMs: performance.now() - started, value, rows: profileRows(), render }
  }

  async function filterProxies(value, expectedNames, timeoutMs = 1800000) {
    if (location.pathname !== '/browser/proxy-pool') throw new Error('Wrong route for proxy filter')
    const input = requireElement(proxySelector)
    const started = performance.now()
    setInput(input, value)
    const render = await waitRendered(() => controlled(input, proxySelector, value) && sameList(proxyRows().map(row => row.name), expectedNames), 'proxy filter exact identities and controlled input', timeoutMs)
    return { elapsedMs: performance.now() - started, value, names: proxyNames(), rows: proxyRows(), render }
  }

  async function openKeyword(profileId, profileName) {
    const rowLink = () => [...document.querySelectorAll('tbody a[href^="/browser/detail/"]')]
      .find(link => link.getAttribute('href') === `/browser/detail/${profileId}`)
    const row = rowLink()?.closest('tr')
    if (!row) throw new Error(`Profile ${profileId} not rendered`)
    requireElement('button[title="更多"]', row).click()
    await waitRendered(() => [...document.querySelectorAll('button')].some(button => button.textContent.trim() === '关键字'), 'keyword menu')
    exactButton('关键字').click()
    const modal = () => [...document.querySelectorAll('.modal-surface')]
      .find(surface => surface.querySelector('h3')?.textContent.trim() === `关键字 — ${profileName}`)
    await waitRendered(() => Boolean(modal()?.querySelector('input[placeholder="输入关键字"]')), 'keyword modal')
    const surface = modal()
    const fields = surface.querySelectorAll('input[placeholder="输入关键字"]')
    if (fields.length !== 1) throw new Error('Fixture expected exactly one keyword')
    return { rowLink, modal, surface, fields }
  }

  async function prepareKeyword(profileId, profileName, keyword) {
    const { rowLink, modal, surface, fields } = await openKeyword(profileId, profileName)
    setInput(fields[0], keyword)
    const render = await waitRendered(() => fields[0].isConnected && fields[0].value === keyword && fields[0].defaultValue === keyword, 'controlled keyword input')
    preparedKeyword = { profileId, profileName, keyword, rowLink, modal, surface, fields }
    return { profileId, keyword, render }
  }

  async function savePreparedKeyword(profileId, profileName, keyword, phase) {
    if (typeof phase !== 'function') throw new Error('Keyword save requires the host phase observer')
    const prepared = preparedKeyword
    if (!prepared || prepared.profileId !== profileId || prepared.profileName !== profileName || prepared.keyword !== keyword) throw new Error('Keyword save preparation mismatch')
    const { rowLink, modal, surface, fields } = prepared
    if (!surface.isConnected || modal() !== surface || fields[0].value !== keyword || fields[0].defaultValue !== keyword) throw new Error('Prepared keyword editor changed')
    const started = performance.now()
    exactButton('保存', surface).click()
    const visiblePromise = waitRendered(() => [...(rowLink()?.closest('tr')?.querySelectorAll('button[title]') || [])]
      .some(button => button.title === `点击复制：${keyword}`), 'saved keyword visible in matching row').then(render => {
        phase('keyword_visible', { profileId, keyword, render })
        return { render, elapsedMs: performance.now() - started }
      })
    const closedPromise = waitRendered(() => !modal(), 'keyword modal closed').then(render => {
      phase('keyword_closed', { profileId, keyword, render })
      return { render, elapsedMs: performance.now() - started }
    })
    const [visible, closed] = await Promise.all([visiblePromise, closedPromise])
    const all = await window.go.main.App.BrowserProfileList()
    const persisted = all.find(profile => profile.profileId === profileId)
    if (!persisted || !sameList(persisted.keywords, [keyword])) throw new Error('Backend keyword verification failed')
    preparedKeyword = null
    return { visibleMs: visible.elapsedMs, modalClosedMs: closed.elapsedMs, profileId, keyword, render: closed.render }
  }

  async function saveKeyword(profileId, profileName, keyword) {
    // Compatibility wrapper; formal metrics use preparation + one timed commit.
    await prepareKeyword(profileId, profileName, keyword)
    return savePreparedKeyword(profileId, profileName, keyword, () => {})
  }

  async function cancelKeyword(profileId, profileName, original) {
    const { modal, surface, fields } = await openKeyword(profileId, profileName)
    setInput(fields[0], 'unsaved-synthetic-value')
    exactButton('取消', surface).click()
    await waitRendered(() => !modal(), 'cancelled keyword modal')
    const profile = (await window.go.main.App.BrowserProfileList()).find(p => p.profileId === profileId)
    if (!sameList(profile.keywords, [original])) throw new Error('Cancelled edit reached backend')
    return true
  }

  async function rejectedKeyword(profileId, profileName, original) {
    const { modal, surface, fields } = await openKeyword(profileId, profileName)
    // Keep the same value so the failure probe does not rely on a rollback
    // behavior which was absent from the baseline's manager implementation.
    setInput(fields[0], original)
    exactButton('保存', surface).click()
    await waitRendered(() => [...document.querySelectorAll('[role="alert"]')]
      .some(element => element.textContent.includes('保存失败')), 'actual failed-save feedback')
    if (!modal()) throw new Error('Failed save closed the editor as if successful')
    exactButton('取消', surface).click()
    await waitRendered(() => !modal(), 'failed-save modal dismissed')
    return true
  }

  async function instanceAction(profileId, start) {
    const row = () => document.querySelector(`a[href="/browser/detail/${profileId}"]`)?.closest('tr')
    if (!row()) throw new Error('Missing instance row')
    const button = requireElement(`button[title="${start ? '启动' : '停止'}"]`, row())
    if (button.disabled) throw new Error('Instance action disabled')
    button.click()
    const render = await waitRendered(() => Boolean(row()?.querySelector(`[aria-label="${start ? '运行中' : '已停止'}"]`)), 'instance action state')
    const profile = (await window.go.main.App.BrowserProfileList()).find(p => p.profileId === profileId)
    if (!profile || Boolean(profile.running) !== start) throw new Error('UI/API instance state mismatch')
    return { ...profile, render }
  }

  async function verifyProxyEvent(profileId, size, expectedOK) {
    const proxyId = profileId.replace('profile', 'proxy')
    const label = `性能代理 ${profileId.slice(-5)}`
    const row = () => [...document.querySelectorAll('tbody a[href^="/browser/detail/"]')]
      .find(link => link.getAttribute('href') === `/browser/detail/${profileId}`)?.closest('tr')
    const latency = () => {
      const block = [...(row()?.querySelectorAll('div[title]') || [])].find(element => element.title === label)
      return [...(block?.querySelectorAll('span') || [])].at(-1)?.textContent.trim()
    }
    const before = latency()
    if (typeof before !== 'string') throw new Error('Missing real proxy result view')
    const events = []
    const unsubscribe = window.runtime.EventsOn('proxy:speed:result', event => {
      if (event?.proxyId === proxyId) events.push(event)
    })
    if (typeof unsubscribe !== 'function') throw new Error('Cannot register real proxy event observer')
    try {
      // Call the real backend directly: do not update a frontend store or click
      // a handler that writes its returned result to the store itself.
      const result = await window.go.main.App.BrowserProxyTestSpeed(proxyId)
      if (result.ok !== expectedOK || result.proxyId !== proxyId) throw new Error('Local proxy event fixture returned an unexpected result')
      const after = result.ok ? `${result.latencyMs}ms` : '失败'
      if (after === before) throw new Error('Proxy event probe requires a visible state transition')
      const ids = Array.from({ length: size }, (_, i) => `perf-profile-${String(i).padStart(5, '0')}`)
      const eventMatches = event => event.ok === result.ok && event.testedAt === result.testedAt && event.latencyMs === result.latencyMs && event.error === result.error
      const render = await waitRendered(() => events.some(eventMatches) && latency() === after && exactProfileRows(ids), 'real backend event, changed result view and full catalog')
      return { profileId, proxyId, before, after, result, event: events.find(eventMatches), render }
    } finally { unsubscribe() }
  }

  window.antPerfGui = { filterProfiles, filterProxies, saveKeyword, prepareKeyword, savePreparedKeyword,
                       cancelKeyword, rejectedKeyword, initialReady, catalogAPI,
                       instanceAction, verifyProxyEvent, profileRows, proxyNames, proxyRows, waitRendered, sameList }
})()
