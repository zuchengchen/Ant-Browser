import { accountEmail, normalizeVaultSite, VaultError } from './vault.js'

const storeName = 'site-keys'
const curve = { name: 'ECDSA', namedCurve: 'P-256' }

function openKeyDatabase() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('keypouch-gpt', 1)
    request.onupgradeneeded = () => request.result.createObjectStore(storeName)
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
}

// Rechecking in the write transaction also covers concurrent first use and two
// pickers asking to replace the same displayed code. A stale picker keeps the winner.
async function storedPair(site, candidate, replacing) {
  const db = await openKeyDatabase()
  try {
    return await new Promise((resolve, reject) => {
      const transaction = db.transaction(storeName, candidate ? 'readwrite' : 'readonly')
      const store = transaction.objectStore(storeName)
      const request = store.get(site)
      let pair
      request.onsuccess = () => {
        pair = request.result
        if (candidate && (pair === undefined || (replacing && pair.generation === replacing.generation))) {
          store.put(candidate, site)
          pair = candidate
        }
      }
      transaction.oncomplete = () => resolve(pair)
      transaction.onabort = () => reject(transaction.error)
    })
  } finally {
    db.close()
  }
}

function base64url(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

async function siteIdentity(site, replacing) {
  try {
    let pair = replacing ? undefined : await storedPair(site)
    if (pair === undefined) {
      const keys = await crypto.subtle.generateKey(curve, false, ['sign', 'verify'])
      pair = await storedPair(site, { ...keys, generation: crypto.randomUUID() }, replacing)
    }
    const { privateKey, publicKey } = pair
    if (privateKey.type !== 'private' || privateKey.extractable || !privateKey.usages.includes('sign') ||
        privateKey.algorithm.name !== curve.name || privateKey.algorithm.namedCurve !== curve.namedCurve ||
        publicKey.type !== 'public' || publicKey.algorithm.name !== curve.name || publicKey.algorithm.namedCurve !== curve.namedCurve) {
      throw new VaultError('key_storage')
    }
    const publicBytes = new Uint8Array(await crypto.subtle.exportKey('raw', publicKey))
    const digest = await crypto.subtle.digest('SHA-256', publicBytes)
    return { generation: pair.generation, id: base64url(digest), code: 'KPG1.' + base64url(publicBytes) }
  } catch {
    throw new VaultError('key_storage')
  }
}

function validRecord(record) {
  return Number.isSafeInteger(record?.id) && record.id > 0 && !!record.email && accountEmail(record.email) === record.email
}

async function signedRequest(site, identity, action, id, email, signal) {
  const controller = new AbortController()
  const cancel = () => controller.abort(new DOMException('读取 KeyPouch 已取消。', 'AbortError'))
  if (signal?.aborted) cancel()
  else signal?.addEventListener('abort', cancel, { once: true })
  const timeout = setTimeout(() => controller.abort(new VaultError('timeout')), 15_000)
  let failure = 'key_storage'
  try {
    controller.signal.throwIfAborted()
    // A picker that predates an explicit rotation must not sign with its old key.
    const pair = await storedPair(site)
    if (!pair || pair.generation !== identity.generation) throw new VaultError('grant_required')
    const keyID = identity.id
    const time = Math.floor(Date.now() / 1000)
    const nonce = base64url(crypto.getRandomValues(new Uint8Array(24)))
    const message = ['KPG1', action, keyID, time, nonce, id, email].join('\n')
    const signature = await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, pair.privateKey, new TextEncoder().encode(message))
    controller.signal.throwIfAborted()

    failure = 'network'
    const url = site + '/api/gpt-plugin/' + action
    const response = await fetch(url, {
      method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ key_id: keyID, time, nonce, signature: base64url(signature), record_id: id, email }),
      signal: controller.signal,
    })
    controller.signal.throwIfAborted()
    if (response.redirected || (response.url && response.url !== url)) throw new VaultError('network')
    if (response.status === 401 || response.status === 403) throw new VaultError('grant_required')
    if (response.status === 404 || response.status === 405) throw new VaultError('unsupported')
    if (response.status === 409) {
      const body = await response.json().catch(() => null)
      throw new VaultError(action === 'totp' && body?.error === 'totp_unavailable' ? 'totp_unavailable' : 'record_changed')
    }
    if (response.status !== 200) throw new VaultError('service_error')
    failure = 'invalid_response'
    const data = await response.json()
    controller.signal.throwIfAborted()
    return data
  } catch (error) {
    controller.signal.throwIfAborted()
    if (error instanceof VaultError) throw error
    throw new VaultError(failure)
  } finally {
    clearTimeout(timeout)
    signal?.removeEventListener('abort', cancel)
  }
}

export async function openPairedVault(rawSite) {
  const site = normalizeVaultSite(rawSite)
  const identity = await siteIdentity(site)
  async function readSelected(action, record, signal) {
    if (!validRecord(record)) throw new VaultError('record_changed')
    const { id, email } = record
    const data = await signedRequest(site, identity, action, id, email, signal)
    if (data?.id !== id || data.email !== email) throw new VaultError('record_changed')
    return data
  }
  return {
    id: identity.id,
    code: identity.code,
    async replaceKey() {
      await siteIdentity(site, identity)
    },
    async account(signal) {
      try {
        const data = await signedRequest(site, identity, 'account', 0, '', signal)
        if (!validRecord(data) || typeof data.has_password !== 'boolean' || typeof data.has_totp !== 'boolean') {
          throw new VaultError('invalid_response')
        }
        return { id: data.id, email: data.email, hasPassword: data.has_password, hasTOTP: data.has_totp }
      } catch (error) {
        if (error instanceof VaultError && error.code === 'grant_required') return null
        throw error
      }
    },
    async login(record, signal) {
      const data = await readSelected('login', record, signal)
      if (typeof data.password !== 'string') throw new VaultError('invalid_response')
      return { email: data.email, password: data.password }
    },
    async totp(record, signal) {
      const data = await readSelected('totp', record, signal)
      if (typeof data.code !== 'string' || !/^\d{6}$/.test(data.code) || !Number.isSafeInteger(data.expires_at) || data.expires_at <= 0) {
        throw new VaultError('invalid_response')
      }
      return { code: data.code, expires: data.expires_at }
    },
  }
}
