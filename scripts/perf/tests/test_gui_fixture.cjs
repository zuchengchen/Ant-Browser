// Business predicates and frame completion controls; real Wails calibration is separate.
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')
const test = require('node:test')
const { performance } = require('node:perf_hooks')
const source = fs.readFileSync(path.join(__dirname, '../fixtures/gui-operations.js'), 'utf8')

function fixture(options = {}) {
  let keyword = '', selected = ['perf-profile-00000', 'perf-profile-00042'], frames = 0, delivered = 0, inputEvents = 0
  let callbackNow = options.initialNow
  const clock = { now: () => callbackNow ?? performance.now() }
  class Input {
    constructor() { this.current = ''; this.defaultValue = ''; this.isConnected = true; this.listeners = [] }
    get value() { return this.current }
    set value(value) { this.current = value }
    addEventListener(name, handler) { this.listeners.push(handler) }
    removeEventListener(name, handler) { this.listeners = this.listeners.filter(item => item !== handler) }
    dispatchEvent(event) {
      inputEvents++
      event.target = this
      for (const handler of this.listeners) handler(event)
      if (options.controlled !== false) this.defaultValue = this.current
      if (options.persisted !== false) keyword = this.current
      selected = this.current ? ['perf-profile-00042'] : ['perf-profile-00000', 'perf-profile-00042']
    }
  }
  const input = new Input()
  const document = {
    visibilityState: 'visible',
    querySelector: selector => selector.startsWith('input') ? input : null,
    querySelectorAll: selector => {
      if (selector.startsWith('input')) return [input]
      if (selector.startsWith('tbody a')) return (options.breakAfterFrame && delivered >= options.breakAfterFrame ? [] : selected).map(id => ({
        textContent: options.wrongName ? 'Wrong name' : `性能实例 ${id.slice(-5)}`,
        getAttribute: () => `/browser/detail/${id}`,
      }))
      return []
    },
  }
  const context = {
    window: {}, document, location: { pathname: '/browser/list' }, performance: clock,
    localStorage: { getItem: () => JSON.stringify({ keyword }) }, HTMLInputElement: Input,
    Event: class { constructor(type) { this.type = type } }, setTimeout, clearTimeout,
    cancelAnimationFrame: clearTimeout,
    requestAnimationFrame: callback => {
      frames++
      if (options.noFrames || (options.onlyOneFrame && frames > 1)) return 0
      return setTimeout(() => {
        const index = delivered++
        if (options.callbackTimes) callbackNow = options.callbackTimes[index]
        callback(options.rafTimes ? options.rafTimes[Math.min(index, options.rafTimes.length - 1)] : performance.now())
      }, 2)
    },
  }
  vm.runInNewContext(source, context)
  return { gui: context.window.antPerfGui, input, frameCount: () => frames, inputCount: () => inputEvents }
}

test('correct full IDs, names, controlled value and two frames complete', async () => {
  const f = fixture()
  const result = await f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 100)
  assert.equal(result.render.frame_count, 2)
  assert.equal(result.render.render_proof_version, 2)
  assert.equal(result.render.frame_attempts.length, 2)
  assert.ok(result.render.frame_attempts[1].raf_ms > result.render.frame_attempts[0].raf_ms)
  assert.equal(result.rows[0].name, '性能实例 00042')
  assert.equal(f.frameCount(), 2)
  assert.equal(f.input.defaultValue, '性能实例 00042')
})

test('wrong full IDs cannot pass despite correct count', async () => {
  const f = fixture()
  await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00000'], 25), /Timed out/)
})

test('wrong name or stale controlled/persisted state cannot pass', async () => {
  for (const options of [{ wrongName: true }, { controlled: false }, { persisted: false }]) {
    const f = fixture(options)
    await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 25), /Timed out/)
  }
})

test('no frame or only one frame cannot complete', async () => {
  for (const options of [{ noFrames: true }, { onlyOneFrame: true }]) {
    const f = fixture(options)
    await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 25), /frame deadline/)
  }
})

test('duplicate IDs are rejected even if both lists duplicate them', () => {
  const f = fixture()
  assert.equal(f.gui.sameList(['a', 'a'], ['a', 'a']), false)
})

test('coarsened callback clocks may tie when native frame timestamps advance', async () => {
  const f = fixture({ initialNow: 100, callbackTimes: [100, 100], rafTimes: [80, 90] })
  const { render } = await f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 100)
  assert.equal(render.predicate_ms, 100)
  assert.equal(render.first_frame_ms, 100)
  assert.equal(render.second_frame_ms, 100)
  assert.equal(render.completed_ms, 100)
  assert.equal(render.deadline_ms, 200)
  assert.deepEqual(Array.from(render.frame_attempts, item => item.raf_ms), [80, 90])
  assert.equal(f.inputCount(), 1)
})

test('repeated native timestamp waits for the first distinct frame without resending input', async () => {
  const f = fixture({ rafTimes: [10, 10, 10, 20] })
  const { render } = await f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 100)
  assert.deepEqual(Array.from(render.frame_attempts, item => item.raf_ms), [10, 10, 10, 20])
  assert.equal(f.frameCount(), 4)
  assert.equal(f.inputCount(), 1)
})

test('permanently repeated native timestamp cannot complete or reset the deadline', async () => {
  const f = fixture({ rafTimes: [10] })
  const started = performance.now()
  await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 25), /deadline/)
  assert.ok(performance.now() - started < 200)
  assert.ok(f.frameCount() >= 2)
  assert.equal(f.inputCount(), 1)
})

test('backwards and nonfinite clocks are rejected with diagnostic context', async () => {
  for (const options of [
    { rafTimes: [10, 9] },
    { initialNow: 100, callbackTimes: [100, 99], rafTimes: [10, 20] },
    ...[NaN, Infinity, -1, undefined].map(value => ({ rafTimes: [value] })),
    { initialNow: 100, callbackTimes: [NaN], rafTimes: [10] },
  ]) {
    const f = fixture(options)
    await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 100),
      /(?:Invalid rendered-frame clock|clock moved backwards): profile filter IDs, names and controlled input;.*attempt_count/)
  }
})

test('business predicate must still hold after distinct native frames', async () => {
  const f = fixture({ breakAfterFrame: 2 })
  await assert.rejects(f.gui.filterProfiles('性能实例 00042', ['perf-profile-00042'], 25), /Timed out/)
  assert.equal(f.inputCount(), 1)
})
