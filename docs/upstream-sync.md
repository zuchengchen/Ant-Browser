# Fork 开发与上游同步

本项目维护自己的 `master`，通过本地 merge 集成 `black-ant/Ant-Browser`，直接推送到 `origin/master`，不需要同步 PR。日常功能开发与同步上游分开提交，发布由本项目独立决定。

建立本流程时，本地、origin/master 与 upstream/master 的共同基线为 `7d51fb087e6e024a0b66367e90d46779ceb58d16`，上游应用版本为 `1.9.0`。后续基线记录在同步 merge commit 的 `Upstream-Commit` 中。

## 新工作副本的配置

Git remote 和本地 Git 配置不会随源码克隆传播，新工作副本需要执行：

```bash
git remote add upstream https://github.com/black-ant/Ant-Browser.git
git config --local remote.upstream.tagOpt --no-tags
git config --local remote.pushDefault origin
```

已有 `upstream` 时先检查 `git remote -v`，不要重复添加。`origin` 应指向自己的 fork；`master` 跟踪 `origin/master`。

## 查看与合并

只检查上游更新（允许工作区有未提交修改，不修改工作区或当前分支）：

```bash
bash scripts/sync-upstream.sh --check
```

优先在上游正式版本发布后同步。可以显式选择完整标签 ref 或完整提交 SHA，例如：

```bash
bash scripts/sync-upstream.sh --check --ref refs/tags/v1.9.0
bash scripts/sync-upstream.sh --ref refs/tags/v1.9.0
```

标签名为示例，以实际存在的上游标签为准。脚本把选定标签获取到 `FETCH_HEAD`，不创建本地发布标签。

同步上游最新 `master`：

```bash
git switch master
git pull --ff-only origin master
bash scripts/sync-upstream.sh
```

脚本要求工作区干净且本地已包含 `origin/master`。它获取远端、展示上游变化，并用 `--no-ff --no-commit` 准备合并。已经包含目标提交时直接结束。脚本不执行构建测试、不自动提交或推送。

检查 `git status`、`git diff --cached`，解决冲突并逐项 `git add <文件>`。不得以全量覆盖的方式丢弃本项目定制。需要取消未完成的合并时使用 `git merge --abort`。

## 验证与完成合并

根据变更执行验证；涉及后端或依赖时运行 Go 测试，涉及前端时运行生产构建。完整同步默认执行两者：

```bash
(cd frontend && npm ci && npm run build:clean)

# Linux 仅安装 WebKitGTK 4.1 时需要此 build tag；其他平台使用 go test ./...
go test -tags webkit2_41 ./...

git diff --check
git diff --cached --check
```

运行 Go 全仓库测试前需有 `frontend/dist` 静态资源；首次克隆时先完成前端安装与构建。Wails/GTK 等平台依赖按现有开发文档安装。涉及启动、测速、真实连通性、IP 健康、预热或代理下载时，还需验证 Xray + sing-box 组合栈与独立 Mihomo 栈的对应路径，不跨栈回退。

验证通过后：

```bash
git commit --no-edit
git push origin master
```

提交消息包含上游仓库、ref 和完整 SHA。可以用 `git merge-base --is-ancestor <该SHA> master` 确认上游提交已进入主线。不强推 `master`，不同步上游标签，也不创建同步 PR。

修改同步脚本或版本解析器后，执行 `python3 -m unittest discover -s scripts/tests -v`。测试使用临时本地 Git 仓库验证冲突处理、历史保留和标签隔离，不访问 GitHub。

## 独立发布

- 应用内更新检查、项目链接与下载入口指向 `zuchengchen/Ant-Browser`；保留上游来源和许可证说明。
- 发布标签固定为 `czc-vX.Y.Z`，例如 `czc-v1.9.0`。上游 `v*` 等标签不会触发这套发布工作流。
- 包内版本保持数字 `X.Y.Z`。发布前更新 `wails.json`、`frontend/package.json` 与 `frontend/package-lock.json`（包括 `packages[""].version`），提交这些变更。`scripts/resolve-release-version.py` 在三个平台检查版本与标签是否一致。
- 本项目版本独立递增，发行说明单独注明集成的上游版本与提交。不要复用或移动已有发布标签。
- 推送单个自有标签触发 Windows、Linux 和 macOS 工作流；手动 `workflow_dispatch` 只构建产物，不创建 Release。其可选 version 输入是版本一致性检查，不会覆盖源码内嵌版本。
- 工作流通过 `GH_REPO` 明确将 Release 上传到当前仓库。常规发布使用主线的真实提交，不运行 `tools/public-release/` 的聚合快照发布流程。

验证版本并在已通过测试的提交上发布（按实际版本替换示例）：

```bash
python3 scripts/resolve-release-version.py --ref-type tag --ref-name czc-v1.9.0
git tag -a czc-v1.9.0 -m "Fork release 1.9.0; upstream: <版本及完整SHA>"
git push origin refs/tags/czc-v1.9.0
```

同步代码与发布是两个独立操作；同步脚本不会创建或推送标签。
