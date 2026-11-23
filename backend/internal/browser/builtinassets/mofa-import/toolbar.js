const pageURL = chrome.runtime.getURL('import.html')
const pageOrigin = `chrome-extension://${chrome.runtime.id}`
const permitMessage = 'take-mofa-toolbar-click'
const permits = new Map()
let opening = false

chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (message?.type !== permitMessage || sender.id !== chrome.runtime.id ||
      sender.origin !== pageOrigin || sender.url !== pageURL || sender.frameId !== 0 ||
      sender.documentLifecycle !== 'active' || sender.tab?.incognito !== false) return
  const { id, windowId } = sender.tab
  const permit = permits.get(id)
  if (!permit || permit.windowId !== windowId) {
    reply(false)
    return
  }
  permits.delete(id)
  reply(permit.deadline >= Date.now())
})

chrome.tabs.onRemoved.addListener(tabId => permits.delete(tabId))

chrome.action.onClicked.addListener(async ({ windowId, incognito }) => {
  if (incognito !== false || opening) return
  const now = Date.now()
  for (const [id, permit] of permits) {
    if (permit.deadline < now) permits.delete(id)
  }
  if (permits.size > 0) return
  opening = true
  try {
    // Register the new tab before loading the extension document.
    const { id } = await chrome.tabs.create({ windowId, url: 'about:blank', active: false })
    permits.set(id, { windowId, deadline: Date.now() + 20_000 })
    try {
      await chrome.tabs.update(id, { url: pageURL, active: true })
    } catch {
      permits.delete(id)
    }
  } catch {
    // A window can close while its toolbar action is being handled.
  } finally {
    opening = false
  }
})
