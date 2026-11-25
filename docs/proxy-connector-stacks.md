# 代理连接栈

连接栈由 `config.yaml` 的 `browser.default_connector_type` 设置，默认值为 `xray`。

| 设置 | 常见协议与实际内核 |
| --- | --- |
| `xray` | Xray 处理 VMess、VLESS、Trojan、标准 Shadowsocks、链式代理和需桥接的 HTTP/SOCKS5 鉴权。sing-box 处理 Hysteria、Hysteria2、TUIC 和 AnyTLS。 |
| `mihomo` | Mihomo 处理本栈可用的桥接协议，包括 Hysteria2、TUIC、AnyTLS、WireGuard 和部分专用 Shadowsocks 加密。 |

显式直连、无需桥接的 HTTP/SOCKS5 使用原生连接。已支持的 Mieru 节点使用专用 Mieru 桥接。指定节点内核时，该内核必须支持节点协议并可在当前连接栈使用。连接失败时，`xray` 组合栈和 `mihomo` 栈之间禁止自动回退。

实例启动、测速、真实页面连通性、IP 健康、预热、订阅拉取、内核和扩展下载都使用当前连接栈。指定代理 ID 的网络操作应在找不到代理时报告错误，直连需要调用方明确选择。HTTP 操作在响应完成前持有桥接引用，并在结束或取消后释放。

相关实现位于 [内核选择](../backend/internal/proxy/kernel_resolver.go) 和 [HTTP 桥接生命周期](../backend/internal/proxy/http_client.go)。历史检查见 [VPN / 代理协议审计报告](audits/vpn-protocol-audit-report.md)。
