# Ant-Browser Linux 修复验证报告

已完成目标文件约定的六项 Linux 修复，验证环境为 Linux amd64。所有改动保留在本地工作区；未提交、推送、打标签或发布。

- 检查基线：`fbd43230abd6cefec206a46073c1d6853746a2b2`。
- 目标文件：[2026-11-09-fix-linux-audit-findings.md](../goals/2026-11-09-fix-linux-audit-findings.md)。
- 最终验证日志：`/tmp/ant-linux-fix-rrf3m2co/verification/final-20261109T131307.689789Z`。
- 命令、退出状态和构建产物：[results.json](/tmp/ant-linux-fix-rrf3m2co/verification/final-20261109T131307.689789Z/results.json)。
- 测试发现、执行、通过及跳过证据：[evidence.json](/tmp/ant-linux-fix-rrf3m2co/verification/final-20261109T131307.689789Z/evidence.json)。

| 修复 | 最终行为 | 主要文件 |
|---|---|---|
| 归档解压 | TAR/ZIP 拒绝路径与链接逃逸；验证最终目录中的链接链；普通文件先写临时文件再替换，避免跟随预存链接；保留合法相对链接 | `backend/internal/browser/download_core_extract.go`、`download_core_extract_safety.go` |
| 快照恢复 | 完整暂存解压后，锁内复查实例停止状态及目录身份，再备份和替换；失败回滚；回滚失败保留原数据并报告位置 | `backend/app_snapshot_restore.go` |
| 虚拟分组 | 校验、预热和探测只读候选；真正取得启动成员后提交游标；固定、动态、一次性分组均保留 sticky、健康筛选及成员回退行为 | `backend/app_proxy_speed_shared.go`、`app_instance_start_proxy.go` |
| 启动队列 | 等待频道和位置在锁内取值；统一初始化；同实例请求共享名额并独立释放；旧释放回调不能撤销新名额 | `backend/app_instance_launch_gate.go` |
| 分片下载 | 严格验证探测/分片响应、范围、长度与写入结果；重试耗尽返回错误；取消立即结束；支持小文件、重试与单流回退 | `backend/internal/browser/download_core_transfer.go` |
| Linux 桥接测试 | 按当前平台架构选择原生 Xray，并复制到测试目录；明确验证启动成功、本地流量转发及端口回收 | `backend/internal/testutil/runtime.go`、`backend/app_virtual_group_binding_chain_test.go` 及相关代理测试 |

所有必需命令均返回退出状态 `0`：

| 命令 | 结果 |
|---|---|
| `git diff --check` | 通过，无空白差错 |
| `(cd frontend && npm ci && npm run build:clean)` | 通过；dist/index.html 和构建资源属于本次新构建 |
| `go test -count=1 -json -tags webkit2_41 ./...` | 821 个 test pass 事件（含子用例），6 个既有可选/平台用例跳过，0 fail |
| `go test -race -count=1 -json -tags webkit2_41 ./backend/... ./internal/...` | 821 个 test pass 事件（含子用例），同样 6 个跳过，0 fail，未报告数据竞争 |
| `go test -race -count=10 -json -tags webkit2_41 ./backend -run '^TestInstanceLaunchGate(LimitsAndQueues\|Guards)$'` | 两项指定测试分别重复 10 次，共 20 次通过，无跳过 |
| `go test -count=1 -json -tags webkit2_41 ./backend -run '^TestVirtualGroupBindingStartChainWithBridgeMember$'` | 真实 Linux Xray 桥接测试通过，无跳过 |
| `python3 -m unittest discover -s scripts/tests -v` | 12 项测试全部通过 |

六项问题对应的 39 个必需顶层回归测试、148 个含子用例的测试事件，均在最终全量和 race 运行中实际执行并通过。完整名称列在 evidence.json 的 required_groups 中。解析器使用一次真实失败运行和一次真实成功运行校准，并确认普通输出中的 FAIL/error 字样不会误判、缺失必需测试不能通过。

跳过的六项既有测试均不承担本次六项验收：

| 测试 | 原因 |
|---|---|
| `TestMieruBridgeEndToEnd` | 未提供其使用的 Windows Mieru 运行时 |
| `TestMieruConcurrentLaunchSameKey` | 未提供其使用的 Windows Mieru 运行时 |
| `TestMieruCoreDownloadEndToEndFreshEnvironment` | 明确限定 Windows amd64，本次环境是 Linux amd64 |
| `TestMieruResolveBinaryFindsDownloadLayout` | 缺少外部 Windows Mieru 运行时样本 |
| `TestRealJsYamlSamplesAllParse` | 缺少外部 js-yaml 样本文件 |
| `TestVirtualGroupRealUserDataEndToEnd` | 缺少私有用户数据库；本次测试未读取真实用户数据 |

人工审阅确认代理连接栈未发生跨栈回退，公开 API、配置和数据库格式保持兼容；修改未涉及 macOS 发布问题或 Windows/macOS 专属修复。仓库运行时二进制及权限没有变化，测试 Xray 进程已回收。三个子代理承担的五个独立任务均已收集、审阅和整合；五个临时工作副本已在确认补丁完整后清理，补丁与验证日志保留在 `/tmp/ant-linux-fix-rrf3m2co/artifacts`。

边界：快照安装与回滚若同时遭遇文件系统错误，原数据会保留在错误消息给出的 original 备份目录，需从该目录恢复。归档防护按产品私有暂存目录设计；保留普通文件及合法相对链接，但拒绝通过符号链接目录继续写入条目的归档结构。未执行 Windows/macOS 实机验收、Linux GUI 手动运行或发布打包。
