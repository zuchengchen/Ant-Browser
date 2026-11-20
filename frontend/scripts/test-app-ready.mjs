import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createContext, runInContext } from 'node:vm'
import { test } from 'node:test'

const frontend = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(frontend, 'package.json'))
const compiled = require('esbuild').buildSync({
  stdin: {
    contents: `export * from './src/utils/appReady';
      export * from './src/utils/errors';
      export { fetchBrowserProxies, fetchBrowserProxyGroups, fetchBrowserProxiesByGroup,
        fetchBrowserProxyVirtualGroups, fetchBrowserProxyVirtualGroupHealth } from './src/modules/browser/api/proxies';`,
    resolveDir: frontend,
  },
  bundle: true, platform: 'browser', format: 'iife', globalName: 'subject', write: false,
}).outputFiles[0].text

function harness(readyAt = 0) {
  let now = 0, timerID = 0, checks = 0
  const timers = new Map(), calls = []
  const app = { IsAppReady: async () => { checks++; return now >= readyAt } }
  const data = {
    BrowserProxyList: [{ proxyId: 'proxy-1', proxyName: '测试代理' }],
    BrowserProxyListGroups: ['测试分组'],
    BrowserProxyListByGroup: [{ proxyId: 'proxy-1' }],
    BrowserProxyVirtualGroupList: [{ groupId: 'virtual-1', groupName: '虚拟分组' }],
    BrowserProxyVirtualGroupHealthList: [{ groupId: 'virtual-1', healthy: true }],
  }
  for (const [method, value] of Object.entries(data)) {
    app[method] = async (...args) => { calls.push({ method, args, time: now }); return value }
  }
  const context = createContext({
    go: { main: { App: app } },
    setTimeout: (fn, ms) => { const id = ++timerID; timers.set(id, { at: now + ms, fn }); return id },
    clearTimeout: id => timers.delete(id),
  })
  context.window = context
  runInContext(compiled, context)
  const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }
  const advance = async ms => {
    await flush()
    const target = now + ms
    while (true) {
      const next = [...timers].filter(([, timer]) => timer.at <= target).sort((a, b) => a[1].at - b[1].at)[0]
      if (!next) break
      const [id, timer] = next
      now = timer.at; timers.delete(id); timer.fn()
      await flush()
    }
    now = target
    await flush()
  }
  return { api: context.subject, context, app, calls, timers, advance, flush, checks: () => checks }
}

test('a 60-second startup makes no premature catalog calls and shares one readiness wait', async () => {
  const h = harness(60_000)
  const result = Promise.all([
    h.api.fetchBrowserProxies(), h.api.fetchBrowserProxyGroups(),
    h.api.fetchBrowserProxiesByGroup('测试分组'), h.api.fetchBrowserProxyVirtualGroups(),
    h.api.fetchBrowserProxyVirtualGroupHealth(),
  ])
  await h.advance(59_999)
  assert.equal(h.calls.length, 0)
  await h.advance(1)
  const values = await result
  assert.equal(values[0][0].proxyId, 'proxy-1')
  assert.equal(values[1][0], '测试分组')
  assert.equal(values[3][0].groupId, 'virtual-1')
  assert.equal(values[4][0].healthy, true)
  assert.equal(h.calls.length, 5)
  assert.ok(h.calls.every(call => call.time === 60_000))
  assert.equal(h.checks(), 241, 'parallel requests must not multiply readiness polling')
  assert.equal(h.timers.size, 0)
})

test('already-ready backends load immediately without receiving an app:ready event', async () => {
  const h = harness()
  assert.equal((await h.api.fetchBrowserProxies())[0].proxyId, 'proxy-1')
  await h.api.fetchBrowserProxies()
  assert.equal(h.checks(), 1)
  assert.equal(h.calls.length, 2)
  assert.equal(h.timers.size, 0)
})

test('genuine Wails string errors propagate once and retain their diagnostic text', async () => {
  const h = harness()
  let calls = 0
  h.app.BrowserProxyList = async () => { calls++; throw '数据库读取失败：database is locked' }
  await assert.rejects(h.api.fetchBrowserProxies(), error => {
    assert.equal(h.api.errorMessage(error, '加载代理目录失败'), '数据库读取失败：database is locked')
    return true
  })
  assert.equal(calls, 1)
  assert.equal(h.timers.size, 0)
})

test('a readiness RPC failure does not unlock data reads and a later visit can retry', async () => {
  const h = harness()
  h.app.IsAppReady = async () => { throw '就绪状态查询失败' }
  await assert.rejects(h.api.fetchBrowserProxies(), error => error === '就绪状态查询失败')
  assert.equal(h.calls.length, 0)
  assert.equal(h.timers.size, 0)
  h.app.IsAppReady = async () => true
  assert.equal((await h.api.fetchBrowserProxies())[0].proxyId, 'proxy-1')
})

test('an unfinished startup reports a bounded timeout and allows recovery', async () => {
  const h = harness(Infinity)
  const timedOut = assert.rejects(h.api.fetchBrowserProxies(), /应用初始化超时/)
  await h.advance(120_000)
  await timedOut
  assert.equal(h.calls.length, 0)
  assert.equal(h.timers.size, 0)
  h.app.IsAppReady = async () => true
  assert.equal((await h.api.fetchBrowserProxies())[0].proxyId, 'proxy-1')
})

test('even an unresponsive readiness RPC times out and a late response cannot restart polling', async () => {
  const h = harness()
  let respond
  h.app.IsAppReady = () => new Promise(resolve => { respond = resolve })
  const timedOut = assert.rejects(h.api.fetchBrowserProxies(), /应用初始化超时/)
  await h.advance(120_000)
  await timedOut
  respond(false)
  await h.flush()
  assert.equal(h.calls.length, 0)
  assert.equal(h.timers.size, 0)
  h.app.IsAppReady = async () => true
  await h.api.fetchBrowserProxies()
  assert.equal(h.calls.length, 1)
})

test('browser-only previews need no native readiness bridge', async () => {
  const h = harness()
  delete h.context.go
  await h.api.waitForAppReady()
  assert.equal(h.checks(), 0)
  assert.equal(h.timers.size, 0)
})

test('error display accepts Error, string and structured Wails rejections', () => {
  const h = harness()
  const error = runInContext('new Error("原始异常")', h.context)
  for (const [value, expected] of [
    [error, '原始异常'], ['读取失败', '读取失败'], [{ message: '读取失败' }, '读取失败'],
    [{ error: '失败原因' }, '失败原因'], [{ detail: '详细原因' }, '详细原因'],
    [null, '默认错误'], ['', '默认错误'], [{ message: '' }, '默认错误'],
  ]) assert.equal(h.api.errorMessage(value, '默认错误'), expected)
})
