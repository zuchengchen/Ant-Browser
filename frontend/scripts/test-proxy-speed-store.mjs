import assert from 'node:assert/strict'
import { createRequire, Module } from 'node:module'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'

// Execute the actual TypeScript store with the project's installed dependencies.
// No alternate store or copied implementation is used for these behavior checks.
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(frontendRoot, 'package.json'))
const ts = require('typescript')
const sourcePath = resolve(frontendRoot, 'src/modules/browser/proxySpeedStore.ts')
const compiled = ts.transpileModule(readFileSync(sourcePath, 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.CommonJS },
}).outputText
const module = new Module(sourcePath)
module.filename = sourcePath
module.paths = Module._nodeModulePaths(dirname(sourcePath))
module._compile(compiled, sourcePath)
const store = module.exports.useProxySpeedStore
const fresh = '2026-11-10T00:00:00.000Z'
const stale = '2020-01-01T00:00:00.000Z'
const isFresh = timestamp => timestamp === fresh

test('backend hydration preserves live results and accepts only fresh complete results', () => {
  store.getState().reset()
  const live = { proxyId: 'live', ok: false, latencyMs: -1, error: 'live failure', testedAt: fresh }
  store.getState().setResult(live)
  const originalResults = store.getState().results
  store.getState().applyBackendResults([
    { proxyId: 'live', lastTestedAt: fresh, lastLatencyMs: 1, lastTestOk: true },
    { proxyId: 'fresh', lastTestedAt: fresh, lastLatencyMs: 42, lastTestOk: true, lastTestEngine: 'xray', speedTestFingerprint: 'fp' },
    { proxyId: 'stale', lastTestedAt: stale, lastLatencyMs: 5, lastTestOk: true },
    { proxyId: 'missing-time', lastLatencyMs: 5 },
    { proxyId: 'missing-latency', lastTestedAt: fresh },
    { proxyId: 'failed', lastTestedAt: fresh, lastLatencyMs: -1, lastTestOk: false },
  ], isFresh)
  const results = store.getState().results
  assert.deepEqual(Object.keys(results).sort(), ['failed', 'fresh', 'live'])
  assert.strictEqual(results.live, live)
  assert.deepEqual(results.fresh, {
    proxyId: 'fresh', ok: true, latencyMs: 42, engine: 'xray', error: '', testedAt: fresh, fingerprint: 'fp',
  })
  assert.equal(results.failed.ok, false)
  assert.deepEqual(Object.keys(originalResults), ['live'], 'hydration must not mutate previous state')
})

test('duplicate IDs retain the first valid fresh result while IDs remain case sensitive', () => {
  store.getState().reset()
  store.getState().applyBackendResults([
    { proxyId: 'same', lastTestedAt: stale, lastLatencyMs: 1 },
    { proxyId: 'same', lastTestedAt: fresh, lastLatencyMs: 20, lastTestOk: true },
    { proxyId: 'same', lastTestedAt: fresh, lastLatencyMs: 99, lastTestOk: false },
    { proxyId: 'SAME', lastTestedAt: fresh, lastLatencyMs: 30, lastTestOk: true },
  ], isFresh)
  assert.equal(store.getState().results.same.latencyMs, 20)
  assert.equal(store.getState().results.same.ok, true)
  assert.equal(store.getState().results.SAME.latencyMs, 30)
})

test('unchanged hydration keeps state identity and emits no unnecessary store notifications', () => {
  store.getState().reset()
  const proxies = [{ proxyId: 'present', lastTestedAt: fresh, lastLatencyMs: 10, lastTestOk: true }]
  store.getState().applyBackendResults(proxies, isFresh)
  const previous = store.getState()
  let notifications = 0
  const unsubscribe = store.subscribe(() => { notifications += 1 })
  try {
    store.getState().applyBackendResults(proxies, isFresh)
    store.getState().applyBackendResults([], isFresh)
    store.getState().applyBackendResults([{ proxyId: 'expired', lastTestedAt: stale, lastLatencyMs: 20 }], isFresh)
    assert.strictEqual(store.getState(), previous)
    assert.equal(notifications, 0)
  } finally {
    unsubscribe()
  }
})

test('setTesting no-ops preserve the whole store and real transitions notify exactly once', () => {
  store.getState().reset()
  const result = { proxyId: 'A', ok: true, latencyMs: 42, error: '', testedAt: fresh }
  store.getState().setResult(result)
  const results = store.getState().results
  const changes = []
  const unsubscribe = store.subscribe((next, previous) => changes.push({ next, previous }))
  try {
    const idle = store.getState()
    store.getState().setTesting('missing', false)
    assert.strictEqual(store.getState(), idle)
    assert.equal(changes.length, 0)
    store.getState().setTesting('A', true)
    const testing = store.getState()
    assert.equal(changes.length, 1)
    assert.equal(testing.testingIds.A, true)
    assert.strictEqual(changes[0].previous, idle)
    store.getState().setTesting('A', true)
    store.getState().setTesting('missing', false)
    assert.strictEqual(store.getState(), testing)
    assert.equal(changes.length, 1)
    store.getState().setTesting('A', false)
    const ended = store.getState()
    assert.equal(changes.length, 2)
    assert.deepEqual(ended.testingIds, {})
    assert.strictEqual(changes[1].previous, testing)
    store.getState().setTesting('A', false)
    assert.strictEqual(store.getState(), ended)
    assert.equal(changes.length, 2)
    assert.strictEqual(ended.results, results)
    assert.strictEqual(ended.results.A, result)
  } finally {
    unsubscribe()
  }
})
