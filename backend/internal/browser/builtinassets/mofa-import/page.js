import { requestMofaImport } from './account.js'
import { configuredMofa } from './site.js'

const siteLabel = document.getElementById('site')
const status = document.getElementById('status')
const pageURL = chrome.runtime.getURL('import.html')
const fromToolbar = '请点击浏览器工具栏中的「导入到 MOFA」图标发起导入。'
const notConfigured = '请先在 Ant 的「系统设置 → 基础设置」配置 MOFA 后台地址、管理员用户名和密码。'
const missingBridge = '无法连接 Ant 的 MOFA 导入服务。请确认 Ant 正在运行，再重开工具栏。'
const outcomes = {
  skipped: '已核对。账号授权有效且已在「GPT-不降智」，本次跳过。',
  imported: '已导入账号，并加入「GPT-不降智」。',
  reauthorized: '已重新授权原账号，并确认账号在「GPT-不降智」。',
  group_added: '已将原账号加入「GPT-不降智」。',
}

const hex = bytes => Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('')

async function startImport() {
  const eligible = window === window.top && location.href === pageURL && chrome.extension.inIncognitoContext === false
  const permitted = eligible && await chrome.runtime.sendMessage({type: 'take-mofa-toolbar-click'}).catch(() => false)
  if (permitted !== true) {
    status.textContent = fromToolbar
    return
  }
  status.textContent = '正在启动 MOFA 导入。'
  let challenge
  try {
    const config = await configuredMofa().catch(() => null)
    if (!config?.site) throw new Error(notConfigured)
    siteLabel.textContent = config.site
    if (!config.bridgePort) throw new Error(missingBridge)
    const proof = crypto.getRandomValues(new Uint8Array(32))
    const digest = hex(await crypto.subtle.digest('SHA-256', proof))
    const expires = performance.now() + 20_000
    challenge = digest
    // Ant checks the browser and extension identity before consuming the
    // challenge. Only the digest is used to locate this tab.
    Object.defineProperty(window, 'antMofaClaimBridge', {
      value(candidate) {
        if (challenge !== candidate || typeof candidate !== 'string' || performance.now() > expires) return false
        challenge = undefined
        return true
      },
    })
    history.replaceState(null, '', `${pageURL}#ant-mofa-${digest}`)
    status.textContent = '正在核对本实例登录的 ChatGPT 账号。如需手动授权，请在本实例切换到 OpenAI 授权标签页。'
    const result = await requestMofaImport(config, hex(proof))
    status.textContent = outcomes[result.status]
  } catch (error) {
    status.textContent = error?.message || missingBridge
  } finally {
    challenge = undefined
    history.replaceState(null, '', pageURL)
  }
}

void startImport()
