import assert from 'node:assert/strict'
import { createRequire, Module } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'

// Load the production selectors, freshness policy and actual Zustand store.
// Compilation stays in memory and uses the project's existing esbuild package.
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(frontendRoot, 'package.json'))
const compiled = require('esbuild').buildSync({
  stdin: {
    contents: `
      export * from './src/modules/browser/profileProxySpeed';
      export { useProxySpeedStore } from './src/modules/browser/proxySpeedStore';
      export { isProxySpeedResultFresh, PROXY_SPEED_RESULT_CLOCK_SKEW_MS } from './src/modules/browser/pages/proxyPool/storage';
    `,
    resolveDir: frontendRoot,
  },
  bundle: true,
  platform: 'node',
  format: 'cjs',
  packages: 'external',
  write: false,
}).outputFiles[0].text
const sourcePath = resolve(frontendRoot, 'scripts/profile-proxy-speed-test.cjs')
const module = new Module(sourcePath)
module.filename = sourcePath
module.paths = Module._nodeModulePaths(dirname(sourcePath))
module._compile(compiled, sourcePath)
const {
  findProfileProxySpeedResult: findResult,
  selectVirtualGroupSpeedResult: selectGroup,
  useProxySpeedStore: store,
  isProxySpeedResultFresh: isFresh,
  PROXY_SPEED_RESULT_CLOCK_SKEW_MS: clockSkew,
} = module.exports
const now = Date.parse('2026-11-10T00:00:00.000Z')
const maxAge = 10_000
const result = (proxyId, overrides = {}) => ({
  proxyId, ok: true, latencyMs: 20, error: '', testedAt: new Date(now).toISOString(), ...overrides,
})

test('exact keys outrank normalized aliases, including failed and stale exact results', () => {
  const alias = result(' Alpha ')
  const exactFailure = result('alpha', { ok: false, latencyMs: -1 })
  const staleExact = result('ALPHA', { testedAt: new Date(now - maxAge - 1).toISOString() })
  const results = { alias, alpha: exactFailure, ALPHA: staleExact }
  assert.strictEqual(findResult(results, 'alpha'), exactFailure)
  assert.strictEqual(findResult(results, 'ALPHA'), staleExact)
  assert.strictEqual(findResult(results, 'aLpHa'), alias)
  assert.equal(isFresh(staleExact.testedAt, now, maxAge), false)
  assert.equal(findResult(results, 'missing'), null)
})

test('fallback uses the first result ID in Object.values order, not record keys or last duplicate', () => {
  const first = result(' ALPHA ', { latencyMs: 90 })
  const later = result('alpha', { latencyMs: 1 })
  const results = { label: later, 20: later, 3: first }
  assert.strictEqual(findResult(results, 'AlPhA'), first, 'integer keys precede string keys in numeric order')
  assert.strictEqual(findResult({ alias: first, next: later }, 'alpha'), first)
  assert.equal(findResult({ alpha: result('different') }, 'ALPHA'), null, 'fallback matches result.proxyId')
  assert.strictEqual(findResult({ alpha: first }, 'alpha'), first, 'exact lookup still uses the record key')
})

test('empty custom targets remain empty while direct and chain IDs retain their own result', () => {
  const direct = result('__direct__')
  const chain = result('chain-id')
  const custom = result('profile-id')
  const results = { __direct__: direct, 'chain-id': chain, 'profile-id': custom, '': result('') }
  assert.strictEqual(findResult(results, '__direct__'), direct)
  assert.strictEqual(findResult(results, 'chain-id'), chain)
  assert.equal(findResult(results, ''), null)
  assert.equal(findResult(results, '   '), null)
})

test('missing-key selectors share one fallback enumeration per immutable results snapshot', () => {
  let enumerations = 0
  const first = result('Alpha')
  const results = new Proxy({ Alpha: first }, {
    ownKeys(target) { enumerations += 1; return Reflect.ownKeys(target) },
  })
  assert.strictEqual(findResult(results, 'Alpha'), first)
  assert.equal(enumerations, 0, 'exact lookup does not build the fallback index')
  for (const id of ['missing-a', 'alpha', 'missing-b', 'ALPHA', 'missing-a']) findResult(results, id)
  assert.equal(enumerations, 1, 'different rows reuse the same index, including absent results')

  const replacement = result('Alpha', { latencyMs: 44 })
  const next = new Proxy({ Alpha: replacement }, {
    ownKeys(target) { enumerations += 1; return Reflect.ownKeys(target) },
  })
  assert.strictEqual(findResult(next, 'alpha'), replacement)
  assert.strictEqual(findResult(results, 'alpha'), first, 'an older snapshot remains independently valid')
  assert.equal(enumerations, 2)
})

test('virtual groups preserve first-alias, success, freshness and member-order tie rules', () => {
  const stale = result(' Alpha ', { latencyMs: 1, testedAt: new Date(now - maxAge - 1).toISOString() })
  const shadowed = result('alpha', { latencyMs: 2 })
  const failed = result('failed', { ok: false, latencyMs: -1 })
  const beta = result('Beta', { latencyMs: 30 })
  const gamma = result('Gamma', { latencyMs: 30 })
  const results = { first: stale, later: shadowed, failed, beta, gamma }
  assert.strictEqual(selectGroup(results, new Set(['alpha', 'failed', 'beta', 'gamma']), now, maxAge), beta)
  assert.strictEqual(selectGroup(results, new Set(['gamma', 'beta']), now, maxAge), gamma)
  assert.equal(selectGroup(results, new Set(['alpha', 'failed']), now, maxAge), null)
  assert.equal(selectGroup(results, new Set(), now, maxAge), null)
  const failedFirst = { first: failed, later: result('FAILED', { latencyMs: 1 }) }
  assert.equal(selectGroup(failedFirst, new Set(['failed']), now, maxAge), null)
})

test('group freshness re-evaluates unchanged snapshots at expiry and clock-skew boundaries', () => {
  const current = result('alpha')
  const results = { alpha: current }
  const members = new Set(['alpha'])
  assert.strictEqual(selectGroup(results, members, now + maxAge, maxAge), current)
  assert.equal(selectGroup(results, members, now + maxAge + 1, maxAge), null)
  assert.strictEqual(selectGroup(results, members, now - clockSkew, maxAge), current)
  assert.equal(selectGroup(results, members, now - clockSkew - 1, maxAge), null)
  assert.equal(selectGroup({ alpha: result('alpha', { testedAt: 'invalid' }) }, members, now, maxAge), null)
})

test('all production result writers invalidate lookup snapshots without changing untouched objects', () => {
  store.getState().reset()
  const empty = store.getState().results
  assert.equal(findResult(empty, 'alpha'), null)
  const alpha = result('Alpha')
  store.getState().setResult(alpha)
  const first = store.getState().results
  assert.notStrictEqual(first, empty)
  assert.strictEqual(findResult(first, 'alpha'), alpha)
  assert.equal(findResult(first, 'beta'), null)

  store.getState().setTesting('Alpha', true)
  store.getState().setManyTesting(['Alpha', 'Beta'], true)
  store.getState().setManyResults([])
  store.getState().prune(new Set())
  assert.strictEqual(store.getState().results, first)

  const beta = result('Beta')
  const lastBeta = result('Beta', { latencyMs: 45 })
  store.getState().setManyResults([beta, lastBeta])
  const second = store.getState().results
  assert.notStrictEqual(second, first)
  assert.strictEqual(findResult(second, 'alpha'), alpha)
  assert.strictEqual(findResult(second, 'beta'), lastBeta, 'last exact duplicate batch write wins')
  assert.equal(findResult(first, 'beta'), null)
  assert.equal(findResult(second, 'gamma'), null)

  const fresh = timestamp => isFresh(timestamp, now, maxAge)
  store.getState().applyBackendResults([
    { proxyId: 'Alpha', lastTestedAt: alpha.testedAt, lastLatencyMs: 999 },
    { proxyId: 'Gamma', lastTestedAt: alpha.testedAt, lastLatencyMs: 15, lastTestOk: true },
    { proxyId: 'Gamma', lastTestedAt: alpha.testedAt, lastLatencyMs: 99, lastTestOk: true },
  ], fresh)
  const third = store.getState().results
  assert.notStrictEqual(third, second)
  assert.strictEqual(findResult(third, 'alpha'), alpha, 'backend hydration keeps the live object')
  assert.equal(findResult(third, 'gamma').latencyMs, 15, 'first valid backend duplicate wins')
  store.getState().applyBackendResults([], fresh)
  assert.strictEqual(store.getState().results, third)

  store.getState().prune(new Set(['Beta', 'Gamma']))
  const fourth = store.getState().results
  assert.notStrictEqual(fourth, third)
  assert.equal(findResult(fourth, 'alpha'), null)
  assert.strictEqual(findResult(fourth, 'beta'), lastBeta)
  assert.strictEqual(findResult(third, 'alpha'), alpha)
  store.getState().reset()
  assert.equal(findResult(store.getState().results, 'beta'), null)
})

test('real store notifications retain Object.is equality for unrelated selected results', () => {
  store.getState().reset()
  const alpha = result('Alpha')
  store.getState().setResult(alpha)
  const selected = [findResult(store.getState().results, 'alpha')]
  const missing = [findResult(store.getState().results, 'late')]
  let notifications = 0
  // Observe the selector equality contract on real store notifications. Actual
  // React commit suppression is separately checked by the browser fixture.
  const unsubscribe = store.subscribe(state => {
    notifications += 1
    for (const [target, values] of [['alpha', selected], ['late', missing]]) {
      const next = findResult(state.results, target)
      if (!Object.is(next, values[values.length - 1])) values.push(next)
    }
  })
  try {
    store.getState().setResult(result('unrelated'))
    assert.equal(notifications, 1)
    const unchanged = store.getState()
    store.getState().setTesting('unrelated', false)
    assert.equal(notifications, 1, 'An already idle proxy must not notify subscribers')
    assert.strictEqual(store.getState(), unchanged)
    store.getState().setManyTesting(['Alpha'], true)
    assert.equal(notifications, 2)
    store.getState().setManyResults([alpha, result('another')])
    assert.equal(notifications, 3)
    assert.deepEqual(selected, [alpha])
    assert.deepEqual(missing, [null])

    const replacement = result('Alpha', { latencyMs: 55 })
    const late = result('late')
    store.getState().setManyResults([replacement, late])
    assert.equal(notifications, 4)
    assert.deepEqual(selected, [alpha, replacement])
    assert.deepEqual(missing, [null, late])
    store.getState().prune(new Set(['late']))
    assert.equal(notifications, 5)
    store.getState().reset()
    assert.equal(notifications, 6)
    assert.deepEqual(selected, [alpha, replacement, null])
    assert.deepEqual(missing, [null, late, null])
  } finally {
    unsubscribe()
    store.getState().reset()
  }
})
