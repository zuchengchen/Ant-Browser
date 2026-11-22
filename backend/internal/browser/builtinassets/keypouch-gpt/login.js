const messages = {
  success: ['success', '已确认此实例登录的是所选 ChatGPT 账号。'],
  mismatch: ['mismatch', '页面上的邮箱与所选账号不同，已保留当前登录状态。'],
  challenge: ['manual', '请在登录标签页中完成人机验证。'],
  emailCode: ['manual', '请在登录标签页中完成邮箱或短信验证。'],
  authenticator: ['manual', '请在登录标签页中填写身份验证器验证码，或在 KeyPouch 检查 2FA 密钥。'],
  passwordRequired: ['manual', '这条记录没有登录密码，请在登录标签页中继续。'],
  passwordIdentity: ['manual', '未能确认密码页的邮箱，请在登录标签页核对账号后继续。'],
  external: ['manual', '请在登录标签页中完成当前身份验证。'],
  unknown: ['manual', '未能确认登录表单或当前账号，请在登录标签页中继续。'],
  redirectStalled: ['manual', '登录跳转未完成，请在登录标签页查看当前页面后继续。'],
  stalled: ['manual', '登录页面未继续，请在实例中查看网页提示。'],
  interrupted: ['manual', '已停止自动填写，可在登录标签页中继续。'],
  rejected: ['failed', '网站未接受登录信息，请检查所选账号及网页提示。'],
  unavailable: ['failed', '无法访问此次登录的标签页，请重新打开登录。'],
}

const signInRedirectPages = ['https://chatgpt.com/auth/login_with', 'https://auth.openai.com/oauth/authorize']

function result(reason) {
  const [status, message] = messages[reason]
  return { status, message }
}

// KeyPouch uses Base32, HMAC-SHA1, six digits and a 30-second time step.
// Derive each code in the extension page. Only that code goes to the form.
async function authenticatorCode(secret, now) {
  const data = []
  let buffer = 0, bits = 0
  for (const c of secret.replace(/[\s-]/g, '').replace(/=+$/, '').toUpperCase()) {
    const digit = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(c)
    if (digit < 0) return null
    buffer = (buffer << 5) | digit
    bits += 5
    if (bits >= 8) {
      bits -= 8
      data.push((buffer >>> bits) & 255)
    }
  }
  if (!data.length) return null
  const key = await crypto.subtle.importKey('raw', new Uint8Array(data), { name: 'HMAC', hash: 'SHA-1' }, false, ['sign'])
  const counter = new DataView(new ArrayBuffer(8))
  const step = Math.floor(now / 30000)
  counter.setBigUint64(0, BigInt(step))
  const digest = new DataView(await crypto.subtle.sign('HMAC', key, counter))
  const offset = digest.getUint8(digest.byteLength - 1) & 15
  const code = (digest.getUint32(offset) & 0x7fffffff) % 1000000
  return { value: String(code).padStart(6, '0'), expires: (step + 1) * 30000 }
}

// Chrome serializes this function into the extension's isolated world. All DOM
// decisions (including the final form/account check) run in that document.
function loginPage(request) {
  const here = new URL(location.href)
  const official = u => u.protocol === 'https:' && !u.username && !u.password && (u.origin === 'https://chatgpt.com' || u.origin === 'https://auth.openai.com')
  if (window !== window.top || !official(here)) return 'external'

  const key = '__antKeyPouchLogin'
  let guard = request.receiptOnly ? { id: request.id } : globalThis[key]
  if (request.end) {
    if (guard?.id === request.id) { guard.events.abort(); delete globalThis[key] }
    return 'ended'
  }
  if (request.stage && guard?.id !== request.id) return 'interrupted'
  if (guard?.id !== request.id) {
    guard?.events.abort()
    guard = { id: request.id, events: new AbortController(), interrupted: false, submitted: new Set() }
    globalThis[key] = guard
    const observe = event => {
      if (!event.isTrusted || guard.interrupted) return
      guard.interrupted = true
      guard.events.abort()
      // The user may navigate before the next poll can see this document.
      chrome.runtime.sendMessage({ type: 'keypouch-login-interaction', id: guard.id }).catch(() => {})
    }
    for (const event of ['pointerdown', 'keydown', 'input']) {
      window.addEventListener(event, observe, { capture: true, signal: guard.events.signal })
    }
    window.addEventListener('submit', event => {
      const fill = guard.fill
      if (fill?.complete && fill.form === event.target) {
        const submitted = () => {
          guard.submitted.add(fill.stage)
          chrome.runtime.sendMessage({ type: 'keypouch-login-submitted', id: guard.id, stage: fill.stage }).catch(() => {})
        }
        // The authenticator form dispatches a separate field-validation
        // submit during input. Its normal Continue button submits the code.
        const submitter = event.submitter, primary = guard.plan?.button
        if (fill.stage === 'totp' && !fill.slots && event.cancelable && primary?.form === fill.form
          && submitter?.form === fill.form && submitter !== primary && submitter.type === 'submit'
          && ['__intent__', 'intent'].includes(submitter.name)
          && /^https:\/\/auth\.openai\.com\/mfa-challenge\/[^/?#]+$/.test(location.href.split(/[?#]/, 1)[0])) {
          let validation = submitter.value === 'validate'
          if (!validation) {
            try {
              const intent = JSON.parse(submitter.value)
              validation = intent?.type === 'validate' && (intent.payload?.name === undefined || intent.payload.name === fill.field.name)
            } catch { /* Unknown intents count as submissions. */ }
          }
          if (validation) {
            // Capture runs before the application's submit handler. Give it
            // the entire dispatch to cancel validation's native form action.
            Promise.resolve().then(() => { if (!event.defaultPrevented) submitted() })
            return
          }
        }
        submitted()
      }
    }, { capture: true, signal: guard.events.signal })
  }
  if (guard.interrupted) return 'interrupted'
  const redirect = request.redirectPages.includes(here.origin + here.pathname)
  if (!redirect && (!document.body || document.readyState === 'loading')) return 'waiting'

  const visible = el => {
    const rect = el.getBoundingClientRect(), style = getComputedStyle(el)
    return el.isConnected && rect.width > 0 && rect.height > 0 && style.visibility !== 'hidden' && style.opacity !== '0' && !el.closest('[inert],[aria-hidden="true"]')
  }
  const shown = (scope, selector) => [...scope.querySelectorAll(selector)].filter(visible)
  const label = el => (el.innerText || el.value || el.getAttribute('aria-label') || '').trim()
  const enabled = el => !el.disabled && el.getAttribute('aria-disabled') !== 'true'
  const ownPartial = node => guard.fill?.node === node && guard.fill.written === node.value
  const userContent = 'article,[data-message-author-role],[data-testid^="conversation-turn"],[contenteditable],textarea,[role="textbox"]'
  const exactEmail = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/
  const unsafePath = path => !/^\/log-in-or-create-account\/?$/.test(path)
    && /sign[-_]?up|regist(?:er|ration)|create[-_]?account|reset|forgot|recovery|enroll|setup|(?:change|new|update)[-_]?password/i.test(path)
  const chat = here.origin === 'https://chatgpt.com'
  const settings = chat && (/^\/settings\/account\/?$/i.test(here.pathname) || (here.pathname === '/' && /^#settings\/account$/i.test(here.hash)))
  const humanPrompt = /verify (?:that )?you are human|请完成人机验证|請完成人機驗證/i

  if (/just a moment|security verification|verify (?:that )?you are human|人机验证|人機驗證/i.test(document.title)
    || shown(document, 'iframe[src*="challenges.cloudflare.com"],iframe[src*="recaptcha"],iframe[src*="hcaptcha.com"]').length) return 'challenge'
  if (redirect) return humanPrompt.test((document.querySelector('main') || document.body)?.innerText || '') ? 'challenge' : 'signInRedirect'
  if (unsafePath(here.pathname)) return 'unknown'
  if (chat && !settings && !request.receiptOnly && !/^\/(?:auth\/login\/?)?$/.test(here.pathname)) return 'unknown'
  // A redirect observed by tabs.get may have just committed a different
  // document. Wait for its next inspection with the usual account checks.
  if (!request.email) return 'waiting'
  const email = request.email.toLowerCase()

  if (settings) {
    // A conversation underneath a dialog can contain arbitrary email/label text.
    const addresses = new Set()
    const accountEmailLabel = /^(?:Email(?: address)?|电子邮件|電子郵件|邮箱|電郵)$/i
    const region = '[role="dialog"],[role="tabpanel"],main'
    const interactive = userContent + ',form,input,select,button,a,[role="button"]'
    const visibleBranch = (node, stop) => {
      for (; node && node !== stop; node = node.parentElement) {
        const style = getComputedStyle(node)
        if (node.hidden || style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return false
      }
      return true
    }
    for (const area of shown(document, region)) {
      if (area.closest(userContent) || !visibleBranch(area)) continue
      if (area.matches('main') && here.pathname === '/') continue
      // The detailed account row has a label and description on one side,
      // with a single static address on the other side of that same row.
      const readDescribedRow = row => {
        if (!row.matches('div') || row.children.length !== 2) return
        const [caption, value] = row.children
        if (caption.children.length !== 1 || value.children.length !== 1) return
        const group = caption.children[0], address = value.children[0]
        if (group.children.length !== 2 || !address.matches('span') || address.children.length) return
        const [heading, description] = group.children
        if (heading.children.length || description.children.length || ![caption, value, group, heading, description].every(el => el.matches('div'))) return
        if (row.closest(region) !== area || row.closest(interactive) || row.querySelector(interactive) || !visibleBranch(row, area)) return
        if (![row, caption, group, heading, description, value, address].every(visible)) return
        if (!accountEmailLabel.test(heading.innerText.trim()) || description.innerText.trim() !== 'The email address linked to your account') return
        const raw = address.innerText.trim()
        if (exactEmail.test(raw) && value.innerText.trim() === raw) addresses.add(raw.toLowerCase())
      }
      let emailLabel = false
      const readText = text => {
        if (typeof text !== 'string') return
        for (const line of text.split('\n').map(value => value.trim()).filter(Boolean)) {
          if (emailLabel && exactEmail.test(line)) addresses.add(line.toLowerCase())
          emailLabel = accountEmailLabel.test(line)
        }
      }
      const visit = node => {
        if (node.nodeType === Node.TEXT_NODE) return readText(node.textContent)
        if (node.nodeType !== Node.ELEMENT_NODE) return
        if (node.matches(userContent)) { emailLabel = false; return }
        const transparent = getComputedStyle(node).display === 'contents'
        if (!transparent && !visible(node)) return
        if (!transparent && !node.querySelector(userContent)) {
          readText(node.innerText)
          readDescribedRow(node)
          for (const row of node.querySelectorAll('div')) readDescribedRow(row)
          return
        }
        for (const child of node.childNodes) visit(child)
      }
      visit(area)
    }
    if (addresses.size) {
      if (addresses.size !== 1 || !addresses.has(email)) return 'mismatch'
      if (request.stage === 'openChat' && document.visibilityState === 'visible' && document.hasFocus()) location.assign('https://chatgpt.com/')
      return 'success'
    }
  }

  if (!chat && /passkey|webauthn|\/sso(?:\/|$)/i.test(here.pathname)) return 'external'
  const authScope = chat ? shown(document, '[role="dialog"]')[0] || document : document
  const controls = shown(authScope, 'button,a,input[type="submit"],[role="button"]').filter(el => !el.closest(userContent))
  const fields = shown(authScope, 'input').filter(el => !el.closest(userContent))
  const emailInput = el => el.type === 'email' || /^(?:email|username)$/i.test(el.name) || /(?:^|\s)(?:username|email)$/.test(el.autocomplete)
  const emailField = fields.find(emailInput)
  const password = fields.find(el => el.type === 'password')
  const findControl = pattern => controls.find(el => enabled(el) && pattern.test(label(el)))
  const profileMenu = chat && (findControl(/^(?:Account menu|Open (?:the )?profile menu|个人资料菜单|账户菜单|帳戶選單)$/i)
    || controls.find(el => ['profile-button', 'accounts-profile-button'].includes(el.getAttribute('data-testid'))))
  if (request.receiptOnly) return profileMenu && authScope === document ? 'signedIn' : 'waiting'
  const guestChooser = chat && !settings && !profileMenu && (here.pathname.startsWith('/auth/login') || authScope !== document
    && (request.openedLogin || shown(authScope, 'h1,h2,h3,[role="heading"]').some(el => /^(?:Log in(?: or sign up)?|Sign in|登录|登入|登錄)$/i.test(label(el)))))
  const text = chat ? '' : (document.querySelector('main') || document.body).innerText
  if (!chat && humanPrompt.test(text)) return 'challenge'
  const alerts = shown(authScope, '[role="alert"],[aria-live="assertive"],[data-error],.error-message')
  const invalidCredential = /^(?:(?:incorrect|invalid|wrong) (?:email(?: address)? (?:or|and) password|username or password|password|credentials)|(?:your |the )?password (?:is (?:incorrect|invalid|wrong)|isn['’]t correct)|(?:邮箱或密码|电子邮件或密码|账号或密码|密码)(?:不正确|错误|有误)|(?:電子郵件或密碼|密碼)(?:不正確|錯誤))[.!。！]?$/i
  if (!chat && alerts.some(el => !el.closest(userContent) && invalidCredential.test(el.innerText.trim()))) return 'credentialRejected'
  if (alerts.some(el => /incorrect|invalid|too many|expired|wrong|unable to|try again|不正确|不正確|无效|無效|错误|錯誤|过期|過期|次数过多/i.test(el.innerText))) return 'rejected'
  if (shown(authScope, '[aria-busy="true"],[role="progressbar"]').length) return 'waiting'

  let stage, field, button, slots
  const codeField = el => el.autocomplete === 'one-time-code' || /^(?:code|otp|totp|verification_code)$/i.test(el.name)
  const digitSlot = el => el.maxLength === 1 && ['text', 'tel', 'number'].includes(el.type)
  const code = fields.find(codeField) || fields.find(digitSlot)
  const passwordOption = !chat && findControl(/^(?:Use (?:a |your )?password(?: instead)?|(?:Continue|Log in|Sign in) with (?:a |your )?password(?: instead)?|使用密码(?:登录)?|改用密码|使用密碼(?:登入)?|密码登录)$/i)
  const authenticator = /authenticator (?:app|code)|authentication app|身份验证器|身份驗證器|验证器应用|驗證器應用/i
  if (!chat && password) {
    const purposes = password.autocomplete.split(/\s+/)
    const sharedEntry = /^\/log-in-or-create-account\/?$/.test(here.pathname) && purposes.includes('current-password')
    if (!/^(?:\/u)?\/(?:log-?in|sign-?in)(?:\/|$)/i.test(here.pathname) && here.pathname !== '/password' && !sharedEntry) return 'unknown'
    if (purposes.includes('new-password') || fields.filter(el => el.type === 'password').length !== 1) return 'unknown'
    stage = 'password'
    field = password
  } else if (passwordOption && request.offerPassword) {
    stage = 'usePassword'
    button = passwordOption
  } else if (!chat && (/email[-_/]?(?:verification|otp)|verify[-_]?email|sms/.test(here.pathname) || /check your (?:email|inbox)|(?:code|message).{0,40}(?:sent|emailed)|(?:send|sent|emailed).{0,45}code|text message|SMS code|邮箱验证码|邮件.{0,20}验证码|短信验证码/i.test(text))) {
    return 'emailCode'
  } else if (!chat && code) {
    if (!authenticator.test(text) && !/\/mfa\/totp(?:\/|$)/i.test(here.pathname)) return 'external'
    if (code.maxLength === 1) {
      slots = fields.filter(el => el.form === code.form && digitSlot(el))
      if (!code.form || slots.length !== 6 || fields.some(el => el.form === code.form && ['text', 'tel', 'number', 'password'].includes(el.type) && !slots.includes(el) && !el.readOnly && !emailInput(el))) return 'external'
      if (slots.some(el => el.readOnly || !enabled(el))) return 'waiting'
    }
    stage = 'totp'
    field = slots ? slots[0] : code
  } else if (emailField && !emailField.readOnly && (!chat || here.pathname.startsWith('/auth/login') || authScope !== document)) {
    stage = 'email'
    field = emailField
  } else if (!chat && (button = findControl(/^(?:Authenticator app|Authentication app|身份验证器(?:应用)?|身份驗證器(?:應用)?)$/i))) {
    stage = 'chooseAuthenticator'
  } else if ((guestChooser || !chat && /^\/(?:log-?in(?:-or-create-account)?|choose[-_]account|select[-_]account|u\/login)(?:\/|$)/i.test(here.pathname))
    && (button = findControl(/^(?:(?:Log in (?:to|with)|Use) another account|使用其他(?:账户|账号|帳戶|帳號)(?:登录|登入)?|登录其他账号)$/i))) {
    stage = 'anotherAccount'
  } else if (chat && !profileMenu && (button = controls.find(el => enabled(el) && el.getAttribute('data-testid') === 'login-button')
    || findControl(/^(?:Log in|Login|Sign in|登录|登入|登錄)$/i))) {
    stage = 'login'
  } else if (chat && here.pathname === '/' && !settings) {
    // The guest header renders asynchronously. Its absence alone is never
    // evidence of a signed-in session. Wait for an account menu before asking
    // account settings to confirm the email, including after a login redirect.
    if (authScope !== document || !profileMenu) return 'waiting'
    stage = 'settings'
  } else {
    return 'waiting'
  }

  let form = field?.form
  let companion, missingPasswordIdentity = false
  if (field) {
    if (!form || !enabled(field) || field.readOnly) return 'waiting'
    button = shown(form, 'button,input[type="submit"]').find(el => el.form === form && /^(?:Continue|Next|Log in|Login|Sign in|Verify(?: code)?|Submit|继续|繼續|下一步|登录|登入|登錄|验证|驗證|确认|確認)$/i.test(label(el)))
    if (!button && !slots) return 'waiting'
    if (stage === 'password') {
      companion = fields.find(el => el.form === form && emailInput(el) && enabled(el) && !el.readOnly)
    }
    if (stage === 'password' || stage === 'totp') {
      const area = form.closest('main,[role="dialog"]') || document.body
      // The password page can display its account in an unnamed, read-only
      // text input. Its explicitly associated label identifies that value.
      const labelledReadOnlyEmail = input => {
        if (stage !== 'password' || input.form !== form || input.type !== 'text' || !input.readOnly || input.closest(userContent) || !visible(input)) return false
        const ids = input.getAttribute('aria-labelledby')?.trim()
        const labels = ids ? ids.split(/\s+/).map(id => document.getElementById(id)) : Array.from(input.labels || [])
        const caption = labels.map(node => node?.innerText || node?.textContent || '').join(' ').trim().replace(/\s+/g, ' ')
        return /^(?:e-?mail(?: address)?|(?:电子邮件|電子郵件|电子邮箱|電子郵箱|邮箱|郵箱|電郵)(?:地址)?)[:：]?$/i.test(caption)
      }
      // Keep complete tokens. An ASCII substring match would discard a non-ASCII
      // prefix in another account's address before comparing it to the selection.
      const names = area.innerText.split(/[\s<>]+/u).filter(value => value.includes('@'))
      if (names.some(name => name.toLowerCase() !== email)) return 'mismatch'
      let identified = names.length > 0
      for (const input of new Set([...form.elements, ...fields])) {
        if (input === companion && ownPartial(input)) continue
        const labelledAccount = labelledReadOnlyEmail(input)
        if (!labelledAccount && !emailInput(input)) continue
        const value = input.value?.trim()
        if (!value || !labelledAccount && !exactEmail.test(value)) continue
        if (value.toLowerCase() !== email) return 'mismatch'
        // A visible username for a different form can reveal a conflict. Only
        // a control owned by this password form can identify its account.
        if (stage !== 'password' || input.form === form) identified = true
      }
      // Never send a standalone password to an unidentified account, even
      // after an earlier email step. MFA pages commonly omit the identity;
      // for those, require our confirmed email/password progression in this tab.
      if (!identified && !companion) {
        if (stage === 'password') missingPasswordIdentity = true
        else if (!request.confirmedEmail) return 'unknown'
      }
    }
    if (stage === 'email' && field.value && !ownPartial(field) && field.value.trim().toLowerCase() !== email) return 'mismatch'
  }
  const sameSlots = (left, right) => left?.length === right?.length && (!left || left.every((node, i) => node === right[i]))
  const filling = guard.fill
  if (filling?.stage === 'totp' && filling.complete && stage !== 'totp') guard.submitted.add('totp')
  if (filling && !guard.submitted.has(filling.stage)
    && (filling.stage !== stage || filling.form !== form || filling.field !== field || filling.companion !== companion || !sameSlots(filling.slots, slots))) return 'interrupted'
  if (button?.href) {
    const link = new URL(button.href, here)
    if (!official(link) || unsafePath(link.pathname) || (button.target && button.target !== '_self')) return 'external'
  }
  form ||= button?.form
  if (form) {
    const action = new URL(button?.hasAttribute('formaction') ? button.formAction : form.action || here.href, document.baseURI)
    const target = button?.getAttribute('formtarget') || form.target || document.querySelector('base[target]')?.target
    const method = button?.getAttribute('formmethod') || form.method
    const emailHandoff = stage === 'email' && chat && action.origin === 'https://auth.openai.com'
    if (!official(action) || (action.origin !== here.origin && !emailHandoff) || unsafePath(action.pathname) || (target && target !== '_self')) return 'unknown'
    // Native GET submission would include credentials in history and request logs.
    if ((stage === 'password' || stage === 'totp') && method.toLowerCase() !== 'post') return 'unknown'
  }
  if (stage === 'password') {
    if (guard.passwordIdentityDeadline !== undefined && Date.now() >= guard.passwordIdentityDeadline) return 'passwordIdentity'
    if (missingPasswordIdentity) {
      // A client-rendered password page may attach its account after its form.
      // Only wait after the email step, on a validated form, before any typing.
      if (!request.confirmedEmail || !request.offerPassword || request.stage || guard.fill?.stage === 'password') return 'passwordIdentity'
      guard.passwordIdentityDeadline ??= Date.now() + 6000
      return 'waiting'
    }
    delete guard.passwordIdentityDeadline
  }
  if (!request.stage) {
    guard.plan = { stage, form, field, companion, button, slots }
    return stage
  }
  if (document.visibilityState !== 'visible' || !document.hasFocus()) return 'interrupted'
  if (stage !== request.stage) return 'changed'
  const plan = guard.plan
  if (!plan || plan.stage !== stage || plan.form !== form || plan.field !== field || plan.companion !== companion || plan.button !== button || !sameSlots(plan.slots, slots)) return 'changed'
  if (guard.submitted.has(stage)) return 'submitted'
  if (stage === 'settings') {
    guard.submitted.add(stage)
    location.assign('https://chatgpt.com/settings/account')
    return 'submitted'
  }
  if (stage === 'totp' && request.expires - Date.now() < 3000) return guard.fill?.stage === stage ? 'authenticator' : 'waiting'
  if (field) {
    if (slots && !/^\d{6}$/.test(request.value)) return 'authenticator'
    const entries = slots ? slots.map((node, i) => [node, request.value[i]])
      : companion ? [[companion, request.email], [field, request.value]] : [[field, request.value]]
    const matches = (node, value) => emailInput(node) ? node.value.trim().toLowerCase() === value.toLowerCase() : node.value === value
    if (guard.fill?.stage !== stage) {
      if (entries.some(([node, value]) => !value || (node.value && !matches(node, value)))) return 'interrupted'
      guard.fill = { stage, form, field, companion, slots, index: 0, written: '', node: null, complete: false }
    }
    const fill = guard.fill
    for (let i = 0; i < fill.index; i++) {
      if (!matches(...entries[i])) return 'interrupted'
    }
    while (fill.index < entries.length) {
      const [node, value] = entries[fill.index]
      if (!fill.node && matches(node, value)) {
        fill.index++
        continue
      }
      if (node.value !== fill.written || !value.startsWith(fill.written)) return 'interrupted'
      if (!fill.node) {
        fill.node = node
        node.focus()
        if (guard.interrupted || !node.isConnected || node.value !== fill.written) return 'interrupted'
      }
      const char = String.fromCodePoint(value.codePointAt(fill.written.length))
      const next = fill.written + char
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(node, next)
      fill.written = next
      fill.complete = next === value && fill.index === entries.length - 1
      node.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: char }))
      if (guard.submitted.has(stage)) return 'submitted'
      if (stage === 'totp' && fill.complete && !node.isConnected) return 'submitted'
      if (guard.interrupted || !node.isConnected || node.value !== next) return 'interrupted'
      if (next !== value) return 'typing'
      fill.index++
      fill.node = null
      fill.written = ''
      node.dispatchEvent(new Event('change', { bubbles: true }))
      if (guard.submitted.has(stage) || (fill.complete && !button)) return 'submitted'
      return slots && !fill.complete ? 'typing' : 'filled'
    }
    fill.complete = true
    if (!button) return 'submitted'
  }
  if (!enabled(button)) return 'waiting'
  if (form && !form.checkValidity()) return 'unknown'
  guard.submitted.add(stage)
  button.click()
  return 'submitted'
}

function officialTab(raw) {
  try {
    const url = new URL(raw)
    return url.protocol === 'https:' && !url.username && !url.password && (url.origin === 'https://auth.openai.com' || url.origin === 'https://chatgpt.com')
  } catch { return false }
}

function pause(signal, milliseconds) {
  return new Promise(resolve => {
    const finish = () => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', finish)
      resolve()
    }
    const timer = setTimeout(finish, milliseconds)
    if (signal?.aborted) finish()
    else signal?.addEventListener('abort', finish, { once: true })
  })
}

// Timers in the background extension tab are rounded up to one second.
// This isolated-world wait runs only in the exact document we just filled.
// Its arguments contain no account, input value or authenticator code.
function waitForVisibleInput({ id, delay }) {
  const guard = globalThis.__antKeyPouchLogin
  const here = new URL(location.href)
  if (window !== window.top || here.username || here.password ||
    !['https://chatgpt.com', 'https://auth.openai.com'].includes(here.origin) || guard?.id !== id) return 'changed'
  const active = () => !guard.interrupted && !guard.events.signal.aborted &&
    document.visibilityState === 'visible' && document.hasFocus()
  if (!active()) return 'interrupted'
  return new Promise(resolve => {
    const cleanup = new AbortController()
    const finish = outcome => {
      clearTimeout(timer)
      cleanup.abort()
      resolve(outcome)
    }
    const timer = setTimeout(() => finish(active() ? 'ready' : 'interrupted'), delay)
    const options = { signal: cleanup.signal }
    guard.events.signal.addEventListener('abort', () => finish('interrupted'), options)
    window.addEventListener('pagehide', () => finish('changed'), options)
    window.addEventListener('blur', () => finish('interrupted'), options)
    document.addEventListener('visibilitychange', () => { if (!active()) finish('interrupted') }, options)
  })
}

export async function loginToChatGPT(tabId, account, signal, getCurrentCode) {
  const email = account?.email?.trim()
  if (!Number.isInteger(tabId) || !email) return result('unavailable')
  const id = crypto.randomUUID()
  const submitted = new Map()
  let usedSavedCredential = false
  const finishResult = reason => {
    const outcome = result(reason === 'credentialRejected' ? 'rejected' : reason)
    if (usedSavedCredential) {
      if (reason === 'success' || reason === 'credentialRejected') outcome.verdict = reason === 'success' ? 'success' : 'rejected'
      if (account.attempt && ['emailCode', 'challenge', 'authenticator'].includes(reason)) outcome.waitForIdentity = true
    }
    return outcome
  }
  let interrupted = false, documentId, confirmedEmail = false, currentCode
  let typing, delay = 0
  let redirectDeadline = 0
  const observe = (message, sender) => {
    if (message?.id !== id || sender.tab?.id !== tabId || sender.frameId !== 0) return
    if (message.type === 'keypouch-login-interaction') interrupted = true
    // A form can navigate within the same document. An earlier stage's receipt
    // must never end a later field that is still waiting to submit.
    if (message.type === 'keypouch-login-submitted' && sender.documentId === documentId && submitted.has(message.stage)) {
      if (message.stage === 'password' || message.stage === 'totp') usedSavedCredential = true
      if (typing?.stage === message.stage && typing.documentId === sender.documentId) typing = undefined
      if (message.stage === 'email' || message.stage === 'password') confirmedEmail = true
    }
  }
  const inject = async (target, payload, func = loginPage) => {
    const [answer] = await chrome.scripting.executeScript({
      target: { tabId, ...target }, world: 'ISOLATED', injectImmediately: true, func, args: [payload],
    })
    if (answer?.frameId !== 0 || !answer.documentId) throw new Error('Document unavailable')
    return answer
  }
  const stopped = () => signal?.aborted || interrupted
  chrome.runtime.onMessage.addListener(observe)
  try {
    const deadline = Date.now() + 90000
    while (Date.now() < deadline && (!redirectDeadline || Date.now() < redirectDeadline)) {
      if (delay && typing) {
        // The surrounding AbortSignal lives in the extension. Ending the run
        // also cancels the document's pending timer through the existing guard.
        let onAbort
        try {
          const wait = inject({ documentIds: [typing.documentId] }, { id, delay }, waitForVisibleInput)
            .then(answer => answer.result, () => 'changed')
          const aborted = new Promise(resolve => {
            onAbort = () => resolve('interrupted')
            signal?.addEventListener('abort', onAbort, { once: true })
            if (stopped()) onAbort()
          })
          const outcome = await Promise.race([wait, aborted])
          if (outcome === 'interrupted' || (outcome === 'changed' && typing && !typing.codeEntered)) return result('interrupted')
        } finally {
          signal?.removeEventListener('abort', onAbort)
        }
      } else if (delay) await pause(signal, delay)
      delay = 400
      if (stopped()) return result('interrupted')
      if (Date.now() >= deadline || redirectDeadline && Date.now() >= redirectDeadline) break
      const tab = await chrome.tabs.get(tabId)
      const url = tab.pendingUrl || tab.url
      if (!officialTab(url)) {
        if ((!url || url === 'about:blank') && tab.status === 'loading') continue
        return result('external')
      }
      const redirectTab = [tab.url, tab.pendingUrl].some(raw => raw && signInRedirectPages.includes(raw.split(/[?#]/, 1)[0]))
      const info = { id, redirectPages: signInRedirectPages }
      if (redirectTab) redirectDeadline ||= Date.now() + 15000
      else Object.assign(info, { email, confirmedEmail, openedLogin: submitted.has('login'), offerPassword: !!account.password && !submitted.has('password') })
      let inspection
      try {
        inspection = await inject({ frameIds: [0] }, info)
      } catch { continue } // A navigation can destroy the document between these calls.
      documentId = inspection.documentId
      const stage = inspection.result
      if (typing && typing.documentId !== documentId && !typing.codeEntered) return result('interrupted')
      if (typing?.codeEntered && (documentId !== typing.documentId || stage !== 'totp' && stage !== 'waiting')) {
        // Some OTP widgets navigate as soon as the last digit arrives, without
        // a submit event. Wait for that actual transition and never retype it.
        usedSavedCredential = true
        submitted.set('totp', Date.now())
        typing = undefined
      }
      if (stopped()) return result('interrupted')
      if (stage === 'signInRedirect' || (redirectTab && (!stage || stage === 'waiting'))) {
        if (typing) return result('interrupted')
        // Both documents belong to the same redirect chain. No form action
        // runs in either one, even if it has inputs or a Continue button.
        redirectDeadline ||= Date.now() + 15000
        continue
      }
      redirectDeadline = 0
      if (stage === 'success') {
        // Navigation is optional, and only the still-focused verified settings
        // document may take the user to the chat. A replaced tab is left alone.
        const finish = await inject({ documentIds: [documentId] }, { ...info, stage: 'openChat' }).catch(() => null)
        if (stopped()) return result('interrupted')
        if (finish && finish.result !== 'success') return result(Object.hasOwn(messages, finish.result) ? finish.result : 'unknown')
        return finishResult('success')
      }
      if (stage === 'credentialRejected' || Object.hasOwn(messages, stage)) return finishResult(stage)
      if (!stage || stage === 'waiting') continue
      if (submitted.has(stage)) {
        if (Date.now() - submitted.get(stage) > 12000) return result('stalled')
        continue
      }
      const action = { ...info, stage }
      if (stage === 'email') action.value = email
      if (stage === 'password') {
        if (!account.password) return result('passwordRequired')
        action.value = account.password
      }
      if (stage === 'chooseAuthenticator' || stage === 'totp') {
        if (!getCurrentCode && !account.totpSecret) return finishResult('authenticator')
        if (stage === 'totp') {
          if (typing && currentCode && currentCode.expires - Date.now() < 3000) return finishResult('authenticator')
          if (!currentCode || currentCode.expires <= Date.now()) {
            try {
              currentCode = getCurrentCode ? await getCurrentCode(signal) : await authenticatorCode(account.totpSecret, Date.now())
            } catch {
              return finishResult(stopped() ? 'interrupted' : 'authenticator')
            }
          }
          if (!currentCode) return finishResult('authenticator')
          if (!typing && currentCode.expires - Date.now() < 7000) continue
          Object.assign(action, currentCode)
        }
      }
      if (stopped()) return result('interrupted')
      // A navigation can consume a submit and destroy the document before Chrome
      // replies. Treat that action as attempted without resending it.
      submitted.set(stage, Date.now())
      let outcome
      try { outcome = (await inject({ documentIds: [documentId] }, action)).result }
      catch { continue }
      if (stopped()) return result('interrupted')
      if (Object.hasOwn(messages, outcome)) return finishResult(outcome)
      if (outcome === 'credentialRejected') return finishResult(outcome)
      if (outcome === 'submitted') {
        if (stage === 'password' || stage === 'totp') usedSavedCredential = true
        typing = undefined
        if (stage === 'email' || stage === 'password') confirmedEmail = true
      } else {
        submitted.delete(stage)
        if (outcome === 'typing' || outcome === 'filled') {
          typing = { stage, documentId, codeEntered: stage === 'totp' && outcome === 'filled' }
          // Give each edit a visible interval, and leave time for the page's
          // input validation to settle before moving on from a finished field.
          delay = outcome === 'typing' ? 80 + Math.floor(Math.random() * 61) : 350 + Math.floor(Math.random() * 151)
        }
      }
    }
    return result(redirectDeadline ? 'redirectStalled' : 'stalled')
  } catch {
    return result(stopped() ? 'interrupted' : 'unavailable')
  } finally {
    chrome.runtime.onMessage.removeListener(observe)
    if (documentId) void inject({ documentIds: [documentId] }, { id, end: true }).catch(() => {})
  }
}

// After a manual challenge, observe only the tab from this click. Once it returns
// to ChatGPT, read the account settings in one background tab without touching
// the user's form, cookies or session response.
export async function verifyAfterChallenge(tabId, email, signal) {
  let settingsTab
  const deadline = Date.now() + 8 * 60_000
  try {
    while (!signal.aborted && Date.now() < deadline) {
      await pause(signal, 1200)
      if (signal.aborted) break
      const tab = await chrome.tabs.get(tabId)
      const address = tab.pendingUrl || tab.url
      if (!officialTab(address)) return
      const url = new URL(address)
      if (url.origin !== 'https://chatgpt.com' || url.pathname.startsWith('/auth/')) continue
      const inspect = async id => {
        const [answer] = await chrome.scripting.executeScript({
          target: { tabId: id, frameIds: [0] }, world: 'ISOLATED',
          func: loginPage, args: [{ id: 'receipt', receiptOnly: true, email, redirectPages: [] }],
        }).catch(() => [])
        return answer?.frameId === 0 ? answer.result : undefined
      }
      if (!settingsTab) {
        const landing = await inspect(tabId)
        if (signal.aborted || landing === 'mismatch') return
        if (landing === 'success') return 'success'
        if (landing !== 'signedIn') continue
        settingsTab = await chrome.tabs.create({ windowId: tab.windowId, url: 'https://chatgpt.com/settings/account', active: false })
      }
      const identity = await inspect(settingsTab.id)
      if (signal.aborted || identity === 'mismatch') return
      if (identity === 'success') return 'success'
    }
  } catch { /* Closed tabs and offline accounts have no conclusive result. */ }
  finally { if (settingsTab) await chrome.tabs.remove(settingsTab.id).catch(() => {}) }
}
