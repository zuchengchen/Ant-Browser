import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import test from 'node:test'
import vm from 'node:vm'
import { loginToChatGPT } from '../../backend/internal/browser/builtinassets/keypouch-gpt/login.js'

const email = 'sample@example.test'
const password = ' sample $ & password '
const totpSecret = 'gezd-gnbv gy3tqojq gezdgnbv gy3tqojq='
const account = { email, password, totpSecret }
const auth = 'https://auth.openai.com'
const settings = 'https://chatgpt.com/settings/account'
const loginWith = 'https://chatgpt.com/auth/login_with?state=fixture'
const authorize = auth + '/oauth/authorize?client_id=fixture'
const tabId = 137

class Element {
  constructor(tag, props = {}) {
    Object.assign(this, { tag, innerText: '', name: '', type: '', autocomplete: '', isConnected: true, children: [], ancestor: null }, props)
  }
  get innerText() { return [this.ownText, ...this.children.filter(child => !child.hidden).map(child => child.innerText)].filter(Boolean).join('\n') }
  set innerText(value) { this.ownText = value }
  get nodeType() { return 1 }
  get childNodes() { return this.ownText ? [{ nodeType: 3, textContent: this.ownText }, ...this.children] : this.children }
  append(tag, props = {}) {
    const child = tag === 'input' ? new Input(tag, props) : new Element(tag, props)
    child.ancestor = this
    child.form = this.tag === 'form' ? this : this.form
    this.children.push(child)
    return child
  }
  get parentElement() { return this.ancestor }
  getAttribute(name) {
    const value = this[name === 'formaction' ? 'formAction' : name]
    return value == null || value === '' ? null : String(value)
  }
  hasAttribute(name) { return this.getAttribute(name) !== null }
  matches(selectors) {
    return selectors.split(',').some(selector => {
      const tag = selector.match(/^[\w-]+/)?.[0]
      if ((tag && this.tag !== tag) || selector.startsWith('.')) return false
      return Array.from(selector.matchAll(/\[([\w-]+)(?:(\^?\*?=)"([^"]+)")?\]/g)).every(([, attr, op, value]) => {
        const actual = this.getAttribute(attr)
        if (!op) return actual !== null
        if (op === '^=') return actual?.startsWith(value)
        if (op === '*=') return actual?.includes(value)
        return actual === value
      })
    })
  }
  closest(selector) { return this.matches(selector) ? this : this.ancestor?.closest(selector) || null }
  querySelectorAll(selector) { return this.children.flatMap(el => [...(el.matches(selector) ? [el] : []), ...el.querySelectorAll(selector)]) }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null }
  getBoundingClientRect() { return this.hidden ? { width: 0, height: 0 } : { width: 80, height: 30 } }
  get elements() { return this.querySelectorAll('input,button') }
  checkValidity() { return true }
  dispatchEvent(event) { this.onEvent?.(event) }
  focus() { this.focused = true }
  click() { this.onClick?.() }
}

class Input extends Element {
  get value() { return this.textValue || '' }
  set value(value) { this.textValue = value }
}

class TestInputEvent extends Event {
  constructor(type, options) {
    super(type, options)
    this.data = options.data
    this.inputType = options.inputType
  }
}

// Chrome creates a fresh isolated world on navigation, rejects dead document
// IDs and serializes the injected function without the extension's closures.
function browser(t, spec) {
  const calls = [], clicks = [], edits = [], delays = [], pageDelays = [], urls = [], listeners = new Set()
  let serial = 0, page, now = 1_800_000_010_000
  const controller = new AbortController()
  t.mock.method(Date, 'now', () => now)
  t.mock.method(globalThis, 'setTimeout', (fn, delay) => { delays.push(delay); now += Math.max(1000, delay); return setImmediate(fn) })
  t.mock.method(globalThis, 'clearTimeout', clearImmediate)
  const previousChrome = globalThis.chrome
  t.after(() => { if (previousChrome) globalThis.chrome = previousChrome; else delete globalThis.chrome })

  const endpoint = {
    runtime: {
      onMessage: { addListener: listener => listeners.add(listener), removeListener: listener => listeners.delete(listener) },
      sendMessage: async message => { for (const listener of listeners) listener(message, { tab: { id: tabId }, frameId: 0, documentId: page.id }) },
    },
    tabs: { get: async id => { assert.equal(id, tabId); return { url: page.url, status: 'complete' } } },
    scripting: { executeScript: async ({ target, world, func, args }) => {
      const [payload] = args
      assert.equal(target.tabId, tabId)
      assert.equal(world, 'ISOLATED')
      assert.ok(!JSON.stringify(payload).includes(totpSecret), 'TOTP seed entered the target document')
      assert.ok(payload.value !== password || payload.stage === 'password', 'password entered a different stage')
      if (payload.delay) assert.deepEqual(Object.keys(payload).sort(), ['delay', 'id'])
      if (payload.stage || payload.end || payload.delay) assert.deepEqual(Object.keys(target).sort(), ['documentIds', 'tabId'])
      else assert.deepEqual(target.frameIds, [0])
      const current = page
      if (target.documentIds && target.documentIds[0] !== current.id) throw Error('Document replaced')
      calls.push({ stage: payload.stage, value: payload.value, hasEmail: Object.hasOwn(payload, 'email'), url: current.url })
      const answer = await vm.runInContext('(' + func.toString() + ')(payload)', Object.assign(current.world, { payload }))
      current.onInject?.(answer, payload)
      if (current.loseSubmit && answer === 'submitted') throw Error('Navigation destroyed the document')
      return [{ frameId: 0, documentId: current.id, result: answer }]
    } },
  }
  globalThis.chrome = endpoint
  function navigate(options) {
    const url = options.url || (options.kind === 'settings' ? settings : auth + '/log-in/' + (options.kind || ''))
    urls.push(url)
    const document = new Element('document')
    document.getElementById = id => document.querySelectorAll('[id]').find(el => el.id === id) || null
    document.readyState = 'complete'; document.title = options.title || ''
    document.baseURI = url; document.visibilityState = 'visible'; document.hasFocus = () => !options.background
    document.body = document.append('body')
    const main = document.body.append('main', { innerText: options.conversation ? '' : options.text || '' })
    if (options.conversation) main.append('article', { 'data-message-author-role': 'user', innerText: options.text })
    if (options.composer) main.append('div', { contenteditable: 'true', innerText: options.composer })
    if (options.iframe) main.append('iframe', { src: options.iframe })
    const callbacks = {}
    const win = { addEventListener: (event, cb) => { callbacks[event] = cb } }
    win.top = win
    document.addEventListener = win.addEventListener
    const pageTimer = (fn, milliseconds) => {
      pageDelays.push(milliseconds)
      now += milliseconds
      return setImmediate(() => { if (options.onPageWait?.(page) !== false) fn() })
    }
    const location = { href: url, assign: dst => { if (dst !== 'https://chatgpt.com/') navigate({ kind: 'settings', text: 'Email\n' + email }); else urls.push(dst) } }
    page = {
      url, id: 'doc-' + (++serial), main, callbacks, ...options,
      world: vm.createContext({ window: win, document, location, chrome: endpoint, HTMLInputElement: Input, Node: { ELEMENT_NODE: 1, TEXT_NODE: 3 }, Date, URL, Event, InputEvent: TestInputEvent, AbortController,
        setTimeout: pageTimer, clearTimeout: clearImmediate, getComputedStyle: () => ({}) }),
    }
    const sources = {
      email: { type: 'email', name: 'username' },
      password: { type: 'password', autocomplete: 'current-password' },
      totp: { type: 'text', autocomplete: 'one-time-code' },
    }
    if (sources[options.kind]) {
      page.form = main.append('form', { action: url, method: 'post', ...options.form })
      if (options.combined) page.companion = page.form.append('input', sources.email)
      page.field = page.form.append('input', { ...sources[options.kind], ...options.input })
      const button = page.form.append('button', { innerText: 'Continue', type: 'submit', ...options.button })
      page.field.onEvent = ev => {
        if (ev.type === 'input') edits.push({ kind: options.kind, value: page.field.value, data: ev.data, inputType: ev.inputType, focused: page.field.focused, at: now })
        options.onField?.(page, ev)
      }
      button.onClick = () => {
        clicks.push({ kind: options.kind, account: page.companion?.value, value: page.field.value, at: now })
        if (options.next) navigate(options.next)
      }
    }
    return page
  }
  navigate(spec)
  return {
    get page() { return page }, urls, clicks, edits, delays, pageDelays, calls, controller, navigate,
    run: (selected = account, getCurrentCode) => loginToChatGPT(tabId, selected, controller.signal, getCurrentCode),
  }
}

const verified = { kind: 'settings', text: 'Account\nEmail\n' + email }

test('the two committed sign-in redirects only observe the page before reaching the identified password form', async t => {
  const home = 'https://chatgpt.com/'
  const b = browser(t, { url: home, kind: 'email', form: { action: loginWith } })
  b.page.main.role = 'dialog'
  const homeButton = b.page.form.querySelector('button')
  const submitEmail = homeButton.onClick
  homeButton.onClick = () => {
    submitEmail()
    b.navigate({ kind: 'password', url: loginWith, onInject: (stage, request) => {
      if (request.stage || request.end) return
      b.navigate({ kind: 'email', url: authorize, onInject: (stage, request) => {
        if (!request.stage && !request.end) b.navigate({ kind: 'password', text: email, next: verified })
      } })
    } })
  }
  assert.equal((await b.run()).status, 'success')
  assert.deepEqual(b.urls, [home, loginWith, authorize, auth + '/log-in/password', settings, home])
  assert.deepEqual(b.clicks.map(click => click.kind), ['email', 'password'])
  assert.ok(!b.calls.some(call => [loginWith, authorize].includes(call.url) && (call.stage || call.value || call.hasEmail)))
})

test('redirects reached without a prior email share one timeout and ignore actionable fields', async t => {
  const b = browser(t, {})
  const start = Date.now()
  let firstPagePolls = 0
  const first = b.navigate({ kind: 'email', url: loginWith, onInject: (stage, request) => {
    if (request.stage || request.end || ++firstPagePolls !== 8) return
    const second = b.navigate({ kind: 'email', url: authorize })
    second.form.append('input', { type: 'password', autocomplete: 'current-password' })
  } })
  first.main.append('button', { innerText: 'Log in' }).onClick = () => assert.fail('clicked the redirect')
  const outcome = await b.run()
  assert.equal(outcome.status, 'manual')
  assert.match(outcome.message, /跳转/)
  assert.deepEqual(b.urls.slice(-2), [loginWith, authorize])
  assert.ok(Date.now() - start >= 12000 && Date.now() - start <= 15000)
  assert.deepEqual(b.clicks, [])
  assert.deepEqual(b.edits, [])
  assert.ok(!b.calls.some(call => call.stage || call.value || call.hasEmail))
})

test('a challenge or trusted user interaction stops the read-only redirects', async t => {
  const b = browser(t, {})
  for (const url of [loginWith, authorize]) {
    b.navigate({ url, kind: 'email', text: 'Please verify you are human' })
    assert.match((await b.run()).message, /人机验证/)
    b.navigate({ url, kind: 'email', iframe: 'https://challenges.cloudflare.com/turnstile/example' })
    assert.match((await b.run()).message, /人机验证/)
    b.navigate({ url, kind: 'email', onInject: (stage, request) => {
      if (!request.stage && !request.end) b.page.callbacks.pointerdown({ isTrusted: true })
    } })
    assert.match((await b.run()).message, /停止/)
  }
  assert.ok(!b.calls.some(call => call.stage || call.value))
})

test('a late redirect is still constrained by the original overall login deadline', async t => {
  const b = browser(t, { url: 'https://chatgpt.com/' })
  const began = Date.now()
  b.page.onInject = (stage, request) => {
    if (!request.end && stage === 'waiting' && Date.now() - began >= 85000) b.navigate({ url: authorize, kind: 'email' })
  }
  assert.match((await b.run()).message, /跳转/)
  assert.equal(Date.now() - began, 90000)
  assert.deepEqual(b.clicks, [])
  assert.ok(!b.calls.some(call => call.url === authorize && (call.stage || call.hasEmail || call.value)))
})

test('redirects cannot authorize the next password, change origins or restart an interrupted email', async t => {
  const b = browser(t, {})
  for (const [changes, status] of [[{ text: 'other@example.test' }, 'mismatch'], [{ form: { method: 'get' } }, 'manual']]) {
    b.navigate({ url: authorize, onInject: (stage, request) => {
      if (!request.end) b.navigate({ kind: 'password', text: email, ...changes })
    } })
    assert.equal((await b.run()).status, status)
  }
  for (const url of ['https://chatgpt.com/auth/login_with/extra', 'https://chatgpt.com/oauth/authorize', auth + '/oauth/authorize/extra', 'https://other.example.test/oauth/authorize']) {
    b.navigate({ kind: 'password', url, text: email })
    const before = Date.now()
    const outcome = await b.run()
    assert.equal(outcome.status, 'manual')
    assert.doesNotMatch(outcome.message, /登录跳转/)
    assert.ok(Date.now() - before < 1000)
  }
  assert.ok(!b.calls.some(call => call.value))
  const page = b.navigate({ url: 'https://chatgpt.com/auth/login', kind: 'email', onField: (current, event) => {
    if (event.type === 'input') {
      current.url = loginWith
      current.world.location.href = loginWith
    }
  } })
  assert.match((await b.run()).message, /停止/)
  assert.equal(page.field.value, email[0])
  assert.deepEqual(b.clicks, [])
  assert.ok(!b.calls.some(call => call.url === loginWith && (call.value || call.hasEmail)))
})

test('a paired account requests a fresh code only for its authenticator challenge', async t => {
  const selected = { email, password }
  const b = browser(t, { kind: 'password', text: email, next: {
    kind: 'totp', url: auth + '/mfa/totp', text: 'Enter the code from your authenticator app', next: verified,
  } })
  let requests = 0
  const getCurrentCode = async signal => {
    assert.equal(signal, b.controller.signal)
    assert.deepEqual(b.clicks.map(click => click.kind), ['password'])
    requests++
    return { value: '046128', expires: Date.now() + 30000 }
  }
  assert.equal((await b.run(selected, getCurrentCode)).status, 'success')
  assert.equal(requests, 1)
  assert.equal(b.clicks[1].value, '046128')
  b.navigate({ kind: 'totp', url: auth + '/email-verification', text: 'Check your inbox for the code' })
  assert.equal((await b.run(selected, getCurrentCode)).status, 'manual')
  assert.equal(requests, 1)
})

test('one normal email, password and authenticator submission, then verify the visible settings email', async t => {
  const b = browser(t, { kind: 'email', url: auth + '/log-in-or-create-account', next: {
    kind: 'password', text: email, next: { kind: 'totp', url: auth + '/mfa/totp', text: 'Enter the code from your authenticator app', next: verified },
  } })
  assert.equal((await b.run()).status, 'success')
  assert.deepEqual(b.clicks.map(c => c.kind), ['email', 'password', 'totp'])
  assert.equal(b.clicks[0].value, email)
  assert.equal(b.clicks[1].value, password)
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(b.clicks[2].at / 30000)))
  const digest = createHmac('sha1', '12345678901234567890').update(counter).digest()
  const expected = ((digest.readUInt32BE(digest.at(-1) & 15) & 0x7fffffff) % 1000000).toString().padStart(6, '0')
  assert.equal(b.clicks[2].value, expected)
  assert.equal(b.urls.at(-1), 'https://chatgpt.com/')
})

test('a combined password form uses the selected email, while an existing other session is retained', async t => {
  const b = browser(t, { kind: 'password', combined: true, next: verified })
  assert.equal((await b.run()).status, 'success')
  assert.equal(b.clicks[0].account, email)
  b.navigate({ ...verified, text: '邮箱\nother@example.test' })
  assert.equal((await b.run()).status, 'mismatch')
  assert.equal(b.urls.at(-1), settings)
})

test('the guest homepage waits for a visible sign-in control, then the returning-account chooser and email form', async t => {
  const b = browser(t, { url: 'https://chatgpt.com/' })
  let waits = 0, homeClicks = 0, chooserClicks = 0
  b.page.main.append('button', { innerText: 'Log in', hidden: true }).onClick = () => assert.fail('hidden login')
  b.page.main.append('button', { innerText: 'Sign up' }).onClick = () => assert.fail('registration')
  b.page.onInject = outcome => {
    if (outcome !== 'waiting' || ++waits !== 3) return
    b.page.main.append('button', { innerText: '登录' }).onClick = () => {
      homeClicks++
      const dialog = b.page.main.append('section', { role: 'dialog', innerText: 'Log in or sign up' })
      dialog.append('button', { innerText: 'other@example.test' }).onClick = () => assert.fail('unselected existing account')
      dialog.append('button', { innerText: 'Log in to another account' }).onClick = () => {
        chooserClicks++
        b.navigate({ kind: 'email', url: auth + '/log-in-or-create-account', next: {
          kind: 'password', text: email, next: verified,
        } })
      }
    }
    b.page.main.append('button', { innerText: 'Log in' }).onClick = () => assert.fail('second login')
  }
  assert.equal((await b.run()).status, 'success')
  assert.equal(homeClicks, 1)
  assert.equal(chooserClicks, 1)
  assert.deepEqual(b.clicks.map(item => item.kind), ['email', 'password'])
  assert.deepEqual(b.urls, ['https://chatgpt.com/', auth + '/log-in-or-create-account', auth + '/log-in/password', settings, 'https://chatgpt.com/'])
})

test('each visible input edit inserts one character, preserving Unicode, whitespace and variable bounded pauses', async t => {
  const samples = [0, 0.49, 0.999]
  let index = 0
  t.mock.method(Math, 'random', () => samples[index++ % samples.length])
  const selected = { ...account, password: ' päss🌿 word ' }
  const b = browser(t, { kind: 'email', next: { kind: 'password', text: email, next: {
    kind: 'totp', url: auth + '/mfa/totp', text: 'Authenticator app', next: verified,
  } } })
  assert.equal((await b.run(selected)).status, 'success')
  for (const { kind, value, at } of b.clicks) {
    const entries = b.edits.filter(edit => edit.kind === kind)
    assert.deepEqual(entries.map(edit => edit.data), [...value])
    assert.deepEqual(entries.map(edit => edit.value), [...value].map((_, i, chars) => chars.slice(0, i + 1).join('')))
    assert.ok(entries.every(edit => edit.inputType === 'insertText' && edit.focused))
    const intervals = entries.slice(1).map((edit, i) => edit.at - entries[i].at)
    assert.ok(intervals.every(n => n >= 80 && n <= 140))
    assert.ok(new Set(intervals).size > 1)
    assert.ok(at - entries.at(-1).at >= 350 && at - entries.at(-1).at <= 500)
  }
  assert.ok(b.delays.every(ms => ms === 400), 'the background picker must never time a character or pre-submit pause')
  assert.ok(b.pageDelays.some(ms => ms <= 140) && b.pageDelays.some(ms => ms >= 350))
})

test('switching tabs during the visible-document timer leaves the next character untouched', async t => {
  const b = browser(t, { kind: 'password', text: email, onPageWait: page => {
    page.world.document.visibilityState = 'hidden'
    page.callbacks.visibilitychange()
    return false
  } })
  assert.equal((await b.run()).status, 'manual')
  assert.equal(b.edits.length, 1)
  assert.deepEqual(b.clicks, [])
})

test('cancelling the background picker wakes a pinned document wait without another edit', async t => {
  const b = browser(t, { kind: 'password', text: email, onPageWait: () => {
    b.controller.abort()
    return false
  } })
  assert.equal((await b.run()).status, 'manual')
  assert.equal(b.edits.length, 1)
  assert.deepEqual(b.clicks, [])
})

test('manual interaction, a human challenge or navigation during typing stops before another character', async t => {
  const b = browser(t, {})
  for (const interrupt of [
    page => page.callbacks.keydown({ isTrusted: true }),
    page => { page.world.document.title = 'Security Verification' },
    page => { page.form.action = 'https://external.example.test/' },
    () => b.navigate({ kind: 'password', text: email }),
  ]) {
    b.navigate({ kind: 'password', text: email, onField: (page, event) => {
      if (event.type === 'input') interrupt(page)
    } })
    const before = b.edits.length
    assert.equal((await b.run()).status, 'manual')
    assert.equal(b.edits.length - before, 1)
    assert.equal(b.edits.at(-1).value, password[0])
    assert.deepEqual(b.clicks, [])
  }
})

test('a signed-in homepage verifies the selected email via account settings', async t => {
  const b = browser(t, { url: 'https://chatgpt.com/' })
  b.page.main.append('button', { 'data-testid': 'profile-button', innerText: 'A' })
  b.page.main.append('button', { innerText: 'Use another account' }).onClick = () => assert.fail('must preserve the signed-in account')
  assert.equal((await b.run()).status, 'success')
  assert.deepEqual(b.urls, ['https://chatgpt.com/', settings, 'https://chatgpt.com/'])
  assert.deepEqual(b.edits, [])
  assert.deepEqual(b.clicks, [])
})

test('six authenticator slots advance one digit per focus and wait for the site to submit', async t => {
  const b = browser(t, {})
  for (const nativeSubmit of [true, false]) {
    const page = b.navigate({ kind: 'totp', url: auth + '/mfa/totp', input: { maxLength: 1 }, text: email + '\nAuthenticator app' })
    if (!nativeSubmit) page.form.querySelector('button').hidden = true
    const slots = [page.field, ...Array.from({ length: 5 }, () => page.form.append('input', { type: 'tel', maxLength: 1 }))]
    const seen = []
    for (const node of slots) {
      node.onEvent = ev => {
        if (ev.type !== 'input') return
        assert.equal(node.focused, true)
        assert.equal(node.value, ev.data)
        seen.push(ev.data)
        if (seen.length === 6) {
          if (nativeSubmit) page.callbacks.submit({ target: page.form })
          b.navigate(verified)
        }
      }
    }
    assert.equal((await b.run({ email, password }, async () => ({ value: '016432', expires: Date.now() + 30000 }))).status, 'success')
    assert.equal(seen.join(''), '016432')
    assert.equal(b.clicks.length, 0, 'a site-initiated submit must not receive another click')
  }
})

test('an ambiguous or unrelated group of code inputs stays manual', async t => {
  const b = browser(t, {})
  for (const count of [4, 5, 7, 8]) {
    const page = b.navigate({ kind: 'totp', url: auth + '/mfa/totp', text: email, input: { maxLength: 1 } })
    for (let i = 1; i < count; i++) page.form.append('input', { type: 'text', maxLength: 1 })
    const status = (await b.run()).status
    assert.equal(status, 'manual', 'slots=' + count)
    assert.equal(b.edits.length, 0)
  }
})

test('a late password submit receipt cannot complete the authenticator step in the same document', async t => {
  const b = browser(t, { kind: 'password', text: email })
  const page = b.page
  const runtime = globalThis.chrome.runtime, deliver = runtime.sendMessage
  let lateReceipt, nativeCodeSubmits = 0, codeInputs = 0, codeRequests = 0
  runtime.sendMessage = async message => {
    if (message.type === 'keypouch-login-submitted' && message.stage === 'password') lateReceipt = message
    else await deliver(message)
  }
  page.form.querySelector('button').onClick = () => {
    page.callbacks.submit({ target: page.form })
    const challenge = auth + '/mfa-challenge/fixture'
    page.url = page.world.location.href = page.world.document.baseURI = challenge
    page.main.innerText = 'Check your authenticator app'
    page.main.children = []
    page.form = page.main.append('form', { action: challenge, method: 'post' })
    page.field = page.form.append('input', { type: 'text', name: 'code', autocomplete: 'one-time-code', maxLength: 6 })
    page.field.onEvent = event => { if (event.type === 'input') codeInputs++ }
    page.form.append('button', { type: 'submit', innerText: 'Continue' }).onClick = () => {
      nativeCodeSubmits++
      page.callbacks.submit({ target: page.form })
      b.navigate(verified)
    }
    page.onInject = (outcome, payload) => {
      if (outcome === 'ready' && payload.delay >= 350 && codeInputs === 6 && lateReceipt) {
        void deliver(lateReceipt)
        lateReceipt = undefined
      }
    }
  }
  const outcome = await b.run({ email, password }, async () => {
    codeRequests++
    return { value: '012345', expires: Date.now() + 30000 }
  })
  assert.equal(lateReceipt, undefined)
  assert.equal(codeInputs, 6)
  assert.equal(codeRequests, 1)
  assert.equal(nativeCodeSubmits, 1)
  assert.equal(outcome.status, 'success')
})

test('a native submit confirms a prefilled password even when its action response is lost', async t => {
  const b = browser(t, {
    kind: 'password', text: email, input: { value: password }, loseSubmit: true,
    next: { kind: 'totp', text: 'Check your authenticator app', next: verified },
  })
  const page = b.page, button = page.form.querySelector('button'), submit = button.onClick
  button.onClick = () => {
    page.callbacks.submit({ target: page.form })
    submit()
  }
  assert.equal((await b.run()).status, 'success')
  assert.deepEqual(b.clicks.map(click => click.kind), ['password', 'totp'])
})

test('authenticator validation while typing leaves Continue available, and real submissions are never repeated', async t => {
  const b = browser(t, {})
  const fieldIntent = JSON.stringify({ type: 'validate', payload: { name: 'code' } })
  for (const { name = '__intent__', value = fieldIntent, cancel = true, source = 'secondary', verify = false } of [
    { verify: true }, { name: 'intent', value: '{"type":"validate"}', verify: true },
    { name: 'intent', value: 'validate', verify: true }, { cancel: false },
    { value: '{"type":"validate","payload":{"name":"other"}}' }, { value: '{"type":"submit"}' },
    { value: 'unrecognized' }, { name: 'other' }, { source: 'primary' }, { source: 'implicit' },
  ]) {
    let validations = 0, codes = 0
    const before = b.clicks.length, edits = b.edits.length
    b.navigate({
      kind: 'totp', url: auth + '/mfa-challenge/fixture', text: email + '\nCheck your authenticator app', next: verified,
      input: { name: 'code', maxLength: 6 },
      onField: (page, input) => {
        if (input.type !== 'input') return
        validations++
        const primary = page.form.querySelector('button')
        const submitter = source === 'implicit' ? null : source === 'primary' ? primary
          : page.form.append('button', { type: 'submit', hidden: true, formNoValidate: true })
        if (submitter) Object.assign(submitter, { name, value })
        const event = new Event('submit', { bubbles: true, cancelable: true })
        Object.defineProperties(event, { target: { value: page.form }, submitter: { value: submitter } })
        page.callbacks.submit(event)
        if (cancel) event.preventDefault()
        if (source === 'secondary') {
          page.form.children = page.form.children.filter(node => node !== submitter)
          submitter.isConnected = false
        }
      },
    })
    const result = await b.run(account, async () => {
      codes++
      return { value: '012345', expires: Date.now() + 30000 }
    })
    assert.equal(validations, 6)
    assert.equal(codes, 1)
    assert.equal(b.edits.length - edits, 6)
    assert.equal(b.clicks.length - before, verify ? 1 : 0)
    assert.equal(result.status, verify ? 'success' : 'manual')
  }
})

test('a code with four seconds left is replaced before the first visible digit', async t => {
  const b = browser(t, { kind: 'totp', url: auth + '/mfa/totp', text: email, next: verified })
  let issued = 0
  const getCurrentCode = async () => {
    const stale = issued++ === 0
    return { value: stale ? '111111' : '028350', expires: Date.now() + (stale ? 4000 : 30000) }
  }
  assert.equal((await b.run({ email, password }, getCurrentCode)).status, 'success')
  assert.equal(issued, 2)
  assert.equal(b.edits.map(edit => edit.data).join(''), '028350')
})

test('login stops at third-party, insecure, wrong-port or opaque documents', async t => {
  const b = browser(t, {})
  for (const url of ['https://accounts.google.com/login', 'https://auth.openai.com.evil.test/', 'https://auth.openai.com:444/', 'http://auth.openai.com/', 'https://user@auth.openai.com/', 'blob:https://auth.openai.com/a']) {
    b.navigate({ kind: 'password', url, text: email })
    assert.equal((await b.run()).status, 'manual', url)
  }
  assert.deepEqual(b.calls, [])
})

test('a different current email, unsafe actions, other verifications and disabled auto-login forms never receive a secret', async t => {
  const b = browser(t, {})
  for (const [changes, status] of [
    [{ text: 'other@example.test' }, 'mismatch'],
    [{ form: { action: 'https://mail.example.test/' } }, 'manual'],
    [{ button: { formAction: 'https://mail.example.test/' } }, 'manual'],
    [{ form: { method: 'get' } }, 'manual'],
    [{ form: { target: '_blank' } }, 'manual'],
    [{ input: { autocomplete: 'new-password' } }, 'manual'],
    [{ input: { autocomplete: 'new-password webauthn' } }, 'manual'],
    [{ url: auth + '/password-reset' }, 'manual'],
    [{ text: '' }, 'manual'],
    [{ iframe: 'https://challenges.cloudflare.com/turnstile/example' }, 'manual'],
    [{ kind: 'totp', url: auth + '/email-verification', text: 'Check your inbox for the code' }, 'manual'],
    [{ kind: 'totp', text: 'Enter the SMS code' }, 'manual'],
    [{ kind: 'totp', text: 'Enter the code' }, 'manual'],
  ]) {
    b.navigate({ kind: 'password', text: email, ...changes })
    assert.equal((await b.run()).status, status, JSON.stringify(changes))
  }
  assert.deepEqual(b.clicks, [])
  assert.ok(!b.calls.some(call => call.value))
})

test('a post-fill change to the form destination is rechecked before submit', async t => {
  const b = browser(t, { kind: 'password', text: email, onField: page => { page.form.action = 'https://mail.example.test/' } })
  assert.equal((await b.run()).status, 'manual')
  assert.deepEqual(b.clicks, [])
})

test('a same-form read-only text input explicitly labelled as email identifies the password account', async t => {
  const b = browser(t, {})
  for (const linkage of ['label', 'aria-labelledby', 'both']) {
    const page = b.navigate({
      kind: 'password',
      url: linkage === 'both' ? auth + '/log-in-or-create-account' : auth + '/log-in/password',
      form: { noValidate: true }, input: { name: 'current-password', autocomplete: 'current-password webauthn' },
      next: verified,
    })
    const label = page.form.append('label', { id: 'email-caption', innerText: linkage === 'label' ? '电子邮件地址' : 'Email address' })
    const identity = page.form.append('input', { id: 'login-identity', type: 'text', readOnly: true, value: email.toUpperCase() })
    if (linkage !== 'aria-labelledby') {
      label.for = identity.id
      identity.labels = [label]
    }
    if (linkage !== 'label') identity['aria-labelledby'] = label.id
    page.form.append('input', { type: 'hidden', name: 'username', hidden: true })
    assert.equal((await b.run()).status, 'success', linkage)
    assert.equal(identity.value, email.toUpperCase())
  }
  assert.deepEqual(b.clicks.map(click => click.kind), ['password', 'password', 'password'])
})

test('a visibly different labelled address conflicts with even a matching hidden username', async t => {
  const b = browser(t, { kind: 'password' })
  const label = b.page.form.append('label', { innerText: '邮箱' })
  b.page.form.append('input', { type: 'text', readOnly: true, labels: [label], value: 'other@example.test' })
  b.page.form.append('input', { type: 'hidden', name: 'username', value: email, hidden: true })
  assert.equal((await b.run()).status, 'mismatch')
  assert.equal(b.page.field.value, '')
  assert.ok(!b.calls.some(call => call.value === password))
})

test('unlabelled, unrelated, editable and hidden text inputs cannot identify a password account', async t => {
  const b = browser(t, {})
  for (const variant of ['unlabelled', 'different label', 'other form', 'editable', 'hidden']) {
    const page = b.navigate({ kind: 'password' })
    const owner = variant === 'other form' ? page.main.append('form') : page.form
    const label = owner.append('label', { id: 'caption', innerText: variant === 'different label' ? 'Display name' : 'Email address' })
    owner.append('input', {
      type: 'text', readOnly: variant !== 'editable', hidden: variant === 'hidden', value: email,
      'aria-labelledby': variant === 'unlabelled' ? undefined : label.id,
    })
    assert.equal((await b.run()).status, 'manual', variant)
    assert.equal(page.field.value, '')
  }
  assert.ok(!b.calls.some(call => call.value))
})

test('a named email control in another visible form can flag a conflict but cannot authorize the password', async t => {
  const b = browser(t, {})
  for (const address of [email, 'other@example.test']) {
    const page = b.navigate({ kind: 'password' })
    const unrelated = page.main.append('form')
    unrelated.append('input', { type: 'email', name: 'username', value: address })
    if (address !== email) page.form.append('input', { type: 'hidden', name: 'username', value: email, hidden: true })
    assert.equal((await b.run()).status, address === email ? 'manual' : 'mismatch')
    assert.equal(page.field.value, '')
  }
  assert.ok(!b.calls.some(call => call.value))
})

test('after an email submission, a safe password form waits for its own matching account to hydrate', async t => {
  const b = browser(t, {})
  for (const address of [email, 'other@example.test']) {
    let polls = 0
    const before = b.calls.length
    b.navigate({ kind: 'email', next: { kind: 'password', next: verified, onInject: outcome => {
      if (outcome !== 'waiting') return
      assert.equal(b.page.field.value, '')
      assert.ok(!b.calls.slice(before).some(call => call.value === password))
      if (++polls === 3) b.page.form.append('input', { type: 'hidden', name: 'username', hidden: true, value: address })
    } } })
    assert.equal((await b.run()).status, address === email ? 'success' : 'mismatch')
    assert.equal(polls, 3)
    if (address !== email) assert.ok(!b.calls.slice(before).some(call => call.value === password))
  }
  assert.deepEqual(b.clicks.map(click => click.kind), ['email', 'password', 'email'])
})

test('an email step alone cannot authorize a password form that does not identify its account', async t => {
  let waits = 0
  const b = browser(t, { kind: 'email', next: { kind: 'password', text: 'Enter your password', onInject: outcome => { if (outcome === 'waiting') waits++ } } })
  const outcome = await b.run()
  assert.equal(outcome.status, 'manual')
  assert.match(outcome.message, /邮箱/)
  assert.ok(waits > 0 && waits <= 10, 'the identity wait must be bounded')
  assert.deepEqual(b.clicks.map(click => click.kind), ['email'])
  assert.ok(!b.calls.some(call => call.value === password))
})

test('waiting for an account neither defers an unsafe form nor overrides user interaction', async t => {
  const b = browser(t, {})
  for (const changes of [{ form: { method: 'get' } }, { url: auth + '/password-reset' }]) {
    b.navigate({ kind: 'email', next: { kind: 'password', ...changes, onInject: outcome => assert.notEqual(outcome, 'waiting') } })
    assert.equal((await b.run()).status, 'manual')
  }
  b.navigate({ kind: 'email', next: { kind: 'password', onInject: outcome => {
    if (outcome !== 'waiting') return
    b.page.callbacks.pointerdown({ isTrusted: true })
    b.page.form.append('input', { type: 'hidden', name: 'username', value: email, hidden: true })
  } } })
  const outcome = await b.run()
  assert.equal(outcome.status, 'manual')
  assert.match(outcome.message, /停止/)
  assert.ok(!b.calls.some(call => call.value === password))
})

test('the entire displayed Unicode address must match before any password reaches the document', async t => {
  const b = browser(t, {})
  const selected = { ...account, email: 'selected@example.test' }
  for (const other of ['éselected@example.test', '\u0301selected@example.test', 'selected@example.test界']) {
    b.navigate({ kind: 'password', text: 'Account: ' + other })
    assert.equal((await b.run(selected)).status, 'mismatch', other)
    assert.equal(b.page.field.value, '')
  }
  assert.ok(!b.calls.some(call => call.value === password))
  b.navigate({ kind: 'password', text: 'ÉSELECTED@EXAMPLE.TEST', next: { kind: 'settings', text: 'Email\néselected@example.test' } })
  assert.equal((await b.run({ ...account, email: 'éselected@example.test' })).status, 'success')
  assert.equal(b.clicks.length, 1)
})

test('missing or invalid KeyPouch TOTP seeds leave the authenticator input for the user', async t => {
  const challenge = { kind: 'totp', url: auth + '/mfa/totp', text: email + '\nEnter your authenticator code' }
  const b = browser(t, challenge)
  for (const seed of ['', 'not a valid 2fa seed 🌱']) {
    b.navigate(challenge)
    assert.equal((await b.run({ ...account, totpSecret: seed })).status, 'manual')
  }
  assert.equal(b.clicks.length, 0)
  assert.ok(!b.calls.some(call => call.value))
})

test('a matching settings email must not come from a conversation', async t => {
  const b = browser(t, { kind: 'settings', text: 'Email\n' + email, conversation: true })
  assert.equal((await b.run()).status, 'manual')
  assert.equal(b.urls.at(-1), settings)
  b.navigate({ ...verified, background: true })
  assert.equal((await b.run()).status, 'success')
  assert.equal(b.urls.at(-1), settings)
})

function staticAccountEmailRow(parent, address, description = 'The email address linked to your account') {
  const row = parent.append('div')
  const title = row.append('div').append('div')
  title.append('div', { innerText: 'Email' })
  title.append('div', { innerText: description })
  const value = row.append('div').append('span', { innerText: address })
  return { row, title, value }
}

test('the account settings email row keeps its description with its label and reads only the same row value', async t => {
  const b = browser(t, { kind: 'settings' })
  const { main } = b.page
  main.append('h1', { innerText: 'Account' })
  staticAccountEmailRow(main, email.toUpperCase())
  staticAccountEmailRow(main.append('article'), 'other@example.test')
  main.append('section', { innerText: 'Recovery contact\nrecovery@example.test' })
  assert.equal((await b.run()).status, 'success')
  assert.equal(b.urls.at(-1), 'https://chatgpt.com/')
  for (const role of ['tabpanel', 'dialog']) {
    const modal = b.navigate({ url: 'https://chatgpt.com/#settings/account' })
    staticAccountEmailRow(modal.main, email).row.role = role
    assert.equal((await b.run()).status, 'success', role)
  }
  const next = b.navigate({ kind: 'settings' })
  staticAccountEmailRow(next.main, email)
  staticAccountEmailRow(next.main, 'é' + email)
  assert.equal((await b.run()).status, 'mismatch')
  assert.equal(b.urls.at(-1), settings)
})

test('neighboring, editable, hidden and non-account branches cannot confirm the email in settings', async t => {
  const b = browser(t, {})
  for (const variant of ['description', 'account value', 'hidden section', 'different row', 'editable', 'conversation', 'another settings page']) {
    const url = variant === 'another settings page' ? 'https://chatgpt.com/settings/profile' : settings
    const page = b.navigate({ kind: 'settings', url })
    const parent = variant === 'conversation' ? page.main.append('article') : page.main.append('section', { hidden: variant === 'hidden section' })
    const { row, title, value } = staticAccountEmailRow(parent, email)
    if (variant === 'description') title.children[1].innerText = 'Contact address shown to others'
    if (variant === 'account value') value.hidden = true
    if (variant === 'different row') {
      row.children.pop()
      page.main.append('div').append('div').append('span', { innerText: email })
    }
    if (variant === 'editable') value.contenteditable = 'true'
    assert.equal((await b.run()).status, 'manual', variant)
    assert.equal(b.urls.at(-1), url)
  }
})

test('the detailed account row is checked again before leaving settings', async t => {
  const b = browser(t, { kind: 'settings' })
  const { value } = staticAccountEmailRow(b.page.main, email)
  b.page.onInject = (outcome, request) => {
    if (outcome === 'success' && !request.stage) value.innerText = 'other@example.test'
  }
  assert.equal((await b.run()).status, 'mismatch')
  assert.equal(b.urls.at(-1), settings)
})

test('a visible composer or conversation cannot hide or impersonate a real settings account', async t => {
  const b = browser(t, { kind: 'settings', composer: 'Email\n' + email })
  assert.equal((await b.run()).status, 'manual')
  b.navigate({ ...verified, composer: 'Email\nother@example.test' })
  Object.defineProperty(b.page.main.append('svg'), 'innerText', { value: undefined })
  assert.equal((await b.run()).status, 'success')
  b.navigate({ kind: 'settings', conversation: true, text: 'Email\n' + email, composer: 'Email\n' + email })
  const section = b.page.main.append('section')
  section.append('span', { innerText: 'Email' })
  section.append('bdi', { innerText: 'é' + email })
  assert.equal((await b.run()).status, 'mismatch')
  assert.equal(b.urls.at(-1), settings)
  assert.ok(!b.calls.some(call => call.value === password))
})

test('cancel between inspection and filling does not send a password', async t => {
  const b = browser(t, { kind: 'password', text: email, onInject: outcome => { if (outcome === 'password') b.controller.abort() } })
  assert.equal((await b.run()).status, 'manual')
  assert.ok(!b.calls.some(call => call.value))
})

test('trusted interaction between filling and submission stops even across navigation', async t => {
  const b = browser(t, { kind: 'password', text: email, onInject: outcome => {
    if (outcome !== 'filled') return
    b.page.callbacks.pointerdown({ isTrusted: true })
    b.navigate({ kind: 'password', text: email })
  } })
  assert.equal((await b.run()).status, 'manual')
  assert.deepEqual(b.clicks, [])
})

test('a lost response after a submit cannot submit the same stage again', async t => {
  const b = browser(t, { kind: 'password', text: email, loseSubmit: true })
  assert.equal((await b.run()).status, 'manual')
  assert.equal(b.clicks.length, 1)
})

test('a replaced inspected document is never injected by tab ID with its credentials', async t => {
  const b = browser(t, { kind: 'password', text: email, onInject: outcome => {
    if (outcome === 'password') b.navigate({ kind: 'password', url: 'https://external.example.test/', text: email })
  } })
  assert.equal((await b.run()).status, 'manual')
  assert.ok(!b.calls.some(call => call.value))
})
