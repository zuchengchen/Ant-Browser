import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire, Module } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'

const here = dirname(fileURLToPath(import.meta.url))
const frontend = resolve(here, '..')
const require = createRequire(resolve(frontend, 'package.json'))
const compiled = require('esbuild').buildSync({
  entryPoints: [resolve(frontend, 'src/modules/browser/pages/browserList/profileSnapshot.ts')],
  bundle: true, platform: 'node', format: 'cjs', write: false,
}).outputFiles[0].text
const loaded = new Module(resolve(here, 'compiled-test.cjs'))
loaded.filename = resolve(here, 'compiled-test.cjs')
loaded._compile(compiled, loaded.filename)
const reuse = loaded.exports.reuseProfileSnapshot

const make = (overrides = {}) => ({
  profileId: 'profile-1', profileName: '实例 1', userDataDir: 'data/1', coreId: 'core',
  restoreLastSession: 'enabled', fingerprintArgs: ['--fingerprint=1'], proxyMode: 'fixed',
  proxyId: 'proxy', proxyConfig: '', proxyGroupName: 'group', proxyBindSourceId: 'source',
  proxyBindSourceUrl: 'https://example.invalid/sub', proxyBindName: 'proxy name',
  proxyBindUpdatedAt: '2026-11-01T00:00:00Z', memoryLimitMb: 256, launchArgs: ['--one', '--two'],
  lastLaunchArgs: ['--one'], tags: ['A', 'B'], keywords: ['词一', '词二'], groupId: 'group-1',
  running: false, debugPort: 0, debugReady: false, pid: 0, runtimeWarning: '', lastError: '',
  createdAt: '2026-11-01T00:00:00Z', updatedAt: '2026-11-01T00:00:00Z', deletedAt: '',
  lastStartAt: '', lastStopAt: '', launchCode: 'CODE', windowMarkerCode: 'A', ...overrides,
})
const clone = value => JSON.parse(JSON.stringify(value))
const deepFreeze = value => {
  if (value && typeof value === 'object') { Object.values(value).forEach(deepFreeze); Object.freeze(value) }
  return value
}

test('equal complete snapshots retain the original list, rows and ordered arrays without mutating API values', () => {
  const previous = deepFreeze([make(), make({ profileId: 'profile-2' })])
  const incoming = deepFreeze(clone(previous))
  const output = reuse(previous, incoming)
  assert.strictEqual(output, previous)
  assert.notStrictEqual(incoming, previous)
  assert.notStrictEqual(incoming[0].keywords, previous[0].keywords)
  assert.deepEqual(output, incoming)
  assert.strictEqual(reuse(previous, previous), previous)
  const empty = []; assert.strictEqual(reuse(empty, []), empty)
})

test('every declared BrowserProfile field invalidates unchanged-row reuse when its value changes', () => {
  const definition = readFileSync(resolve(frontend, 'src/modules/browser/types.ts'), 'utf8').split('export interface BrowserProfile {')[1].split('\n}')[0]
  const declared = [...definition.matchAll(/^\s+(\w+)\??:/gm)].map(match => match[1]).sort()
  const profile = make()
  assert.deepEqual(Object.keys(profile).sort(), declared, 'Update this coverage if the API gains fields')
  for (const key of declared) {
    const incoming = clone(profile), value = incoming[key]
    incoming[key] = Array.isArray(value) ? [...value, 'changed'] : typeof value === 'boolean' ? !value : typeof value === 'number' ? value + 1 : value + 'changed'
    const result = reuse([profile], [incoming])
    assert.strictEqual(result[0], incoming, key)
    assert.deepEqual(result[0], incoming, key)
  }
})

test('runtime-only and code/proxy changes are visible even when IDs and updatedAt remain identical', () => {
  const previous = [make(), make({ profileId: 'profile-2' })]
  for (const changed of [
    { running: true, pid: 123, debugPort: 9222, debugReady: true },
    { lastError: 'crashed', runtimeWarning: 'warning', lastStopAt: '2026-11-11T01:00:00Z' },
    { proxyMode: 'dynamic', proxyId: 'new-proxy', proxyBindName: 'new' },
    { launchCode: 'NEW', windowMarkerCode: 'B' },
  ]) {
    const incoming = [make(changed), clone(previous[1])]
    const result = reuse(previous, incoming)
    assert.strictEqual(result[0], incoming[0])
    assert.strictEqual(result[1], previous[1])
    assert.deepEqual(result, incoming)
  }
})

test('ordered array edits and reorderings stay observable, including duplicates', () => {
  const previous = [make()]
  for (const key of ['fingerprintArgs', 'launchArgs', 'lastLaunchArgs', 'tags', 'keywords']) {
    for (const value of [[], ['new'], ['same', 'same'], ['same', 'different']]) {
      const incoming = [make({ [key]: value })]
      assert.deepEqual(reuse(previous, incoming), incoming)
    }
  }
  const incoming = [make({ keywords: ['词二', '词一'] })]
  assert.strictEqual(reuse(previous, incoming)[0], incoming[0])
})

test('order, duplicate IDs, append and deletion retain exact incoming membership and values', () => {
  const previous = [make(), make({ profileName: 'same ID second row' }), make({ profileId: 'other' })]
  for (const incoming of [
    [clone(previous[1]), clone(previous[0]), clone(previous[2])],
    [clone(previous[0])], [],
    [...clone(previous), make({ profileId: 'new' })],
    [clone(previous[2]), clone(previous[1])],
  ]) {
    const result = reuse(previous, incoming)
    assert.deepEqual(result, incoming)
    assert.deepEqual(result.map(row => row.profileId), incoming.map(row => row.profileId))
  }
})

test('added, removed and explicitly undefined own fields are not silently conflated', () => {
  const base = make()
  for (const incoming of [{ ...base, future: 'new' }, { ...base, future: undefined }]) {
    assert.strictEqual(reuse([base], [incoming])[0], incoming)
    assert.strictEqual(reuse([incoming], [base])[0], base)
  }
  const withConstructor = { ...base, constructor: 'custom' }
  assert.strictEqual(reuse([base], [withConstructor])[0], withConstructor)
  const without = { ...base }; delete without.proxyBindName
  assert.strictEqual(reuse([base], [without])[0], without)
})

test('unknown nested object fields conservatively refresh and scalar additions participate in comparison', () => {
  const previous = [make({ future: { nested: 1 }, scalar: 'same' })]
  const incoming = clone(previous)
  assert.strictEqual(reuse(previous, incoming)[0], incoming[0], 'No speculative recursive cache for future objects')
  const scalarOnly = [make({ scalar: 'same' })]
  assert.strictEqual(reuse(scalarOnly, clone(scalarOnly)), scalarOnly)
  assert.notStrictEqual(reuse(scalarOnly, [make({ scalar: 'changed' })]), scalarOnly)
})

test('a changed middle row preserves both preceding and succeeding equal references', () => {
  const previous = [make({ profileId: '1' }), make({ profileId: '2' }), make({ profileId: '3' })]
  const incoming = clone(previous); incoming[1].keywords.push('new')
  const result = reuse(previous, incoming)
  assert.strictEqual(result[0], previous[0]); assert.strictEqual(result[1], incoming[1]); assert.strictEqual(result[2], previous[2])
  assert.deepEqual(result, incoming)
})
