# Project Agent Instructions

<!-- ant-ready-start-skills:start -->

## Shared Local Skills

Use the shared local skills below when the task matches their scope:

- `page-style-linear-flow`: `D:\code\open_source\ant-ready-start\skills\page-style-linear-flow\SKILL.md`
- `ui-ux-pro-max`: `D:\code\open_source\ant-ready-start\skills\ui-ux-pro-max\SKILL.md`
- `frontend-skill`: `D:\code\open_source\ant-ready-start\skills\frontend-skill\SKILL.md`
- `create-plan`: `D:\code\open_source\ant-ready-start\skills\create-plan\SKILL.md`
- `create-plan-doc`: `D:\code\open_source\ant-ready-start\skills\create-plan-doc\SKILL.md`

Apply it for frontend page design or refactors involving pages, admin panels, forms, tables, dashboards, detail views, wizards, modal/drawer placement, or multi-step flows.

Apply `ui-ux-pro-max` for broader UI/UX design work involving visual direction, design-system shaping, palette and typography selection, component styling, landing pages, dashboards, and cross-stack interface generation when linear-flow rules alone are not enough.

Apply `frontend-skill` when the task needs stronger frontend art direction, visual hierarchy, landing-page composition, sparse premium layouts, image-led sections, or restrained motion design.

Apply `create-plan` when the user explicitly asks for a plan, task breakdown, implementation roadmap, rollout outline, or a step-by-step execution plan before coding.

Apply `create-plan-doc` when the user explicitly asks for a plan that should also be saved into the repository as a markdown document under `docs/plan`.

Core expectations:

- Keep each page focused on one primary responsibility.
- Do not mix operational tables and submit forms on the same screen.
- Use modal/drawer for short low-risk forms; use a dedicated page or wizard for complex flows.
- Remove filler copy, repeated headings, decorative cards, and meaningless whitespace.
- Keep the next action obvious and preserve predictable back/cancel/save behavior.
- 代理连接必须遵守两套连接栈规则，详见 `docs/proxy-connector-stacks.md`：`browser.default_connector_type=xray` 表示 Xray + sing-box 组合栈，Xray 负责 vmess/vless/trojan/shadowsocks/链式代理等，sing-box 负责 hysteria2/tuic/anytls 等协议；`browser.default_connector_type=mihomo` 表示独立 Mihomo 栈。实例启动、测速、真实连通性、IP 健康、预热和代理下载都必须按当前连接栈执行，不允许在 `xray` 组合栈和 `mihomo` 栈之间自动混用；不要把 sing-box 协议误判成“xray 不支持”。
- For detailed UI checks, selectively read `D:\code\open_source\ant-ready-start\skills\page-style-linear-flow\references\checklist.md`.

These shared skill instructions supplement project-specific rules in this `AGENTS.md`; keep more specific project rules authoritative for this repository.

<!-- ant-ready-start-skills:end -->

## 子代理并行协作

处理本项目任务时，主动将可独立推进的工作分配给子代理并行执行，尽量用满当前可用的子代理名额。

## Fork 开发、上游同步与发布规则

本节适用于整个仓库，放在共享规则自动生成区域之外。后续同步上游时保留本节，并按需要整合上游规则。

- `origin` 是本项目 `zuchengchen/Ant-Browser`，`upstream` 是 `https://github.com/black-ant/Ant-Browser.git`。`master` 是包含上游代码和本项目定制功能的产品主线；默认推送到 `origin`，不向 `upstream` 推送。
- 每次完成仓库修改并通过相关检查后，只暂存本次任务的改动，自动执行 `git commit`，再用 `git push origin HEAD` 推送到 `origin` 的同名分支，无需另行确认。
- 日常独立开发和上游同步都不要求创建 PR，不增加同步 PR 或强制 PR 审批流程。打标签和发布仍按用户当前任务的授权范围执行。
- 上游同步入口为 `bash scripts/sync-upstream.sh`，只查看更新用 `--check`；按正式版本同步用 `--ref refs/tags/<上游标签>` 或完整提交 SHA。默认目标为上游 `master`，优先按上游正式版本安排同步。
- 合并前必须处于干净的 `master`，没有未完成的 merge/rebase/cherry-pick，且已包含 `origin/master`。脚本使用 `merge --no-ff --no-commit`，不自动提交、推送、发布或创建 PR。
- 保留完整的上游合并历史和本项目定制；不通过 reset、强推、squash、重写已共享主线历史或整批选择 ours/theirs 来完成同步。冲突逐项处理，无法确认语义时保留现场并说明问题。
- 合并提交保留脚本生成的 `Upstream-Repository`、`Upstream-Ref`、`Upstream-Commit` 信息。检查变更、解决冲突并通过适用的 Go 测试和前端构建后，用 `git commit --no-edit` 完成合并，随后执行 `git push origin master`。无新提交时不制造空同步提交。
- 每次同步检查本项目维护的差异：本节规则、同步脚本、更新源、发布标签及解析、发布仓库参数，以及既有两套代理连接栈约束。涉及代理路径的变更不得引入跨栈自动回退。
- 上游 fetch 默认使用 `--no-tags`。需要某个上游标签时仅显式获取该 ref；不把上游标签批量复制到本项目，不使用 `git push --tags` 或 `git push --mirror` 做同步。
- 本项目完整版本号为 `X.Y.Z-N`。主版本号 `X.Y.Z` 跟随实际合入 `master` 的上游三段版本号。首次合入某个上游版本时，本地子版本号（修订号）`N` 取 `1`。在同一上游版本上，每完成一次本地更新，`N` 加 1。一次更新可包含多个提交，重复构建、打包或安装同一更新均沿用原号。
- 合并上游时，将 `wails.json` 的 `info.productVersion`、`frontend/package.json` 的 `version` 和 `frontend/package-lock.json` 的 `version`、`packages[""].version` 对齐到 `X.Y.Z`。每次定版时，在仓库根目录的 `VERSION` 单独一行写入完整版本 `X.Y.Z-N`，并同步 `README.md` 的当前版本行。Arch/Manjaro 包分别使用 `pkgver=X.Y.Z` 和 `pkgrel=N`。
- 本项目更新源和产品链接指向 `zuchengchen/Ant-Browser`。现有 GitHub 发布流程使用主版本标签 `czc-vX.Y.Z`，发行说明记录完整本地版本、上游版本和提交。只发布明确选定的标签，不覆盖已发布的标签。合并上游不自动打标签或发布。
- 本项目直接从已验证的主线提交构建发行包。`tools/public-release/` 是上游遗留快照工具，不作为本 fork 的常规同步或发布入口，避免聚合快照取代真实的合并历史。
- 详细操作及验证命令见 `docs/upstream-sync.md`。新增开发保持提交边界清晰，优先用独立模块和少量接入点承载定制，减少后续上游合并冲突。
