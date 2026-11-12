import assert from 'node:assert/strict'
import { createRequire, Module } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'

// Compile the actual production functions in memory, using existing esbuild.
const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(frontendRoot, 'package.json'))
const compiled = require('esbuild').buildSync({
  stdin: { contents: "export * from './src/modules/browser/pages/proxyPool/proxyPoolSpeed';", resolveDir: frontendRoot },
  bundle: true, platform: 'node', format: 'cjs', packages: 'external', write: false,
}).outputFiles[0].text
const sourcePath = resolve(frontendRoot, 'scripts/proxy-pool-speed-test.cjs')
const module = new Module(sourcePath)
module.filename = sourcePath
module.paths = Module._nodeModulePaths(dirname(sourcePath))
module._compile(compiled, sourcePath)
const { deriveProxyPoolSpeedMaps: maps, deriveFetchProxyOptions: fetchOptions,
  deriveDownloadProxyOptions: downloadOptions } = module.exports
const result = (proxyId, extra = {}) => ({ proxyId, ok: true, latencyMs: 20,
  testedAt: '2026-11-10T00:00:00Z', error: '', engine: 'xray', ...extra })
const proxy = (proxyId, extra = {}) => ({ proxyId, proxyName: proxyId,
  proxyConfig: 'http://' + proxyId + ':80', ...extra })

test('pool maps preserve exact keys, failure precedence, engines and testing override', () => {
  const results = {
    alias: result(' Alpha ', { latencyMs: 1 }),
    Alpha: result('different', { ok: false, latencyMs: -1, error: 'i/o timeout' }),
    both: result('both', { ok: false, error: '协议不支持 timeout' }),
    failed: result('failed', { ok: false, error: 'connection refused', engine: 'mihomo' }),
    zero: result('zero', { latencyMs: 0 }),
    unusual: result('unusual', { latencyMs: -1 }),
  }
  const original = JSON.stringify(results)
  const values = maps(results, { Alpha: true, missing: true })
  assert.equal(values.latencyMap.alpha, undefined, 'Do not introduce alias normalization')
  assert.equal(values.latencyMap.alias, 1)
  assert.equal(values.latencyMap.Alpha, -1)
  assert.equal(values.latencyMap.missing, -1)
  assert.equal(values.latencyMap.both, -3, 'Unsupported outranks timeout text')
  assert.equal(values.latencyMap.failed, -4)
  assert.equal(values.latencyMap.zero, 0)
  assert.equal(values.latencyMap.unusual, -1)
  assert.equal(values.latencyEngineMap.Alpha, 'xray', 'Aggregate maps retain engine while testing')
  assert.equal(values.latencyErrorMap.Alpha, 'i/o timeout')
  assert.equal(values.latencyEngineMap.failed, 'mihomo')
  assert.equal(maps(results, {}).latencyMap.Alpha, -2)
  assert.equal(JSON.stringify(results), original, 'Derivation mutated results')
})

test('fetch and download candidates retain their deliberately different semantics', () => {
  const proxies = [proxy('A'), proxy('B'), proxy('C'), proxy('D'),
    proxy('persisted', { lastLatencyMs: 7 }), proxy('direct', { proxyConfig: 'direct://' })]
  const results = {
    A: result('A', { latencyMs: 30 }),
    B: result('B', { latencyMs: 0 }),
    C: result('C', { latencyMs: 10, ok: false, error: 'failure' }),
    D: result('D', { latencyMs: 5 }),
    direct: result('direct', { latencyMs: 1 }),
  }
  assert.deepEqual(fetchOptions(proxies, results).map(p => [p.proxyId, p.lastLatencyMs]),
    [['D', 5], ['C', 10], ['A', 30]], 'Fetch uses raw positive latency, not toLatencyValue or persisted fallback')
  assert.deepEqual(downloadOptions(proxies, maps(results, { D: true }).latencyMap),
    [{ value: 'http://A:80', label: 'A · 30ms' }], 'Download excludes failures, testing and zero')
  assert.equal(proxies[0].lastLatencyMs, undefined, 'Fetch projection must not modify catalog rows')
})

test('download keeps fastest twenty and stable input order for equal latency', () => {
  const proxies = Array.from({ length: 24 }, (_, i) => proxy(String(i)))
  const results = Object.fromEntries(proxies.map((p, i) => [p.proxyId, result(p.proxyId, { latencyMs: 24 - i })]))
  const options = downloadOptions(proxies, maps(results, {}).latencyMap)
  assert.equal(options.length, 20)
  assert.equal(options[0].label, '23 · 1ms')
  assert.equal(options[19].label, '4 · 20ms')
  const tie = [proxy('second'), proxy('first')]
  assert.deepEqual(downloadOptions(tie, { second: 9, first: 9 }).map(item => item.value),
    ['http://second:80', 'http://first:80'])
})
