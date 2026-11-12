import type { ComponentProps } from 'react'
import type { BrowserProxy } from '../../types'
import { ProxyPoolImportModal } from './ProxyPoolImportModal'
import { ProxySourceRefreshModal } from './ProxySourceRefreshModal'
import { ProxyCoreDownloadModal } from './ProxyCoreDownloadModal'
import { useDownloadProxyOptions, useFetchProxyOptions } from './proxyPoolSpeed'

// Keep every original Modal mounted. In particular, continue supplying current
// options while its backdrop exits; open=false must not erase a visible draft.
export function ConnectedProxyPoolImportModal({ proxies, ...props }:
  Omit<ComponentProps<typeof ProxyPoolImportModal>, 'fetchProxyOptions'> & { proxies: BrowserProxy[] }) {
  const options = useFetchProxyOptions(proxies)
  return <ProxyPoolImportModal {...props} fetchProxyOptions={options} />
}

export function ConnectedProxySourceRefreshModal(props:
  Omit<ComponentProps<typeof ProxySourceRefreshModal>, 'fetchProxyOptions'>) {
  const options = useFetchProxyOptions(props.proxies)
  return <ProxySourceRefreshModal {...props} fetchProxyOptions={options} />
}

export function ConnectedProxyCoreDownloadModal({ proxies, ...props }:
  Omit<ComponentProps<typeof ProxyCoreDownloadModal>, 'proxyOptions'> & { proxies: BrowserProxy[] }) {
  const options = useDownloadProxyOptions(proxies)
  return <ProxyCoreDownloadModal {...props} proxyOptions={options} />
}
