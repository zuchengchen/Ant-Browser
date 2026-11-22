import { normalizeVaultSite } from './vault.js'

// This module only talks to the vault. Login remains usable offline and with
// older servers. Neither a page error nor a missing receipt changes the verdict.
async function post(site, path, data, signal) {
  const url = normalizeVaultSite(site) + path
  const headers = { 'Content-Type': 'application/json' }
  const controller = new AbortController()
  const cancel = () => controller.abort()
  if (signal?.aborted) cancel()
  signal?.addEventListener('abort', cancel, { once: true })
  const timer = setTimeout(cancel, 3500)
  try {
    const response = await fetch(url, {
      method: 'POST', headers, body: JSON.stringify(data), credentials: 'omit',
      cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer', signal: controller.signal,
    })
    if (!response.ok || response.redirected || (response.url && response.url !== url)) return null
    return await response.json()
  } catch { return null }
  finally { clearTimeout(timer); signal?.removeEventListener('abort', cancel) }
}

export function receiptToken(value) {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{32}$/.test(value) ? value : undefined
}

export async function localLoginAttempt(entry, signal) {
  if (typeof entry.loginBinding !== 'string' || !/^[A-Za-z0-9_-]{32}\.[A-Za-z0-9_-]{43}$/.test(entry.loginBinding)) return
  const reply = await post(entry.site, '/api/gpt-plugin/local-login-attempt', { binding: entry.loginBinding }, signal)
  return receiptToken(reply?.attempt)
}

export async function sendLoginResult(site, attempt, email, result, signal) {
  if (!receiptToken(attempt) || !['success', 'rejected'].includes(result)) return
  const reply = await post(site, '/api/gpt-plugin/login-result', { attempt, email, result }, signal)
  return reply !== null
}
