// Run in the target Ant test instance's private Xvfb and D-Bus session.
// node run.mjs PORT < synthetic-account.json
// Input contains email (.test), password, totpSecret, and optional source site.
// This driver never supplies credentials to Ant or reads extension storage.
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createHmac, randomUUID } from 'node:crypto'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(require.resolve('playwright', {
  paths: [process.cwd(), process.env.KEYPOUCH_PROJECT_DIR || process.cwd()],
}))
const [port] = process.argv.slice(2)
assert(/^\d+$/.test(port) && Number(port) > 0 && Number(port) < 65536, '需要本机调试端口')
const pickerURL = /^chrome-extension:\/\/[a-p]{32}\/accounts\.html$/
const success = '已确认此实例登录的是所选 ChatGPT 账号。'
const chunks = []
for await (const chunk of process.stdin) chunks.push(chunk)
const fixture = JSON.parse(Buffer.concat(chunks).toString('utf8'))
assert(/^[-+.a-z0-9]+@[-.a-z0-9]+\.test$/i.test(fixture.email), '只允许合成测试邮箱')
assert(typeof fixture.password === 'string' && fixture.password.length > 0, '需要合成密码')
const encoded = fixture.totpSecret?.toUpperCase().replace(/[\s=-]/g, '')
assert(/^[A-Z2-7]+$/.test(encoded), '需要合成 Base32 种子')
const bits = [...encoded].map(c => 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(c).toString(2).padStart(5, '0')).join('')
const secret = Buffer.from(bits.match(/.{8}/g).map(byte => parseInt(byte, 2)))
const chat = 'https://chatgpt.com'
const auth = 'https://auth.openai.com'
const mfaPath = '/mfa-challenge/fixture'
const keyOrigin = fixture.site ? new URL(fixture.site).origin : undefined
const marker = 'AntFixture' + randomUUID()
const counts = { home: 0, logIn: 0, email: 0, loginWith: 0, authorize: 0, password: 0, totp: 0, verified: 0, keyRequests: 0 }
const checkedHandoffs = new Set()
let authenticated = false
let failure
// Keep Chromium's native tab focus and background timer throttling. Focus
// emulation would make the background picker appear visible during login.
const browser = await chromium.connectOverCDP('http://127.0.0.1:' + port, { noDefaults: true })
const network = await browser.newBrowserCDPSession()
const [context] = browser.contexts()
assert(context)
const initialPages = new Set(context.pages())
const stub = await context.newPage()

function fixtureCode(timestamp) {
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(timestamp / 30_000)))
  const digest = createHmac('sha1', secret).update(counter).digest()
  const number = digest.readUInt32BE(digest[digest.length - 1] & 15) & 0x7fff_ffff
  return String(number % 1_000_000).padStart(6, '0')
}

function form(label, action, input) {
  return `<p>${label}</p><form id="login-form" method="post" action="${action}">${input}
    <input type="hidden" name="typing"><button type="submit">Continue</button></form>
    <script>
      const form = document.currentScript.previousElementSibling;
      const field = form.querySelector('input:not([readonly]):not([type="hidden"])');
      const trace = { steps: 0, incremental: true, pasted: false, gaps: [] };
      let previous = '', lastInput;
      field.addEventListener('paste', () => { trace.pasted = true; });
      field.addEventListener('input', () => {
        const now = performance.now();
        trace.incremental &&= field.value.startsWith(previous) && Array.from(field.value.slice(previous.length)).length === 1;
        if (lastInput !== undefined) trace.gaps.push(Math.round(now - lastInput));
        previous = field.value;
        lastInput = now;
        trace.steps++;
      });
      form.addEventListener('submit', () => {
        trace.beforeSubmit = Math.round(performance.now() - lastInput);
        form.elements.typing.value = JSON.stringify(trace);
      });
    </script>`
}

function checkTyping(values, fieldName, expected) {
  assert(values.get(fieldName) === expected, '测试字段的最终值不匹配')
  const trace = JSON.parse(values.get('typing'))
  assert(trace?.incremental && !trace.pasted, '应逐字符输入')
  assert.equal(trace.steps, Array.from(expected).length)
  assert(trace.gaps.every(ms => ms > 0) && trace.beforeSubmit > 0, '输入和提交之间需要停顿')
  if (trace.gaps.length >= 5) {
    assert(new Set(trace.gaps).size > 1, '字符间隔应有变化')
    const ordered = [...trace.gaps].sort((a, b) => a - b)
    assert(ordered[Math.floor(ordered.length / 2)] < 600, '字符输入被后台标签页限速')
  }
}

function page(contents) {
  return '<!doctype html><title>Test login</title><main>' + contents + '</main>'
}

function homePage() {
  return page(`<button id="login" data-testid="login-button" hidden>Log in</button>
    <dialog id="signin" role="dialog">${form('Email address', '/_fixture/email',
      '<input type="email" name="email" autocomplete="username"><input type="hidden" name="homeClick">')}</dialog>
    <script>
      const login = document.getElementById('login'), dialog = document.getElementById('signin');
      login.addEventListener('click', () => {
        dialog.querySelector('[name=homeClick]').value = '1';
        dialog.showModal();
      });
      dialog.querySelector('form').addEventListener('submit', async event => {
        event.preventDefault();
        const form = event.currentTarget;
        const response = await fetch(form.action, { method: 'POST', body: new URLSearchParams(new FormData(form)) });
        if (response.ok) location.assign('/auth/login_with?fixture=1');
      });
      setTimeout(() => { login.hidden = false; }, 800);
    </script>`)
}

function handoffPage(next) {
  // A rendered handoff can outlive multiple inspection polls. Any form here
  // belongs to the handoff document, even if it resembles the next login step.
  return page(`<form method="post" action="/_fixture/unexpected-submit">
    <label>Email address<input name="email" autocomplete="username"></label>
    <label>Password<input type="password" autocomplete="current-password"></label>
    <button type="submit">Continue</button></form>
    <script>
      const form = document.querySelector('form');
      let edits = 0, clicks = 0, submits = 0;
      form.addEventListener('input', () => edits++);
      form.addEventListener('click', () => clicks++);
      form.addEventListener('submit', event => { event.preventDefault(); submits++; });
      setTimeout(async () => {
        const filled = Array.from(form.querySelectorAll('input')).some(input => input.value !== '');
        const response = await fetch('/_fixture/handoff', { method: 'POST',
          body: new URLSearchParams({ edits, clicks, submits, filled: Number(filled) }) });
        if (response.ok) location.replace(${JSON.stringify(next)});
      }, 1800);
    </script>`)
}

function authenticatorPage() {
  return page(`<h1>Check your authenticator app</h1>${form('Code', mfaPath, `
    <input type="text" name="code" autocomplete="one-time-code" inputmode="numeric" maxlength="6" required>
    <input type="hidden" name="validations"><input type="hidden" name="continueClicks">`)}
    <a href="/mfa-choice">Try another method</a>
    <script>
      const mfaForm = document.getElementById('login-form'), otp = mfaForm.elements.code;
      const continueButton = mfaForm.querySelector('button[type="submit"]');
      let validations = 0, continueClicks = 0;
      continueButton.addEventListener('click', () => continueClicks++);
      // The client validates on every edit. This hidden submitter dispatches a
      // native submit event even though the page cancels it without a request.
      otp.addEventListener('input', () => {
        const intent = document.createElement('button');
        intent.hidden = true;
        intent.type = 'submit';
        intent.name = '__intent__';
        intent.value = JSON.stringify({ type: 'validate', payload: { name: 'code' } });
        mfaForm.append(intent);
        try { mfaForm.requestSubmit(intent); } finally { intent.remove(); }
      });
      mfaForm.addEventListener('submit', event => {
        if (event.submitter?.hidden && event.submitter.name === '__intent__') {
          event.preventDefault();
          validations++;
          return;
        }
        mfaForm.elements.validations.value = String(validations);
        mfaForm.elements.continueClicks.value = String(continueClicks);
      });
    </script>`)
}

async function intercept({ requestId, request }) {
  const url = new URL(request.url)
  const values = new URLSearchParams(request.postData)
  const respond = (responseCode, html) => network.send('Fetch.fulfillRequest', {
    requestId, responseCode,
    responseHeaders: [{ name: 'Content-Type', value: 'text/html; charset=utf-8' }],
    body: Buffer.from(html).toString('base64'),
  })
  const abort = () => network.send('Fetch.failRequest', { requestId, errorReason: 'BlockedByClient' })
  const html = body => respond(200, body)
  const redirect = location => html('<script>location.replace(' + JSON.stringify(location) + ')</script>')
  try {
    if (url.origin === keyOrigin) {
      counts.keyRequests++
      return await abort()
    }
    if (url.origin === chat && url.pathname === '/') {
      if (authenticated) return await html(page('<button data-testid="profile-button" aria-label="Open profile menu">Profile</button>'))
      counts.home++
      return await html(homePage())
    }
    if (url.origin === chat && url.pathname === '/settings/account') {
      assert(authenticated, '应先在首页点 Log in')
      counts.verified++
      return await html(page(`<div>
        <div><div><div>Email</div><div>The email address linked to your account</div></div></div>
        <div><span>${fixture.email}</span></div>
      </div>
      <article data-message-author-role="user"><p>Email</p><p>conversation@example.test</p></article>`))
    }
    if (url.origin === chat && url.pathname === '/_fixture/email' && request.method === 'POST') {
      assert.equal(values.get('homeClick'), '1', '须先点击首页上的 Log in')
      counts.logIn++
      checkTyping(values, 'email', fixture.email)
      counts.email++
      return await respond(200, '')
    }
    if (url.origin === chat && url.pathname === '/auth/login_with') {
      assert.equal(request.method, 'GET')
      assert.equal(counts.email, 1)
      counts.loginWith++
      return await html(handoffPage(auth + '/oauth/authorize?fixture=1'))
    }
    if (url.origin === auth && url.pathname === '/oauth/authorize') {
      assert.equal(request.method, 'GET')
      assert(checkedHandoffs.has(chat), '须先经过已渲染的 ChatGPT 登录中转页')
      counts.authorize++
      return await html(handoffPage(auth + '/log-in/password?fixture=1'))
    }
    if ([chat, auth].includes(url.origin) && url.pathname === '/_fixture/handoff' && request.method === 'POST') {
      assert(!checkedHandoffs.has(url.origin), '中转页应仅完成一次')
      assert.deepEqual(Object.fromEntries(values), { edits: '0', clicks: '0', submits: '0', filled: '0' }, '自动登录操作了中转页的表单')
      checkedHandoffs.add(url.origin)
      return await respond(200, '')
    }
    if (url.origin === auth && url.pathname === '/log-in/password' && request.method === 'GET') {
      assert(checkedHandoffs.has(auth), '须先经过已渲染的 OAuth 授权中转页')
      return await html(page(form('Enter your password', mfaPath, `
        <label id="email-label" for="display-email">Email address</label>
        <input id="display-email" type="text" readonly aria-labelledby="email-label" value="${fixture.email}">
        <input type="hidden" name="username" form="login-form" value="">
        <input type="password" name="password" autocomplete="current-password webauthn" required>`)))
    }
    if (url.origin === auth && url.pathname === mfaPath && request.method === 'POST' && values.has('password')) {
      checkTyping(values, 'password', fixture.password)
      counts.password++
      return await html(authenticatorPage())
    }
    if (url.origin === auth && url.pathname === mfaPath && request.method === 'POST' && values.has('code')) {
      assert.equal(values.get('validations'), '6', '须先处理六次本地输入校验，再提交验证码')
      assert.equal(values.get('continueClicks'), '1', '应点击一次正式 Continue 按钮')
      assert(!values.has('__intent__'), '本地校验不应发送验证码')
      checkTyping(values, 'code', fixtureCode(Date.now()))
      counts.totp++
      authenticated = true
      return await redirect(chat + '/')
    }
    return await respond(404, '')
  } catch (error) {
    failure = error
    await abort().catch(() => {})
  }
}

function clickAction() {
  return new Promise((resolve, reject) => {
    const filename = fileURLToPath(new URL('click-toolbar.py', import.meta.url))
    const process = spawn('/usr/bin/python3', [filename, marker], { stdio: ['ignore', 'ignore', 'inherit'] })
    process.on('error', reject)
    process.on('exit', code => code === 0 ? resolve() : reject(new Error('工具栏操作失败')))
  })
}

try {
  // Each invocation has a fresh synthetic website session and intercepts both
  // official origins for all tabs before the action. The browser profile and
  // the desktop-provided extension snapshot remain intact.
  // Playwright context.route enables Fetch for "*" even with an origin
  // predicate. The explicit browser-level patterns leave privileged extension
  // scripts, styles and the toolbar worker on Chromium's normal loading path.
  network.on('Fetch.requestPaused', intercept)
  const origins = [chat, auth, keyOrigin].filter(Boolean)
  await network.send('Fetch.enable', { patterns: origins.map(origin => ({ urlPattern: origin + '/*' })) })
  // Reload any old progress page so a success message from another run
  // cannot satisfy this assertion. Reloading must not start a login.
  for (const page of initialPages) {
    if (!pickerURL.test(page.url())) continue
    await page.reload()
    await page.locator('#bound:not([hidden]) .account').waitFor({ timeout: 5000 })
  }
  assert(Object.values(counts).every(value => value === 0), '尚未点击工具栏，测试站已收到请求')
  await stub.goto('data:text/html,<title>' + marker + '</title>')
  await stub.bringToFront()
  await clickAction()
  const deadline = Date.now() + 65_000
  let status = ''
  while (!failure && Date.now() < deadline) {
    for (const page of context.pages()) {
      if (!pickerURL.test(page.url())) continue
      const email = await page.locator('#login-email').textContent({ timeout: 500 }).catch(() => '')
      if (email !== fixture.email) continue
      status = await page.locator('#status').textContent({ timeout: 500 }).catch(() => '')
      if (status === success && counts.verified) break
    }
    if (status === success && counts.verified) break
    await new Promise(resolve => setTimeout(resolve, 150))
  }
  if (failure) throw failure
  assert(status === success, '扩展未确认登录成功')
  assert.deepEqual(counts, { home: 1, logIn: 1, email: 1, loginWith: 1, authorize: 1, password: 1, totp: 1, verified: 1, keyRequests: 0 })
  console.log(JSON.stringify({ nativeToolbarClicks: 1, ...counts, selectedAccountMatched: true }))
} finally {
  for (const page of context.pages()) {
    if (!initialPages.has(page)) await page.close().catch(() => {})
  }
  await network.send('Fetch.disable').catch(() => {})
  await network.detach()
  await browser.close()
}
