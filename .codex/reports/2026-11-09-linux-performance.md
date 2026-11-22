# Ant-Browser Linux 性能优化验收记录

状态：包含两项补充候选的最终 20 文件产品版本已通过完整功能复验。第二次正式基线在 10,000 条 GUI 筛选阶段失败；独立原生输入诊断也在第四次筛选自然超时，确认当前环境无法可靠取得此档完整基线。已触发目标的必需验证停止条件，保留结果并等待用户决定后续方向，未降低标准或修改目标。尚未达到 Goal 完成条件。本文中的单轮诊断仅解释候选选择，不作为正式收益。

目标文件：[2026-11-09-linux-performance.md](../goals/2026-11-09-linux-performance.md)。批准内容 SHA-256 为 `6d64e45f1624a5f79b3b00376a99f4349f486a9d1985a7ae4e22a8a5b1337f72`，保持不变。

## 交付与环境

本轮修改 20 个产品文件，另增加行为回归、性能夹具和严格验证器。以本地工作区差异交付。上一轮 19 个 Linux 修复文件中，18 个与冻结基线逐字相同；`app_proxy_speed_shared.go` 仅替换目录查询入口一行，原有保存失败处理、连接栈来源和基础字段降级写入修复逐字保留。证据为 `artifacts/preserved-baseline-revised.json`。

基线包含原工作区的全部已存在 Linux 修复，HEAD 为 `fbd43230abd6cefec206a46073c1d6853746a2b2`。建立基线时从复制清单中排除了真实配置、数据库和浏览器目录。

| 路径变量 | 实际值 |
| --- | --- |
| `PERF_WORK_ROOT` | `/tmp/ant-linux-perf-cntr19l1` |
| `PERF_BASELINE_ROOT` | `/tmp/ant-linux-perf-cntr19l1/baseline` |
| `PERF_CANDIDATE_ROOT` | `/tmp/ant-linux-perf-cntr19l1/candidate` |
| `PERF_EVIDENCE_ROOT` | `/tmp/ant-linux-perf-cntr19l1/evidence/run2` |

量化环境为 Linux amd64、16 个逻辑 CPU、约 64 GB 内存，GTK 3/WebKitGTK 4.1 和独立 Xvfb。Chromium 使用本机 `/usr/lib/chromium/chromium`，两侧记录实际版本和二进制哈希。Go 统一为 1.24.13，Wails 固定 2.12.0；默认 Go 1.27 开发版本曾导致 Wails binding generator 无法读取包类型，故使用同一兼容工具链完成两侧构建。产品依赖和 Go 模块版本保持原样。

工作根目录位于本机 `/tmp` 的 tmpfs（`rw,noatime,inode64`），合成 SQLite 数据库和文件样本均在该文件系统。两侧一致，SQLite 的生产 WAL/synchronous 设置未改；但此环境没有实体存储介质的同步延迟，数据库和文件 I/O 数字不能当作 SSD/HDD 耗时或断电持久性证明。

Xray 和 sing-box 使用项目自带 Linux amd64 二进制的隔离副本。Mihomo 使用产品所固定的 v1.19.27，官方资产的校验记录保存在 `agents/proxy/tools/mihomo-provenance.json`；其二进制位于 `agents/proxy/tools/mihomo-v1.19.27`。本地 AnyTLS 使用临时 CA 和正常 TLS 验证，包含错误主机名必须被拒绝的反例。

## 已实现改动

| 领域及路径 | 改动 | 正确性依据 |
| --- | --- | --- |
| 启动：代理绑定初始化 | 每轮读取一次最新代理目录，建立同轮使用的 ID 首项索引；避免对每个实例重新全表读取代理 | 重新读取新一轮目录、订阅节点 ID 改变后的重绑、重复 ID、匹配优先级和歧义、DAO 失败、重启读回测试 |
| 实例管理与 SQLite | 编辑、关键字保存、创建、复制及启动默认补齐只持久化目标实例；显式全量保存仍保留 | SQLite 审计/拒写触发器、无关行保持不变、写失败内存回滚、并发读写、重开数据库测试 |
| 分组查询 | SQLite 用 `GROUP BY/COUNT(*)` 聚合当前有效实例，避免读取全部配置列和 JSON | 已删除/未分组/空组、大小写、直接数据库修改后的新鲜度、旧 DAO、错误路径测试 |
| 前端列表 | 代理精确 ID 首项索引；将共享测速目录初始化移到面板；无变化时保持 store 引用并避免通知 | 3 个真实 store 测试，6 组旧新静态渲染一致检查，以及真实 Wails 窗口操作验收 |
| 前端测速订阅（补充候选） | 普通单元格订阅其结果对象；按不可变结果 map 共享规范化首项索引，继续独立判断新鲜度 | 类型检查及 11 项实际 store/selector 测试、9 项真实 React/WebKit 订阅行为检查通过；收益测量待完成 |
| 后台测速目录（补充候选） | 短事务扫描 ID 确认无歧义，再读取完整单行；仅独立 HTTP/HTTPS/SOCKS5 节点使用，其他路径回到原全目录 | 定向普通及 race 各 124 次 pass，跨连接快照普通及 race 各通过；21 种 ID、37 种配置边界和完整字段覆盖 |
| 代理准备 | 就绪重试改为 10/20/40/80/100 ms，保留超时、取消和协议握手 | 真实 Xray、sing-box、Mihomo 本地流量，错误 SOCKS 服务、取消和超时回归 |
| 代理解析 | 同一次选栈调用复用已计算的协议和支持内核列表 | 诊断字段、显式内核与当前连接栈约束、AnyTLS/VLESS/HTTP/直连测试 |
| 后台日志 | 2000 条内存日志改为固定容量环形缓冲，减少扩容和复制 | 回绕后的时间顺序、快照独立性、Clear、过滤及并发 race 检查 |
| 启动可靠性 | 删除插件加载警告分支对已持有 `Manager.Mutex` 的重复加锁 | 同一个回归修复前超时并给出锁栈，修复后正常完成；保留原警告、Running/DebugReady 和 LastError 语义 |

可单独撤销本次改动的补丁保存在 `artifacts/performance-only.patch`，文件清单为 `artifacts/performance-change-list.json`。不要用整份工作区差异撤销本轮，以免连带撤销原有 Linux 修复。

## 七领域评估与候选处置

1. **应用及实例启动**：落实同轮代理目录复用、默认补齐单条保存，并修复警告分支自锁。保留现有调试端口稳定窗口、最后一次探测、启动队列和 launcher 分离处理。`ps` 进程扫描尚无收益证据支持替换；替换须覆盖 PID 复用、权限、参数边界和进程消失。备份前逐实例进程扫描已记录为后续候选，本轮没有通过跳过活跃状态检查获取速度。
2. **实例管理**：落实单目标写入和错误回滚。启动码已有缓存；没有再增加缓存。列表复制、排序及批量标签操作保留现有语义；持续排序缓存需要更多失效点，标签批事务会改变部分失败行为。用现有列表和更新基准监测。
3. **前端**：落实重复目录初始化和查询消除，补充普通单元格的单结果订阅与共享首项索引。保留 15 秒实例刷新、30 秒测速结果新鲜度、自然排序、分组和页面结构。虚拟组仍订阅全结果；动态候选来源和排序规则保持。10,000 行全 DOM 仍是候选；虚拟化涉及滚动、焦点、选中项和行状态，当前不做界面结构改造。路由和快捷启动对话框已经使用 lazy 加载。
4. **代理**：落实就绪重试和同次解析复用，并补充受限的后台单代理读取。每次调用仍读取当前已提交数据库，不增加 TTL 缓存；歧义及复杂配置继续原目录路径。桥接复用仍做真实握手；锁外握手需要重新证明回收与同 key 生命周期。端口二次绑定保留。Xray + sing-box 与独立 Mihomo 之间没有新增自动回退。
5. **数据库与持久化**：落实单条写入和聚合计数；未新增没有收益依据的索引或迁移。YAML 路径保持整份配置保存。启动迁移的空表读取保留；正式启动场景包括其成本。
6. **后台资源**：落实内存日志环形缓冲。文件日志有界队列、flush、调度并发上限和健康检查周期保持原语义。没有扩大虚拟分组或成员状态 TTL；真实运行矩阵分别记录应用、WebView、浏览器、代理及其他子进程的 CPU/PSS/RSS、FD、进程数和应用 goroutine。
7. **文件 I/O**：没有足够证据支持产品改动。下载已经使用 8 个有界分片、256 KiB 流式缓冲及进度限频；备份在一次遍历中同时写归档和计算哈希，并使用临时文件发布；快照恢复在管理锁外解压且保留回滚。固定下载/ZIP 样本测量 CPU、耗时及分配，验证内容哈希、取消、异常和文件描述符回收。远端下载最优并发不能从本地 loopback 测量推断。

补充查询仍是每次 O(N) ID 扫描，整轮仍有 O(N²) 个 ID 检查；下述辅助基准证明正常数据单次读取的耗时及分配改善，整轮后台/GUI 收益仍待正式验证。无关行的非 ID 字段损坏时，旧 List 会整体扫描失败，新查询可能仍能读取健康目标；目标字段、ID 和 SQL 错误均回到原错误路径。不能把正常数据下的等价扩大为对任意损坏数据库的全表校验等价。两项补充候选的完整验收仍待完成。

详细处置来源位于 `agents/database/candidate-report.md`、`agents/database/startup-process-review.md`、`agents/database/startup-binding-report.md`、`agents/frontend/review-and-handoff.md` 和 `agents/proxy/proxy-performance-findings.md`。其中早期文档描述的“未整合”以本报告最终集成状态为准。

## 补充查询的局部收益

隔离目录 `agents/database/proxy-lookup-measure/` 的辅助基准比较原完整目录读取与无歧义单目标读取。采用生产 SQLite DAO、完整 24 字段、相同逻辑数据库与五个轮换目标，每个模式每种规模 10 个样本、每样本 25 次查询；完整记录 6 个叶子、60 个样本和 120 条输入证明，生产者退出 0。测试前后验证完整字段、排序及提交后的修改可见性。单独校准适配器 39 项正反例通过，正式 32 叶登记保持不变。

| 代理条目 | 原完整目录读取中位数 | 新单目标读取中位数 | 耗时变化 | 变化率 95% CI |
| --- | --- | --- | --- | --- |
| 100 | 1.476 ms | 0.129 ms | -91.26% | [-93.89%, -86.72%] |
| 1,000 | 19.594 ms | 1.439 ms | -92.66% | [-94.80%, -92.33%] |
| 10,000 | 148.451 ms | 9.340 ms | -93.71% | [-94.64%, -92.81%] |

10,000 条时，每次分配字节中位数从 35,680,934 降至 966,651.5，分配次数从 676,420 降至 60,205。统计使用相同的固定种子 1729 和 4,000 次独立中位数 bootstrap。两种模式固定先旧后新，存在顺序与时段漂移边界；这些是已预热的单次数据库读取，不包含 App 的配置白名单检查、网络、调度或 GUI，不计入 Goal 的关键用户流程改善。原始日志、全部分布、CI 和来源见该目录 `RESULTS.md`、`capture-01/benchmark.stdout` 与 `capture-01/aux-report.json`。

## 32 项微基准的辅助对照

`agents/proxy/formal-microbench-v2/runs/run1/` 已完成相同 Go 夹具的前后对照：每侧实际执行登记的 21 个包、32 个叶基准，每叶恰好 10 次，共 640 个样本。两侧四个 producer 均退出 0，输入清单前后一致；比较时重新解析原始 stdout，校验命令、包完成、样本及封印。使用正式 collector 的原样副本和相同固定种子/4,000 次 bootstrap。父级复核见 `artifacts/microbench-v2-parent-review.json`；比较文件 SHA-256 为 `f931321422aa45de1c6864ae02ae96973d946b08acb8273af2f829c17ce4b171`。

| 局部操作 | 优化前中位数 | 优化后中位数 | 耗时变化 | 变化率 95% CI |
| --- | --- | --- | --- | --- |
| 分组列表，100 条 | 2.913 ms | 0.294 ms | -89.92% | [-90.37%, -89.21%] |
| 分组列表，1,000 条 | 22.611 ms | 0.637 ms | -97.18% | [-97.30%, -96.99%] |
| 分组列表，10,000 条 | 185.714 ms | 5.563 ms | -97.00% | [-97.12%, -96.90%] |
| 关键字保存，100 条 | 7.415 ms | 0.103 ms | -98.61% | [-98.68%, -98.52%] |
| 关键字保存，1,000 条 | 64.708 ms | 0.085 ms | -99.87% | [-99.88%, -99.86%] |
| 关键字保存，10,000 条 | 580.474 ms | 0.065 ms | -99.99% | [-99.989%, -99.988%] |
| 实例更新，100 条 | 7.225 ms | 0.114 ms | -98.42% | [-98.57%, -98.33%] |
| 实例更新，1,000 条 | 64.074 ms | 0.086 ms | -99.87% | [-99.88%, -99.86%] |
| 实例更新，10,000 条 | 558.076 ms | 0.067 ms | -99.99% | [-99.989%, -99.988%] |
| 内存日志写入 | 378.1 ns | 281.3 ns | -25.60% | [-31.86%, -20.01%] |

12 项代理选栈/解析中，10 项耗时改善的 CI 排除零，中位数改善约 4.53%–45.81%；10,000 条 AnyTLS 和 HTTP URI 的改善区间包含零，不能声称稳定加速。独立 SingBoxOutbound 未修改，其结果只用于回退观察。全部原始分布、p95、分配量和 CI 保留在 `comparison.json`，32 项与改动的对应关系见 `agents/proxy/review/microbench-v2/coverage.md`。

耗时共 20 项稳定改善、4 项满足辅助 5% 回退检查但无稳定改善、8 项不确定。后 8 项为 100 条 ListByTag/DAOList，以及 1,000 和 10,000 条 List/ListByTag/DAOList。尤其 10,000 条 List 中位数从 10.508 ms 增至 11.901 ms（+13.25%，CI [-4.86%, +19.26%]）；该结果既不能证明稳定退化，也不能认定无超过 5% 退化。全部 64 项分配指标满足辅助检查，不能据此替代耗时结论。后续仍需实际用户流程及资源矩阵；不确定项不能计作通过。

针对这 8 项的只读审阅确认，`List/ListByTag` 源文件逐字一致，`DAOList` 的 SQL、扫描和 JSON 解码没有改变；新增单条写入及分组计数不进入这些定时读取路径。类型、快捷码缓存、SQLite 参数、依赖及夹具也一致。尚未找到应直接修改的读取算法缺陷，但相同源码不能证明最终性能没有回退。全部 8 项仍保留为不确定，可能的前序写入工作量、堆状态和顺序影响尚未量化。审阅及尚未执行的完整反序追加方案见 `agents/database/microbench-read-path-review.md`。

此轮采用先完整 before、再完整 after 的固定顺序，数据库仍位于 tmpfs，没有随机交叉排除时段漂移。父级两侧均做轻量进度读取；after 初期因保存更快而产生更多 stdout，完整日志读取成本高于 before，随后改为只读末尾 8 KiB。因此不以此轮归因微小差异。该对照只作为局部收益与回退观察，不是完整 `measure`，`auxiliary=true`、`improvement_eligible=false`、`goal_pass=false`；不能单独满足关键用户流程改善或整体 5% 门槛。

原入口 `agents/proxy/formal-microbench/runs/run1/` 在 benchmark 开始前因错误要求 Go 回显 `GOENV=off` 而失败。Go 1.24.13 实际将禁用配置文件回显为空字符串；v2 同时严格检查进程 `GOENV=off` 和回显空串，19 项校准通过，原目录及失败输出未覆盖。源码、8 份 Go 夹具、32 叶和命令登记保持相同。

## 测量方法和证据完整性

正式登记见 `artifacts/final-measurement-registration-v2.json`，共 192 个指标。目录规模为 100/1,000/10,000；运行并发为 0/1/5/10。短操作每侧 30 样本，启动和常驻资源每侧 10 个独立轮次，资源每轮预热 15 秒后采样 30 秒。固定数据、Go/Wails/Chromium、构建参数和观察入口在两侧相同。

实际 GUI 由 Wails/WebKitGTK 运行。测试入口仅注入隔离构建，使用权限为 0600 的 Unix socket；普通产品构建没有该入口。每个实例操作点击真实行按钮，CDP 另行验证页面 URL、标题和正文。列表检查完整 ID/名称集合后等待两个绘制帧。关键字保存检查 UI、后端、SQLite 和重启读回；取消和故障提示放在计时循环外。

合成实例初始化正常产品默认的 `--disable-sync`、`--no-first-run`。早期空参数夹具触发 Chromium 首次运行确认窗口，已校正两侧夹具；没有关闭沙箱或 TLS。重启前端会保留搜索条件，夹具通过真实搜索框清空后验证完整目录。

每侧还验证 70 份应用生命周期文件，共 73 次启动（含三次持久化重启）。采样纳入精确引用本轮 runtime 路径且同 UID 的重挂父进程；资源读取缺失不能视为零。所有退出均要求状态 0、无强制清理、无残留存活或僵尸进程。信号通过 pidfd 绑定并复核启动身份。正式比较会重新校验原始生命周期记录。0/1/5/10 实例的诊断校准均通过，识别的 32 个 crashpad 和全部 230 个受控进程身份在结束后消失。

首次正式采样在 10,000 条基线启动阶段主动中止，用于补齐退出码与重挂父进程证据；该次结果状态为 failed、没有完成封印，不能用于验收。新的两侧测量均使用 `fixture-v2` 封存的相同工具，SHA-256 为 `b9ee6c4e87dab966f30a960093491cf45e0e39dd743867bc5c2f696ed863666f`。

第二次基线（`evidence/run2/before`）也为 failed，没有完成封印，after 未启动。10,000 条初次显示记录为 946,132.7 ms；仅完成 4 对筛选/恢复，恢复耗时依次约 23.8、24.1、25.0、387.0 秒。随后约 30 分钟未收到操作结果，正常关闭也超时，最终退出 -9；所有记录的受控进程已清理。原始操作异常被 finally 中的关闭异常覆盖，因此超时阶段仍需诊断，不能把推断当作已证实根因。独立事件阶段插桩不计入正式性能数据；正式工具另增加逐操作日志和完整异常链保留，86 项校准通过。后续正式比较须使用相同的最终夹具重新产生有效两侧证据，不能复用此失败结果。

独立诊断 `qd-large/` 使用冻结 baseline 和相同 seed，仅增加阶段记录。首次目录显示为 971,043.1 ms，后台明确记录 10,000 条绑定补齐及保存完成。已完成操作中的输入节点始终有效，DOM 值、持久化筛选值和结果集合正确，未发现旧输入引用导致的失效。取得下述阶段证据后，于 2026-11-10 03:23 UTC 人工中止该诊断以更换观察方法；不是自然操作超时，更不是完成的性能证据。正常关闭超时后应用退出 -9；全部 6 个受控应用身份、driver 及 Xvfb 均已消失。完整人工中止、异常链和清理记录保留。部分阶段如下：

| 诊断操作 | 已确认阶段 |
| --- | --- |
| 前三次恢复完整列表 | 同步 input dispatch 分别 23.751、24.417、24.168 秒，之后谓词与两帧均完成 |
| 第四次恢复完整列表 | 同步 dispatch 26.533 秒；内容已满足后的第二帧另等 60.798 秒 |
| 第五次筛选 | 提交至 JS 开始约 291.119 秒；JS 开始后 3.442 秒完成，其中同步 dispatch 2.532 秒；阶段回传约 1–2 ms |
| 第五次恢复完整列表 | 内容已满足后，第一帧等待 454.592 秒；该片段跨越下述显示器诊断介入，不作标准性能样本 |

第五次筛选的时间戳证明长延迟发生在 JS 代码开始之前，尚不能细分为 GTK 派发、WebKit 执行队列或此前正在运行的产品长任务。Wails 的后台事件与 observer 的 ExecJS 共用出站派发路径；后端事件接收顺序可能与 JS 顺序不同，诊断按相同 `performance.timeOrigin` 的 JS 时钟解释。正式夹具的筛选耗时从 JS 内 dispatch 前起算，入口排队仅在驱动日志和超时中体现；因此不能把该数字等同于完整的宿主输入到界面响应时间。后续原生输入诊断用于判断是否存在可修复的测量入口偏差。

原生输入的 100 条校准 `np-small-2/` 已通过：预先安装一次观察器，再通过专属 Xvfb 上的真实 XTest 键盘粘贴及清空；每次操作不再经 ExecJS 发出。两次操作均有恰好一次可信 `beforeinput/input`，中文粘贴完整，分别得到精确的 1/100 条 ID 集合，受控输入值、localStorage、焦点及两帧检查一致。22 个 JS 阶段序号连续，16 条外部命令退出 0 且已回收；应用及 Xvfb 正常退出，剪贴板 helper 受控 TERM 后 owner 清零，全部进程身份消失。输入至渲染观察值为 36/196 ms，仅用于正向校准，不能推导大目录或正式性能收益。父级复核见 `artifacts/np-small-2-parent-review.json`。第一次校准 `np-small/` 在应用和输入启动前，因错误要求 Xvfb `-displayfd` 模式产生锁文件而失败；新版本改用 Unix socket `SO_PEERCRED` 核实 Xvfb PID/UID 并复核启动身份，原失败记录保留。

10,000 条原生输入诊断 `np-large/` 于 2026-11-10 04:12:24 UTC 开始，使用与小校准相同的冻结 runner/observer、原 baseline 二进制及 seed，登记 6 对操作和原有 1,800 秒单次截止。一次安装已正向回传，后续输入无需逐次 ExecJS。目录就绪的 JS 观测值为 948.814 秒；最终只完成三对操作，第四次筛选已由 XTest 发送，但直到截止都未收到该 token 的任何页面 keydown/paste/input 阶段。05:04:34 UTC 驱动以状态 1 结束，结果明确为 diagnostic/failed，没有人工中断或追加输入。已完成的输入至两帧结果如下，均为诊断值而非正式性能样本：

| 操作序号 | 筛选至完成 | 恢复完整列表至完成 |
| --- | --- | --- |
| 1 | 1.895 秒 | 26.112 秒 |
| 2 | 2.253 秒 | 26.477 秒 |
| 3 | 2.500 秒 | 276.163 秒 |

前三次筛选从宿主发送命令至最早可信 keydown 分别约 8.553、8.712、11.068 秒；同一事件的 `performance.now() - event.timeStamp` 分别约 8.550、8.709、11.067 秒，而 helper 均约 32 ms 退出、阶段回传通常约 1–5 ms（完整记录也有约 9–12 ms 值）。因此延迟发生在事件产生后、页面监听器执行前，仍不能细分 GTK/WebKit 内部派发和具体产品长任务。第三次恢复在内容满足后，两个帧回调分别另等约 51.462 和 196.349 秒。04:41:47 UTC 的线程快照显示 WebKit 主线程累计 CPU 占主导，其余线程近乎空闲；未取得函数调用栈，不据此指定某个 JavaScript 函数为根因。完整只读快照保存于 `artifacts/np-large-monitor.jsonl` 和 `np-large-webkit-threads.jsonl`。

第四次筛选的 helper 33.241 ms 后退出 0，发送到进入清理的 monotonic 时间差为 1,800.196 秒。主要异常明确是 `Native stage timeout without retry: ...:3:filter/native_paste`，原始 traceback 完整保留；正常关闭随后另行超时，应用最终退出 -9，`cleanup_errors` 单独记录，没有覆盖输入超时。剪贴板 helper 在关闭前存活且 owner 正确，受控 TERM 后退出 -15、owner 清零；Xvfb 退出 0 且已回收。31 条外部命令均退出 0 并已回收；58 个 native 阶段序号连续，六个完成结果的可信输入、计数和完整 ID 集合均复核。父级检查全部 41 个 PID/start 身份已消失，包含应用、WebKit、早退子进程、driver、wrapper、剪贴板、显示器及命令 helper；无存活或僵尸残留。复核见 `artifacts/np-large-parent-review.json`。

前端代理的独立最终审阅见 `agents/frontend/native-input-probe/np-large-review.json` 和同名 `.md`：50 项证据一致性核查满足，仍明确 `diagnostic failed`、`formal_pass=false`，不把记录一致性当作性能通过。父级另对同 UID、命令行引用整个隔离工作根目录的进程做只读检查，无匹配残留；范围和排除当前审阅进程祖先的规则见 `artifacts/work-root-process-review.json`。全部三个子代理的结果均已收集，未留下运行中的构建、测试或性能任务。

该轮 WebKit 的 `/proc` 高水位 RSS 达约 17.13 GiB，属于失败诊断的进程观测值，不能充当正式资源对比。04:56:12 UTC 的主机快照仍有约 25.5 GiB 可用内存、swap 未用，memory PSI 的 10/60/300 秒均为零；只能说明该时点未见主机内存压力，不能排除其他阶段的影响。见 `artifacts/np-large-host-memory.jsonl`。

运行 678.9 秒时只读复核专属 Xvfb 的 PID/start/UID、父 driver 和 Unix socket `SO_PEERCRED`：`xset q` 退出 0、timeout=0，XScreenSaverQueryInfo 返回 state=3（Disabled）、idle=678598 ms、since=0；没有重置 idle、显示或输入。证据为 `agents/frontend/native-input-probe/np-large-display-review.json`。原生输入且从启动起关闭屏保的诊断仍出现长时间排队及帧延迟，因而不能把旧 run2 的全部延迟归因于逐次 ExecJS 或屏保；尚未证明根因。

还发现当前独占 Xvfb 默认屏保在 600 秒后激活。03:08:43 UTC 的 XScreenSaverQueryInfo 确认 state=On；03:09:25 对已验证属于该 driver 的 `:1` 执行 `s off`、`s reset`，随后实际状态为 Disabled。期间没有修改输入、关闭产品后台任务或调整超时。第一帧约两分钟后才返回，不构成屏保立即解除卡顿的证据，不能将其认定为主因。介入的命令、退出码、时间及前后阶段见 `artifacts/qd-large-display-intervention.json` 和 `qd-large-xscreensaver.jsonl`。

统计使用固定种子 1729 的独立中位数 bootstrap，重采样 4,000 次，报告中位数、p95、变化率和 95% 置信区间。只有两侧中位数均接近零时，CPU 使用两个内核计时 tick、PSS 使用 4 KiB 页的绝对下限；其他核心指标保持 5% 回退界限。不确定结果不能通过。

代理 CPU 指标描述一次冷启动、首次请求及回收的生命周期。父进程指标使用 RUSAGE_SELF，包含夹具内 Go 运行时、本地 HTTP 服务和读取 PSS 的开销；内核指标只含当前桥接 run 进程。Xray `run -test` 和 sing-box `check` 的独立预检 CPU 未计入，这两处实现前后相同，因此不能把两项指标之和称为完整进程树总启动 CPU，也不能推断固定时长持续代理负载的成本。各内核的上游夹具位置不同，父进程 CPU 不适宜在内核之间直接横比。这里 timeval 的微秒量化与运行矩阵 /proc 的 tick 分辨率不同；维持已登记门槛。

32 个 Go 微基准各执行 10 次，作为时间和分配的辅助证据，不计入关键用户流程改善。验证器要求显式登记全部叶子基准和包完成记录；支持 Go 在基准名前缀与数字结果间插入产品日志的格式。Go JSON 按 Package/Test 拼接分片，要求完整生命周期、指定测试、准确报告和样本量。

验证器校准包括真实失败、非零退出、无害 FAIL/error 文本、分片、截断、缺失/过期/空证据、零执行、样本错误、明确改善/退化和置信区间不确定；最新 89 项全部通过。新增逐操作日志、异常链保留与分类，分别记录原始操作及清理错误，多重错误不推断唯一根因。`compare` 还要求来源、场景、命令、检查项、内核哈希一致，并校验原始文件与报告封印。

## 功能验证

最终产品的功能生产者日志保存在 `functional-v3/`，普通/race、前端、桥接及普通 Wails 构建均对应当前 20 文件产品版本。产品输入 manifest 共 870 项，SHA-256 为 `a6bff362fb74147a6c61ca72dd64a9d3d2d10360836b4148b03f22b6fe75f991`；验证前后及当前主区/候选一致。较早的 `functional/` 和 `functional-final/` 仅记录历史版本。

| 检查 | 当前结果 |
| --- | --- |
| `git diff --check` | 退出 0 |
| `npm ci`、`npm run build:clean` | 退出 0，前端源码与最终版本相同 |
| 前端实际 store/selector 测试 | 11/11 通过 |
| 实际 React/WebKit 订阅检查 | 9/9 通过，无 React 错误；测试窗口退出 0，无强制退出和残留进程 |
| Go 全量 `-count=1 -json -tags webkit2_41 ./...` | 退出 0；683 个必需根测试通过，993 次测试/子测试 pass 事件，23 个包完成 |
| Go race 全量 | 退出 0；同一组必需测试全部通过，993 次 pass 事件，21 个包完成 |
| 启动闸门 race `-count=10` | 两个必需根测试各 10 次通过 |
| `TestVirtualGroupBindingStartChainWithBridgeMember` | 实际执行并通过 |
| 仓库 Python 规则测试 | 12/12 通过 |
| 性能验证器 Python 校准 | 最新 89/89 通过（`tooling-v5/`）；产品完整复验时的 86/86 也通过 |
| Wails Linux amd64 普通产品构建 | 退出 0，生成 ELF64；构建未改变登记源码 |

Go 全量两次均有相同的 7 个跳过：原有三项缺少测试用 Windows Mieru 可执行文件的检查、一项仅适用 Windows 的 Mieru 下载检查、一项外部 JS/YAML 样本检查、一项真实用户数据检查，以及仅供受控子进程调用的单连接测试入口。最后一项由父测试实际启动，两个完整运行均记录了精确的 10 case 完成 JSON，另有缺失/截断/零执行反例校准；没有用默认 skip 充当成功。完整身份清单、命令和汇总见 `functional-v3/required-tests.json`、`commands.json`、`verified-summary.json`。

完整产品复验的输入 manifest 不包含 Python 夹具及外置验证器，因此不能将产品 hash 扩大解释为所有验证输入的历史稳定性证明。独立审阅确认本轮日志、实际执行项和当前主区/候选夹具相符；后续 `tooling-v4/` 另保存 Python 性能夹具、规则测试文件及外置验证器的 49 项输入清单，并在所列文件前后 hash 一致的条件下重新执行 89 项性能校准及 12 项仓库规则测试。规则测试间接调用的 `scripts/resolve-release-version.py`、`scripts/sync-upstream.sh` 不在该清单内，当前主区/候选两者一致；不能扩大为所有间接依赖的前后 hash 证明。外置 Go 验证器的当前 hash 用于审阅，不伪称补录历史 hash。

真实订阅检查见 `qd-small/results.json`：把当前生产 selector 的独立 React 测试树注入真实 Wails/WebKitGTK，验证无关/相关结果、缺失结果到达、模式切换、时间推进、失败结果、剪裁和重置。它证明该 hook 在实际 WebKit 中的行为，不替代完整候选页面及运行矩阵性能验收。该轮同时验证了阶段诊断日志的正向链路。

屏保诊断后，当前测试工具的 Xvfb 启动参数增加 `-s 0`，仅作用于新的专属测试显示器，产品代码不变。`tooling-v5/` 在 53 项输入前后 hash 一致条件下再次执行 89 项性能校准和 12 项仓库规则测试，补充登记上述两项间接脚本依赖。`np-small-2/` 已验证该参数下专属显示器可以正常启动、输入并退出；短校准不能证明长时间卡顿已解决。正式比较须使用重新封存的相同夹具重跑两侧，不与旧 `fixture-v2` 混用。

正式性能对照因基线失败尚未完成。独立 32 项微基准已完成，部分辅助耗时检查仍不确定；有效完整 GUI/资源对照与所有核心回退检查仍待完成，不能据上述功能通过声称性能 Goal 已完成。原生输入自然超时后，已按目标中的“必需的 GUI、真实实例、代理、构建或性能验证无法可靠完成”停止继续正式测量，待用户决定是否保留现有严格标准并更换量化环境，或明确修订 10,000 条场景的验收方式。旧目标及全部失败记录保持原样。

## 复现

```sh
export GOTOOLCHAIN=go1.24.13
export PERF_WORK_ROOT=/tmp/ant-linux-perf-cntr19l1
export PERF_BASELINE_ROOT="$PERF_WORK_ROOT/baseline"
export PERF_CANDIDATE_ROOT="$PERF_WORK_ROOT/candidate"
export PERF_EVIDENCE_ROOT="$PERF_WORK_ROOT/evidence/run2"
export LINUXPERF_MIHOMO_BINARY="$PERF_WORK_ROOT/agents/proxy/tools/mihomo-v1.19.27"
python3 scripts/perf/linux_perf.py measure --root "$PERF_BASELINE_ROOT" --output "$PERF_EVIDENCE_ROOT/before"
python3 scripts/perf/linux_perf.py measure --root "$PERF_CANDIDATE_ROOT" --output "$PERF_EVIDENCE_ROOT/after"
python3 scripts/perf/linux_perf.py compare --before "$PERF_EVIDENCE_ROOT/before" --after "$PERF_EVIDENCE_ROOT/after" --output "$PERF_EVIDENCE_ROOT/comparison"
python3 -m unittest discover -s scripts/perf/tests -v
```

重跑时需要全新的输出目录。保留同一份冻结源码和夹具，不复用旧日志作为新证据。

## 已知限制

结论只适用于记录的 Linux amd64 主机、本机 Chromium、本地页面和合成数据，不能推广到指纹内核、公网代理、其他架构或操作系统。没有改变公开 API、数据库表、更新源或发行版本。

审阅还记录两项原有的非性能行为风险：分组操作只更新数据库时，同目标实例的旧内存对象随后整行保存仍可能覆盖其新分组；`snapshot.ZipDir` 仍使用未向调用者返回关闭错误的 deferred Close。这些不是本次性能差异引入，也不能宣称现有异常夹具已经覆盖所有关闭错误。详见 `agents/database/integration-review.md` 和 I/O 审阅记录。

本次所有运行状态均在标记的临时目录内；本次大目录诊断的受控进程已全部退出，临时目录和合成数据库随原始日志保留供失败复核。源码、补丁、日志及统计结果均未删除，真实用户数据及运行时内核未改。

完整工作证据目录另已归档至持久存储 `/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-09-linux-performance/evidence.tar.zst`，避免依赖 `/tmp` 的跨重启保留。归档没有排除项，包含 107,896 个普通文件，压缩后 1,058,814,593 字节；SHA-256 为 `a73cb33f8dc3add06f23e970613ff3cbb361f7c7a5975e6b034a51c1fb8b376b`。创建、`zstd --test` 和 `tar --compare` 均退出 0，归档前后完整源目录清单一致，原目录未删除。命令、日志和清单位于归档旁；最终报告、批准目标和可逆补丁也单独保存于该目录，并附文件哈希清单。
