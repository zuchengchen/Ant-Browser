const pickerURL = chrome.runtime.getURL('accounts.html')
const opening = new Set()
const clicks = new Map()

// Only an action click may start a local login. An accounts tab opened or
// restored by Chromium can show its record, but cannot reuse an earlier click.
chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (message?.type !== 'take-toolbar-click' || sender.id !== chrome.runtime.id ||
      sender.url !== pickerURL || sender.frameId !== 0 || !sender.tab) return
  const deadline = clicks.get(sender.tab.id)
  clicks.delete(sender.tab.id)
  reply(deadline !== undefined && deadline >= Date.now())
})
chrome.tabs.onRemoved.addListener(tabId => clicks.delete(tabId))

chrome.action.onClicked.addListener(async tab => {
  const windowId = tab.windowId
  if (opening.has(windowId)) return
  opening.add(windowId)
  try {
    const contexts = await chrome.runtime.getContexts?.({ contextTypes: ['TAB'], windowIds: [windowId], documentUrls: [pickerURL] })
    if (contexts?.length) {
      const tabId = contexts[0].tabId
      try {
        clicks.set(tabId, Date.now() + 30_000)
        await chrome.tabs.update(tabId, { active: true })
        // An already open picker consumes the click immediately. A picker
        // still starting up will request it in its initial page load.
        await chrome.runtime.sendMessage({ type: 'toolbar-click', tabId }).catch(() => {})
        return
      } catch {
        clicks.delete(tabId)
        // The picker can close between enumeration and activation.
      }
    }
    const picker = await chrome.tabs.create({ windowId, url: 'about:blank', active: false })
    clicks.set(picker.id, Date.now() + 30_000)
    try {
      await chrome.tabs.update(picker.id, { url: pickerURL, active: true })
    } catch {
      clicks.delete(picker.id)
    }
  } finally {
    opening.delete(windowId)
  }
})
