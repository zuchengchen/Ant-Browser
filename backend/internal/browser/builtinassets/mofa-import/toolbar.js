import { permitKey, withToolbarPermit } from './permit.js'

const pageURL = chrome.runtime.getURL('import.html')
let opening = false

function discardTabPermit(tabId) {
  return withToolbarPermit(permit => {
    if (permit?.tabId === tabId) return chrome.storage.session.remove(permitKey)
  })
}

chrome.tabs.onRemoved.addListener(tabId => {
  void discardTabPermit(tabId).catch(() => {})
})

chrome.action.onClicked.addListener(async ({ windowId, incognito }) => {
  if (incognito !== false || opening) return
  opening = true
  let tabId
  try {
    await withToolbarPermit(async permit => {
      if (permit?.deadline >= Date.now()) return
      const tab = await chrome.tabs.create({ windowId, url: 'about:blank', active: false })
      tabId = tab.id
      if (!Number.isInteger(tabId) || tabId < 0 || !Number.isInteger(tab.windowId) || tab.incognito !== false) {
        throw new Error('Invalid MOFA import tab')
      }
      // Publish the one-time permit before the new document can try to claim it.
      await chrome.storage.session.set({
        [permitKey]: { tabId, windowId: tab.windowId, deadline: Date.now() + 20_000 },
      })
      await chrome.tabs.update(tabId, { url: pageURL })
    })
  } catch {
    if (Number.isInteger(tabId) && tabId >= 0) {
      await chrome.tabs.remove(tabId).catch(() => {})
      await discardTabPermit(tabId).catch(() => {})
    }
  } finally {
    opening = false
  }
})
