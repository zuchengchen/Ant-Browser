import { getVaultAccount, getVaultSession, listVaultAccounts, normalizeVaultSite, VAULT_PAGE_SIZE, VaultError } from './vault.js'
import { loginToChatGPT, verifyAfterChallenge } from './login.js'
import { sendLoginResult } from './receipt.js'
import { openPairedVault } from './paired.js'
import { localKey, localVault } from './local.js'

const el = id => document.getElementById(id)
const search = el('query')
let site = ''
let offset = 0
let searchTimer
let reading
let login
let targetTabId
let useWebSession = false
let pairingVault

function showSection(section, message = '', isError = false) {
  for (const name of ['connect', 'pairing', 'bound', 'picker', 'login']) el(name).hidden = name !== section
  el('web-session').hidden = useWebSession || !['pairing', 'bound'].includes(section)
  el('instance-auth').hidden = !useWebSession || !['connect', 'picker'].includes(section)
  el('status').textContent = message
  el('status').dataset.tone = isError ? 'error' : ''
}

function connectionMessage(message, action = 'refresh', isError = false) {
  showSection('connect', message, isError)
  el('grant').hidden = action !== 'grant'
  el('open-vault').hidden = action !== 'open-vault'
  el('refresh').hidden = action === 'grant'
  el('web-session').hidden = useWebSession || !site || action === 'grant'
}

function reportReadError(error) {
  if (error instanceof VaultError) {
    const needsSignIn = ['unauthorized', 'password_change_required', 'forbidden'].includes(error.code)
    connectionMessage(error.message, useWebSession && needsSignIn ? 'open-vault' : 'refresh', !needsSignIn)
  } else {
    connectionMessage('读取 KeyPouch 失败，请重新检查站点设置。', 'refresh', true)
  }
}

async function configuredSite(signal) {
  const response = await fetch(chrome.runtime.getURL('site.json'), { cache: 'no-store', signal })
  if (!response.ok) throw new Error()
  const config = await response.json()
  return config.site ? normalizeVaultSite(config.site) : ''
}

function sitePermission() {
  const url = new URL(site)
  return { origins: [url.protocol + '//' + url.hostname + '/*'] }
}

function newRead() {
  clearTimeout(searchTimer)
  reading?.abort()
  reading = new AbortController()
  return reading.signal
}

async function connect(fromToolbar = false) {
  if (login) return
  const signal = newRead()
  el('accounts').replaceChildren()
  el('bound-account').replaceChildren()
  pairingVault = undefined
  el('pair-code').value = ''
  el('pair-fingerprint').textContent = ''
  el('replace-key').open = false
  showSection(null, '正在读取实例账号…')
  try {
    const local = await localVault.account()
    if (signal.aborted) return
    if (local) {
      el('site').textContent = local.site
      el('site-line').hidden = false
      if (fromToolbar === true) return startLogin(local, localVault)
      el('bound-account').replaceChildren(accountRow(local, localVault))
      showSection('bound', '此账号的登录凭据已同步到本实例。')
      el('web-session').hidden = true
      return
    }
    const configured = await configuredSite(signal)
    if (signal.aborted) return
    if (configured !== site) {
      search.value = ''
      useWebSession = false
    }
    site = configured
    el('site').textContent = site
    el('site-line').hidden = !site
    if (!site) {
      connectionMessage('请在 Ant 的「系统设置 → 基础设置」保存 KeyPouch 站点地址，再回来重新检查。')
      return
    }
    const granted = await chrome.permissions.contains(sitePermission())
    if (signal.aborted) return
    if (!granted) {
      connectionMessage('允许插件连接此站点后，即可绑定这个实例的 GPT 账号。', 'grant')
      return
    }
    if (!useWebSession) {
      const vault = await openPairedVault(site)
      if (signal.aborted) return
      const record = await vault.account(signal)
      if (signal.aborted) return
      if (!record) {
        pairingVault = vault
        el('pair-code').value = vault.code
        el('pair-fingerprint').textContent = '指纹 ' + vault.id.slice(0, 12)
        showSection('pairing')
        return
      }
      el('bound-account').replaceChildren(accountRow(record, vault))
      showSection('bound', '已授权此实例登录以下账号。')
      return
    }
    const signedIn = await getVaultSession(site, signal)
    if (signal.aborted) return
    if (!signedIn) {
      connectionMessage('请在此实例登录 KeyPouch，然后返回本页选择账号。', 'open-vault')
      return
    }
    showSection('picker')
    await list(0)
  } catch (error) {
    if (!signal.aborted) reportReadError(error)
  }
}

function accountRow(account, vault) {
  const row = document.createElement('li')
  const button = document.createElement('button')
  button.className = 'account'
  const label = document.createElement('span')
  const email = document.createElement('bdi')
  email.textContent = account.email
  const detail = document.createElement('small')
  detail.textContent = [
    '记录 #' + account.id, account.hasPassword ? '密码' : '未设密码', ...(account.hasTOTP ? ['验证器'] : []),
    ...(vault?.id ? ['实例 ' + vault.id.slice(0, 12)] : []),
  ].join(' · ')
  label.append(email, detail)
  const action = document.createElement('span')
  action.className = 'action'
  action.textContent = '登录'
  button.append(label, action)
  button.addEventListener('click', () => startLogin(account, vault))
  row.append(button)
  return row
}

async function list(start) {
  const signal = newRead()
  offset = start
  el('accounts').replaceChildren()
  el('pages').hidden = true
  el('list-status').textContent = '正在查找账号…'
  try {
    if (await configuredSite(signal) !== site) return connect()
    if (signal.aborted) return
    const { items, total } = await listVaultAccounts(site, search.value, start, signal)
    if (signal.aborted) return
    if (start >= total && start > 0) return list(Math.max(0, Math.ceil(total / VAULT_PAGE_SIZE) - 1) * VAULT_PAGE_SIZE)
    el('list-status').textContent = items.length ? '' : total ? '本页没有可登录的邮箱记录。' : '没有找到匹配的账号。'
    el('accounts').replaceChildren(...items.map(account => accountRow(account)))
    el('pages').hidden = total <= VAULT_PAGE_SIZE
    el('previous').disabled = !start
    el('next').disabled = start + VAULT_PAGE_SIZE >= total
    el('page-number').textContent = '第 ' + (Math.floor(start / VAULT_PAGE_SIZE) + 1) + ' / ' + Math.ceil(total / VAULT_PAGE_SIZE) + ' 页'
  } catch (error) {
    if (!signal.aborted) reportReadError(error)
  }
}

async function startLogin(record, vault) {
  if (login) return
  newRead()
  login = new AbortController()
  const signal = login.signal
  targetTabId = undefined
  el('login-email').textContent = record.email
  el('cancel').hidden = false
  el('choose-again').hidden = true
  el('choose-again').textContent = vault === localVault ? '返回实例账号' : (vault ? '返回已授权账号' : '返回账号列表')
  el('show-gpt').hidden = true
  showSection('login', '正在读取所选记录…')
  try {
    // Windows in one Chromium profile share website sessions. Serialize their
    // explicit login attempts so two accounts cannot race in the same cookie jar.
    await navigator.locks.request('keypouch-gpt-login', { ifAvailable: true }, async lock => {
      if (!lock) {
        showSection('login', '此实例的另一个窗口正在登录 GPT，请在那个窗口继续。')
        return
      }
      if (vault !== localVault && await configuredSite(signal) !== site) {
        connectionMessage('KeyPouch 站点已变更，请重新连接并选择账号。')
        return
      }
      const account = vault ? await vault.login(record, signal, true) : await getVaultAccount(site, record.id, record.email, signal, true)
      if (vault !== localVault && await configuredSite(signal) !== site) {
        connectionMessage('KeyPouch 站点已变更，请重新连接并选择账号。')
        return
      }
      const tab = await chrome.tabs.getCurrent()
      if (signal.aborted) return
      const target = await chrome.tabs.create({ windowId: tab.windowId, url: 'https://chatgpt.com/', active: true })
      targetTabId = target.id
      if (signal.aborted) return
      el('show-gpt').hidden = false
      showSection('login', '正在 ChatGPT 标签页登录…')
      const getCurrentCode = vault?.totp && record.hasTOTP ? async codeSignal => {
        if (await configuredSite(codeSignal) !== site) throw new VaultError('grant_required')
        const { code, expires } = await vault.totp(record, codeSignal)
        if (await configuredSite(codeSignal) !== site) throw new VaultError('grant_required')
        return { value: code, expires }
      } : undefined
      const result = await loginToChatGPT(target.id, account, signal, getCurrentCode)
      if (signal.aborted) return
      const needsAttention = result.status === 'failed' || result.status === 'mismatch'
      let message = result.message
      let verdict = result.verdict
      if (result.waitForIdentity) {
        showSection('login', message)
        verdict = await verifyAfterChallenge(target.id, account.email, signal)
        if (signal.aborted) return
        if (verdict === 'success') message = '已确认此实例登录的是所选 ChatGPT 账号。'
      }
      if (verdict && account.attempt) {
        const reported = await sendLoginResult(vault === localVault ? record.site : site, account.attempt, record.email, verdict, signal)
        if (signal.aborted) return
        message += reported ? ' 已回填到 KeyPouch。' : ' 本次结果未能回填到 KeyPouch。'
      }
      showSection('login', message, needsAttention)
      if (needsAttention) {
        const [active] = await chrome.tabs.query({ windowId: tab.windowId, active: true })
        if (active?.id === target.id && !signal.aborted) await chrome.tabs.update(tab.id, { active: true })
      }
    })
  } catch (error) {
    if (!signal.aborted) {
      if (error instanceof VaultError) reportReadError(error)
      else showSection('login', '无法完成登录。请检查实例中的 ChatGPT 标签页后重试。', true)
    }
  } finally {
    login = undefined
    el('cancel').hidden = true
    el('choose-again').hidden = false
  }
}

el('grant').addEventListener('click', async () => {
  try {
    if (await chrome.permissions.request(sitePermission())) await connect()
    else connectionMessage('需要此站点的访问权限才能读取你选中的账号。', 'grant')
  } catch {
    connectionMessage('无法取得站点权限，请在 Chromium 的扩展管理中检查「登录 GPT」的站点访问权限。', 'grant', true)
  }
})
el('open-vault').addEventListener('click', async () => {
  try {
    if (await configuredSite() !== site) return connect()
    const tab = await chrome.tabs.getCurrent()
    await chrome.tabs.create({ windowId: tab.windowId, url: site })
  } catch {
    connectionMessage('无法打开 KeyPouch，请重新检查站点设置。', 'refresh', true)
  }
})
el('refresh').addEventListener('click', connect)
el('check-pair').addEventListener('click', connect)
el('copy-code').addEventListener('click', async () => {
  const input = el('pair-code')
  if (!input.value) return
  try {
    await navigator.clipboard.writeText(input.value)
    el('status').textContent = '授权码已复制。'
  } catch {
    input.select()
    el('status').textContent = '授权码已选中，请手动复制。'
  }
})
el('replace-code').addEventListener('click', async () => {
  const vault = pairingVault
  if (!vault || el('pairing').hidden) return
  el('replace-code').disabled = true
  try {
    if (await configuredSite() !== site) return connect()
    await vault.replaceKey()
    if (pairingVault === vault) await connect()
  } catch (error) {
    if (pairingVault === vault) reportReadError(error)
  } finally {
    el('replace-code').disabled = false
  }
})
el('web-session').addEventListener('click', () => { useWebSession = true; void connect() })
el('instance-auth').addEventListener('click', () => { useWebSession = false; void connect() })
el('choose-again').addEventListener('click', connect)
el('previous').addEventListener('click', () => list(Math.max(0, offset - VAULT_PAGE_SIZE)))
el('next').addEventListener('click', () => list(offset + VAULT_PAGE_SIZE))
el('search-form').addEventListener('submit', event => { event.preventDefault(); void list(0) })
search.addEventListener('input', () => {
  newRead()
  el('accounts').replaceChildren()
  el('pages').hidden = true
  el('list-status').textContent = '正在查找账号…'
  searchTimer = setTimeout(() => list(0), 200)
})
el('cancel').addEventListener('click', () => {
  login?.abort()
  el('cancel').hidden = true
  showSection('login', '已停止自动填写。')
})
el('show-gpt').addEventListener('click', async () => {
  try { await chrome.tabs.update(targetTabId, { active: true }) }
  catch {
    el('show-gpt').hidden = true
    el('status').textContent = 'ChatGPT 标签页已关闭。'
  }
})
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && (!el('pairing').hidden || (!el('connect').hidden && !el('open-vault').hidden))) void connect()
})
window.addEventListener('pagehide', () => { newRead(); login?.abort() })
async function handleToolbarClick(expectedTabId) {
  try {
    if (expectedTabId !== undefined && (await chrome.tabs.getCurrent())?.id !== expectedTabId) return
    const clicked = await chrome.runtime.sendMessage({ type: 'take-toolbar-click' })
    if (clicked || expectedTabId === undefined) await connect(clicked === true)
  } catch {
    if (expectedTabId === undefined) await connect()
  }
}

chrome.runtime.onMessage.addListener((message, sender) => {
  if (message?.type === 'toolbar-click' && sender.id === chrome.runtime.id && !sender.tab) {
    void handleToolbarClick(message.tabId)
  }
})
chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== 'session' || !Object.hasOwn(changes, localKey)) return
  if (login) {
    login.abort()
    showSection('login', '实例凭据已变更，请重新点击「登录 GPT」。')
  } else {
    void connect()
  }
})
void handleToolbarClick()
