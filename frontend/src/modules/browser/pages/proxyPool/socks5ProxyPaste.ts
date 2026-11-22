import type { ClipboardEvent, FocusEvent } from 'react'
import { toast } from '../../../../shared/components/Toast'
import { parseDirectProxyURL } from './helpers.direct'
import type { DirectImportForm } from './helpers.types'

type ProxyInput = HTMLInputElement | HTMLTextAreaElement

export function socks5ProxyInputProps(apply: (fields: Omit<DirectImportForm, 'proxyName'>) => void) {
  function recognize(value: string) {
    const text = value.trim()
    if (!/^socks5:\/\/\S+$/i.test(text)) return false

    let form: DirectImportForm
    try {
      form = parseDirectProxyURL(text)
    } catch {
      toast.error('SOCKS5 链接格式无效，请检查主机、端口及账号密码的 URL 编码。')
      return true
    }
    const { protocol, server, port, username, password } = form
    apply({ protocol, server, port, username, password })
    return true
  }

  return {
    onPaste(event: ClipboardEvent<ProxyInput>) {
      const { tagName, value, selectionStart, selectionEnd } = event.currentTarget
      // A complete proxy URL replaces a host. In an existing YAML or URL draft,
      // leave partial selections alone so users can still paste into its contents.
      const editingDraft = tagName === 'TEXTAREA' || value.includes('://')
      if (editingDraft && value.trim() && (selectionStart !== 0 || selectionEnd !== value.length)) return
      if (recognize(event.clipboardData.getData('text/plain'))) event.preventDefault()
    },
    onBlur(event: FocusEvent<ProxyInput>) {
      recognize(event.currentTarget.value)
    },
  }
}
