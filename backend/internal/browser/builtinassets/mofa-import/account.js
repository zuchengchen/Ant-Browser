import readAccountEmail from './account-email.js'

const settingsURL = 'https://chatgpt.com/settings/account'
const emailPattern = /^[^\s@<>\u0000-\u001f\u007f]+@[^\s@<>\u0000-\u001f\u007f]+\.[^\s@<>\u0000-\u001f\u007f]+$/

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
