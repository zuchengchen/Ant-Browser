const statuses = new Set(['skipped', 'imported', 'reauthorized', 'group_added'])
const connectionError = '未收到 Ant 的导入结果。若实例已打开授权页，请继续核对，再到 MOFA 检查结果。'

export async function requestMofaImport({site, bridgePort}, proof) {
  const url = `http://127.0.0.1:${bridgePort}/mofa/import`
  let response, body
  try {
    response = await fetch(url, {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({site, proof}), cache: 'no-store', credentials: 'omit',
      redirect: 'error', referrerPolicy: 'no-referrer', signal: AbortSignal.timeout(9 * 60_000),
    })
    if (response.redirected || (response.url && response.url !== url) ||
        !response.headers.get('Content-Type')?.toLowerCase().startsWith('application/json')) {
      throw new Error()
    }
    body = await response.json()
  } catch {
    throw new Error(connectionError)
  }
  if (!response.ok && typeof body?.error === 'string' && body.error.trim() && body.error.length <= 512) {
    throw new Error(body.error)
  }
  const result = body?.result
  if (!response.ok || !statuses.has(result?.status) ||
      !Number.isSafeInteger(result.accountId) || result.accountId <= 0 ||
      !Number.isSafeInteger(result.groupId) || result.groupId <= 0 || typeof result.inTargetGroup !== 'boolean') {
    throw new Error(connectionError)
  }
  return result
}
