# Linux 性能优化 v2：交付与未通过项

**当前结论：已实现 Linux 性能改动并完成有效的完整双侧测量，但总体性能目标未完成。** 新版 10,000 条目录完成性及正确性通过；关键字保存与三种代理内核冷启动有稳定收益。目录固定补采后仍有 1 项明确回退、6 项核心指标不确定；目录外原有 17 项核心未通过项保留。没有放宽 5% 门槛，也没有把功能通过等同于性能通过。

当前停止依据为已批准 [v2 目标](../goals/2026-11-10-linux-performance-v2.md) 的 Stop Conditions：**“A 的核心指标出现无法在范围内消除的超过 5% 退化，或增加样本后仍无法可靠判断。”** 本次已经满足后半句；没有证明回退不可修复。停止进一步采样和产品修改，先交付证据，再请用户决定后续方向。Goal 未标记完成；所有历史失败和不确定样本均保留。

工作区保留本轮修改，可审阅或回退。没有提交、推送、改版本或发布。原有 Linux 修复及两套代理连接栈约束保留；没有把 Xray + sing-box 与独立 Mihomo 自动混用。

## 可直接审阅的交付

- [可逆性能补丁](../artifacts/performance-v2-only.patch)、[90 个变更路径](../artifacts/performance-v2-change-list.json)、[正向/反向预检查](../artifacts/performance-v2-patch-validation.json)。补丁相对任务开始时的用户工作区快照，含 27 个产品文件（后端 17、前端 10）及测试、测量工具，不把原有修复当作本轮成果。SHA-256 `a74b5fcd9ecc01d15baf5dab10b12af25fd0edc8928b62912ed3a270982b6a18`。
- [原始完整比较](../artifacts/linux-performance-v2/paired-comparison.json)、[追加后的目录比较](../artifacts/linux-performance-v2/catalog-supplement-comparison.json)、[追加登记](../artifacts/linux-performance-v2/catalog-supplement-registration.json)。补充报告明确 `goal_pass=false`，不会覆盖原比较。
- [功能验证](../artifacts/linux-performance-v2/functional-summary.json)、[完整功能命令](../artifacts/linux-performance-v2/functional-commands.json)、[最终输入核对](../artifacts/linux-performance-v2/final-input-checks.json)、[进程回收](../artifacts/linux-performance-v2/cleanup-review.md)。小型报告副本及来源哈希见 [manifest](../artifacts/linux-performance-v2/manifest.json)。
- 原执行报告已逐字保留为 [执行历史](../artifacts/linux-performance-v2/execution-history.md)，其中“正在运行／尚未应用”等表述是当时状态，以本报告当前结果为准。原 [11 月 9 日报告](2026-11-09-linux-performance.md) 与原目标保持历史记录。
- [全量持久归档](/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-10-linux-performance-v2/evidence.tar.zst) 已完成，完整性及 SHA-256 见末尾归档凭据。

## 实测收益与未通过结果

以下均为中位数。目录采用完整原样本加预先登记的 B1→A1 追加，每侧短操作 60 次、首显 20 次；三内核保留 run3 每侧 30 次。统计固定种子 1729、4,000 次 bootstrap、95% CI；没有改变阈值、绝对噪声下限或删除慢样本。

| 场景 | 原始基线 | 当前候选 | 变化 | 变化 95% CI |
| --- | ---: | ---: | ---: | --- |
| 1000 条关键字保存至界面可见（符合关键流程改善资格） | 216.432 ms | 144.937 ms | -33.03% | [-37.59%, -25.78%] |
| 1000 条首次完整显示（非关键改善资格指标） | 13348.728 ms | 3452.220 ms | -74.14% | [-74.97%, -73.20%] |
| mihomo 冷启动至本地代理可用 | 122.078 ms | 31.638 ms | -74.08% | [-74.13%, -74.04%] |
| sing-box 冷启动至本地代理可用 | 119.037 ms | 28.173 ms | -76.33% | [-76.48%, -76.05%] |
| xray 冷启动至本地代理可用 | 120.423 ms | 30.291 ms | -74.85% | [-74.95%, -74.71%] |

四条预登记关键流程取得稳定收益：1000 条关键字保存和三内核冷启动。正式 run3 原始关键字结果为 220.800→132.855 ms（−39.83%）；加入全部登记追加样本后为表中 −33.03%，最终引用合并结果。1000 条首显约减少 74.14%，但按登记不能单独满足关键流程改善要求。三内核是本地隔离流量路径，不能解释为公网延迟提升。

原完整 pair 共 182 项对比：154 pass、25 inconclusive、3 regression；其中 107 项核心为 **80 pass、24 inconclusive、3 regression**。两侧测量均 exit 0 且通过完整 raw/seal 验证；compare exit 1，driver 保持 failed。其三项原明确回退是 100 条实例恢复 +10.71%、1000 条代理 API +53.22%、零 Chromium 的 WebView CPU +17.07%。目录补采没有重写这份原结果。

完整目录家族的追加遵守相同冻结工具、产品、二进制和校验规则：每侧新增 20 项指标、560 个数值、20 个目录轮次、22 次应用启动、584 个宿主操作及完整业务/持久化检查。追加自身两侧均 complete；原 A0+A1、B0+B1 共 2,240 个数值全部参与合并。最终 20 项中 **13 pass、6 inconclusive、1 regression**，两项非核心均 pass。以下 7 项核心仍未通过：

| 目录指标 | 原始基线 | 当前候选 | 变化 | 变化 95% CI |
| --- | ---: | ---: | ---: | --- |
| 100 条实例恢复（不确定） | 189.716 ms | 203.096 ms | +7.05% | [+4.09%, +9.62%] |
| 100 条代理筛选（不确定） | 26.473 ms | 28.766 ms | +8.66% | [-3.19%, +14.63%] |
| 100 条代理恢复（不确定） | 111.644 ms | 116.350 ms | +4.22% | [+1.44%, +7.88%] |
| 100 条实例 API（不确定） | 2.980 ms | 2.505 ms | -15.91% | [-61.20%, +11.41%] |
| 100 条代理 API（不确定） | 2.971 ms | 2.984 ms | +0.44% | [-12.64%, +23.63%] |
| 1000 条实例 API（不确定） | 19.488 ms | 18.923 ms | -2.90% | [-10.09%, +9.12%] |
| 1000 条代理 API（明确回退） | 19.333 ms | 26.404 ms | +36.58% | [+7.26%, +53.82%] |

判定使用冻结比较器的差值 CI 与 5% 基线中位数限值；上表同时报告变化比例 CI。1000 条代理 API 的差值 CI 为 [+1.393,+9.694] ms，完整落在 0.967 ms 的允许增幅之外。100 条实例恢复原先明确回退，追加后区间跨越 5% 门槛，仍不能判为通过。部分中位数下降的 API 也因区间过宽而不确定。

目录外的 17 项核心未通过项全部保留，没有以目录补采代替：

| 范围 | 未通过指标 | 当前状态 |
| --- | --- | --- |
| 0 实例 | app CPU、WebView CPU | 前者不确定；后者 0.205→0.240 s／30 s，+17.07%，比例 CI [+9.09%,+36.84%]，明确回退 |
| 1 实例 | app/WebView/browser CPU、停止延迟 | 4 项不确定 |
| 5 实例 | 实例就绪、app CPU | 2 项不确定；就绪约 +6.54% |
| 10 实例 | 应用就绪、实例就绪、app CPU | 3 项不确定；两个就绪约 +6.69%／+6.13% |
| 代理 | Mihomo 冷启动内核 CPU、sing-box 冷启动内核 PSS | 2 项不确定 |
| 归档 | 不可压缩样本的压缩／解压 wall 与 CPU | 4 项不确定；本轮未改归档算法 |

sing-box 的 `cold_kernel_pss_kib` 是历史名称；最终比较单位实际为 bytes，已由原 KiB 乘 1024。对应中位数约 22.10→22.43 MiB，不能写成 GiB。所有运行矩阵的 PSS 核心比较均通过，不能据此声称所有 CPU 指标改善。

目录外完整家族补采工具已准备，但因目录追加触发停止条件，没有登记或采样。其代码、静态评审、校准和尚存缺口一并归档；不会把 prepared 工具当作性能证据，也不通过无限追加运行争取通过。[最终交接](../artifacts/linux-performance-v2/noncatalog-prepared-handoff.md) 记录 20 项惰性 Python 校准通过，但有效 Go 配置封印、异常进程回收、三核哈希独立回绑和真实生产者路径仍未验证，注册入口已经禁用。[前端静态审阅](../artifacts/linux-performance-v2/noncatalog-frontend-review.md) 与 [代理静态审阅](../artifacts/linux-performance-v2/noncatalog-proxy-review.md) 明确其不能验收。[最终目录原始证据核验](../artifacts/linux-performance-v2/catalog-final-verification.json) 则再次通过全部原 pair 和追加批次的来源检查，性能判定保持未通过。

## 10,000 条目录：完成但仍慢、内存占用高

新版 51 项独立指标的完成性和正确性全部通过：10 次独立首显、10 个预热 15 秒后采样至少 30 秒的资源窗口、每项 30 次短操作；逐条检查完整 ID/名称、事件刷新、输入、保存、SQLite、重启读回、取消和错误反馈。全部单操作在原 1,800 秒上限内完成，退出正常。

| 操作 | 中位数 | p95 | 最大值 |
| --- | ---: | ---: | ---: |
| 首次完整显示 | 20.656 s | 22.780 s | 22.944 s |
| 实例筛选 | 3.836 s | 21.455 s | 41.031 s |
| 实例恢复 | 19.574 s | 70.140 s | 83.605 s |
| 关键字保存至可见 | 0.168 s | 0.212 s | 15.262 s |
| 实例 API | 0.203 s | 0.269 s | 0.284 s |
| 分组 API | 0.009 s | 0.015 s | 0.022 s |
| 代理 API | 0.191 s | 0.241 s | 0.279 s |
| 代理筛选 | 1.489 s | 1.684 s | 145.746 s |
| 代理恢复 | 9.332 s | 9.566 s | 87.702 s |

初始实例页 WebView PSS 中位数 **5,314,343,424 bytes（约 4.95 GiB）**，30 秒窗口 CPU 中位数 10.595 s，PSS 窗口观测峰值最大 6,077,830,144 bytes。另一个只读诊断记录到长交互阶段某 WebKit 进程 VmHWM 为 **14,087,628 KiB（约 13.44 GiB）**；这是该进程历史 RSS 高水位，不是当前 PSS，也不足以证明内存泄漏。

旧版大目录没有有效配对完成分布，按用户批准的精确例外保留压力失败，不填零、不提供优化百分比或 5% 对照通过结论。新版完成性通过不等于交互流畅；上述慢尾和资源成本仍是实质限制。

## 七领域处置及各改动的证据边界

| 领域 | 本轮实现／处置 | 证据与限制 |
| --- | --- | --- |
| 应用与实例启动 | 同轮代理列表读取和首项索引复用；默认补齐只保存目标实例；解除扩展警告分支重入锁 | 绑定新鲜度、重复 ID 首项、错误、重开、锁回归通过；1000 条首显稳定改善。5/10 实例启动仍不确定，不能声称全部启动变快。锁修复属于有复现的终止性修复，不虚构正常延迟百分比。 |
| 实例管理 | 关键字、编辑、创建与复制使用目标行保存，写失败回滚内存 | SQLite 审计、拒写、并发及重开检查通过；关键字端到端改善明确。Create/Copy、一般编辑和启动保存没有各自独立耗时分布，不套用关键字百分比。 |
| 前端交互 | 精确 ID 索引；测速 hydration/监听上移；单结果订阅及不变引用；Code Modal 首次打开才挂载；代理表 3 个测速叶组件独立订阅；选项推导移入 3 个 Modal 子树；setTesting 空更新不通知 | 实际 store/selector、React/WebKit、完整 DOM、事件及表单回归通过。首显明显改善，但小目录恢复/筛选仍有不确定项。完整 DOM、15 秒刷新、30 秒新鲜度时钟保留；没有逐补丁消融。排序、available、虚拟组、首次开弹窗有正确性覆盖，没有专属性能分布。 |
| 代理操作 | 有界 readiness 重试；同次解析/选栈复用；普通独立 HTTP/HTTPS/SOCKS5 目标读取 | 三真实内核的本地就绪延迟稳定改善约 74%–76%，两套栈不混用。目标读取辅助耗时下降约 91%–94%，但每次仍扫描 N 个 ID，整轮仍 O(N²)，不能称整轮后台提速同幅。内核两项资源仍不确定。 |
| 数据库与持久化 | 单目标写入；SQLite 分组计数；不引入 TTL、破坏性迁移或无收益依据的新索引 | 分组 API 合并中位数约下降 73%–74%；是非关键改善资格指标。保存持久化完整通过。代理列表 API 明确回退，读取辅助结果部分仍不确定。 |
| 后台资源 | 2000 条内存日志环形缓冲；保留文件 flush、并发限制和健康检查周期 | 回绕、快照、Clear、过滤和 race 通过；局部分配量 292→24 B/op，不能等同 PSS。零实例 WebView CPU 回退仍在，未关闭后台任务规避。日志查询/竞争无独立性能量化。 |
| 文件 I/O | 评估后保留现有下载与归档算法、原有恢复/安全修复 | 原实现已有有界分片、流式缓冲、同时哈希；实际固定内容、错误、取消和清理检查通过。不可压缩归档 4 项仍不确定；本地 tmpfs/loopback 不代表实体磁盘或远程网络。 |

逐文件正确性及适用指标映射见 [后端映射](../artifacts/linux-performance-v2/backend-change-evidence.md) 和 [前端映射](../artifacts/linux-performance-v2/frontend-change-evidence.md)。两表是在 run3 运行期生成，其 pending 部分由本报告实测结论更新；限制说明仍适用。总流程收益来自整合候选对照，不能按补丁拆分归因。未量化分支不被虚构为已证明收益，整体 Goal 也未获通过。

原登记 32 个 Go 微基准叶均完成两侧每叶 10 次，run3 96 项结果为 91 pass、5 inconclusive、0 regression 标签；所有分配指标通过。5 项不确定耗时为 100 条 DAOList、1000 条 List、10000 条 VLESS/Xray 解析、AnyTLS/Xray 选栈、HTTP auth 解析。更早完整反序追加的 1,280 样本仍原样保存，其 9 项 List/ListByTag/DAOList 耗时不确定并未被新轮覆盖或择优改判。微基准不能单独满足用户流程改善条件。

## 回退调查：已确认和仍未知

- 100 条实例恢复的 JS 开始至业务 predicate 中位数从 163→182 ms；predicate 后帧等待约 13→14 ms，宿主额外成本基本相同。操作发生在页面 1.6–8.3 秒，早于 15/30 秒周期任务，不能用定时刷新解释这项回退。额外订阅挂载和布局尚无消融证据。
- 1000 条代理 API 包含 Wails 调用、完整结果检查和回传；该读取直接入口与 DAO 实现未改，JS 段约 15→21.5 ms，但没有 DAO／桥接／结果检查分段记录，不能断言是数据库变慢或纯噪声。
- 零实例 CPU 的大部分成本落在约页面 31.8/46.8 秒的两个采样桶，与周期任务时间接近；缺少 timer/React commit 因果跟踪。内容完整相等时复用 profile 引用是待验证候选，尚未实施，不能只按 updatedAt 比较或关闭时钟牺牲新鲜度。
- 5/10 实例就绪回退主要在开始至 running predicate 段，帧后和宿主附加成本近似；CDP 150 ms 轮询及 1200 ms 稳定窗口两侧相同，尚未确认根因。

三方独立审阅：[数据库与统计](../artifacts/linux-performance-v2/run3-database-review.md)、[前端原始回执](../artifacts/linux-performance-v2/run3-frontend-review.md)、[代理／运行／Go 原始证据](../artifacts/linux-performance-v2/run3-proxy-review.md)。[目录追加审阅](../artifacts/linux-performance-v2/catalog-review.md) 复核全部 2,240 个值、来源、中位数和 p95，没有用选择性重跑改变结论。

## 功能与工具校验

`functional-v4r1` 与交付产品 SHA 完全一致，13 个命令／26 份日志哈希最终再次核对通过。已实际执行：

```sh
git diff --check
(cd frontend && npm ci && npm run build:clean)
go test -count=1 -json -tags webkit2_41 ./...
go test -race -count=1 -json -tags webkit2_41 ./backend/... ./internal/...
go test -race -count=10 -json -tags webkit2_41 ./backend -run '^TestInstanceLaunchGate(LimitsAndQueues|Guards)$'
go test -count=1 -json -tags webkit2_41 ./backend -run '^TestVirtualGroupBindingStartChainWithBridgeMember$'
python3 -B -m unittest discover -s scripts/tests -v
go run github.com/wailsapp/wails/v2/cmd/wails@v2.12.0 build -s -platform linux/amd64 -tags webkit2_41 -o ant-chrome
go test -run '^$' -bench '^BenchmarkLinuxPerf' -benchmem -count=10 -tags webkit2_41 ./backend/... ./internal/...
(cd frontend && node --test scripts/test-proxy-speed-store.mjs scripts/test-profile-proxy-speed.mjs scripts/test-proxy-pool-speed.mjs)
python3 -B -m unittest discover -s scripts/perf/tests -v
node --test scripts/perf/tests/test_gui_fixture.cjs
```

Go 普通与 race 各覆盖全部 683 项必需测试、各 993 个通过事件；启动门限 2×10 与真实 bridge 检查通过。7 项登记的私有／外部样本／可选 child 入口 skip 明列于功能摘要，不算必需验收通过；child 的实际子进程用例由父测试核对完成标记。前端 store/selector 15/15，仓库 Python 12 项；最终测量工具 Python 269 项与 Node 10 项全部通过且零跳过。实际 React/WebKit 的 profile hook 9/9、lazy modal 8/8、代理表/选项 8/8；真实 Code 保存/取消/重复错误与重启检查通过。

普通产品构建是有效 Linux amd64 ELF，未注入性能观察入口：`candidate-v4r1/build/bin/ant-chrome`，SHA-256 `f3632bb3ccc0d9ba63a67d7e4b1cf86835a0b91138b75b5e27a7017da6ad954b`。正式测量使用隔离的 observer 构建，产物来源单独封印。后续只有测量方法修正，产品源码未变；受影响的 GUI 两侧均重新实测，269+10 工具校准重新执行，没有沿用失效 GUI 数据。

## 输入、方法与复现边界

| 输入 | 固定身份 |
| --- | --- |
| 原始 HEAD | `fbd43230abd6cefec206a46073c1d6853746a2b2` |
| 原始用户工作区基线，843 项 | `b62741030294e2481b33c4350d6bfd72187e6f05fa66237c924a5e8c75785389` |
| 当前产品，875 项 | `86c82c47b33344a15cba3aadf2c87ddf0701fccac27a0f4edc86910eb37aae19` |
| 冻结测量工具，37 文件 | `534f24b31c4048d32e6d8076947b6c81d3f2cd8c19f3a2bf3359c3529062744e` |
| 当前批准 Goal | `9d3cb85cfbdd0d52a76f2b519a99b7b47c44edc71b54d8727d07a02d19cfd571` |
| run3 before results | `a6544cf560725d2ac84dd6e64ecf2f21589196c7afbbaa26c2f8967e142a6301` |
| run3 after results | `b7224327a0a0fe9dae626bc9d741d31449bd23c6205505ab9112c8c45d85c8dd` |
| 目录追加 registration | `8f4abc41dc40ff67d08daf31f852356ff7f7912cc2dc6fc76672ccf908e1a00c` |
| 目录追加 comparison | `40eff6d958d81824a977512427c8a839ae0b33b89f4591d582a353d1b152800c` |

工作根 `/tmp/ant-linux-perf-cntr19l1`，原始基线 `baseline`，交付源副本 `candidate-v4r1`。Manjaro Linux amd64，同机 16 逻辑 CPU、64 GB RAM、Go 1.24.13、Wails 2.12.0、WebKitGTK 2.52.6／GTK 3.24.52、固定 Chromium 155.0.8059.79 与三代理内核。Chromium、内核、50 个前置证据、源文件及共享／冻结工具最终再次核对一致。

完整 run3 入口 `artifacts/run-v2-frame2-pair.py`，登记 `artifacts/run3-start-registration.json`；全新 before→after→compare，before 3,212.836 s、after 5,483.022 s。0/1/5/10 个真实 Chromium 每侧各 10 轮，预热 15 秒、采样至少 30 秒，UI/CDP/真实页面一致。GUI 包含宿主动作发出、入口排队、业务条件、两个严格递增的原生 rAF 帧及回传；全部等待计入原 deadline。采样期间没有并行构建/race/压测。

共享接口仍是 `scripts/perf/linux_perf.py measure --role baseline|candidate --root <隔离源码> --output <新目录>` 与 `compare --before <before> --after <after> --output <新目录>`。实际命令、环境、二进制及 fixture 在原登记／结果中完整保留，必须按其设置固定 Chromium、Mihomo 和旧版压力 reference；不能在原结果目录重跑覆盖。目录 supplement 入口 `agents/database/catalog-supplement-v2-frame2/sidecar.py`，已依次 register、verify、collect after、collect before、compare；属于完整指定家族追加，不冒充全矩阵运行。

仅适用于本机 Linux amd64、合成目录、临时数据库、固定本地网页与代理服务。数据库/文件在 tmpfs，不能外推为实体磁盘 fsync、断电恢复、ARM64、其他平台或公网服务性能。状态新鲜度、取消、错误反馈、完整列表和两栈隔离未取消。

## 历史失败、清理与后续建议

- 原 10,000 条基线及原生输入诊断自然超时的压力结果保持原样。11 月 9 日全量归档 SHA-256 `a73cb33f8dc3add06f23e970613ff3cbb361f7c7a5975e6b034a51c1fb8b376b`。
- run1 候选第三次代理恢复在 1,800,000.451285 ms 达到截止，不能算成功样本；checkpoint SHA-256 `3b996f56ec03650477cf11198e9c4a4bed1457b2c809da54aee01b8656726a4c`。
- run2 在零实例第六轮缺少有效“两帧递增”观察证明，1,425.881241 ms 收到测量错误，非 1800 秒超时；原数值未记录，根因仍未知。改进帧证据并校准后 run3 两侧从头执行；checkpoint SHA-256 `07c00c6813e39a022bd742fbd3c776293cbf5c0da5816a4cb35b3cd329c1dd73`。
- 所有诊断的初次失败与修正后结果均保留。psd1 的“手动测速 pending 覆盖完整筛选/恢复对”没有发生，保持 inconclusive；不拿其 10,000 事件匹配当作并发验收通过。
- 原有 19 个 Linux 修改文件中 18 个逐字一致，另 1 个只替换经过审阅的查询入口，原修复代码保留。当前补丁通过 baseline 正向和当前工作区反向 `git apply --check`；没有实际撤销用户工作。
- run3 加目录追加共 170 个 lifecycle 文件、179 次应用正常退出、4 次显示正常退出；4,918 个应用家族/显示身份加 6 个 producer，共 **4,924 个 PID/start 身份均已消失**。无强制清理、live/zombie 或主要清理错误。Go 内核由真实 fixture 的 wait/reap 断言检查；不把未长期保存的内核 PID 冒称独立身份审计。

建议后续先做有界的因果定位：为代理 API 分开记录 DAO、Wails/JS 与结果检查成本；记录周期刷新触发及 React 提交；验证完整内容相等时复用引用是否保持所有状态语义。只有找到并修复原因后，才重跑受影响的完整两侧及资源矩阵。既有 5% 标准、数据、失败和基线保持不变。此建议尚未授权越过当前停止条件实施；当前交付不声称已达到最大性能。

## 持久归档凭据

全量归档已完成：[evidence.tar.zst](/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-10-linux-performance-v2/evidence.tar.zst)，**2,287,170,800 bytes**（约 2.29 GB），SHA-256：

```text
4be9ce979e585a34c2189756ca85b4c10b9e0035dac3f09c232025750e7f0a53
```

覆盖整个 `/tmp/ant-linux-perf-cntr19l1`，无排除项：337,154 个普通文件、原文件共 12,262,231,462 bytes、418,357 个归档成员。创建、压缩完整性、tar 对照源内容、成员集合一致及归档前后源属性清单一致全部通过；[详细凭据](../artifacts/linux-performance-v2/archive-status.json) 保存命令和退出状态。所有代理已停止写入，源证据保持原样。

归档内 `delivery-v2/project/.codex/` 包含报告的归档前快照、批准 Goal、可逆补丁及小型审阅证据；本段及外部 `archive-status.json` 是归档生成后附加的凭据，因此不声称归档能包含自己的最终哈希。完整原始日志、源码副本、冻结工具、失败场景和未执行草稿均在归档中。归档完成只证明证据已持久保存，**性能 Goal 仍未完成**。
