import type { MouseEventHandler } from 'react'
import { Wifi } from 'lucide-react'

import { Button } from '../../../../shared/components/Button'
import type { ProxyDisplayInfo } from './helpers'
import { useProxyPoolSpeedValue } from './proxyPoolSpeed'

function targetId(record: ProxyDisplayInfo) {
  return record.isVirtual || record.proxyConfig === 'direct://' ? undefined : record.proxyId
}

export function ProxyPoolLatencyCell({ record }: { record: ProxyDisplayInfo }) {
  const { latency: value, error } = useProxyPoolSpeedValue(targetId(record))
  if (record.isVirtual) return <>按成员节点</>
  if (record.proxyConfig === 'direct://') return <span className="text-[var(--color-text-muted)] text-xs">不适用</span>
  if (value === undefined) return <span className="text-[var(--color-text-muted)] text-xs">未测速</span>
  if (value === -1) return <span className="text-[var(--color-text-muted)] text-xs animate-pulse">测试中...</span>
  if (value === -2) return <span className="text-red-500 text-xs" title={error || '测速超时'}>超时</span>
  if (value === -3) return <span className="text-gray-400 text-xs" title={error || '协议不支持'}>不支持</span>
  if (value === -4) return <span className="text-red-500 text-xs" title={error || '测速失败'}>失败</span>
  const color = value < 200 ? 'text-green-500' : value < 500 ? 'text-yellow-500' : 'text-red-500'
  return <span className={`text-xs font-medium ${color}`}>{value} ms</span>
}

export function ProxyPoolLatencyEngineCell({ record }: { record: ProxyDisplayInfo }) {
  const { latency: value, engine } = useProxyPoolSpeedValue(targetId(record))
  if (record.isVirtual) return <>虚拟分组</>
  if (record.proxyConfig === 'direct://') return <span className="text-[var(--color-text-muted)] text-xs">不适用</span>
  if (value === undefined) return <span className="text-[var(--color-text-muted)] text-xs">-</span>
  if (value === -1) return <span className="text-[var(--color-text-muted)] text-xs animate-pulse">-</span>
  return engine
    ? <span className="text-xs text-[var(--color-text-secondary)] whitespace-nowrap">{engine}</span>
    : <span className="text-[var(--color-text-muted)] text-xs">-</span>
}

export function ProxyPoolTestButton({ record, onClick }: {
  record: ProxyDisplayInfo
  onClick: MouseEventHandler<HTMLButtonElement>
}) {
  // Preserve the original final latency-value check, including unusual result
  // payloads and direct records, rather than changing it to testingIds alone.
  const { latency } = useProxyPoolSpeedValue(record.proxyId)
  return <Button size="sm" variant="ghost" className="px-2" title="测速"
    onClick={onClick} loading={latency === -1} disabled={record.proxyConfig === 'direct://'}>
    {latency !== -1 && <Wifi className="w-3.5 h-3.5" />}
  </Button>
}
