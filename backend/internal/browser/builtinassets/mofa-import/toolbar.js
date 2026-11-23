import { verifyCurrentAccount } from './account.js'
import { accountManagerURL, configuredSite } from './site.js'

const pageURL = chrome.runtime.getURL('import.html')
const opening = new Set()

chrome.action.onClicked.addListener(async ({ windowId }) => {
  if (opening.has(windowId)) return
  opening.add(windowId)
  try {
    const contexts = await chrome.runtime.getContexts?.({
      contextTypes: ['TAB'], windowIds: [windowId], documentUrls: [pageURL],
    })
    if (contexts?.length) {
      const tab = await chrome.tabs.update(contexts[0].tabId, { active: true }).catch(() => null)
      if (tab) return
    }
    await chrome.tabs.create({ windowId, url: pageURL, active: true })
  } catch {
    // A window can close while its toolbar action is being handled.
  } finally {
    opening.delete(windowId)
  }
})

async function openImport(sourceTab, displayedSite) {
  return navigator.locks.request('mofa-import', { ifAvailable: true }, async lock => {
    if (!lock) return 'busy'
    const site = await configuredSite()
    if (!site) return 'unconfigured'
    if (site !== displayedSite) return 'configuration_changed'
    const account = await verifyCurrentAccount(sourceTab.windowId)
    if (account.status !== 'verified') return account.status
    const tab = await chrome.tabs.get(sourceTab.id).catch(() => null)
    if (!tab || tab.windowId !== sourceTab.windowId || tab.url && tab.url !== pageURL) return 'cancelled'
    const view = await chrome.windows.get(tab.windowId)
    if (site !== await configuredSite()) return 'configuration_changed'
    await chrome.tabs.create({
      windowId: tab.windowId,
      url: accountManagerURL(site),
      active: !!(tab.active && view.focused),
    })
    return { email: account.email }
  })
}

chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (message?.type !== 'open-mofa-import' || sender.id !== chrome.runtime.id ||
      sender.url !== pageURL || sender.frameId !== 0 || !sender.tab) return false
  openImport(sender.tab, message.site).then(reply, () => reply('unavailable'))
  return true
})
