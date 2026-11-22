# Linux 性能优化：2026-11-11 继续执行记录

用户选择 `1`，继续定位并修复回退，保留既定 5% 验收标准。适用目标为 [v2 目标](../goals/2026-11-10-linux-performance-v2.md)。本记录中的诊断不替代正式验收，也不将上一轮失败改记为成功。

当前共享产品为 v7：保留 v6 恢复 Mihomo 原探测节奏的处理，并整合了经过真实子进程负对照、普通和 race 回归验证的 Xray 启动 stdout 捕获修复。其余产品输入保持；880 项输入已冻结，完整功能／Linux 构建已通过，v7 正式 IO 新层已完整完成并比较失败，触发既定停止条件。v5 的 Mihomo HTTP 502 和 v6 第 50 个生产者的 Xray exit 255 失败批次原样保留，均没有补齐或生成局部性能比较。原 Xray 退出的具体原因仍未知。Goal 未完成，旧成绩不改记为新版本成绩。

上一轮的报告、原始样本和归档保持不变。新的可写证据根为 `/tmp/ant-linux-perf-resume-gokzruj0`；`/tmp/ant-linux-perf-cntr19l1` 已归档，此次只读。共享源码以此前 `v4r1` 的 875 项输入清单为起点，基准不重建为不同版本。

本轮候选 v5 已冻结，879 项产品输入哈希为 `acdf87a185cf08b9e61c0eab38ed31e64f5b48005be0db8567d64762d83f7d2d`，位置为新证据根的 `candidate-v5`。以下是候选处置，均不代替完整正式对照：

- 前端：整合全字段等值快照引用复用，保留 15 秒请求、30 秒新鲜度时钟、运行态事件和 API 返回值。8 项定向 Node 检查通过；真实 React/WebKit 的原版与候选各 11 项行为检查通过。旧版自然刷新全字段相同仍触发整表工作；新的整候选资源与自然刷新诊断 rf4 已完成，具体结果及单会话限制见后文。100 行筛选恢复主要在一次布局中耗时，目前没有可信的专门修复。
- Linux 浏览器启动：撤下 stderr 端口通知候选。21 项定向普通/race 测试通过，但 s1 的 12 个真实应用会话、90 次启动和停止没有证明可信收益；1,249 个唯一受控身份均已回收。端口公告常靠近原 150 ms 重试边界。阶段记录未包含单次 HTTP 起止及通知消费，不能推断精确重叠分支，也没有解决历史启动不确定项。隔离补丁、测试和全部原始证据保留。
- 归档：整合单次操作共享 32 KiB 拷贝缓冲及 128 KiB ZIP 输出缓冲，最终写入/关闭错误仍返回；未整合额外解压输出缓冲 D。定向内容、ZIP 字节、限制、错误和句柄检查通过；同进程交错诊断显示分配显著减少、不可压缩 ZIP 耗时降低。可压缩解压在两次诊断中分别出现约 +5.54% 和改善，结果冲突，未算作正式通过。
- 代理列表：整合单次扫描复用结构体及布尔临时变量，保留 24 列、完整值、排序和错误语义。5 项定向普通/race 测试通过。同进程 60 对、120 次调用完成，124 次完整字段/排序及 4 次错误检查通过；100／1000 条分配中位数下降 18.36%／16.85%，耗时基本不变（约 +0.10%／+0.21%）。真实 Wails API 分段诊断 g2 两侧各 30 次调用通过，没有重现旧回退；g1 的回执匹配器错误及旧 +36.58% 结果保留，不宣称已消除回退。

父级统一集成共享源码；三个子代理只写各自隔离目录。运行性能负载的窗口串行分配。集成及撤回记录位于新证据根 `artifacts/integration-1.json`、`integration-2-frontend.json`、`integration-3-database.json`、`readiness-disposition.json`；最终源码清单为 `artifacts/candidate-source-v5.json`。子代理发生服务限流后，父级接管剩余验证。

完整功能检查已通过，证据在 `functional-v5`：前端干净构建、23 项 Node 测试；普通 Go 与 race 各 691 项必需测试、各 1003 个通过事件；启动门限 2×10、真实 bridge 1 项、仓库 Python 12 项、性能工具 Python 269 项，以及普通 Linux amd64 产品构建。原登记的 7 个私有／外部／可选 child 入口保持单列，不计入必需验收。产品 ELF SHA-256 为 `43a6cd70f99ba122fccb70ed51ff9d0e4c5370035bdadd99b0c6bac138966943`，不含测试观察入口。新的性能运行统一固定 Go 1.24.13、实际编译器及运行时环境，旧证据的环境记录限制不会被改写。

已复核已有组件行为证据与 v5 输入的适用关系：profile speed hook 9 项、lazy modal 8 项、代理表与选项 8 项的受测产品输入均保持；新的 profile snapshot 原版／候选各 11 项真实 React/WebKit 检查与 v5 对应输入一致。记录在 `artifacts/frontend-behavior-v5-review.json`。这些证据只覆盖原夹具的组件行为，不复用旧性能时值，也不替代新版完整产品集成验证。

自然刷新资源诊断 rf3 在首次资源采样后因观察脚本安装表达式触发自动分号规则而失败，应用与显示进程正常回收；原脚本、注册、原始失败均保留。仅在新的 `refresh2` 工具副本修复返回表达式，加上安装结果检查；8 个安装及恢复断言含原错误的负例通过，再于独立 `rf4` 目录重跑全部四会话，不沿用 rf3 局部样本。资源窗口和产品代码保持不变。

rf4 四会话全部通过完整检查，每会话原 15 秒预热及 30 秒、31 点资源窗口后，再观察两次自然刷新；24 个受控应用家族身份均已消失。两侧所有完整字段的连续响应一致。100／10,000 条目录的旧候选分别执行 200／20,000 次关键词高度读取，新候选均为 0；日期格式化分别从 300／30,000 次降至 100／10,000 次，保留原时钟。WebView CPU 的单次 30 秒样本分别为旧→新 0.200→0.130 秒、10.950→4.600 秒；10,000 条 WebView PSS 为 5,249,713,152→4,665,493,504 字节。10,000 条应用进程 CPU 则为 0.250→0.330 秒，不能隐去。每侧每规模仅一会话，这些是整候选诊断，不能据此宣称稳定百分比、单补丁归因或 5% 正式通过。原始及独立重算记录为 `agents/frontend/rf4/results.json` 与 `artifacts/refresh-rf4-review.json`。

原有 19 处用户 Linux 修复已重新逐项核对：17 个文件逐字不变；测速文件仅替换一处列表查询入口；归档文件仅改缓冲及 ZIP 最终错误返回，原有路径、重复项、符号链接、限额与解压关闭错误处理均保留。记录为 `artifacts/preserved-user-linux-fixes-v5.json`。相比原始基线，新候选共涉及 31 个产品文件；含测试和测量工具的 97 路径可逆补丁已生成并通过正向／反向应用及逐文件哈希核验，恢复目标包括原有用户 Linux 修复。

完整新对照已开始，输出为 `evidence/formal-v2`，注册文件 `artifacts/resumed-pair-registration.json` 的 SHA-256 为 `0ee6ffc65895ed7b248c46a17cc09498eb768f7fb0d2834198cec4fca95c65ac`。使用原始基线 `b6274103…` 和新候选 `acdf87a1…`，先 before 再 after，再严格比较；冻结工具哈希仍为 `534f24b3…`。不拼接历史正式样本，不变更原 30 个短操作样本、10 轮启动及资源、15+30 秒窗口、1,800 秒单操作上限、bootstrap 或 5% 门槛。

新测基线已完整结束，运行 ID `14c0c3ea-fcd5-44a7-b5e5-540f799354d7`，耗时 3,238.251 秒；182 项指标、58 项正确性检查及 32 个微基准叶子齐全，10 条生产命令均退出 0，冻结验证器接受完整运行。`before/results.json` SHA-256 为 `120a4d4eb870ab6b059c2f84a4a44fef4b685e53524bcd17d4c181f619c90019`，父级已核对完成封印与驱动记录。40 个实例运行轮次均正常关闭，2,160 个唯一受控身份独立复查均已消失，显示进程正常退出；记录为 `artifacts/baseline-runtime-all-progress-review.json` 和 `artifacts/baseline-complete-summary.json`。

新候选已在同一次正式对照中完成 100／1,000 条目录的全部轮次。10,000 条目录首轮已完成预登记的 30 次短操作，以及完整结果、事件、取消、错误和重启读回检查；该轮及重启的两个生命周期均正常退出，12 个唯一受控身份独立复查已消失，记录为 `artifacts/candidate-large-round0-lifecycle-review.json`。大目录全部 10 轮随后完成，独立复核了 310 个资源点与缓存汇总一致性、完整 GUI ID／名称／渲染证明、14 项业务与资源检查和 11 个正常生命周期；66 个唯一受控身份均已消失，记录为 `artifacts/candidate-large-catalog-progress-review.json`。候选的 0／1／5 实例各 10 轮也已完成，30 个正常生命周期与全部 900 个唯一受控身份回收已独立核对，记录为 `artifacts/candidate-runtime-015-progress-review.json`。随后全部 40 个运行会话均正常结束，2,160 个唯一受控身份独立复查已消失，显示进程也正常退出，记录为 `artifacts/candidate-runtime-all-progress-review.json`。代理、下载、归档、日志和全部微基准随后完成，完整封印及严格前后比较已结束；最终判定见下文，以上阶段完成性不表示 Goal 性能要求通过。

本轮完整对照已经完成，但性能 Goal 未完成。后续继续处理明确回退与不确定项，按事前固定方案诊断／补采，并完成最终报告及新增证据的持久归档。此前通过的功能与构建检查保持绑定到冻结的 v5 输入。

## v5 完整新对照结果

候选运行 ID `e0d84c4b-67a1-41d6-b988-279ae73b1664`，耗时 5403.441 秒；233 项指标、71 项正确性检查、32 个微基准叶子各 10 次齐全，10 条生产命令均退出 0。`after/results.json` SHA-256 为 `699336920caee38aeb11624e96d4438e4be2cc149876361d3a300bb2bab0a11a`。比较退出 1 表示性能门槛未通过，不是采样程序失败。汇总见 `artifacts/formal-v5-complete-summary.json`，完整数值与 95% CI 见 `evidence/formal-v2/comparison/comparison.json`。

182 项双侧比较为 157 pass、22 inconclusive、3 regression；其中 107 项核心为 **83 pass、21 inconclusive、3 regression**。5 条预登记关键用户流程有稳定改善。旧批次失败和不确定结果不改写；以下仅描述本轮独立结果。

| 关键流程 | 基线中位数 ms | v5 中位数 ms | 变化 | 变化的 95% CI |
| --- | ---: | ---: | ---: | --- |
| 1000 条实例筛选 | 160.974 | 134.307 | -16.57% | [-23.41%, -8.95%] |
| 1000 条关键字保存可见 | 206.955 | 140.075 | -32.32% | [-40.07%, -25.59%] |
| Mihomo 冷就绪 | 122.018 | 31.370 | -74.29% | [-74.35%, -74.24%] |
| sing-box 冷就绪 | 119.187 | 28.432 | -76.14% | [-76.35%, -75.98%] |
| Xray 冷就绪 | 119.908 | 28.454 | -76.27% | [-76.34%, -76.16%] |

全部未通过的核心指标如下。变化为候选相对基线；inconclusive 不计作通过。

| 指标 | 状态 | 中位数变化 | 变化的 95% CI |
| --- | --- | ---: | --- |
| `catalog.100.initial_render` | inconclusive | -1.18% | [-7.95%, +5.25%] |
| `catalog.100.profile_filter` | inconclusive | -6.13% | [-14.37%, +5.26%] |
| `catalog.100.keyword_save_visible` | inconclusive | -6.11% | [-14.55%, +8.93%] |
| `catalog.100.profiles_api` | inconclusive | -7.09% | [-26.68%, +34.23%] |
| `catalog.100.proxies_api` | inconclusive | -5.65% | [-21.85%, +122.81%] |
| `catalog.100.proxy_filter` | inconclusive | +2.50% | [-6.88%, +12.03%] |
| `catalog.1000.proxies_api` | inconclusive | -3.36% | [-19.98%, +38.58%] |
| `runtime.1.app.cpu` | inconclusive | +0.00% | [-25.00%, +33.33%] |
| `runtime.5.app.cpu` | inconclusive | +14.29% | [-25.00%, +66.67%] |
| `runtime.10.app.cpu` | inconclusive | -0.00% | [-40.00%, +33.33%] |
| `proxy.xray.reuse_ready_ms` | inconclusive | -9.04% | [-25.13%, +7.75%] |
| `download.download_compressible_stream_cpu_ms` | inconclusive | -10.19% | [-32.34%, +10.19%] |
| `download.download_incompressible_range_cpu_ms` | regression | +43.57% | [+29.43%, +63.14%] |
| `download.download_incompressible_range_wall_ms` | regression | +44.51% | [+29.96%, +58.56%] |
| `download.download_incompressible_stream_cpu_ms` | regression | +27.38% | [+7.45%, +72.32%] |
| `download.download_incompressible_stream_wall_ms` | inconclusive | +17.83% | [+0.32%, +93.43%] |
| `archive.snapshot_compressible_unzip_cpu_ms` | inconclusive | -7.11% | [-57.39%, +54.48%] |
| `archive.snapshot_compressible_unzip_wall_ms` | inconclusive | +7.82% | [-53.70%, +63.02%] |
| `archive.snapshot_compressible_zip_cpu_ms` | inconclusive | -0.35% | [-46.71%, +39.65%] |
| `archive.snapshot_compressible_zip_wall_ms` | inconclusive | +0.37% | [-46.28%, +42.63%] |
| `archive.snapshot_incompressible_unzip_cpu_ms` | inconclusive | -2.46% | [-52.87%, +102.71%] |
| `archive.snapshot_incompressible_unzip_wall_ms` | inconclusive | -6.16% | [-53.54%, +103.00%] |
| `archive.snapshot_incompressible_zip_cpu_ms` | inconclusive | +32.14% | [-32.14%, +44.55%] |
| `archive.snapshot_incompressible_zip_wall_ms` | inconclusive | +33.47% | [-32.09%, +45.55%] |

本轮 `catalog.100.profile_restore` 已通过，四种实例负载的 WebView CPU 也均通过。1000 条 proxies API 仍为不确定，不能宣称其历史回退已完全消除。主进程 1／5／10 实例 CPU 的原始计量有 10 ms 阶跃，而 5% 界约为 1.5–2 ms；5 实例中位数从约 35 ms 增至 40 ms，仍须调查，不能修改或启用不适用的近零下限。

下载实现及冻结夹具两侧逐字一致，原始内容哈希、请求计数、取消／错误和回收核验一致，但这不能推翻当前 3 项明确下载回退。首个 200 探测响应复用会破坏既有 `TestCoreDownloadSingleStreamFallback` 支持的行为，已撤下该候选。后续采用新隔离根中的固定交错诊断；不直接复写或重用已封印目录作为可写运行目录。

辅助微基准为 90 pass／6 inconclusive／0 regression，6 项均为 ns/op：Profile DAOList 100／1000、Profile ListByTag 1000／10000、Profile List 10000、SingBoxOutbound。所有 32 项 B/op 与 32 项 allocs/op 均按原比较规则通过。Profile DAOList 不是本轮 tiny scan 优化的 Proxy DAO，不能错作该优化的因果证据。

辅助微基准运行期间，父级两次进度计数误用整文件读取；第二次日志已达 91,617,405 字节。原记录为 `artifacts/microbenchmark-progress-reader-note.json`。复核发现该字节边界已有 98 条完整数字结果，原粗解析仅计到 60 条，不能由粗计数界定受影响的叶子或精确时段。全部 32 叶的延迟保留潜在读取负载限制，计划在新根做固定双侧完整补采，保留原始、补采、合并三层。此事件发生在全部核心测量结束后，不能用来解释核心回退。

新版 10000 条目录完成性、完整结果与正常回收通过；仍有明显长尾。以下为候选自身分布，缺少可比旧版基线，不生成改善百分比或配对通过结论。

| 10000 条场景 | 中位数 ms | p95 ms | 最大 ms |
| --- | ---: | ---: | ---: |
| 首次完整显示 | 19270.647 | 20159.783 | 20408.923 |
| 实例筛选 | 3453.999 | 15867.307 | 39079.538 |
| 实例恢复 | 18774.729 | 45878.786 | 78219.796 |
| 代理筛选 | 1472.511 | 53723.023 | 137334.685 |
| 代理恢复 | 9323.097 | 28816.363 | 66387.991 |
| 关键字保存可见 | 175.868 | 217.434 | 373.277 |

10000 条 WebView CPU 的 30 秒窗口中位数为 5.029984 CPU 秒，PSS 中位数为 4,748,225,536 字节（约 4.42 GiB）。全部 51 个候选专属指标见比较 JSON；采样窗口内观测峰值与 RSS VmHWM 上界须分别解释，不能作为全运行精确 PSS 峰值或无泄漏证明。

独立复核分别位于 `agents/frontend/v5-formal-review`、`agents/database/v5-micro-review`、`agents/proxy/review/download-v5`，包含原始回执／资源重算、完整微基准流式核对和下载调用链边界；没有用这些复核替换原始样本。

可逆补丁为仓库 `.codex/artifacts/performance-v5-only.patch`，SHA-256 `fcbaa3c74adfc8748f784e247e13cbeec9520197ecfb8a85bd54c61620a9565b`；97 路径的前向、反向检查及实际应用和逐文件哈希均通过，记录见 `artifacts/delivery-patch-v5/performance-v5-patch-validation.json`。该补丁检查与性能 Goal 验收分别记录。

测量边界仍为本机 Linux amd64、Go 1.24.13、固定 Chromium／代理内核、tmpfs 数据与本地服务；不能外推为实体磁盘、真实公网或其他平台的收益。当前没有提交、推送或发布。

## 完整对照后的定位与固定补采

下载与归档的独立进程交错诊断已完成，证据为 `agents/proxy/download-v5-interleaved-d2`。两个 suite 各按固定 A–B–B–A–B–A–A–B 执行，16 个会话均正常退出；80 个根测试／子测试、192 个各含 30 值的指标数组齐全，原内容、错误、适用的取消和回收检查通过。包含校准与构建在内的 126 个记录身份均已消失。登记 SHA-256 为 `a00bd9d4d78fc09d19c2e23c2c5746b1bb94a5b0681e47b5351be8f46c6f66eb`，结果 SHA-256 为 `4dcd47c17eacce7acd56f62cbf417559114f15295262a7539e346c8f22f84a8e`。初次 d1 在构建与计时前因 `GOSUMDB=off` 阻止工具链校验而失败；原现场保留，新尝试恢复标准校验，并未关闭验证。

四个固定版本对的不可压缩 range CPU 变化为 +5.74%、−4.39%、+0.53%、+49.58%，wall 为 +2.99%、−5.78%、−0.41%、+57.04%；同一个基线二进制在相邻两个进程之间也出现 CPU −37.97%、wall −40.01%。不可压缩 ZIP 的四对 CPU 均下降，但同基线控制也下降约 29%。这证明进程间差异足以影响局部读数，尚不能把差异归因于某个版本、CPU 频率或 GC。诊断使用独立测试 ELF，与正式 `go test` 入口不同；不计正式样本，不撤销正式回退。完整逐指标描述见 `agents/proxy/review/download-v5-interleaved-result/RESULTS.md`。

5 实例的外部 Linux 进程 CPU 时钟诊断 k2 也已完成：30 项校准通过，固定 A/B/B/A 四个真实 Wails 会话正常完成 40 次启动／停止、124 个原始资源点及 248 次时钟读取；275 个记录身份均已消失。采用指定应用 PID 的 `clock_getcpuclockid`，通过 PID/start/UID 和 pidfd 绑定，测量整个应用进程的线程，不包含子进程。原 15+30 秒窗口、31 个采样点、`/state` 请求和 `/proc` 结果均保留。登记 SHA-256 为 `6c5931d6ea1e25dcf3127764c618b0f1502facb21b63c15ab4f573ea7ee130d6`，结果 SHA-256 为 `bdd0fc3531e06bc889ce207f1dfa32c3cc21b27fcd23a26a449b4f41d29ca541`。先前 k1 的非法 PID 校准使用了超出 CPU clockid 编码范围的值，尚未进入 GUI 即失败；原失败保留，k2 改用经过验证不存在的 `pid_max+1`。

| 五实例诊断会话 | 原 tick CPU ms | 外部时钟端点包络 ms |
| --- | ---: | ---: |
| A1 | 30 | 37.209202 |
| B1 | 30 | 35.952531 |
| B2 | 40 | 42.891370–43.140976 |
| A2 | 30 | 34.460314–34.735546 |

以上包络只表达原始读取时点前后的范围，不是统计置信区间。每侧两个会话的描述性均值差，tick 为 +5 ms，时钟包络为约 +3.45 至 +3.71 ms；不能把原差异完全归为 tick 量化，也不能据此判定 5% 通过或稳定回退。纳秒表示和 `clock_getres` 不代表纳秒准确度，外部观察也不是零开销。

完整 32 叶微基准补采已完成，固定候选 B1 后基线 A1，每侧每叶 10 次、全部 22 包和原命令不变。登记文件位于 `agents/database/v5-micro-supplement-run1`，SHA-256 为 `1c543c8f571f7a91ac6a2c49ad27d28bd4a1cc93f13492cbeefdde04b7ed824e`。28 项解析及进程／入口控制校准通过；较早校准失败保留。两个采集分别耗时 768.690／689.133 秒；采集、两侧验证和比较全部退出 0，27 个记录身份已消失。仅在两侧都结束后解析和哈希原始日志，未重复原观察错误。新增 640 条、全部合并 1280 条，三个视图完整保留。

| 微基准视图 | 耗时通过 | 耗时不确定 | 耗时回退 | B/op、allocs/op |
| --- | ---: | ---: | ---: | --- |
| 原始 | 26 | 6 | 0 | 各 32 项通过 |
| 独立补采 | 27 | 4 | 1 | 各 32 项通过 |
| 全部合并 | 28 | 4 | 0 | 各 32 项通过 |

独立补采的 `SingBoxOutbound` 为 21.800→24.9645 μs，增加 14.516%，比率 95% CI 为 [+8.687%, +25.536%]，明确回退。合并后仍为 +10.779%、不确定，不能撤销独立回退。实际夹具、直接解析路径和依赖文件两侧相同；之前执行的 resolver 基准所走产品代码有差异，但尚不能将回退归因于进程历史、布局或其他因素。该叶测量 AnyTLS YAML 解析，不启动代理内核或网络；不能据此改写真实冷就绪收益。

合并不确定项为 Profile DAOList 1000（+2.101%）、ListByTag 10000（−2.740%）、DAOList 10000（+2.437%）和 SingBoxOutbound（+10.779%）。DAOList 10000 原先通过、追加后不确定的变化同样保留。完整 32 叶、三种指标、三个视图共 288 组统计见 `agents/database/v5-micro-supplement-run1/handoff/all-32-three-views.csv`。原始与合并的全部延迟继续带有旧观察干扰限制；新增结果也不宣称零系统干扰。

比较封印 SHA-256 为 `d1b1dcc73b5bd77af6df4ca1d4f7643a12a7b7ed90bceef4c1e9ef79146d1b89`；交付说明 SHA-256 为 `da1dae304fba673f20278cc5e62b8b14f148b925aadcee8364590108fbbeac9c`。父级已独立核对小封印、样本总数和原 24 项核心失败的完整保留，见 `artifacts/micro-v5-supplement-parent-review.json`。本补采只提供辅助证据，`goal_pass=false`。

代理／下载／归档正式补采在 `agents/proxy/v5-core-supplement-run1` 登记并执行后，因真实代理请求失败而停止，未进行性能比较。登记 SHA-256 为 `0c8ca6664c82f9d926d1e53e9cbe04eb7319a55f1c3556da3cfb7a4bd4b76536`。原计划覆盖全部 45 项指标（37 项核心、8 项分配观察），固定 12 轮，每轮依次 proxy、download、archive；奇数轮各 suite 为 AB，偶数为 BA，共 72 个原始 `go test -count=1 -json` 进程。原夹具、30 样本、产品版本、Go 1.24.13、三核内容和有效环境不变。源 A/B 分别复制 325／335 个必要输入，原每侧 45×30 向量重新从封印 Go 输出解析核对。旧 d2 只提供工具函数，不提供正式样本。

实际完成 25 个生产者，第 26 个 `r05-proxy-B` 在 Mihomo 第 16 次冷启动后的真实请求中返回 HTTP 502、空正文；Xray 和 sing-box 子测试此前已通过。该生产者退出 1，没有输出完整指标报告，余下 46 个未启动。原始失败、退出状态及输入封印均保留；没有解析前 25 个进程的性能分布，没有重试、合并或生成比较。所有 26 个私有会话均为空，外层及 Go 生产者共 27 个 PID/start 身份均已消失，无强制清理或清理错误。最终输入核验通过。执行结果 SHA-256 为 `49549917b9df3abb6e8050dbd9045cf15e2b3aa55721b89757d1540040bdac4b`，执行封印 SHA-256 为 `b1e34a078b0e4aa7705e16291e69051afa2418c1b8e7c7c043c0e68e98b718b8`。详情见 `agents/proxy/review/v5-core-supplement-run1-failure/failure-summary.json`。

两份独立源码复核将官方 Mihomo v1.19.27 固定到 commit `5184081ac327394d9e15fa5d5f9f4a61e723fd94`：mixed 监听创建后，内核仍需初始化 provider、执行 GC，最后才允许普通外部转发；期间关闭的内部连接可以被 HTTP 层转换为空 502。现有 controller 配置查询和初始配置日志都不是 Running 屏障。这是与本次失败兼容的机制，尚不是本次因果证明；上游 CONNECT／传输失败同样可能被转换成空 502。产品两侧在该路径上仅改变了失败后的 TCP 探测节奏，100 ms 改为 10/20/40/80/100 ms。复核位于 `agents/database/mihomo-1.19.27-startup-review/REVIEW.md` 和 `agents/proxy/review/v5-core-supplement-run1-failure/REVIEW.md`。原 Mihomo 冷就绪数值仍保留，但结合这次真实请求失败，不能将该加速宣称为已通过可靠性验收。

父级批准固定 8 进程 F–C–C–F–C–F–F–C 局部诊断：F 为原 v5，C 仅恢复 Mihomo 原 100 ms 重试节奏；两侧完整三栈入口及原请求次数不变，加入相同事件记录。诊断不增加原请求前的控制查询、预热或等待，不重试 502，不取得正式性能验收资格。固定方案为 `agents/proxy/review/v5-core-supplement-run1-failure/diagnostic-plan.json`；完成记录见后文。共享产品已先撤回这项 Mihomo 加速并冻结为 v6，避免保留已暴露可靠性风险的优化；恢复原节奏也不代表可以保证原生窗口永不发生。

此次未执行的预登记比较方法是：原比较器对全部值计算；另按完整进程的 30 值向量重采样。新层每侧 12 簇／360 值，合并层加入原进程后每侧 13 簇／390 值。每指标每层独立初始化 Random(1729)，每次先抽 A 的完整簇，再独立抽 B，保留簇内顺序和抽中次数，固定 4,000 次、中位数、原 percentile 与原差值 CI 判定。一个核心指标只有 IID 与整簇复核都支持通过才通过；新增层明确回退即使合并层通过也会阻止模块通过。原单簇不生成虚假的簇置信区间。当前失败批次没有达到这些完整性要求，不能从局部样本给出比较结论。

测量前的 31 项解析／统计校准及 7 项真实 Python 进程控制均通过，包括合法 Go build-output、缺失／重复／错误测试、过期校准、簇数／顺序、超时、中断、孤儿回收和写入失败时的原错误保留。12 个校准身份已消失。回执 SHA-256 为 `c26666e93c34097f79688991cf96351e3bcd5913e76715906100646b3a0e8037`，受测工具清单 SHA-256 为 `c6d7a0d3bf83325bf7f8821767254f87641d6118391ec5392bde6502a89d0f39`。较早通过的 validation-1 和后来修订前的工具快照仍保留。父级静态审查及独立统计／来源复核完成，登记入口强制校准与工具绑定，父级门限记录为 `artifacts/core-v5-supplement-parent-gate.json`。

前端正式补采仅完成静态准备，位于 `agents/frontend/f6-core-prep`，没有登记或执行。计划覆盖两种目录规模和四种实例负载，共 134 项指标／68 项核心；四项应用 CPU 对两侧所有负载使用同一精细时钟，并同时保留 tick 数据和端点最不利比较。其余指标保留原始、新增、全部合并视图。prepared.json SHA-256 为 `c600d6f416293c6437ff8abcce91d893d2ec60b07f04479d596a4259b212fc84`。将先完成代理与文件模块的固定补采；若已经触发 v2 停止条件，就报告前端未执行，不启动额外 80 个运行会话。

所有新判定保持 5%、原近零资格、seed 1729、4,000 次 bootstrap 和 95% CI。固定追加完成后仍不确定时适用 v2 停止条件，不因结果不利自动增加轮数或挑选统计视图。

## v6 的保守撤回与验证

879 项源码输入中仅 `backend/internal/proxy/mihomo_bridge.go` 改变，恢复后与原始基线整文件相同，SHA-256 为 `fb9afd2dd9160c6604a29f7446b84592b450eb37013ace11ab27c94480f221c4`。v6 产品源码哈希为 `157c83dc142932250db42ff68999ff0e297ebc9c5158e1fabf0cf0c5bac76f60`，冻结于 `candidate-v6`。Xray／sing-box 的 SOCKS 握手轮询、其他性能改动和 19 项用户既有 Linux 修复均保持。Mihomo 冷启动的 mixed／controller 及复用 TCP 检查都恢复原节奏；没有请求失败重试、固定启动等待、控制器伪就绪或跨栈回退。

相对用户修复基线的可逆补丁现为 96 个路径，仓库文件 `.codex/artifacts/performance-v6-only.patch` 的 SHA-256 为 `b0bdd4ea29e13dd2a988d5dc7ed36c369da076c74c123af13a62d0b4069a0652`。前向／反向检查、实际应用及逐文件哈希均通过；原 v5 补丁保持不变。产物及回执见 `artifacts/delivery-patch-v6`。

首次完整功能运行 `functional-v6` 的 13 项生产命令全部退出 0，必需用例、构建及源绑定检查与 v5 同规模通过，生成普通 amd64 ELF `5466ba45d316dc237ce5f3fd7e3c4c9f34d04203d91eb9f721e5e500ced026e8`。但父级将 Go 专用严格进程控制用于整批 npm／Node／Go 构建，外层在 leader 退出时先看见 4 个已被 subreaper 接管的僵尸辅助进程，判定失败，随后 `waitpid` 全部收割且退出状态均为 0；没有强制信号、清理错误或残留。不能推断这些已死进程的具体程序名，也不把外层退出 1 改为成功。原始记录在 `functional-v6-command/verify.command.json`，父级独立确认 5 个记录身份均已消失，见 `artifacts/functional-v6-parent-review.json`。

功能批次专用进程控制已修正为先核对身份并收割已经退出的被接管子进程，再要求私有会话为空；仍拒绝存活孤儿、非零退出、异常信号及身份核验失败。7 个真实正反例通过，17 个记录身份均消失，校准 SHA-256 为 `ff7f4d51cb3f7f0e2c0a1910808a6461ebbed8d34ead5edf453daaee38d7b7be`。该修改只用于功能批次，性能测量的原严格 guardian 字节不变。`functional-v6-r1` 随后重新完整执行：13 条命令和外层均退出 0，前端 23 项 Node、普通／race 各 691 项必需 Go 测试、各 1003 个通过事件、启动门限 2×10、bridge 1 项、Python 12＋269 项，以及普通 Linux amd64 产品构建齐全；保留原 7 个可选 child 条目。源码及环境绑定一致，产品 ELF 哈希与首次构建相同，不含测试观察入口。4 个已死辅助进程正常收割，5 个记录身份独立复核均已消失，无外层强制信号或清理错误。汇总 SHA-256 为 `baf92dbae0b1a74f222f1c7f6480c28797a090024097bb8b71177270faadc749`，执行记录 SHA-256 为 `fba2c138a6f896d08b4f1772a6ab928dc3f6d73ad2709140d0385738df31b97e`，父级复核为 `artifacts/functional-v6-r1-parent-review.json`。

Mihomo 诊断的第一版准备与 42 项解析校准均保留，但尚未开始固定 8 进程时，独立审查发现 target handler 中间写入事件和孤立 native 阶段的完整性检查不足。在新的 `mihomo-cadence-diagnostic-prep2` 中仅修正验证器并新增 3 个针对性反例；45 项校准通过，实际 Go 插桩和生产进程控制未改变。工具清单 SHA-256 为 `235f900cb02c067bea543b225cd85a82481a3cfe166bd9cb06498d0a089562e0`，校准结果 SHA-256 为 `e1b3aaa694ed622354dd90d7df15740f2f47607f780b2c3fff41e0b1113a4f87`。

`agents/proxy/mihomo-cadence-diagnostic-run2` 按原固定顺序完成全部 8 个测试进程，均退出 0；每侧各 4 个进程，三栈各完成 240 次冷路径和 240 次复用原请求，总计 1,440 次成功请求。248 个 Mihomo native 生命周期及构建、校准、CLI／Go 等共 261 个唯一记录身份均已消失，无外层强制清理。父级重新核对完整原始记录、哈希、校准、输入、固定顺序及清理，见 `artifacts/mihomo-diagnostic2-parent-review.json`。登记 SHA-256 为 `55e53238c2390e08462a664ce79c5737ee208adcf2b14450929ca0b436c7b80b`，执行结果 SHA-256 为 `285507ae24a334905e01c8d61da39072837c3323293d219cf2454f78e06e8b13`。

本次没有复现 502，不构成旧失败原因已排除或原快速探测可靠的证明。两侧共同插桩具有非零开销，可能改变问题出现概率；原生进程按原 manager 生命周期停止，不能将“无外层强制清理”解释为内核没有收到正常停止信号。保留已撤回的 Mihomo 优化处置，诊断性能值不进入正式对照。v6 后续代理／下载／归档计划仅使用新 A12／B12 的完整进程数据，不混入旧 A、v5 B 或失败批次的局部样本；固定追加后仍有核心退化或不确定项时执行 v2 停止条件。

独立只读阶段复核为 `agents/proxy/review/mihomo-cadence-run2/REVIEW.md`，SHA-256 `4050c6ecbd333a1c027710f2bbceec7589da15f99b563d392f508cd9546a6466`。248 份 native stdout 共 992 条配置解析日志、stderr 全空，没有 Running 时刻。trace 中保留 960 条转发 copy 的闭连接错误，其中 16 条先于 Go 侧 `request_success` 标记，但均在目标完整写入之后且原请求最终成功；不能将其全部解释为成功之后的关闭，也不能当作 502 复现。父级处置 `artifacts/mihomo-v6-diagnostic-disposition.json`（SHA-256 `f7d45ebe4d58f6e8054d64de79542e78be427c96e1b959c69ed604c49383982a`）允许在新工具复核、校准及登记门限满足后执行一次 v6 新层，不放宽原可靠性或性能要求。

## v6 独立新层：代理、下载与归档

`agents/database/v6-core-supplement-run1` 按 72 个生产者的固定计划开始执行，在第 50 个生产者失败时停止。登记 SHA-256 为 `993e7d80a1f8359ea53bf4d34c320f785450c8d1c39aa1003aa057c1e97cbf70`，登记封印 SHA-256 为 `64add7e5d1a58e7c416889fadf696737be41d8b80bf0acef2463c0d33fa76f5a`。A 为原始 baseline，B 为冻结 v6，分别复制并验证 325／335 项必要输入。固定计划为两侧各 12 轮，每项每进程原 30 值，覆盖三栈 21 项、下载 12 项和归档 12 项，共 45 项／37 核心。套件顺序仍为 proxy→download→archive，奇数轮 AB、偶数轮 BA；Mihomo 诊断插桩没有进入正式源码。

此层只使用新 A12／B12，每指标每侧 360 值；旧 A、v5 B、失败补采局部数据及所有诊断数值均不参与统计。历史基线原始输出仅提供固定内容／请求／TLS／取消／错误／回收的正确性契约。全部完成后，对每项同时运行原 IID 与整进程簇 bootstrap；两者都 pass 才通过，任何核心不确定或回退均触发已登记停止规则。保持原 seed 1729、4000 次、95% CI、5% 及原近零资格。单个生产者／证据／回收失败即停止后续执行，不做局部比较。

最小新工具适配只涉及来源、前置及新 12 簇汇总，原 `producer.py` 与 `cluster.py` 字节保持。工具清单 SHA-256 为 `2ed060eac5ade80015500ba384ea6956a5b2a41bd1f12a012b23054977c44394`。42 项解析／来源／统计校准与 7 项真实短进程控制通过，12 个记录身份全部消失；校准结果 SHA-256 为 `192aaea1736be1e8683e264d7c3f6c7b9a56522a34bea95c55b83e390ed10452`。父级校准检查和正式启动门限见 `artifacts/core-v6-calibration-parent-review.json`、`artifacts/core-v6-supplement-parent-gate.json`。独立输入审查 SHA-256 为 `59734c2f4d6cb11c5a385182d8d1685ead0b354d5beaa08b308c50f71ffbbc17`；随后实际登记已通过源码、普通 ELF、成功功能回执、诊断处置、夹具、内核和有效环境核验。此时尚无新性能比较结论。

实际完整完成 49 个生产者，第 50 个 `r09-proxy-B` 在 Xray 第 8 次 cold 启动中退出 255；前 7 次完成原请求并回收。同一 Go 进程的 sing-box 和 Mihomo 子测试随后通过，但整套测试退出 1、没有输出完整 `LINUXPERF_JSON`。余下 22 个生产者未启动，没有 retry／resume，没有汇总前 49 个进程的性能分布，也没有比较目录。50 个私有会话均为空，50 个 Go 加外层 CLI 共 51 个 PID/start 身份独立复查已消失，无外层清理信号、清理错误或残留，最终输入核验通过。执行结果 SHA-256 为 `4fbc53a7117d32b96191eb48069e7c71a85d99e7d0298b623c733b7bc40b445e`，执行封印 SHA-256 为 `3f0d24a5158b092c04f1bd53bc3d0c2da2d600ba2d14c25dfdcd79d05058a239`；父级记录为 `artifacts/core-v6-failure-parent-review.json`。

失败内核为 PID 2387729、端口 45509，原日志在启动约 8 ms 后记录提前退出，但返回给调用方的是上一进程的 `started` 警告行。静态复核确认 Xray v26.2.6 的 `server.Start()` 失败会用 `fmt.Println` 将原因写到 stdout，再 `os.Exit(-1)`；项目真实启动仅接 stderr，stdout 被丢弃，因而可能遗漏原有端口冲突重试策略所需的错误文本。配置预检已接 stdout，不能代替真实启动捕获。这是已证实的错误反馈缺口，不是本次退出 255 属于 bind 冲突或由快速 SOCKS 探测引起的证明。正在隔离准备最小输出捕获修复及真实短进程回归，未增加重试次数、固定等待或跨栈回退。

v6 GUI 工具适配与独立静态审查已完成，交付为 `agents/frontend/v6-gui-prep/delivery.json`（SHA-256 `c49b08f913f5f51953498c551a8f5a799b1ae8c54a8e3b35c6732336ea28bc92`）。范围仍为 134 项配对／68 核心和 51 项候选 10000 专属必需指标，22 项校准仅编写、执行为 0。当前 IO 前置未通过，所以没有构建观测版、运行 GUI／时钟校准或新增 GUI 样本。独立静态报告为 `agents/database/v6-gui-static-review/REVIEW.md`（SHA-256 `65c2a7c985b19c44f4e7a7cee7dcbb7f37284236ea23ba43e20638a8283dd919`），不替代任何实际验收。



## v7：保留 Xray 原生启动输出

官方 Xray v26.2.6 固定 commit `12ee51e4bb1d02ece4ef4b7114efa2bcdc130995` 的 `server.Start()` 失败会写 stdout；原项目只接 stderr。产品修复仅在现有文件成功打开后增加 `cmd.Stdout = stderrFile`，复用同一描述符，不增加 pipe、读取 goroutine、重试上限、等待或跨栈回退。成功启动也会保存版本输出；无法打印原因的退出或日志文件打开失败并不因此得到完整诊断。原 exit 255 是否为端口冲突仍未确定。

隔离验证目录 `agents/proxy/xray-startup-output-fix/validation-1`：原 v6 加新测试的负对照按预期 exit 1，三个涉及 stdout 的子项失败、两个 stderr 子项通过；修复后的普通／race 各有 3 顶层及 5 子项通过，无 skip。真实 preflight/run 进程共 30 个，连同 7 个命令生产者的原 PID/start 均独立复查已消失，无强制清理、清理错误或残留。新测试通过实际启动入口检查当次错误、原 bind 分类、非 bind 拒绝、双流保留、预检隔离和产品已 Wait。该验证不是原内核故障复现，也没有性能样本。

验证 summary SHA-256 为 `58632b640ee49800a5ac04405c92d4fcf3a734d77f030917b7d66ad661d2a7a6`，交付封印为 `c13e8162829a7f8e592042bc80920ce94d8a90d1f45dd22b043d828d1e888937`。父级复核为 `artifacts/xray-output-validation-parent-review.json`；首次只读审计误要求预期失败负对照没有 primary_error，修正为精确匹配 `Producer exited with code 1` 后通过，原回归没有重跑或改写。独立静态报告 `agents/database/xray-output-fix-static-review-20261111/REVIEW.md` SHA-256 为 `f4bd2275eab42b320c34bab2cba0cb2b34fa151109c81a37f9381a279d6cced6`。

整合只改变 `backend/internal/proxy/xray_bridge_launch.go`，新增 `backend/internal/proxy/xray_bridge_output_linux_test.go`；其他 v6 输入逐项保持。新 `candidate-v7` 的 880 项源码哈希为 `f33ca85615ce61d1e55b6079c8cc6e83b3e78ab9e53c2fac33c7c80c94bb0b44`，源码记录 SHA-256 为 `ef9dfc9faace584727ceef923bb95943cc40389ebe80d57239a3229eeb7d211d`。完整验证在新目录 `functional-v7` 完成：13 条生产命令及外层全部 exit 0，普通／race 各 692 项必需 Go 顶层、1009 个通过事件；原 7 项非必需可选 skip 仍逐项记录。23 项前端测试、12 项仓库及 269 项性能 Python 检查通过，启动队列 2 项各 10 次 race 和桥接链路检查通过。新普通 amd64 ELF SHA-256 为 `797b98f6b16497433f7165716cb94ca8d00e43b8d59d4f232282194202e42613`，不含观察入口；最终源码与共享工作区及冻结清单一致。summary SHA-256 为 `ee43a903bf94cdb45e42a387991abaa4c22121aa0131441c59bfe8fd32300c5e`，外层记录为 `f6a2f97c17614a06e2bf5ddbdbe5494e19410e67ee03c5a4486aa274e4ed006e`。父级复核 25 个记录身份均已消失；4 个已退出的零状态子进程经已校准功能守护器 Wait 回收，无强制清理或残留。

新 `.codex/artifacts/performance-v7-only.patch` 包含 98 个路径，SHA-256 为 `4dd7c7a5205e3d96e9f0e14080fd66375c1a6e0c3050d1ada0aa64ce27f53f3b`；在隔离副本实际正向应用／哈希核对／反向应用／基线核对均通过，反向恢复保留原 19 项用户 Linux 修复的基线。

新的 IO／GUI 工具起初仅作版本输入适配。父级一次性处置 `artifacts/xray-v7-repair-disposition.json`（SHA-256 `386a7b3a780d6efbcd169c151032c875a911e5962edde45d6939e9120bb31269`）绑定 11 类、28 份真实输入／修复／功能／历史失败证据，允许工具门限通过后执行一次新 A12/B12 的完整 45 项层；该次执行现已完成，结果见下文。这不是性能通过结论，也不授权重复失败批次或追加到通过。GUI 仍未执行。


## v7 固定新层：完整采集完成，性能门限未通过

`agents/database/v7-core-supplement-prep` 交付封印 SHA-256 为 `77b599108a39f59c791d51255410389c1e33a21c326892922b0c8a87a5a9efe8`，tooling 为 `088395a3940458eda7e3d278b603781c189a3d2298e129a30744aaa7fee4aa2f`。采样／统计主体仅换版本标签，原严格 producer、整进程簇比较、7 项进程控制及验证入口逐字保留。新增 v7 来源／driver／处置／失败历史拒绝校准，总计实际 47 项合成检查和 7 项真实进程控制均通过，12 个记录身份独立复核已消失；校准结果 SHA-256 为 `af36ecaa8c1b0b5a65b26ed04ad7927adb1278c60f0e35f02d3783d07d647251`。这些是方法校准，没有产品性能样本。

独立静态审阅 `agents/proxy/review/v7-repair-and-core-inputs/REVIEW.md`（SHA-256 `a9ae17741787cd5a02fa7251cbf3afe78be8f121f52bb80e3457f2bbdf951807`）未见限定范围内的阻断。父级另实际完成输入复制、功能回执、全部来源／工具／内核及环境核验，A／B 分别 325／336 个选定文件。正式目录 `agents/database/v7-core-supplement-run1` 的登记 SHA-256 为 `a97939c3c80851c243a81d45cc021ea5817d6651ccc1c99ddbd5f02da6a8bcf8`，登记封印为 `7ca01b2618b90c3c0bcf5a054b836370c49686008b65e11ce17bc2c762dfcb8b`；父级放行记录 `artifacts/core-v7-supplement-parent-gate.json`。

仍为新 A12/B12、72 个生产者，每轮 proxy→download→archive，奇数 AB／偶数 BA，每个进程每项 30 值。全部 45 项（37 核心、8 分配观察）完成后才按预登记 IID 与整进程簇双视图比较；任一核心非 pass 即触发停止条件，没有自动追加。全部 72 个生产者及外层采集实际 exit 0，最终来源复查通过；72 个私有进程会话为空，73 个外层／Go 生产者 PID/start 身份均已消失，无强制清理、清理错误或残留。执行结果 SHA-256 为 `1c2087b8c17e37d0d26442b228eb8a2c95c3ba6211e0d43418a8fde179b6f1f9`，执行封印为 `b534650cd7dd9a080b4985ddd7772a9271b9d522ec94250f50f8f47aba438fe7`。旧 v5/v6 部分样本、F/C 与 d2 诊断均不混入。

GUI 新准备目录 `agents/frontend/v7-gui-prep` 已静态收口，prepared seal SHA-256 为 `245af6a4587be1f1041959fdfe7e017390987602ebce3183f092fecc8ba24018`，draft 为 `eaeff70741101d62b47fd112e2d9fcd3a0fdc597cf760186c1cf9d5284aa8712`。父级审阅了 v6→v7 差异、真实功能来源和封印；7 个代码文件逐字不变，12 个 Python 文件只做 AST，22 项校准执行为 0。仍缺匹配观测版／seed 构建、IO37 全过、真实小 GUI 集成及方法校准，不可运行正式 GUI。


## v7 新层结论与当前停止条件

比较产出有效、完整的数值结果，并因核心不通过实际 exit 1；没有 `comparison/failure.json`。37 核心为 **26 pass、2 regression、9 inconclusive**；8 项分配观察均 pass。每指标每侧为 12 个独立生产进程／360 个值，固定 IID 与整进程簇两视图，不含历史样本。比较 SHA-256 为 `ad5712de39366c65ab87753ca3d0e2001b39b645238cd7f33ba3a95eff00e637`；完整 45 项中位数、p95、两种 CI 和判定见同目录 `2026-11-11-linux-performance-v7-metrics.csv`。

| 指标 | 基线中位数（ms） | v7 中位数（ms） | 变化 | 整进程簇 95% CI | 结论 |
|---|---:|---:|---:|---|---|
| proxy.xray.cold_ready_ms | 120.22224 | 29.57673 | -75.40% | [-75.53%, -75.10%] | pass |
| proxy.sing-box.cold_ready_ms | 119.47037 | 28.77460 | -75.91% | [-76.06%, -75.59%] | pass |
| proxy.mihomo.cold_ready_ms | 122.14033 | 122.07614 | -0.05% | [-0.14%, +0.01%] | pass |
| download.download_compressible_stream_wall_ms | 3.99099 | 5.75979 | +44.32% | [+21.62%, +58.25%] | regression |
| download.download_compressible_stream_cpu_ms | 6.36400 | 7.68450 | +20.75% | [+8.93%, +34.50%] | regression |

Xray 和 sing-box 的冷就绪收益在两个统计视图均稳定。Mihomo 冷就绪的整簇改善 CI 包含零，所以虽满足回退门限，不计作稳定收益；此前撤回的快速 TCP 探测没有恢复。正式下载流式可压缩样本的 wall/CPU 在两个视图均明确退化。已有静态及诊断显示相应下载实现／夹具未改，当前回退具体成因仍未确定；代码相同不能用来否定本次结果或宣称仅为噪声。

| 尚不确定的核心指标 | 中位数变化 | 整进程簇变化 95% CI |
|---|---:|---|
| proxy.mihomo.reuse_ready_ms | -6.27% | [-38.31%, +53.26%] |
| proxy.mihomo.reuse_request_ms | +0.71% | [-15.08%, +21.24%] |
| download.download_compressible_range_wall_ms | -3.67% | [-14.69%, +7.02%] |
| download.download_incompressible_range_wall_ms | -3.27% | [-10.24%, +6.42%] |
| download.download_incompressible_range_cpu_ms | -0.50% | [-9.16%, +8.93%] |
| download.download_incompressible_stream_wall_ms | +1.29% | [-20.75%, +33.49%] |
| download.download_incompressible_stream_cpu_ms | +0.38% | [-13.21%, +19.61%] |
| archive.snapshot_compressible_zip_wall_ms | -4.72% | [-34.12%, +11.28%] |
| archive.snapshot_compressible_zip_cpu_ms | -6.88% | [-35.04%, +9.09%] |

该固定补采后仍有核心不确定，已触发当前目标 Stop Conditions 的“增加样本后仍无法可靠判断”条款，以及本批次事前登记的核心非 pass 停止规则。当前停止追加采样，不启动 GUI／真实实例新验收，不改变 5% 门限。Goal 未完成；下一步需在完整证据及归档就绪后确认方向，不能凭冷启动收益覆盖其他回退、未完成 GUI 或历史失败。

## v7 独立审计、交付与待决方向

独立审计目录为 `agents/database/v7-core-audit-20261111`，报告 SHA-256 为 `e180f9d2c2a5cd8778cb160d234eab6bd47e4d5e9ea2ba1d4c63b0b6931de9e0`，审计封印 SHA-256 为 `7d8fe206b9013cd10e7238e2dd33743e040e490052593432980d9d14c37e7528`。72 个生产者的 144 份原始流重新解析，共 336 个测试通过事件及 72 个包通过；45 项指标的 A/B 各 12×30 个值逐项吻合，合计 32,400 个值，历史样本为零。全部中位数、p95、变化率、近零下限、5% 门槛以及由封印 CI 推导的双视图判定均核对一致。审计没有重新运行采样、构建或 bootstrap；CI 端点核对封印与方法，不声称独立重算随机重采样分布。

审计结论没有改变性能失败。登记、采集、比较三个 CLI 加 72 个 Go 生产者，共 75 个已记录 PID/start 身份消失、75 个私有会话为空，无强制清理。各内核的正常退出仍由完整原夹具断言支持，不虚报未独立记录的内核 start 身份。compare 的真实 exit 1、空 stderr、数值失败 stdout 与封印一致；该退出是有效性能失败，不是允许重跑的工具故障。

当前交付是共享工作区中的 v7 改动、98 路径可逆补丁、完整功能验证和未通过的性能报告。没有提交、推送、改版本或发布。下一阶段建议保持现有 Linux 范围和 5% 标准，先对流式可压缩下载的 CPU／耗时及进程间波动做有界归因，再按证据修复或撤回相关候选；在变更与样本方案预先固定后，才开始下一次正式测量。旧失败和当前全部样本持续保留，不能反复补采至通过。

仅当相关 IO 核心指标全部通过且无其他停止条件时，才进入待执行的 v7 或后继版本 GUI／实例／10,000 条目录验收；原 68 项 GUI 核心、2 项日志核心和 32 个辅助 Go 基准的适用证据仍须逐项收口，不能把当前 37 项 IO 子集当作整个目标。

新增工作根将完整持久归档至 `/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-11-linux-performance-v7/evidence.tar.zst`，不排除失败记录。此段为归档前快照；最终归档状态、完整性、逐文件内容比对、成员清单及 SHA-256 以归档目录的 `archive-status.json` 为准。只有实际全部检查通过后，仓库报告才追加完成回执。

## 持久归档完成回执

2026-11-11 17:36:52（Asia/Shanghai），归档主进程正常 exit 0。完整保存 227,703 个普通文件、10,063,153,687 字节源内容，归档成员 285,110 项，无排除项、重复或额外成员。压缩完整性、tar 对源文件逐项内容比对、精确成员集合、归档前后源目录清单一致性均通过，四个实际归档命令全部 exit 0。

- 归档：`/home/czc/.local/share/codex/performance-evidence/Ant-Browser/2026-11-11-linux-performance-v7/evidence.tar.zst`
- 大小：2,077,878,648 字节。
- SHA-256：`1e97eefcea82294edc66050e404a880d060197f2745e51e9ebfe864057b03cce`。
- 实际过程与完成回执：同目录 `archive-status.json`；最终交付绑定：`archive-final-receipt.json`。

归档内 `handoff-v7-stop/report-prearchive.md` 保留归档前报告，最终仓库报告另存为持久目录的 `report-final.md`，由最终回执绑定；没有在归档后修改归档内部文件来伪造自包含哈希。源工作根在归档期间保持无写入，所有子代理已完成。归档成功仅表示证据已持久保存，Goal 仍为 active 且未完成，等待用户决定下一阶段方向。
