# Goal: Ant-Browser Linux 全面性能优化 v2

## Goal Mode Objective

沿用本线程现有 Goal，依据 `/home/czc/projects/working/Ant-Browser/.codex/goals/2026-11-10-linux-performance-v2.md` 完成 Linux 全面性能优化、性能对照和功能回归；全部必需验收通过后才能完成，触发停止条件时说明证据并请用户决定。

## Full Prompt

### Objective

交付运行更高效的 Linux Ant-Browser：完成七领域评估和优化候选处置，至少一条关键用户流程取得可信的稳定改善，其余可对比核心指标没有超过 5% 的退化，功能、安全、可靠性和新版 10,000 条目录验收均通过。

### Context

- 项目为 `/home/czc/projects/working/Ant-Browser`，原始 HEAD 为 `fbd43230abd6cefec206a46073c1d6853746a2b2`，工作区包含应保留的既有 Linux 修复和本轮性能改动。
- 本文件是原目标 `/home/czc/projects/working/Ant-Browser/.codex/goals/2026-11-09-linux-performance.md` 的后继版本。原目标 SHA-256 为 `6d64e45f1624a5f79b3b00376a99f4349f486a9d1985a7ae4e22a8a5b1337f72`，保持只读。本文件明确修改的条款优先，其余原目标全文继续适用。
- 原始基线位于 `/tmp/ant-linux-perf-cntr19l1/baseline`，产品源码哈希为 `b62741030294e2481b33c4350d6bfd72187e6f05fa66237c924a5e8c75785389`，不能用优化后的代码替换基线。
- 当前候选位于 `/tmp/ant-linux-perf-cntr19l1/candidate`，870 项产品输入哈希为 `a6bff362fb74147a6c61ca72dd64a9d3d2d10360836b4148b03f22b6fe75f991`。当前功能回归通过，但完整性能目标未完成，8 项辅助读取耗时仍不确定。
- 旧版 10,000 条正式 GUI 基线失败；原生输入诊断也在第四次筛选等待 1,800 秒后自然超时，只完成 6/12 个操作。主要异常、强制退出和清理结果均保留，不能改写为通过。
- 原始证据已完整归档至 `/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-09-linux-performance/evidence.tar.zst`，SHA-256 为 `a73cb33f8dc3add06f23e970613ff3cbb361f7c7a5975e6b034a51c1fb8b376b`。

### Approved Direction

用户已选择选项 1：保留旧版 10,000 条目录超时作为压力测试结果，继续严格量化 100/1,000 条目录及 0/1/5/10 个真实实例，单独验收新版 10,000 条目录的完成性与资源表现。

仅改变 10,000 条目录场景的双侧 GUI 验收方式。新版大目录仍需完整业务检查和规定样本；100/1,000 条、真实实例并发、代理、文件 I/O 和已登记 Go 微基准的范围保留。不得借此跳过其他失败或不确定项。

沿用本线程现有 Goal 执行，不创建重复 Goal，不把旧目标标记完成以腾出槽位。批准保存并继续后，本后继文件成为同一任务的当前验收依据，原文件和原报告作为历史记录保留。

### Discovery Decisions

- Answered：仅 Linux；全面优化；0/1/5/10 真实实例；本次大目录验收修订方向；保留用户改动、接口、数据兼容和代理栈规则。
- Defaulted：保持同一 Linux amd64 主机、Go 1.24.13、Wails 2.12.0、固定 Chromium 和代理内核，使用合成数据及本地服务；子代理最大可用并行，继承父代理模型与推理设置。
- Unresolved -> 沿用原约束：新版大目录是否能稳定完成尚未证实；仍使用原 1,800 秒单操作观察上限，不延长超时，报告真实延迟和资源数值，不把完成性通过等同于交互流畅。
- Unresolved -> 停止条件：正式输入方法无法校准、候选大目录无法通过、核心指标持续不确定，或需要超出原范围的改动。
- Not applicable：没有指定 token 预算；不设置预算。Skipped：无。

### Scope

继续覆盖原目标的全部七个领域：应用及实例启动、实例管理、前端交互、代理操作、数据库与持久化、后台资源、文件 I/O。每项候选须有实现结果或有证据的处置理由。

允许保持业务语义的内部重构、批处理、查询、缓存及并发优化，以及必要的隔离性能夹具、行为回归和文档。保持状态新鲜度、取消、错误反馈、实例隔离和持久化可靠性。

继续遵守 AGENTS.md：Xray + sing-box 组合栈与独立 Mihomo 栈分别执行；启动、测速、连通性、IP 健康、预热及代理下载均不得跨栈自动回退。保留 fork 更新源及发布规则。

### Out Of Scope

Windows/macOS 优化、arm64 实机量化、主要框架替换、依赖整体升级、重编第三方内核、界面重设计、新产品功能、公开 API 破坏性变化、破坏性迁移、真实用户数据与凭据操作、关闭安全校验，以及提交、推送、PR、合并、改版本号、打标签和发布，均维持原范围外约束。

### Verification

#### 基线、输入和计时

- 保留原始源码基线、所有既有失败和不确定结果。继续使用现有隔离工作根，但每轮新建唯一证据目录；不得复用旧输出伪装成新运行。
- 测量前固定场景角色、输入种子、源码和夹具哈希、工具链、二进制、命令、指标及采样量。可对比场景两侧必须一致；修复测量方法后，两侧重新运行受影响场景。
- GUI 使用真实 Wails/WebKitGTK。正式交互计时包含宿主动作发出至业务结果满足、两帧完成并收到结果的全过程，不能排除入口排队。原生输入或其他入口须先做正反例校准，两侧采用相同方法。
- 测量期间停止同批次其他构建、race、压测和高负载代理任务；保存主机负载。仅使用临时数据库、合成配置、本地页面和测试代理，不关闭沙箱或 TLS。

#### A. 必须完成的双侧量化

- 100 和 1,000 条实例/代理目录：加载、实例及代理筛选与恢复、关键字保存、适用的批量操作，以及配置/分组/代理 API；检查精确完整结果和持久化。
- 0、1、5、10 个真实 Chromium：应用就绪、实例启动到可用、状态更新、停止和回收。真实页面内容、UI 和 CDP 必须一致。
- Xray、sing-box、Mihomo 的本地适用路径，以及固定下载/归档样本：保持真实流量、内容哈希、取消、错误反馈和资源回收检查。
- 原登记的 32 个 Go 基准叶子和 100/1,000/10,000 数据规模保留，每侧每叶至少 10 次；只作为辅助证据。原 8 项读取耗时不确定结果须继续调查、补采并给出处置，不得标记为已通过。
- 短操作每侧至少 30 个有效样本；启动及常驻资源每侧至少 10 个独立轮次，每轮预热至少 15 秒、采样至少 30 秒。增加样本前登记追加方法，保留全部样本，不能择优。
- 核心指标为用户流程延迟、同负载 CPU 和 PSS；同时报告 p95、RSS、分配量、进程、goroutine、FD 等辅助指标。采用固定种子 1729、4,000 次中位数 bootstrap 和 95% CI；近零指标沿用事前标定的绝对噪声下限。
- 至少一条预登记、可双侧对比的关键用户流程改善 CI 排除零。其余核心指标的退化不得超过 5%；CI 跨越门槛时增加样本，仍不确定则不能通过。函数微基准不能单独满足关键用户流程改善。

#### B. 10,000 条目录的明确例外和新版验收

- 旧版目录的既有失败作为独立压力证据保留，记录输入、二进制、阶段、超时、强制退出和资源观测。该分支不再要求旧版产生 30 个成功交互样本或完整对照分布。
- 该例外仅适用于旧版 10,000 条目录分支；不得把整个 before 运行、其他规模或任何缺失结果一并豁免。
- 新版仍须实际运行 10,000 条实例及代理目录：首次完整显示至少 10 个独立轮次；A 中对应的短操作每项至少 30 个有效样本。每次操作均须在原 1,800 秒观察上限内完成，超时、崩溃、错误结果或强制清理均不能通过。
- 完整检查列表/筛选 ID 与名称、事件刷新、输入值、保存、SQLite、重启读回、取消和错误提示。保留真实 GUI 和完整业务结果，不以模拟界面、少量抽样行或只发出请求替代。
- 新版大目录资源至少 10 个独立轮次，每轮预热 15 秒并采样 30 秒，记录应用/WebView 等相关进程的 CPU、PSS、RSS、FD、goroutine、进程数及运行中的可观测内存峰值。每轮须正常退出并回收全部受控身份，无存活或僵尸残留。
- 单独报告新版大目录延迟中位数、p95 和资源分布；旧版未完成的指标标记为无法取得可比基线，不填零、不虚构百分比或 CI，不将其算作已证明改善或通过 5% 对照检查。
- 本条明确免除旧版大目录缺失指标的数值比较；A 中所有核心指标继续满足原 5% 门槛。新版大目录完成性及正确性仍是 Goal 完成的必要条件。

#### 自动化命令和证据有效性

保留并实际满足以下检查，保存完整输出、退出状态、目标和发现/执行清单：

1. `git diff --check`。
2. `(cd frontend && npm ci && npm run build:clean)`。
3. `go test -count=1 -json -tags webkit2_41 ./...`。
4. `go test -race -count=1 -json -tags webkit2_41 ./backend/... ./internal/...`。
5. `go test -race -count=10 -json -tags webkit2_41 ./backend -run '^TestInstanceLaunchGate(LimitsAndQueues|Guards)$'`。
6. `go test -count=1 -json -tags webkit2_41 ./backend -run '^TestVirtualGroupBindingStartChainWithBridgeMember$'`。
7. `python3 -m unittest discover -s scripts/tests -v`。
8. `go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 build -s -platform linux/amd64 -tags webkit2_41 -o ant-chrome`。
9. `go test -run '^$' -bench '^BenchmarkLinuxPerf' -benchmem -count=10 -tags webkit2_41 ./backend/... ./internal/...`。
10. 已登记的前端 store/selector 和真实 React/WebKit 行为检查，以及 `python3 -m unittest discover -s scripts/perf/tests -v`。

完善 `scripts/perf/linux_perf.py`，保留原接口，并为新版矩阵增加明确的 baseline/candidate 角色；新版复现命令为：

    python3 scripts/perf/linux_perf.py measure --role baseline --root "$PERF_BASELINE_ROOT" --output "$PERF_EVIDENCE_ROOT/before"
    python3 scripts/perf/linux_perf.py measure --role candidate --root "$PERF_CANDIDATE_ROOT" --output "$PERF_EVIDENCE_ROOT/after"
    python3 scripts/perf/linux_perf.py compare --before "$PERF_EVIDENCE_ROOT/before" --after "$PERF_EVIDENCE_ROOT/after" --output "$PERF_EVIDENCE_ROOT/comparison"

baseline 角色完成 A 并附带经核验的旧版大目录压力证据；candidate 角色完成 A 和 B。比较器只对 A 做严格双侧比较，并独立验证 B 的全部正向要求；角色差异必须事前登记，不能把任意缺失数据当作例外。

验证器须识别真实失败、无害 FAIL/error 文本、缺失必需用例、零执行、过期/截断/目标不匹配、样本不足、明确改善、明确退化和不确定结果；补充大目录压力例外有效/伪造/错误目标，以及候选大目录失败的反例。校准必须包含可执行断言，保持主要错误与清理错误。

既有成功证据只有在完整输入、工具链、命令、目标、样本及封印证明适用于当前版本时才可复用；变化影响的检查必须重跑。旧 GUI 失败和单轮诊断不能作为新正式样本。必需测试不得跳过；原有非验收的私有数据、外部样本和其他平台可选项仍逐项记录。

每个保留的性能改动必须有匹配的正确性及收益证据。复核缓存失效、并发、取消、故障重试、保存重启一致性、真实进程回收及原有 Linux 修复；普通产品构建须生成有效 amd64 ELF，且不包含测试观察入口。

### Risks And Rollout

保留原目标的风险控制和回退规则，以本地工作区差异交付。新版报告保存至 `/home/czc/projects/working/Ant-Browser/.codex/reports/2026-11-10-linux-performance-v2.md`，列明七领域处置、所有收益和不确定项、例外的精确范围、实际命令、来源、指标、限制及未完成项；原报告不改写为成功。

分别说明 tmpfs 数据库/文件测量、Linux amd64、本机 Chromium 和本地服务的适用边界。完成后清理受控进程，保留原始基线、可逆补丁、日志、统计和失败现场，并将新增证据持久归档。

### Stop Conditions

- 需要破坏接口、数据兼容、安全、状态新鲜度、错误反馈、持久化可靠性或代理栈约束。
- 需要真实用户数据、凭据、远端权限、范围外发布或主要框架/内核改动。
- A 的核心指标出现无法在范围内消除的超过 5% 退化，或增加样本后仍无法可靠判断。
- A 中任一必需验证无法可靠完成，或新版 10,000 条目录的完成性、正确性、资源采样及正常回收无法通过。
- 无法取得关键用户流程的稳定收益，或必要优化超出原范围。
- 出现无法安全整合的新用户修改，或原始基线/证据来源无法核实。

已明确接受的旧版大目录失败本身不再重复触发停止条件；其他失败仍须处理。普通可修复的实现、测试和环境问题自主解决；真正触发停止条件时保留证据并请用户决定，不能继续降低标准。

### Subagent Options

- enabled: true；parallelism: max_available；model: inherit；reasoning_effort: inherit；fallback: serial_if_unavailable。
- 当前能力为父代理加子代理最多 4 个并发槽位。已有三个子代理均已完成，可复用；每次派发前重新检查状态和容量，仅派发独立且必要的任务。
- 使用 collaboration 的派发、消息、状态及等待能力；fork_turns=all，省略模型和推理覆盖。
- 父代理负责共享源码、清单、整合、Goal 状态和最终验收；子代理只能写各自隔离副本和产物，不改主工作区或目标文件。
- 所有子任务必须收集并复核。正式性能测量串行组织，避免并行高负载干扰；不能为填满槽位创造重复工作。

## Completion Rule

只有七领域及全部候选处置完成、A 的关键用户流程收益和所有核心回退检查通过、B 的新版大目录验收通过、全部必需功能/构建/验证器检查有效、所有代理结果已处理且报告和持久证据完整，才能将当前 Goal 标记完成。

明确保留旧版大目录失败和未取得可比数值的限制；不得声称全规模均已完成严格双侧比较或达到理论最优。完成时报告本目标绝对路径、主要改动、实测收益、验收结果和剩余限制。
