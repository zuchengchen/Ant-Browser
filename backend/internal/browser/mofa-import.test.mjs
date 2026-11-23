import assert from 'node:assert/strict'
import test from 'node:test'
import { setImmediate } from 'node:timers/promises'
import { requestMofaImport } from './builtinassets/mofa-import/account.js'
import readAccountEmail from './builtinassets/mofa-import/account-email.js'
import { takeToolbarPermit } from './builtinassets/mofa-import/permit.js'
import { configuredMofa } from './builtinassets/mofa-import/site.js'

const base = 'chrome-extension://abcdef/'
const importPage = base + 'import.html'
const site = 'https://mofa.example.test'
const bridgePort = 19441
const healthy = {status: 'skipped', accountId: 42, groupId: 7, inTargetGroup: true}
const permitKey = 'mofa-toolbar-permit'
const importTab = {id: 50, windowId: 4, incognito: false}

function mockGlobals(t, values) {
  for (const [key, value] of Object.entries(values)) {
    const existing = Object.getOwnPropertyDescriptor(globalThis, key)
    Object.defineProperty(globalThis, key, { value, configurable: true, writable: true })
    t.after(() => {
      if (existing) Object.defineProperty(globalThis, key, existing)
      else delete globalThis[key]
    })
  }
}

function mockPermitStorage(t, permit) {
  const stored = new Map(permit ? [[permitKey, permit]] : [])
  const session = {
    async get(key) {
      assert.equal(key, permitKey)
      return stored.has(key) ? {[key]: stored.get(key)} : {}
    },
    async set(values) {
      assert.deepEqual(Object.keys(values), [permitKey])
      stored.set(permitKey, values[permitKey])
    },
    async remove(key) {
      assert.equal(key, permitKey)
      stored.delete(key)
    },
  }
  let pending = Promise.resolve()
  mockGlobals(t, {navigator: {locks: {request(name, callback) {
    assert.equal(name, permitKey)
    const result = pending.then(callback)
    pending = result.catch(() => {})
    return result
  }}}})
  return {session, stored}
}

test('the MOFA site and ephemeral port come from installation config', async t => {
  let config = {site: ''}
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(url, base + 'site.json')
    assert.deepEqual(options, { cache: 'no-store', credentials: 'omit' })
    return Response.json(config)
  })
  mockGlobals(t, {chrome: {runtime: {getURL: path => base + path}}})
  assert.deepEqual(await configuredMofa(), {site: '', bridgePort: 0})
  config = {site: 'https://[0:0:0:0:0:0:0:1]:443', bridgePort}
  assert.deepEqual(await configuredMofa(), {site: 'https://[::1]', bridgePort})
  config = {site, bridgePort: '19441/other'}
  assert.deepEqual(await configuredMofa(), {site, bridgePort: 0})
  for (const unsafe of [
    'http://example.test',
    'https://user:secret@example.test',
    'https://mofa.example.test/admin/accounts',
    'https://example.test?private=value',
    'https://example.test#unwanted',
    'https://example.test\\',
    {},
  ]) {
    config.site = unsafe
    await assert.rejects(configuredMofa)
  }
})

test('the import only contacts the local bridge and accepts confirmed results', async t => {
  const proof = '42'.repeat(32)
  let response = Response.json({result: healthy})
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(url, `http://127.0.0.1:${bridgePort}/mofa/import`)
    assert.equal(options.method, 'POST')
    assert.equal(options.credentials, 'omit')
    assert.equal(options.redirect, 'error')
    assert.equal(options.referrerPolicy, 'no-referrer')
    assert.deepEqual(JSON.parse(options.body), {site, proof})
    return response
  })
  assert.deepEqual(await requestMofaImport({site, bridgePort}, proof), healthy)
  response = Response.json({error: '该实例正在导入到 MOFA，请稍候'}, {status: 409})
  await assert.rejects(requestMofaImport({site, bridgePort}, proof), /该实例正在导入到 MOFA/)
  response = Response.json({result: {...healthy, status: 'authorization_required'}})
  await assert.rejects(requestMofaImport({site, bridgePort}, proof), /未收到 Ant 的导入结果/)
  response = Response.json({result: healthy}, {status: 500})
  await assert.rejects(requestMofaImport({site, bridgePort}, proof), /未收到 Ant 的导入结果/)
})

test('only the authorized background tab can claim its permit after the worker restarts', async t => {
  const events = {}
  const listen = name => ({addListener: listener => { events[name] = listener }})
  const launch = {windowId: 4, incognito: false}
  const created = []
  const navigated = []
  const removed = []
  const {session, stored} = mockPermitStorage(t)
  const view = {}
  view.top = view
  let currentTab = importTab
  let clock = 100
  let failNavigation = false
  t.mock.method(Date, 'now', () => clock)
  mockGlobals(t, {window: view, location: {href: importPage}, chrome: {
    action: {onClicked: listen('action')},
    extension: {inIncognitoContext: false},
    runtime: {getURL: path => base + path},
    storage: {session},
    tabs: {
      onRemoved: listen('remove'),
      async getCurrent() { return currentTab },
      async create(options) {
        assert.deepEqual(options, {windowId: launch.windowId, url: 'about:blank', active: false})
        const id = created.length + 50
        created.push(id)
        return {...launch, id}
      },
      async update(id, options) {
        assert.ok(created.includes(id))
        assert.deepEqual(options, {url: importPage})
        assert.deepEqual(stored.get(permitKey), {tabId: id, windowId: launch.windowId, deadline: clock + 20_000})
        if (failNavigation) throw new Error('Lost tab navigation')
        navigated.push(id)
      },
      async remove(id) { removed.push(id) },
    },
  }})
  await import('./builtinassets/mofa-import/toolbar.js')
  await events.action({...launch, incognito: true})
  assert.equal(created.length, 0)
  await Promise.all([events.action(launch), events.action(launch)])
  await import('./builtinassets/mofa-import/toolbar.js?restarted')
  await events.action(launch)
  assert.deepEqual(navigated, [50])
  for (const tab of [undefined, {...importTab, id: 51}, {...importTab, windowId: 9}, {...importTab, incognito: true}]) {
    currentTab = tab
    assert.equal(await takeToolbarPermit(), false)
  }
  currentTab = importTab
  assert.deepEqual(await Promise.all([takeToolbarPermit(), takeToolbarPermit()]), [true, false])
  await events.action(launch)
  currentTab = {...importTab, id: 51}
  clock += 20_001
  assert.equal(await takeToolbarPermit(), false)
  await events.action(launch)
  events.remove(52)
  currentTab = {...importTab, id: 52}
  assert.equal(await takeToolbarPermit(), false)
  assert.deepEqual(navigated, [50, 51, 52])
  failNavigation = true
  await events.action(launch)
  assert.deepEqual(removed, [53])
  assert.equal(stored.has(permitKey), false)
})

function mockImportPage(t, {href = importPage, incognito = false, child = false, permit = false} = {}) {
  const labels = {site: {textContent: ''}, status: {textContent: ''}}
  const view = {}
  view.top = child ? {} : view
  const location = {href}
  const {session} = mockPermitStorage(t, permit && {tabId: importTab.id, windowId: importTab.windowId, deadline: Date.now() + 20_000})
  const getCurrent = t.mock.fn(async () => importTab)
  const clock = {now: 100}
  mockGlobals(t, {
    window: view, document: {visibilityState: 'hidden', getElementById: id => labels[id]}, location,
    performance: {now: () => clock.now},
    history: {replaceState: (_, __, value) => { location.href = value }},
    chrome: {extension: {inIncognitoContext: incognito}, runtime: {getURL: path => base + path}, storage: {session}, tabs: {getCurrent}},
  })
  return {view, labels, location, getCurrent, session, clock}
}

for (const [name, options, tabReads] of [
  ['direct page or reload', {}, 1],
  ['forged marker', {href: importPage + '#ant-mofa-' + '0'.repeat(64)}, 0],
  ['incognito', {incognito: true}, 0],
  ['subframe', {child: true}, 0],
]) {
  test(`the ${name} cannot start an import`, async t => {
    const page = mockImportPage(t, options)
    const fetch = t.mock.method(globalThis, 'fetch', () => { assert.fail('unapproved page sent a fetch') })
    await import('./builtinassets/mofa-import/page.js?' + name)
    await setImmediate()
    assert.equal(page.getCurrent.mock.callCount(), tabReads)
    assert.equal(fetch.mock.callCount(), 0)
    assert.equal(page.view.antMofaClaimBridge, undefined)
    assert.match(page.labels.status.textContent, /请点击浏览器工具栏/)
  })
}

test('an unavailable permit store is reported and no import starts', async t => {
  const {labels, session} = mockImportPage(t, {permit: true})
  t.mock.method(session, 'remove', async () => { throw new Error('Storage unavailable') })
  const fetch = t.mock.method(globalThis, 'fetch', () => { assert.fail('unclaimed page sent a fetch') })
  await import('./builtinassets/mofa-import/page.js?permit-storage-unavailable')
  await setImmediate()
  assert.match(labels.status.textContent, /无法确认本次 MOFA 导入的启动许可/)
  assert.equal(fetch.mock.callCount(), 0)
})

for (const expired of [false, true]) {
  test(`the permitted background page starts immediately with an ${expired ? 'expired' : 'unclaimed'} proof`, async t => {
    const {view, labels, location, getCurrent, clock} = mockImportPage(t, {permit: true})
    let settle
    const observed = new Promise(resolve => { settle = resolve })
    const fetch = t.mock.method(globalThis, 'fetch', async (url, options) => {
      if (url === base + 'site.json') return Response.json({site, bridgePort})
      settle(JSON.parse(options.body))
      return new Promise(resolve => { settle = resolve })
    })
    await import('./builtinassets/mofa-import/page.js?permitted=' + expired)
    const {proof} = await observed
    const digest = Buffer.from(await crypto.subtle.digest('SHA-256', Buffer.from(proof, 'hex'))).toString('hex')
    assert.match(proof, /^[0-9a-f]{64}$/)
    assert.equal(location.href, `${importPage}#ant-mofa-${digest}`)
    assert.ok(!location.href.includes(proof))
    assert.equal(labels.site.textContent, site)
    assert.match(labels.status.textContent, /正在核对/)
    assert.equal(getCurrent.mock.callCount(), 1)
    assert.equal(fetch.mock.callCount(), 2)
    assert.ok(!view.antMofaClaimBridge('0'.repeat(64)))
    if (expired) clock.now += 20_001
    assert.equal(view.antMofaClaimBridge(digest), !expired)
    assert.ok(!view.antMofaClaimBridge(digest))
    settle(expired ? Response.json({error: '请重新发起'}, {status: 403}) : Response.json({result: healthy}))
    await setImmediate()
    assert.equal(location.href, importPage)
    assert.match(labels.status.textContent, expired ? /请重新发起/ : /本次跳过/)
  })
}

for (const [status, schedulable, message] of [
  ['reauthorized', false, '已重新授权原账号，并确认账号在「GPT-不降智」。该账号的 MOFA 调度当前已关闭。'],
  ['skipped', false, '已核对。账号授权有效且已在「GPT-不降智」，本次跳过。该账号的 MOFA 调度当前已关闭。'],
  ['reauthorized', true, '已重新授权原账号，并确认账号在「GPT-不降智」。'],
  ['skipped', null, '已核对。账号授权有效且已在「GPT-不降智」，本次跳过。'],
]) {
  test(`the toolbar reports ${status} with schedulable=${schedulable}`, async t => {
    const {labels} = mockImportPage(t, {permit: true})
    let acceptImport
    const requested = new Promise(resolve => { acceptImport = resolve })
    t.mock.method(globalThis, 'fetch', async url => {
      if (url === base + 'site.json') return Response.json({site, bridgePort})
      assert.equal(url, `http://127.0.0.1:${bridgePort}/mofa/import`)
      acceptImport()
      return Response.json({result: {...healthy, status, schedulable}})
    })
    await import(`./builtinassets/mofa-import/page.js?status=${status}&schedulable=${schedulable}`)
    await requested
    await setImmediate()
    assert.equal(labels.status.textContent, message)
  })
}

test('the shared account reader verifies one visible email within official account settings', t => {
  const view = {}
  view.top = view
  const location = {href: 'https://chatgpt.com/settings/account'}
  const text = textContent => ({nodeType: 3, textContent})
  const area = {
    closest: () => null, matches: () => false,
    getBoundingClientRect: () => ({width: 200, height: 100}),
    childNodes: [text('Email'), text('SYNTHETIC@example.test')],
  }
  mockGlobals(t, {
    window: view, location, Node: {ELEMENT_NODE: 1, TEXT_NODE: 3},
    document: {title: 'Account settings', querySelector: () => null, querySelectorAll: () => [area]},
    getComputedStyle: () => ({display: 'block', visibility: 'visible', opacity: '1'}),
  })
  assert.deepEqual(readAccountEmail(), {status: 'verified', email: 'synthetic@example.test'})
  area.childNodes.push(text('邮箱'), text('another@example.test'))
  assert.deepEqual(readAccountEmail(), {status: 'ambiguous'})
  location.href = 'https://example.test/settings/account'
  assert.deepEqual(readAccountEmail(), {status: 'unavailable'})
})
