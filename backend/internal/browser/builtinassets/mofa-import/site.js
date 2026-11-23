export async function configuredMofa() {
  const configURL = chrome.runtime.getURL('site.json')
  const response = await fetch(configURL, { cache: 'no-store', credentials: 'omit' })
  if (!response.ok) throw new Error('MOFA site unavailable')
  const { site, bridgePort } = await response.json()
  const port = Number.isInteger(bridgePort) && bridgePort > 0 && bridgePort <= 65535 ? bridgePort : 0
  if (site === '') return { site, bridgePort: port }
  if (typeof site !== 'string' || /[?#\\]/.test(site)) throw new Error('Invalid MOFA origin')
  const url = new URL(site)
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/') throw new Error('Invalid MOFA origin')
  return { site: url.origin, bridgePort: port }
}
