import { requestMofaImport } from './account.js'
import { configuredMofa } from './site.js'

const button = document.getElementById('start')
const siteLabel = document.getElementById('site')
const status = document.getElementById('status')
const notConfigured = '请先在 Ant 的「系统设置 → 基础设置」配置 MOFA 后台地址、管理员用户名和密码。'
const missingBridge = '无法连接 Ant 的 MOFA 导入服务。请确认 Ant 正在运行，再重开工具栏。'
const outcomes = {
  skipped: '已核对。账号授权有效且已在「GPT-不降智」，本次跳过。',
  imported: '已导入账号，并加入「GPT-不降智」。',
  reauthorized: '已重新授权原账号，并确认账号在「GPT-不降智」。',
  group_added: '已将原账号加入「GPT-不降智」。',
}

let running = false
let challenge

// Only a trusted click in this document can register a challenge. Ant uses
// CDP to consume it after checking the exact extension origin and browser.
Object.defineProperty(globalThis, 'antMofaClaimBridge', {
  value(digest) {
    if (!challenge || challenge.digest !== digest || performance.now() > challenge.expires) return false
    challenge = undefined
    return true
  },
})

const hex = bytes => Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('')

async function showConfiguration() {
  const config = await configuredMofa().catch(() => null)
  if (running) return
  siteLabel.textContent = config?.site || ''
  button.disabled = !config?.site || !config.bridgePort
  if (!config?.site) status.textContent = notConfigured
  else if (!config.bridgePort) status.textContent = missingBridge
  else if ([notConfigured, missingBridge].includes(status.textContent)) status.textContent = ''
}

button.addEventListener('click', async event => {
  if (!event.isTrusted || running || window !== window.top || chrome.extension.inIncognitoContext) return
  const displayedSite = siteLabel.textContent
  const pageURL = chrome.runtime.getURL('import.html')
  running = true
  button.disabled = true
  status.textContent = '正在核对本实例登录的 ChatGPT 账号。若实例打开 OpenAI 授权页，请核对账号和工作空间并确认。'
  try {
    const config = await configuredMofa()
    if (!config.site) throw new Error(notConfigured)
    if (config.site !== displayedSite) throw new Error('MOFA 后台地址已更改，请核对地址后重新发起。')
    if (!config.bridgePort) throw new Error(missingBridge)
    const proof = crypto.getRandomValues(new Uint8Array(32))
    const digest = hex(await crypto.subtle.digest('SHA-256', proof))
    challenge = {digest, expires: performance.now() + 20_000}
    // The fragment carries only the digest to help find this tab. The proof
    // itself is never written to the URL, storage or extension preferences.
    history.replaceState(null, '', `${pageURL}#ant-mofa-${digest}`)
    const result = await requestMofaImport(config, hex(proof))
    status.textContent = outcomes[result.status]
  } catch (error) {
    status.textContent = error?.message || missingBridge
  } finally {
    challenge = undefined
    history.replaceState(null, '', pageURL)
    running = false
    await showConfiguration()
  }
})

window.addEventListener('focus', () => { if (!running) void showConfiguration() })
void showConfiguration()
