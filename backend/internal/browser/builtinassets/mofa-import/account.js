const statuses = new Set(['skipped', 'imported', 'reauthorized', 'group_added'])
const connectionError = '未收到 Ant 的导入结果。若实例已打开授权页，请继续核对，再到 MOFA 检查结果。'
const focusError = '无法切到授权页，请重试或在该实例中手动切换。'
const streamType = 'application/x-ndjson'
const jsonType = 'application/json'

async function postToBridge({site, bridgePort}, request, accept, timeout) {
  const url = `http://127.0.0.1:${bridgePort}/mofa/import`
  const response = await fetch(url, {
    method: 'POST', headers: {'Content-Type': jsonType, Accept: accept},
    body: JSON.stringify({site, ...request}), cache: 'no-store', credentials: 'omit',
    redirect: 'error', referrerPolicy: 'no-referrer', signal: AbortSignal.timeout(timeout),
  })
  if (response.redirected || (response.url && response.url !== url)) throw new Error()
  return response
}

function mediaType(response) {
  return response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase()
}

function replyError(body) {
  const error = body?.error
  return typeof error === 'string' && error.trim() && error.length <= 512 ? error : ''
}

async function readImportStream(body, onPhase) {
  const reader = body.pipeThrough(new TextDecoderStream('utf-8', {fatal: true})).getReader()
  const maxLineLength = 4096
  let pending = ''
  try {
    for (;;) {
      const {value, done} = await reader.read()
      const lines = (pending + (value ?? '')).split('\n')
      pending = done ? '' : lines.pop()
      for (const line of lines) {
        if (line.length > maxLineLength) throw new Error()
        if (!line.trim()) continue
        const update = JSON.parse(line)
        if (update?.result || typeof update?.error === 'string') return update
        if (typeof update?.phase === 'string') onPhase?.(update.phase)
      }
      if (done || pending.length > maxLineLength) throw new Error()
    }
  } finally {
    void reader.cancel().catch(() => {})
  }
}

export async function requestMofaImport(config, proof, onPhase) {
  let response, streaming, body
  try {
    response = await postToBridge(config, {proof}, streamType, 9 * 60_000)
    const type = mediaType(response)
    streaming = response.ok && type === streamType
    if (streaming) body = await readImportStream(response.body, onPhase)
    else if (type === jsonType) body = await response.json()
    else throw new Error()
  } catch {
    throw new Error(connectionError)
  }
  const error = replyError(body)
  if (error && (streaming || !response.ok)) throw new Error(error)
  const result = body?.result
  if (!response.ok || !statuses.has(result?.status) ||
      !Number.isSafeInteger(result.accountId) || result.accountId <= 0 ||
      !Number.isSafeInteger(result.groupId) || result.groupId <= 0 || typeof result.inTargetGroup !== 'boolean') {
    throw new Error(connectionError)
  }
  return result
}

export async function focusMofaAuthorization(config, proof) {
  let error
  try {
    const response = await postToBridge(config, {proof, action: 'focus_oauth'}, jsonType, 8000)
    if (response.status === 204) return
    if (!response.ok && mediaType(response) === jsonType) error = replyError(await response.json())
  } catch {
    // This action can be retried while the import continues.
  }
  throw new Error(error || focusError)
}
