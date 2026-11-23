const pageURL = chrome.runtime.getURL('import.html')
const opening = new Set()

chrome.action.onClicked.addListener(async ({ windowId }) => {
  if (opening.has(windowId)) return
  opening.add(windowId)
  try {
    const contexts = await chrome.runtime.getContexts?.({contextTypes: ['TAB'], windowIds: [windowId]})
    const existing = contexts?.find(({documentUrl}) => documentUrl?.split('#', 1)[0] === pageURL)
    if (existing) {
      const tab = await chrome.tabs.update(existing.tabId, { active: true }).catch(() => null)
      if (tab) return
    }
    await chrome.tabs.create({ windowId, url: pageURL, active: true })
  } catch {
    // A window can close while its toolbar action is being handled.
  } finally {
    opening.delete(windowId)
  }
})
