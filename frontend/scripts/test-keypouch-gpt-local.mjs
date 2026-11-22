import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { setImmediate as nextTurn } from 'node:timers/promises'
import test from 'node:test'
import vm from 'node:vm'
import { localKey, localVault, validateLocalRecord } from '../../backend/internal/browser/builtinassets/keypouch-gpt/local.js'
import { VaultError } from '../../backend/internal/browser/builtinassets/keypouch-gpt/vault.js'

test('desktop handoff and toolbar apply the same record identity checks', () => {
  const entry = { recordId: 17, site: 'https://vault.example.test', email: 'sample@example.test', password: '', totpSecret: '' }
  assert.equal(validateLocalRecord(entry), entry)
  for (const email of ['member@localhost', 'member@[127.0.0.1]']) {
    assert.throws(() => validateLocalRecord({ ...entry, email }), VaultError)
  }
  assert.throws(() => validateLocalRecord({ ...entry, recordId: Number.MAX_SAFE_INTEGER + 1 }), VaultError)
})

const assets = new URL('../../backend/internal/browser/builtinassets/keypouch-gpt/', import.meta.url)
const source = { recordId: 16, site: 'https://key.test', email: 'first@example.test', password: ' fixture ', totpSecret: 'test-only-value' }
const pickerURL = 'chrome-extension://fixture/accounts.html'

function event() {
  const handlers = new Set()
  return {
    addListener(callback) { handlers.add(callback) },
    removeListener(callback) { handlers.delete(callback) },
    emit(...args) { return Promise.all([...handlers].map(callback => callback(...args))) },
  }
}

function replaceGlobal(t, key, value) {
  const original = Object.getOwnPropertyDescriptor(globalThis, key)
  Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
  t.after(() => {
    if (original) Object.defineProperty(globalThis, key, original)
    else delete globalThis[key]
  })
}

test('local metadata hides secrets, and each login reads a complete fresh snapshot of the same site and record', async t => {
  let entry = source
  replaceGlobal(t, 'chrome', { storage: { session: { get: async key => ({ [key]: entry }) } } })
  const record = await localVault.account()
  assert.deepEqual(record, { id: 16, site: source.site, email: source.email, hasPassword: true, hasTOTP: true })
  const signal = new AbortController().signal
  let read = await localVault.login(record, signal)
  assert(read.password === source.password && read.totpSecret === source.totpSecret)
  entry = { ...source, password: '', totpSecret: '' }
  read = await localVault.login(record, signal)
  assert(read.password === '' && read.totpSecret === '')
  for (const changes of [{ recordId: 17 }, { email: 'other@example.test' }, { site: 'https://second.test' }]) {
    entry = { ...source, ...changes }
    await assert.rejects(localVault.login(record, signal), { code: 'local_changed' })
  }
  entry = { ...source, site: 'https://KEY.test:443' }
  assert.equal((await localVault.account()).site, entry.site)
  entry = undefined
  assert.equal(await localVault.account(), null)
  await assert.rejects(localVault.login(record, signal), { code: 'local_changed' })
})

test('absent local storage, malformed snapshots and read failures are distinguished without exposing input', async t => {
  const session = { get: async () => ({}) }
  replaceGlobal(t, 'chrome', { storage: { session } })
  assert.equal(await localVault.account(), null)
  for (const invalid of [null, {}, { ...source, recordId: 0 }, { ...source, password: undefined },
    { ...source, totpSecret: undefined }, { ...source, email: 'not an email' }, { ...source, site: 'http://public.test' }]) {
    session.get = async () => ({ [localKey]: invalid })
    await assert.rejects(localVault.account(), error => error instanceof VaultError && error.code === 'local_storage')
  }
  session.get = async () => { throw new Error('fixture-secret') }
  await assert.rejects(localVault.account(), error => error.code === 'local_storage' && !error.message.includes('fixture-secret'))
})

test('toolbar clicks can be consumed once, only by their own extension top-level accounts tab and before expiry', async () => {
  let now = 100, nextID = 20
  const tabs = { onRemoved: event(), create: async () => ({ id: ++nextID }), update: async () => {} }
  const runtime = { id: 'fixture', getURL: () => pickerURL, onMessage: event() }
  const chrome = { runtime, tabs, action: { onClicked: event() } }
  const source = await readFile(new URL('toolbar.js', assets), 'utf8')
  vm.runInNewContext(source, { chrome, Date: { now: () => now } })
  const take = async (tabId, changes = {}) => {
    let result
    const sender = { tab: { id: tabId }, id: runtime.id, frameId: 0, url: pickerURL, ...changes }
    await runtime.onMessage.emit({ type: 'take-toolbar-click' }, sender, value => { result = value })
    return result
  }
  assert.equal(await take(21), false)
  await chrome.action.onClicked.emit({ windowId: 4 })
  assert.equal(await take(21, { frameId: 1 }), undefined)
  assert.equal(await take(21, { url: 'https://example.test/' }), undefined)
  assert.equal(await take(21, { id: 'another-extension' }), undefined)
  assert.equal(await take(20), false)
  assert.equal(await take(21), true)
  assert.equal(await take(21), false)
  await chrome.action.onClicked.emit({ windowId: 4 })
  now += 31_000
  assert.equal(await take(22), false)
  await chrome.action.onClicked.emit({ windowId: 4 })
  await tabs.onRemoved.emit(23)
  assert.equal(await take(23), false)
})

function uiElement() {
  return { children: [], events: {}, dataset: {}, hidden: true, value: '',
    append(...nodes) { this.children.push(...nodes) },
    replaceChildren(...nodes) { this.children = nodes },
    addEventListener(name, callback) { this.events[name] = callback },
  }
}

async function picker(t, options = {}) {
  const controls = new Map()
  const element = key => {
    if (!controls.has(key)) controls.set(key, uiElement())
    return controls.get(key)
  }
  const runtime = { id: 'fixture', onMessage: event(), getURL: name => 'chrome-extension://fixture/' + name,
    sendMessage: async () => { const clicked = state.clicked; state.clicked = false; return clicked },
  }
  const state = {
    entry: options.entry, clicked: options.clicked === true, keyReads: 0, permissionChecks: 0,
    targets: [], injections: [], storageEvents: event(), runtime,
  }
  replaceGlobal(t, 'chrome', {
    runtime,
    storage: { session: { get: async () => ({ [localKey]: state.entry }) }, onChanged: state.storageEvents },
    permissions: { contains: async () => { state.permissionChecks++; return false } },
    tabs: { getCurrent: async () => ({ id: 23, windowId: 2 }),
      get: async () => ({ id: 38, url: 'https://chatgpt.com/' }),
      create: async args => { state.targets.push(args); return { id: 38 } },
    },
    scripting: { executeScript: async ({ args }) => {
      const [action] = args
      state.injections.push(action)
      if (!action.stage && !action.end && options.hold) await options.hold
      return [{ frameId: 0, documentId: 'fixture-document', result: action.end ? 'ended' : 'success' }]
    } },
  })
  replaceGlobal(t, 'document', { getElementById: element, createElement: uiElement, addEventListener() {} })
  replaceGlobal(t, 'window', { addEventListener() {} })
  replaceGlobal(t, 'navigator', { locks: { request: (_name, _options, task) => task({}) } })
  replaceGlobal(t, 'fetch', async () => { state.keyReads++; return { ok: true, json: async () => ({ site: source.site }) } })
  await import(new URL('accounts.js?case=' + crypto.randomUUID(), assets))
  await nextTurn()
  return { state, element }
}

test('restoring or reloading the accounts page just shows local metadata. A toolbar click in its own tab starts exactly one login', async t => {
  const { state, element } = await picker(t, { entry: source })
  assert.equal(state.targets.length, 0)
  assert.equal(element('bound').hidden, false)
  assert.equal(element('web-session').hidden, true)
  const [accountRow] = element('bound-account').children
  const [button] = accountRow.children
  const [label] = button.children
  assert.equal(label.children[0].textContent, source.email)
  state.clicked = true
  await state.runtime.onMessage.emit({ type: 'toolbar-click', tabId: 19 }, { id: 'fixture' })
  await nextTurn()
  assert.equal(state.targets.length, 0)
  await state.runtime.onMessage.emit({ type: 'toolbar-click', tabId: 23 }, { id: 'fixture' })
  await nextTurn()
  assert.equal(state.targets.length, 1)
  assert.equal(state.targets[0].url, 'https://chatgpt.com/')
  assert.equal(state.keyReads, 0)
  assert.equal(state.permissionChecks, 0)
  assert.equal(element('status').textContent, '已确认此实例登录的是所选 ChatGPT 账号。')
  await state.runtime.onMessage.emit({ type: 'toolbar-click', tabId: 23 }, { id: 'fixture' })
  await nextTurn()
  assert.equal(state.targets.length, 1)
})

test('an unsynced toolbar action keeps the existing KeyPouch connection flow', async t => {
  const { state, element } = await picker(t, { clicked: true })
  assert.equal(state.targets.length, 0)
  assert.equal(state.keyReads, 1)
  assert.equal(state.permissionChecks, 1)
  assert.equal(element('grant').hidden, false)
})

test('refreshing or clearing this instance during an in-flight login stops the old attempt before the next submission', async t => {
  let resume
  const hold = new Promise(resolve => { resume = resolve })
  const { state, element } = await picker(t, { clicked: true, entry: source, hold })
  assert.equal(state.injections.length, 1)
  assert.equal(element('login-email').textContent, source.email)
  state.entry = { ...source, password: '', totpSecret: '' }
  await state.storageEvents.emit({ [localKey]: { oldValue: source, newValue: state.entry } }, 'session')
  resume()
  await nextTurn()
  assert(state.injections.every(action => !action.stage && !('value' in action)))
  assert.equal(element('status').textContent, '实例凭据已变更，请重新点击「登录 GPT」。')
  assert.equal(state.targets.length, 1)
  assert.equal(state.keyReads, 0)
})
