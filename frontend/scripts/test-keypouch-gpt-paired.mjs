import assert from 'node:assert/strict'
import { createHash, createPublicKey, verify } from 'node:crypto'
import { afterEach, test } from 'node:test'
import { openPairedVault } from '../../backend/internal/browser/builtinassets/keypouch-gpt/paired.js'

const site = 'https://vault.example.test:8443'
const email = '测试+Alias@example.test'
const selected = { id: 41, email }
const metadata = { ...selected, has_password: true, has_totp: true }
const hasCode = code => error => error.code === code
const reply = (value, status = 200) => Response.json(value, { status })

// A fresh factory models a different Chromium profile. IDB serializes
// transactions on the store and clones CryptoKeys when reading and writing.
function useProfile() {
  const entries = new Map()
  let queue = Promise.resolve()
  let created = false
  const database = {
    createObjectStore() {}, close() {},
    transaction(_name, mode) {
      const read = {}
      let siteKey
      const store = {
        get(site) { siteKey = site; return read },
        put(pair, site) {
          assert.equal(mode, 'readwrite')
          entries.set(site, structuredClone(pair))
        },
      }
      const transaction = { objectStore: () => store }
      queue = queue.then(() => {
        try {
          read.result = structuredClone(entries.get(siteKey))
          read.onsuccess()
          transaction.oncomplete()
        } catch (error) {
          transaction.error = error
          transaction.onabort()
        }
      })
      return transaction
    },
  }
  globalThis.indexedDB = {
    open() {
      const request = { result: database }
      queueMicrotask(() => {
        if (!created) { created = true; request.onupgradeneeded() }
        request.onsuccess()
      })
      return request
    },
  }
  return entries
}
afterEach(() => { delete globalThis.indexedDB })

test('concurrent first opens share one committed non-extractable key; distinct sites and profiles have different keys', async () => {
  const entries = useProfile()
  const clients = await Promise.all(Array.from({ length: 16 }, () => openPairedVault(site)))
  assert.equal(new Set(clients.map(client => client.code)).size, 1)
  const first = clients[0]
  assert.equal((await openPairedVault(site.toUpperCase() + '/')).code, first.code)
  assert.equal(entries.size, 1)
  const { privateKey } = entries.get(site)
  assert.equal(privateKey.extractable, false)
  assert.deepEqual(privateKey.usages, ['sign'])
  await assert.rejects(crypto.subtle.exportKey('jwk', privateKey), { name: 'InvalidAccessException' })
  assert.notEqual((await openPairedVault('https://vault.example.test')).code, first.code)
  assert.notEqual((await openPairedVault('http://127.0.0.1:8899')).code, first.code)
  assert.equal(entries.size, 3)
  useProfile()
  assert.notEqual((await openPairedVault(site)).code, first.code)
  await assert.rejects(openPairedVault('http://remote.example.test'), hasCode('invalid_site'))
})

test('a broken stored identity fails explicitly and is never silently replaced', async () => {
  const entries = useProfile()
  entries.set(site, null)
  await assert.rejects(openPairedVault(site), hasCode('key_storage'))
  assert.equal(entries.get(site), null)
  entries.set(site, await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']))
  await assert.rejects(openPairedVault(site), hasCode('key_storage'))
  assert.equal(entries.get(site).privateKey.extractable, true)
})

test('explicit rotation atomically supersedes one displayed key; stale tabs and other sites or profiles retain the right identity', async t => {
  const entries = useProfile()
  const originalProfile = indexedDB
  const first = await openPairedVault(site)
  const secondTab = await openPairedVault(site)
  const otherOrigin = 'https://other.example.test'
  const other = await openPairedVault(otherOrigin)
  useProfile()
  const separateProfile = indexedDB
  const independent = await openPairedVault(site)
  globalThis.indexedDB = originalProfile
  const requests = t.mock.method(globalThis, 'fetch', async () => reply(metadata))
  await Promise.all([first.replaceKey(), secondTab.replaceKey()])
  const fresh = await openPairedVault(site)
  assert.notEqual(fresh.code, first.code)
  assert.equal(entries.get(site).privateKey.extractable, false)
  await first.replaceKey()
  assert.equal((await openPairedVault(site)).code, fresh.code)
  assert.equal((await openPairedVault(otherOrigin)).code, other.code)
  await assert.rejects(first.login(selected), hasCode('grant_required'))
  assert.equal(requests.mock.callCount(), 0)
  assert.equal((await fresh.account()).id, selected.id)
  assert.equal(JSON.parse(requests.mock.calls[0].arguments[1].body).key_id, fresh.id)
  globalThis.indexedDB = separateProfile
  assert.equal((await openPairedVault(site)).code, independent.code)
})

test('OpenSSL verifies fresh P-256 signatures over each exact action, key, time, nonce and record; no cookies or reusable TOTP data', async t => {
  useProfile()
  const client = await openPairedVault(site)
  assert.deepEqual(Object.keys(client).sort(), ['account', 'code', 'id', 'login', 'replaceKey', 'totp'])
  assert.match(client.code, /^KPG1\.[\w-]{87}$/)
  const raw = Buffer.from(client.code.slice(5), 'base64url')
  assert.equal(raw.length, 65)
  assert.equal(raw[0], 4)
  assert.equal(client.id, createHash('sha256').update(raw).digest('base64url'))
  const publicKey = createPublicKey({ format: 'jwk', key: {
    kty: 'EC', crv: 'P-256', x: raw.subarray(1, 33).toString('base64url'), y: raw.subarray(33).toString('base64url'),
  } })
  const password = ' synthetic password with whitespace '
  const expires = Date.now() + 23_000
  const replies = {
    account: { ...metadata, unrelated: 'fixture' },
    login: { ...selected, password, totp_secret: 'unused fixture' },
    'login-attempt': { ...selected, password, attempt: 'r'.repeat(32) },
    totp: { ...selected, code: '001234', expires_at: expires, totp_secret: 'unused fixture' },
  }
  const requests = t.mock.method(globalThis, 'fetch', async url => reply(replies[url.split('/').pop()]))
  const started = Math.floor(Date.now() / 1000)
  const record = await client.account()
  assert.deepEqual(record, { ...selected, hasPassword: true, hasTOTP: true })
  assert.deepEqual(await client.login(record), { email, password })
  assert.deepEqual(await client.login(record, undefined, true), { email, password, attempt: 'r'.repeat(32) })
  assert.deepEqual(await client.totp(record), { code: '001234', expires })
  await Promise.all(Array.from({ length: 16 }, () => client.account()))
  const nonces = new Set()
  for (const { arguments: [url, options] } of requests.mock.calls) {
    const action = url.split('/').pop()
    assert.equal(url, site + '/api/gpt-plugin/' + action)
    assert.equal(options.method, 'POST')
    assert.equal(options.credentials, 'omit')
    assert.equal(options.cache, 'no-store')
    assert.equal(options.redirect, 'error')
    assert.equal(options.referrerPolicy, 'no-referrer')
    assert.deepEqual(options.headers, { Accept: 'application/json', 'Content-Type': 'application/json' })
    const body = JSON.parse(options.body)
    assert.deepEqual(Object.keys(body).sort(), ['email', 'key_id', 'nonce', 'record_id', 'signature', 'time'])
    assert.equal(body.key_id, client.id)
    assert.ok(Number.isSafeInteger(body.time) && body.time >= started && body.time <= Math.floor(Date.now() / 1000))
    assert.match(body.nonce, /^[\w-]{32}$/)
    assert.equal(nonces.has(body.nonce), false)
    nonces.add(body.nonce)
    assert.equal(body.record_id, action === 'account' ? 0 : selected.id)
    assert.equal(body.email, action === 'account' ? '' : selected.email)
    assert.match(body.signature, /^[\w-]{86}$/)
    const signature = Buffer.from(body.signature, 'base64url')
    const message = ['KPG1', action, client.id, body.time, body.nonce, body.record_id, body.email].join('\n')
    const key = { key: publicKey, dsaEncoding: 'ieee-p1363' }
    assert.equal(verify('sha256', Buffer.from(message), key, signature), true)
    assert.equal(verify('sha256', Buffer.from(message + 'x'), key, signature), false)
  }
  assert.equal(nonces.size, 20)
})

test('account distinguishes an ungranted key from an unsupported or unavailable site; failed credentials never use another identity', async t => {
  useProfile()
  const client = await openPairedVault(site)
  const requests = t.mock.method(globalThis, 'fetch')
  for (const status of [401, 403]) {
    requests.mock.mockImplementation(async () => reply({}, status))
    assert.equal(await client.account(), null)
    await assert.rejects(client.login(selected), hasCode('grant_required'))
    await assert.rejects(client.totp(selected), hasCode('grant_required'))
  }
  for (const [status, code] of [[404, 'unsupported'], [405, 'unsupported'], [500, 'service_error']]) {
    requests.mock.mockImplementation(async () => reply({ message: 'private fixture' }, status))
    await assert.rejects(client.account(), error => error.code === code && !error.message.includes('private fixture'))
  }
  requests.mock.mockImplementation(async () => reply({ error: 'totp_unavailable' }, 409))
  await assert.rejects(client.totp(selected), hasCode('totp_unavailable'))
  assert.equal(requests.mock.callCount(), 10)
})

test('refuses changed records, malformed input, redirected responses and malformed OTPs', async t => {
  useProfile()
  const client = await openPairedVault(site)
  const requests = t.mock.method(globalThis, 'fetch')
  for (const record of [{ ...selected, id: '41' }, { ...selected, email: email + '\n' }, { id: 0, email }, null]) {
    await assert.rejects(client.login(record), hasCode('record_changed'))
  }
  assert.equal(requests.mock.callCount(), 0)
  for (const changed of [{ ...selected, id: selected.id + 1 }, { ...selected, email: email.toLowerCase() }, null]) {
    requests.mock.mockImplementation(async () => reply(changed))
    await assert.rejects(client.login(selected), hasCode('record_changed'))
    await assert.rejects(client.totp(selected), hasCode('record_changed'))
  }
  requests.mock.mockImplementation(async () => reply({ ...metadata, has_password: 1 }))
  await assert.rejects(client.account(), hasCode('invalid_response'))
  requests.mock.mockImplementation(async () => reply({ ...selected, password: null }))
  await assert.rejects(client.login(selected), hasCode('invalid_response'))
  for (const change of [{ code: 123456 }, { code: '1234567' }, { expires_at: -1 }, { expires_at: 'later' }]) {
    requests.mock.mockImplementation(async () => reply({ ...selected, code: '123456', expires_at: Date.now(), ...change }))
    await assert.rejects(client.totp(selected), hasCode('invalid_response'))
  }
  requests.mock.mockImplementation(async () => ({ status: 200, redirected: true, url: 'https://other.example.test' }))
  await assert.rejects(client.account(), hasCode('network'))
})

test('abort suppresses caller details and one deadline covers response headers and body decoding', async t => {
  useProfile()
  const client = await openPairedVault(site)
  const requests = t.mock.method(globalThis, 'fetch')
  const caller = new AbortController()
  caller.abort('private fixture')
  await assert.rejects(client.account(caller.signal), error => error.name === 'AbortError' && !error.message.includes('private fixture'))
  assert.equal(requests.mock.callCount(), 0)
  let expire
  t.mock.method(globalThis, 'setTimeout', (callback, ms) => { assert.equal(ms, 15_000); expire = callback })
  requests.mock.mockImplementation(async () => { expire(); throw new Error('private fixture') })
  await assert.rejects(client.account(), hasCode('timeout'))
  requests.mock.mockImplementation(async () => ({ status: 200, json: async () => { expire(); return metadata } }))
  await assert.rejects(client.account(), hasCode('timeout'))
})
