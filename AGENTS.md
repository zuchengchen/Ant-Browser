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

## Fork 开发、上游同步与发布规则

本节适用于整个仓库，放在共享规则自动生成区域之外。后续同步上游时保留本节，并按需要整合上游规则。

- `origin` 是本项目 `zuchengchen/Ant-Browser`，`upstream` 是 `https://github.com/black-ant/Ant-Browser.git`。`master` 是包含上游代码和本项目定制功能的产品主线；默认推送到 `origin`，不向 `upstream` 推送。
- 日常独立开发和上游同步都不要求创建 PR，不增加同步 PR 或强制 PR 审批流程。提交、推送与发布仍按用户当前任务的授权范围执行。
- 上游同步入口为 `bash scripts/sync-upstream.sh`，只查看更新用 `--check`；按正式版本同步用 `--ref refs/tags/<上游标签>` 或完整提交 SHA。默认目标为上游 `master`，优先按上游正式版本安排同步。
- 合并前必须处于干净的 `master`，没有未完成的 merge/rebase/cherry-pick，且已包含 `origin/master`。脚本使用 `merge --no-ff --no-commit`，不自动提交、推送、发布或创建 PR。
- 保留完整的上游合并历史和本项目定制；不通过 reset、强推、squash、重写已共享主线历史或整批选择 ours/theirs 来完成同步。冲突逐项处理，无法确认语义时保留现场并说明问题。
- 合并提交保留脚本生成的 `Upstream-Repository`、`Upstream-Ref`、`Upstream-Commit` 信息。检查变更、解决冲突并通过适用的 Go 测试和前端构建后，用 `git commit --no-edit` 完成合并，再按任务授权 `git push origin master`。无新提交时不制造空同步提交。
- 每次同步检查本项目维护的差异：本节规则、同步脚本、更新源、发布标签及解析、发布仓库参数，以及既有两套代理连接栈约束。涉及代理路径的变更不得引入跨栈自动回退。
- 上游 fetch 默认使用 `--no-tags`。需要某个上游标签时仅显式获取该 ref；不把上游标签批量复制到本项目，不使用 `git push --tags` 或 `git push --mirror` 做同步。
- 本项目更新源和产品链接指向 `zuchengchen/Ant-Browser`。自有发布标签使用 `czc-vX.Y.Z`，只发布明确选定的标签；不覆盖已经发布的标签。包内版本为数字 `X.Y.Z`，必须与 `wails.json`、`frontend/package.json`、`frontend/package-lock.json` 的根版本及根 package 版本一致。
- 上游基线与本项目发行版本分别记录；同一上游基线需要再次发布时递增本项目版本，并在发行说明注明上游版本/提交。合并上游不自动打标签或发布。
- 本项目直接从已验证的主线提交构建发行包。`tools/public-release/` 是上游遗留快照工具，不作为本 fork 的常规同步或发布入口，避免聚合快照取代真实的合并历史。
- 详细操作及验证命令见 `docs/upstream-sync.md`。新增开发保持提交边界清晰，优先用独立模块和少量接入点承载定制，减少后续上游合并冲突。
