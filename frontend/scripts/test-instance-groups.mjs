import assert from 'node:assert/strict'
import { createRequire, Module } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { Worker } from 'node:worker_threads'

// Intended destination: frontend/scripts/test-instance-groups.mjs.
// The override permits running this isolated candidate against the real helper.
const here = dirname(fileURLToPath(import.meta.url))
const frontend = process.env.INSTANCE_GROUPS_FRONTEND_ROOT
  ? resolve(process.env.INSTANCE_GROUPS_FRONTEND_ROOT)
  : resolve(here, '..')
const require = createRequire(resolve(frontend, 'package.json'))
const compiled = require('esbuild').buildSync({
  entryPoints: [resolve(frontend, 'src/modules/browser/utils/instanceGroups.ts')],
  bundle: true, platform: 'node', format: 'cjs', write: false,
}).outputFiles[0].text
const loaded = new Module(resolve(here, 'compiled-instance-groups-test.cjs'))
loaded.filename = resolve(here, 'compiled-instance-groups-test.cjs')
loaded._compile(compiled, loaded.filename)
const {
  profileGroupIds,
  groupPath,
  descendantGroupIds,
  profileMatchesGroup,
  groupInstanceCounts,
  migrateGroupFilter,
} = loaded.exports

const clone = value => JSON.parse(JSON.stringify(value))
const freeze = value => {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze)
    Object.freeze(value)
  }
  return value
}
const group = (groupId, groupName, parentId = '', sortOrder = 0) => ({
  groupId, groupName, parentId, sortOrder,
  createdAt: '2026-11-12T00:00:00Z', updatedAt: '2026-11-12T00:00:00Z',
})
// Child-before-parent order and equal child names intentionally defeat shallow
// grouping or name-based identity. Names and memberships are synthetic.
const groups = freeze([
  group('child-b', 'Shared child', 'root-b'),
  group('grand-a', 'Leaf', 'child-a'),
  group('root-b', 'Beta'),
  group('empty', 'Empty'),
  group('child-a', 'Shared child', 'root-a'),
  group('root-a', 'Alpha'),
])
const profile = (profileId, groupIds = [], extra = {}) => ({
  profileId, profileName: `Instance ${profileId}`, groupIds,
  groupId: '', tags: [], deletedAt: '', ...extra,
})
const assertIDs = (actual, expected) => {
  assert.ok(Array.isArray(actual), 'Membership IDs must be an array')
  assert.equal(actual.length, new Set(actual).size, 'Membership IDs must be unique')
  assert.deepEqual([...actual].sort(), [...expected].sort())
}
const assertScope = (actual, expected) => {
  assert.ok(actual instanceof Set, 'Descendant scope must be a Set')
  assert.deepEqual([...actual].sort(), [...expected].sort())
}
const assertPath = (actual, names) => {
  assert.equal(typeof actual, 'string')
  let previousEnd = -1
  for (const name of names) {
    const position = actual.indexOf(name)
    assert.ok(position > previousEnd, `Expected ordered path component ${name}: ${actual}`)
    previousEnd = position + name.length - 1
  }
  assert.ok(actual.endsWith(names.at(-1)), `Path must end at its target: ${actual}`)
}

// A synchronous recursion bug cannot be interrupted by node:test's timeout.
// Isolate malformed hierarchy checks so even an infinite traversal fails safely.
function evaluateCycleCalls(calls) {
  return new Promise((resolveResult, reject) => {
    let settled = false
    const worker = new Worker(`
      const { parentPort, workerData } = require('node:worker_threads')
      const { Module } = require('node:module')
      try {
        const loaded = new Module('/compiled-instance-groups-cycle.cjs')
        loaded.filename = '/compiled-instance-groups-cycle.cjs'
        loaded._compile(workerData.compiled, loaded.filename)
        const results = workerData.calls.map(({ name, args }) => {
          const value = loaded.exports[name](...args)
          return value instanceof Set ? [...value] : value
        })
        parentPort.postMessage({ ok: true, results })
      } catch (error) {
        parentPort.postMessage({ ok: false, error: error?.stack || String(error) })
      }
    `, { eval: true, workerData: { compiled, calls } })
    const finish = (error, results) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      void worker.terminate()
      if (error) reject(error)
      else resolveResult(results)
    }
    const timer = setTimeout(() => finish(new Error('Hierarchy traversal did not terminate within 3 seconds')), 3000)
    worker.once('message', message => {
      if (!message || message.ok !== true || !Array.isArray(message.results)) {
        finish(new Error(message?.error || 'Cycle worker returned no complete result'))
      } else {
        finish(null, message.results)
      }
    })
    worker.once('error', error => finish(error))
    worker.once('exit', code => {
      if (!settled) finish(new Error(`Cycle worker exited without a result: ${code}`))
    })
  })
}

test('instance group helper exposes every intended operation', () => {
  for (const name of ['profileGroupIds', 'groupPath', 'descendantGroupIds', 'profileMatchesGroup', 'groupInstanceCounts', 'migrateGroupFilter']) {
    assert.equal(typeof loaded.exports[name], 'function', name)
  }
})

test('explicit groupIds is authoritative, including an empty array', () => {
  const populated = freeze(profile('p1', ['root-b'], { groupId: 'root-a', tags: ['Alpha'] }))
  assertIDs(profileGroupIds(populated), ['root-b'])
  assertIDs(profileGroupIds(freeze(profile('p2', [], { groupId: 'root-a', tags: ['Alpha'] }))), [])
  // A present malformed new field must not resurrect stale compatibility data.
  assertIDs(profileGroupIds({ groupIds: null, groupId: 'root-a' }), [])
  assertIDs(profileGroupIds({ groupIds: undefined, groupId: 'root-a' }), [])
})

test('only legacy responses without groupIds fall back to a single groupId', () => {
  assertIDs(profileGroupIds(freeze({ groupId: ' root-a ', tags: ['Beta'] })), ['root-a'])
  assertIDs(profileGroupIds(freeze({ groupId: '', tags: ['Alpha'] })), [])
  assertIDs(profileGroupIds({}), [])
})

test('membership normalization deduplicates IDs without changing input arrays', () => {
  const original = profile('p1', ['root-b', ' root-a ', 'root-b', '', '   '])
  const before = clone(original)
  assertIDs(profileGroupIds(freeze(original)), ['root-b', 'root-a'])
  assert.deepEqual(original, before)
})

test('group paths follow ancestry despite child-before-parent order and repeated leaf names', () => {
  assertPath(groupPath('grand-a', groups), ['Alpha', 'Shared child', 'Leaf'])
  assertPath(groupPath('child-a', groups), ['Alpha', 'Shared child'])
  assertPath(groupPath('child-b', groups), ['Beta', 'Shared child'])
  assert.notEqual(groupPath('child-a', groups), groupPath('child-b', groups))
  assert.equal(groupPath('root-a', groups), 'Alpha')
})

test('descendant scope includes the requested group and all depths, without sibling leakage', () => {
  assertScope(descendantGroupIds('root-a', groups), ['root-a', 'child-a', 'grand-a'])
  assertScope(descendantGroupIds('child-a', groups), ['child-a', 'grand-a'])
  assertScope(descendantGroupIds('grand-a', groups), ['grand-a'])
  assertScope(descendantGroupIds('root-b', groups), ['root-b', 'child-b'])
  assertScope(descendantGroupIds('', groups), [])
})

test('orphan groups stay addressable and do not require a valid parent record', () => {
  const orphanGroups = freeze([group('orphan', 'Orphan', 'missing-parent')])
  assertScope(descendantGroupIds('orphan', orphanGroups), ['orphan'])
  assertPath(groupPath('orphan', orphanGroups), ['Orphan'])
  assert.equal(profileMatchesGroup(profile('p', ['orphan']), 'orphan', orphanGroups), true)
})

test('cycles and self-parent links terminate and preserve each reachable identity once', async () => {
  const cyclic = [
    group('cycle-a', 'Cycle Alpha', 'cycle-b'),
    group('cycle-b', 'Cycle Beta', 'cycle-a'),
    group('cycle-child', 'Cycle Child', 'cycle-b'),
    group('self', 'Self', 'self'),
    group('other', 'Other'),
  ]
  const results = await evaluateCycleCalls([
    { name: 'descendantGroupIds', args: ['cycle-a', cyclic] },
    { name: 'descendantGroupIds', args: ['self', cyclic] },
    { name: 'groupPath', args: ['cycle-a', cyclic] },
    { name: 'groupPath', args: ['self', cyclic] },
    { name: 'profileMatchesGroup', args: [profile('p', ['cycle-child']), 'cycle-a', cyclic] },
    { name: 'groupInstanceCounts', args: [[profile('p', ['cycle-a', 'cycle-child'])], cyclic] },
  ])
  assert.deepEqual([...results[0]].sort(), ['cycle-a', 'cycle-b', 'cycle-child'])
  assert.deepEqual(results[1], ['self'])
  assert.equal(typeof results[2], 'string')
  assert.ok(results[2].includes('Cycle Alpha'))
  assert.ok(results[2].length < 200, 'Cycle paths must not repeat without bound')
  assert.equal(results[3], 'Self')
  assert.equal(results[4], true)
  assert.equal(results[5]['cycle-a'], 1)
  assert.equal(results[5]['cycle-b'], 1)
  assert.equal(results[5]['cycle-child'], 1)
  assert.equal(results[5].other, 0)
})

test('all and ungrouped filters distinguish zero memberships from legacy residue', () => {
  const grouped = profile('grouped', ['root-a', 'child-b'])
  const cleared = profile('cleared', [], { groupId: 'root-a', tags: ['Alpha'] })
  for (const all of ['', 'all', '__all__']) {
    assert.equal(profileMatchesGroup(grouped, all, groups), true)
    assert.equal(profileMatchesGroup(cleared, all, groups), true)
  }
  for (const ungrouped of ['ungrouped', '__ungrouped__']) {
    assert.equal(profileMatchesGroup(grouped, ungrouped, groups), false)
    assert.equal(profileMatchesGroup(cleared, ungrouped, groups), true)
    assert.equal(profileMatchesGroup({ groupId: 'root-a' }, ungrouped, groups), false)
  }
})

test('parent filters include descendants and multiple memberships without matching unrelated branches', () => {
  const leaf = profile('leaf', ['grand-a'])
  assert.equal(profileMatchesGroup(leaf, 'root-a', groups), true)
  assert.equal(profileMatchesGroup(leaf, 'child-a', groups), true)
  assert.equal(profileMatchesGroup(leaf, 'grand-a', groups), true)
  assert.equal(profileMatchesGroup(leaf, 'root-b', groups), false)
  const multiple = profile('multiple', ['root-a', 'child-b'])
  assert.equal(profileMatchesGroup(multiple, 'root-a', groups), true)
  assert.equal(profileMatchesGroup(multiple, 'root-b', groups), true)
  assert.equal(profileMatchesGroup(multiple, 'grand-a', groups), false)
})

test('active subtree counts deduplicate parent/child memberships and repeated profile records', () => {
  const items = freeze([
    profile('p1', ['root-a', 'child-a']),
    profile('p2', ['grand-a', 'root-b']),
    profile('p3', ['child-b']),
    profile('p4', []),
    profile('trashed', ['root-a', 'child-b'], { deletedAt: '2026-11-12T00:00:00Z' }),
    profile('p1', ['root-a', 'child-a']),
  ])
  const before = clone(items)
  const counts = groupInstanceCounts(items, groups)
  assert.equal(counts['root-a'], 2)
  assert.equal(counts['child-a'], 2)
  assert.equal(counts['grand-a'], 1)
  assert.equal(counts['root-b'], 2)
  assert.equal(counts['child-b'], 1)
  assert.equal(counts.empty, 0)
  if (Object.hasOwn(counts, '__all__')) assert.equal(counts.__all__, 4)
  if (Object.hasOwn(counts, '__ungrouped__')) assert.equal(counts.__ungrouped__, 1)
  assert.deepEqual(items, before)
})

test('empty and trash-only lists give explicit zero counts for every saved group', () => {
  for (const items of [[], [profile('trash', ['grand-a'], { deletedAt: '2026-11-12T00:00:00Z' })]]) {
    const counts = groupInstanceCounts(items, groups)
    for (const item of groups) assert.equal(counts[item.groupId], 0, item.groupId)
  }
})

test('legacy single-group cache preserves a valid ID and the ungrouped sentinel', () => {
  assert.deepEqual(migrateGroupFilter({ groupId: 'root-a' }, groups), { groupId: 'root-a', groupIds: [] })
  assert.deepEqual(migrateGroupFilter({ groupId: '__ungrouped__' }, groups), { groupId: '__ungrouped__', groupIds: [] })
})

test('legacy tag cache maps unique root names and retains all OR alternatives', () => {
  const raw = freeze({ groupId: '', tags: ['Alpha', 'Beta', 'Alpha'] })
  const before = clone(raw)
  const migrated = migrateGroupFilter(raw, groups)
  assert.equal(migrated.groupId, '')
  assertIDs(migrated.groupIds, ['root-a', 'root-b'])
  assert.deepEqual(raw, before)
})

test('legacy group AND tag-OR filters preserve both clauses rather than arbitrarily picking one', () => {
  const migrated = migrateGroupFilter({ groupId: 'child-a', tags: ['Alpha', 'Beta'] }, groups)
  assert.equal(migrated.groupId, 'child-a')
  assertIDs(migrated.groupIds, ['root-a', 'root-b'])
})

test('unresolved old tags retain stable nonmatching tokens instead of guessing or widening the filter', () => {
  const names = freeze([
    ...groups,
    group('same-root-one', 'Ambiguous'),
    group('same-root-two', 'Ambiguous'),
    group('child-alpha', 'Alpha', 'root-b'),
  ])
  const raw = { tags: ['Ambiguous', 'Shared child', 'Missing', 'Alpha'] }
  const migrated = migrateGroupFilter(raw, names)
  assert.equal(migrated.groupId, '')
  assert.equal(migrated.groupIds.length, 4, 'All original OR terms must survive, including unresolved terms')
  assert.equal(new Set(migrated.groupIds).size, 4)
  assert.ok(migrated.groupIds.includes('root-a'))
  const knownIDs = new Set(names.map(item => item.groupId))
  for (const token of migrated.groupIds.filter(id => id !== 'root-a')) {
    assert.equal(typeof token, 'string')
    assert.ok(token.trim(), 'Unresolved terms need a stable nonempty token')
    assert.equal(knownIDs.has(token), false, 'Ambiguous names cannot choose an arbitrary existing group')
    for (const item of names) {
      assert.equal(profileMatchesGroup(profile('p', [item.groupId]), token, names), false)
    }
  }
  assert.deepEqual(migrateGroupFilter(clone(raw), names), migrated)
  assert.deepEqual(migrateGroupFilter(clone(migrated), names), migrated)

  const unresolvedOnly = migrateGroupFilter({ tags: ['Missing'] }, names)
  assert.equal(unresolvedOnly.groupIds.length, 1, 'An unresolved filter cannot become an empty all filter')
  assert.equal(profileMatchesGroup(profile('p', ['root-a']), unresolvedOnly.groupIds[0], names), false)
})

test('new cache IDs remain authoritative over stale tags and repeated migration is idempotent', () => {
  const raw = freeze({ groupId: 'root-a', groupIds: ['child-b', 'grand-a', 'child-b'], tags: ['Alpha'] })
  const migrated = migrateGroupFilter(raw, groups)
  assert.equal(migrated.groupId, 'root-a')
  assertIDs(migrated.groupIds, ['child-b', 'grand-a'])
  assert.deepEqual(migrateGroupFilter(clone(migrated), groups), migrated)
  assert.deepEqual(migrateGroupFilter({ groupId: '', groupIds: [], tags: ['Alpha'] }, groups), { groupId: '', groupIds: [] })
})

test('invalid cache shapes recover safely while removed IDs remain nonmatching constraints', () => {
  for (const raw of [null, undefined, false, 1, [], {}, { tags: 1 }, { tags: 'Alpha' }, { groupIds: false }]) {
    assert.deepEqual(migrateGroupFilter(raw, groups), { groupId: '', groupIds: [] })
  }
  const stale = migrateGroupFilter({ groupId: 'deleted-group', groupIds: ['deleted-group', 'root-b'] }, groups)
  assert.ok(stale.groupId, 'A removed primary group must not silently widen to all')
  assert.equal(profileMatchesGroup(profile('p', ['root-a']), stale.groupId, groups), false)
  assert.equal(stale.groupIds.length, 2)
  assert.ok(stale.groupIds.includes('root-b'))
  const unresolvedID = stale.groupIds.find(id => id !== 'root-b')
  assert.ok(unresolvedID)
  assert.equal(profileMatchesGroup(profile('p', ['root-a']), unresolvedID, groups), false)
  assert.deepEqual(migrateGroupFilter(clone(stale), groups), stale)
})
