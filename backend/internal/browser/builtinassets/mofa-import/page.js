import { configuredSite } from './site.js'

const button = document.getElementById('start')
const siteLabel = document.getElementById('site')
const status = document.getElementById('status')
let running = false
const notConfigured = '请先在 Ant 的「系统设置 → 基础设置」配置 MOFA 后台地址。'
const messages = {
  opened: '已打开 MOFA 后台，请在后台确认导入。',
  unavailable: '未能确认当前实例的登录邮箱。请在这个实例中打开 ChatGPT，确认已登录后重试。',
  ambiguous: 'ChatGPT 账号设置页显示了多个不同的邮箱，请核对当前登录账号后重试。',
  cancelled: '账号核对已停止。请返回此页重新发起。',
  unconfigured: notConfigured,
  configuration_changed: 'MOFA 后台地址已更改，请核对地址后重新发起。',
  busy: '此实例已有导入请求，请在原来的标签页继续。',
}

async function showConfiguration() {
  const site = await configuredSite().catch(() => '')
  if (running) return
  siteLabel.textContent = site
  button.disabled = !site
  if (!site) status.textContent = notConfigured
  else if (status.textContent === notConfigured) status.textContent = ''
}

button.addEventListener('click', async event => {
  if (!event.isTrusted || running) return
  running = true
  button.disabled = true
  status.textContent = '正在核对本实例登录的 ChatGPT 邮箱…'
  try {
    const result = await chrome.runtime.sendMessage({ type: 'open-mofa-import', site: siteLabel.textContent })
    status.textContent = Object.hasOwn(messages, result) ? messages[result] : messages.unavailable
  } catch {
    status.textContent = messages.unavailable
  } finally {
    running = false
    await showConfiguration()
  }
})

window.addEventListener('focus', () => { if (!running) void showConfiguration() })
void showConfiguration()
