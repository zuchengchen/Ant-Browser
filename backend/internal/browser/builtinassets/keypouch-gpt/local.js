import { accountEmail, normalizeVaultSite, VaultError } from './vault.js'

export const localKey = 'antLocalGPT'

// The desktop writes one complete snapshot to this extension's trusted,
// in-memory storage on startup or sync. An absent entry allows legacy login.
// A broken entry is an error; falling back could select a different account.
async function snapshot() {
  let entry
  try {
    entry = (await chrome.storage.session.get(localKey))[localKey]
  } catch {
    throw new VaultError('local_storage')
  }
  if (entry === undefined) return null
  return validateLocalRecord(entry)
}

export function validateLocalRecord(entry) {
  if (!entry || !Number.isSafeInteger(entry.recordId) || entry.recordId <= 0 ||
      !entry.email || accountEmail(entry.email) !== entry.email ||
      typeof entry.password !== 'string' || typeof entry.totpSecret !== 'string') {
    throw new VaultError('local_storage')
  }
  try {
    // Ant preserves the configured host spelling and explicit default port.
    normalizeVaultSite(entry.site)
  } catch {
    throw new VaultError('local_storage')
  }
  return entry
}

export const localVault = {
  async account() {
    const entry = await snapshot()
    if (!entry) return null
    return { id: entry.recordId, site: entry.site, email: entry.email, hasPassword: !!entry.password, hasTOTP: !!entry.totpSecret }
  },
  async login(record, signal) {
    const entry = await snapshot()
    signal.throwIfAborted()
    if (!entry || entry.recordId !== record.id || entry.email !== record.email || entry.site !== record.site) {
      throw new VaultError('local_changed')
    }
    return { email: entry.email, password: entry.password, totpSecret: entry.totpSecret }
  },
}
