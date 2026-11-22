const settingsURL = 'https://chatgpt.com/settings/account'
const emailPattern = /^[^\s@<>\u0000-\u001f\u007f]+@[^\s@<>\u0000-\u001f\u007f]+\.[^\s@<>\u0000-\u001f\u007f]+$/

// Chromium serializes this self-contained reader into the isolated world.
// Only visible static account settings can identify the signed-in user.
export function readAccountEmail() {
  const here = new URL(location.href)
  if (window !== window.top || here.origin !== 'https://chatgpt.com' || here.username || here.password) return { status: 'unavailable' }
  const dialogRoute = here.pathname === '/' && here.hash === '#settings/account'
  if (!dialogRoute && !/^\/settings\/account\/?$/.test(here.pathname)) return { status: 'unavailable' }
  if (/just a moment|security verification|verify (?:that )?you are human|人机验证|人機驗證/i.test(document.title) ||
      document.querySelector('iframe[src*="challenges.cloudflare.com"],iframe[src*="recaptcha"],iframe[src*="hcaptcha.com"]')) {
    return { status: 'unavailable' }
  }

  const settings = '[role="dialog"],[role="tabpanel"],main'
  const excluded = 'article,[data-message-author-role],[data-testid^="conversation-turn"],[contenteditable],textarea,[role="textbox"],form,input,select,button,a,[role="button"]'
  const hidden = '[hidden],[inert],[aria-hidden="true"]'
  const emailLabel = /^(?:Email(?: address)?|电子邮件|電子郵件|邮箱|電郵)$/i
  const email = /^[^\s@<>\u0000-\u001f\u007f]+@[^\s@<>\u0000-\u001f\u007f]+\.[^\s@<>\u0000-\u001f\u007f]+$/
  const addresses = new Set()
  const visibleBranch = element => {
    if (element.closest(hidden)) return false
    for (let node = element; node; node = node.parentElement) {
      const style = getComputedStyle(node)
      if (style.visibility !== 'visible' || style.display === 'none' || style.opacity === '0') return false
    }
    return true
  }
  const visible = element => {
    const { width, height } = element.getBoundingClientRect()
    return width > 0 && height > 0 && visibleBranch(element)
  }
  const add = value => {
    if (value.length <= 254 && email.test(value)) addresses.add(value.toLowerCase())
  }

  for (const area of document.querySelectorAll(settings)) {
    if (area.closest(excluded) || !visible(area) || (dialogRoute && area.matches('main'))) continue
    let followingLabel = false
    const readText = text => {
      for (const raw of text.split('\n')) {
        const line = raw.trim()
        if (!line) continue
        if (followingLabel) add(line)
        followingLabel = emailLabel.test(line)
      }
    }
    const visit = element => {
      if (element.matches(excluded) || (element !== area && element.matches(settings)) ||
          (!visible(element) && !(getComputedStyle(element).display === 'contents' && visibleBranch(element)))) {
        followingLabel = false
        return
      }
      if (element.matches('div') && element.children.length === 2) {
        const [caption, value] = element.children
        const group = caption.firstElementChild
        const title = group?.firstElementChild
        const description = group?.lastElementChild
        const address = value.firstElementChild
        if (caption.children.length === 1 && value.children.length === 1 && group?.children.length === 2 &&
            title.children.length === 0 && description.children.length === 0 &&
            address.matches('span') && address.children.length === 0 &&
            [caption, group, title, description, value].every(node => node.matches('div') && visible(node)) &&
            visible(address) && !element.querySelector(excluded) &&
            emailLabel.test(title.innerText.trim()) && description.innerText.trim() === 'The email address linked to your account') {
          add(address.innerText.trim())
        }
      }
      for (const node of element.childNodes) {
        if (node.nodeType === Node.ELEMENT_NODE) visit(node)
        if (node.nodeType === Node.TEXT_NODE && node.textContent) readText(node.textContent)
      }
    }
    visit(area)
  }

  if (addresses.size > 1) return { status: 'ambiguous' }
  return addresses.size ? { status: 'verified', email: addresses.values().next().value } : { status: 'waiting' }
}

export async function verifyCurrentAccount(windowId) {
  const target = await chrome.tabs.create({ windowId, url: settingsURL, active: false })
  if (target.id === undefined) return { status: 'unavailable' }
  const deadline = Date.now() + 15_000
  try {
    do {
      const tab = await chrome.tabs.get(target.id)
      // If the user takes over this tab, leave it alone.
      if (tab.active) return { status: 'cancelled' }
      if (tab.status === 'complete') {
        const [answer] = await chrome.scripting.executeScript({
          target: { tabId: target.id, frameIds: [0] },
          world: 'ISOLATED',
          func: readAccountEmail,
        }).catch(() => [])
        if (answer?.frameId === 0) {
          if (answer.result?.status === 'verified' &&
              typeof answer.result.email === 'string' && answer.result.email.length <= 254 &&
              emailPattern.test(answer.result.email)) return answer.result
          if (answer.result?.status !== 'waiting') return answer.result
        }
      }
      await new Promise(resolve => setTimeout(resolve, 250))
    } while (Date.now() < deadline)
    return { status: 'unavailable' }
  } finally {
    const tab = await chrome.tabs.get(target.id).catch(() => null)
    if (tab && !tab.active) await chrome.tabs.remove(target.id).catch(() => {})
  }
}
