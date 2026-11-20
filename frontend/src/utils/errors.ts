export function errorMessage(value: unknown, fallback: string): string {
  if (value instanceof Error) return value.message || fallback
  if (typeof value === 'string') return value.trim() || fallback
  if (value && typeof value === 'object') {
    const source = value as Record<string, unknown>
    for (const key of ['message', 'error', 'detail']) {
      const message = source[key]
      if (typeof message === 'string' && message.trim()) return message.trim()
    }
  }
  return fallback
}
