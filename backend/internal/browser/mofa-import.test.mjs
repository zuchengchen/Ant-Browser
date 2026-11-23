import assert from 'node:assert/strict'
import test from 'node:test'
import { setImmediate } from 'node:timers/promises'
import { requestMofaImport } from './builtinassets/mofa-import/account.js'
import readAccountEmail from './builtinassets/mofa-import/account-email.js'
import { configuredMofa } from './builtinassets/mofa-import/site.js'

const base = 'chrome-extension://abcdef/'
const importPage = base + 'import.html'
const site = 'https://mofa.example.test'
const bridgePort = 19441
const healthy = {status: 'skipped', accountId: 42, groupId: 7, inTargetGroup: true}

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

test('one browser action grants a short-lived permit to only the new background extension tab', async t => {
  const events = {}
  const listen = name => ({addListener: listener => { events[name] = listener }})
  const launch = {windowId: 4, incognito: false}
  const created = []
  const navigated = []
  let clock = 100
  t.mock.method(Date, 'now', () => clock)
  mockGlobals(t, {chrome: {
    action: {onClicked: listen('action')},
    runtime: {id: 'abcdef', getURL: path => base + path, onMessage: listen('message')},
    tabs: {
      onRemoved: listen('remove'),
      async create(options) {
        assert.deepEqual(options, {windowId: launch.windowId, url: 'about:blank', active: false})
        const id = created.length + 50
        created.push(id)
        return {id}
      },
      async update(id, options) {
        assert.ok(created.includes(id))
        assert.deepEqual(options, {url: importPage})
        navigated.push(id)
      },
    },
  }})
  await import('./builtinassets/mofa-import/toolbar.js')
  const take = (id, overrides = {}, type = 'take-mofa-toolbar-click') => {
    const sender = {id: 'abcdef', origin: base.slice(0, -1), url: importPage, frameId: 0,
      documentLifecycle: 'active', tab: {...launch, id}, ...overrides}
    let reply
    events.message({type}, sender, value => { reply = value })
    return reply
  }
  await events.action({...launch, incognito: true})
  assert.equal(created.length, 0)
  await Promise.all([events.action(launch), events.action(launch)])
  assert.deepEqual(navigated, [50])
  await events.action(launch)
  assert.deepEqual(navigated, [50])
  for (const invalid of [
    {id: 'different-extension'},
    {origin: 'https://example.test'},
    {url: importPage + '#forged'},
    {frameId: 1},
    {documentLifecycle: 'cached'},
    {tab: {...launch, id: 50, incognito: true}},
    {tab: undefined},
  ]) {
    assert.equal(take(50, invalid), undefined)
  }
  assert.equal(take(50, {}, 'unrelated'), undefined)
  assert.equal(take(51), false)
  assert.equal(take(50, {tab: {...launch, windowId: 9, id: 50}}), false)
  assert.equal(take(50), true)
  assert.equal(take(50), false)
  await events.action(launch)
  clock += 20_001
  assert.equal(take(51), false)
  await events.action(launch)
  events.remove(52)
  assert.equal(take(52), false)
  assert.deepEqual(navigated, [50, 51, 52])
})

function mockImportPage(t, {href = importPage, incognito = false, child = false, permit = false} = {}) {
  const labels = {site: {textContent: ''}, status: {textContent: ''}}
  const view = {}
  view.top = child ? {} : view
  const location = {href}
  const sendMessage = t.mock.fn(async message => {
    assert.deepEqual(message, {type: 'take-mofa-toolbar-click'})
    return permit
  })
  const clock = {now: 100}
  mockGlobals(t, {
    window: view, document: {visibilityState: 'hidden', getElementById: id => labels[id]}, location,
    performance: {now: () => clock.now},
    history: {replaceState: (_, __, value) => { location.href = value }},
    chrome: {extension: {inIncognitoContext: incognito}, runtime: {getURL: path => base + path, sendMessage}},
  })
  return {view, labels, location, sendMessage, clock}
}

for (const [name, options, messageCount] of [
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
    assert.equal(page.sendMessage.mock.callCount(), messageCount)
    assert.equal(fetch.mock.callCount(), 0)
    assert.equal(page.view.antMofaClaimBridge, undefined)
    assert.match(page.labels.status.textContent, /请点击浏览器工具栏/)
  })
}

for (const expired of [false, true]) {
  test(`the permitted background page starts immediately with an ${expired ? 'expired' : 'unclaimed'} proof`, async t => {
    const {view, labels, location, sendMessage, clock} = mockImportPage(t, {permit: true})
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
    assert.equal(sendMessage.mock.callCount(), 1)
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
