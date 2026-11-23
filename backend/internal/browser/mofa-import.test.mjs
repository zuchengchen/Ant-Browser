import assert from 'node:assert/strict'
import test from 'node:test'
import { setImmediate } from 'node:timers/promises'
import { requestMofaImport } from './builtinassets/mofa-import/account.js'
import readAccountEmail from './builtinassets/mofa-import/account-email.js'
import { configuredMofa } from './builtinassets/mofa-import/site.js'

const base = 'chrome-extension://abcdef/'
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

test('only a trusted toolbar click creates an expiring one-time proof', async t => {
  const start = {disabled: true, addEventListener: (_, listener) => { start.click = listener }}
  const labels = {start, site: {textContent: ''}, status: {textContent: ''}}
  const view = {addEventListener() {}}
  view.top = view
  let address = base + 'import.html'
  let requests = 0
  let clock = 100
  mockGlobals(t, {
    window: view, document: {getElementById: id => labels[id]}, performance: {now: () => clock},
    history: {replaceState: (_, __, value) => { address = value }},
    chrome: {extension: {inIncognitoContext: false}, runtime: {getURL: path => base + path}},
  })
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    if (url === base + 'site.json') return Response.json({site, bridgePort})
    const {proof} = JSON.parse(options.body)
    const digest = Buffer.from(await crypto.subtle.digest('SHA-256', Buffer.from(proof, 'hex'))).toString('hex')
    assert.match(proof, /^[0-9a-f]{64}$/)
    assert.equal(address, `${base}import.html#ant-mofa-${digest}`)
    assert.ok(!address.includes(proof))
    assert.ok(!globalThis.antMofaClaimBridge('0'.repeat(64)))
    requests++
    if (requests === 1) {
      assert.ok(globalThis.antMofaClaimBridge(digest))
      assert.ok(!globalThis.antMofaClaimBridge(digest))
      return Response.json({result: healthy})
    }
    clock += 20_001
    assert.ok(!globalThis.antMofaClaimBridge(digest))
    return Response.json({error: '请重新发起'}, {status: 403})
  })
  await import('./builtinassets/mofa-import/page.js')
  await setImmediate()
  assert.equal(start.disabled, false)
  await start.click({isTrusted: false})
  assert.equal(requests, 0)
  await start.click({isTrusted: true})
  assert.equal(address, base + 'import.html')
  assert.match(labels.status.textContent, /本次跳过/)
  await start.click({isTrusted: true})
  assert.equal(address, base + 'import.html')
  assert.equal(labels.status.textContent, '请重新发起')
  assert.equal(requests, 2)
})

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
