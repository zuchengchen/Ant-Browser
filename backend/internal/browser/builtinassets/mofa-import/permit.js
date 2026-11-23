export const permitKey = 'mofa-toolbar-permit'

export function withToolbarPermit(usePermit) {
  return navigator.locks.request(permitKey, async () => {
    const stored = await chrome.storage.session.get(permitKey)
    return usePermit(stored[permitKey])
  })
}

export async function takeToolbarPermit() {
  const pageURL = chrome.runtime.getURL('import.html')
  if (window !== window.top || location.href !== pageURL || chrome.extension.inIncognitoContext !== false) return false
  const tab = await chrome.tabs.getCurrent()
  if (tab?.incognito !== false || !Number.isInteger(tab.id) || !Number.isInteger(tab.windowId)) return false
  return withToolbarPermit(async permit => {
    if (location.href !== pageURL || permit?.tabId !== tab.id || permit.windowId !== tab.windowId) return false
    await chrome.storage.session.remove(permitKey)
    return permit.deadline >= Date.now()
  })
}
