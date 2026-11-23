export async function configuredSite() {
  const configURL = chrome.runtime.getURL('site.json')
  const response = await fetch(configURL, { cache: 'no-store', credentials: 'omit' })
  if (!response.ok) throw new Error('MOFA site unavailable')
  const { site } = await response.json()
  if (site === '') return ''
  if (typeof site !== 'string' || /[?#\\]/.test(site)) throw new Error('Invalid MOFA origin')
  const url = new URL(site)
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/') throw new Error('Invalid MOFA origin')
  return url.origin
}

export function accountManagerURL(site) {
  return new URL('/admin/accounts', site).href
}
