# 从 KeyPouch 打开 Ant 并登录 GPT 的验收记录

2026-11-21 在隔离的 Linux 测试环境验收 KeyPouch 1.3.0 和 Ant 1.9.4-7。测试使用本地 KeyPouch、两个不同的合成邮箱、真实 Ant 桌面和 Chromium 157 的工具栏。GPT 与认证页面由合成站点响应。生产部署和真实账号登录留待上线后执行。

## 两次点击及离线复用

| 操作 | 实际结果 |
| --- | --- |
| 在 KeyPouch 配对电脑，依次从记录 A、B、A 打开实例 | 桌面和网页短码匹配。三次均显示窗口打开且本次凭据已同步。A 复用原实例，B 使用自己的实例。 |
| 在 A、B 的实例分别点击工具栏「登录 GPT」 | 每次从未登录的测试会话开始，所选邮箱、完整密码及独立计算的当期验证码均匹配。账号页显示对应邮箱。 |
| 关闭测试 key 服务，重启 Ant 和实例，再从 A 的工具栏登录 | 本机使用此前同步的凭据完成登录。 |
| 在来源 A 创建两个克隆 | 勾选的克隆在 key 关闭后通过工具栏登录。未勾选的克隆没有本地 GPT 记录。 |
| 在独立安装导入设置了迁移密码的实例包 | 两套安装的本机密钥不同。导入后从工具栏完成登录。普通实例包导入后没有本地 GPT 记录。 |

五次工具栏操作每次各提交一次邮箱、密码、动态码，并核对一次最终身份。五次均无到 KeyPouch 的请求。隔离驱动观察网页实际产生的外部导航并将同一 URI 交给真实 Ant 可执行文件，系统默认协议处理器的选择弹窗由驱动代为处理。

## 凭据与备份

- 使用本次生成的合成密码及种子，检查了实际网页深层链接、票据状态、旧客户端的签名领取响应，以及 24 个配置、回执和日志文件。旧协议只返回预期元数据，领取凭据的请求被拒绝，其窗口回执可正常提交。
- 对完整备份、普通实例包和迁移密码实例包逐条解包，分别核对了 1299、249、250 个文件，含来源实例的普通配置。三份归档均通过密码、种子、本机密钥和本机密文检查。用正确的迁移密码解密受保护条目后，站点、记录 ID、邮箱、密码、种子全部匹配。
- 错误迁移密码和损坏密文均被拒绝，覆盖目标原有凭据完整。完整备份通过正常恢复入口导入到全新的临时安装，恢复了 9 个实例，并逐项核对了 8 个普通文件和 236 个 Chromium 文件。新安装未创建 GPT 凭据库或本机密钥。
- 全量备份回归在修复前实际触发过普通实例数据缺失。修复后，跳过会话标记文件时会继续遍历同目录的普通文件。恢复器也能根据内置清单及来源识别旧版「隔离自检 Pro」扩展的逻辑 ID。

## 构建与测试

| 套件 | 通过 | 跳过 |
| --- | ---: | ---: |
| key Go 测试及子测试 | 207 | 0 |
| key Playwright 场景 | 76 | 0 |
| Ant Go 测试及子测试 | 1327 | 9 |
| Ant 扩展 Node 测试 | 38 | 0 |

key 完整 CI 覆盖类型检查、Go vet、竞态测试、静态发行构建、加密自检和 Playwright。Ant 已重新安装前端依赖、干净构建、运行完整 Go 测试和扩展测试，并用包含同一套前端资源的生产模式 Wails 可执行文件联调。Ant 的 9 项跳过测试依赖外部代理、真实账号/数据库或测试子进程，以下验收用例全部实际运行。

<details>
<summary>9 类验收对应的具体用例，合计 61 个独立测试</summary>

**兼容旧端及现有票据**

- key_go `TestOpenReceiptIsIndependentAndRequiresAnOpenedForegroundWindow`
- key_go `TestAntOpenCredentialsRequireOptInDeviceAndPinnedProfile`
- key_go `TestAntInstanceUpgradeFromV19AndReopenWithV19`
- key_go `TestAntRuntimeMigrationResumesOldBindingsWithoutInventingState`
- ant_go `TestKeyPouchSavedSiteOpensWithoutRestartAndPreservesLegacyMapping`
- ant_go `TestGPTStaleDebugPortFileCannotAdoptAnotherProcess`
- key_web `打开回执保留历史，本机关闭、重开和断联都更新记录详情`

**设备签名、领取意图与单记录/单实例范围**

- key_go `TestAntPairingOwnershipSignaturesClaimsAndRevocation`
- key_go `TestAntOpenCredentialsRequireOptInDeviceAndPinnedProfile`
- key_go `TestAntOpenCredentialsConcurrentFirstReadsBindExactlyOneProfile`
- key_go `TestAntInstanceInvalidationGroupRecordExpiryAndRevocation`
- ant_go `TestDevicePairsAndSignsNativeRuntimeTransitions`
- ant_go `TestLinksOnlyAcceptTheTwoNativeActionsAndAnOpaqueTicket`
- key_web `同一记录的两台设备分别创建，第二台不会默认领用第一台`

**窗口与凭据分开回执、重试与重复操作**

- key_web `打开与 GPT 凭据回执分开显示，保存失败后可重试`
- key_web `模板缺失可重试，失败结果与已启动的旧同步任务互不影响`
- key_go `TestOpenReceiptIsIndependentAndRequiresAnOpenedForegroundWindow`
- key_go `TestAntOpenCredentialsRequireOptInDeviceAndPinnedProfile`
- ant_go `TestKeyPouchOpenResolvesStableProfileAndKeepsTheTemplateInPlace`
- ant_go `TestKeyPouchFocusRequiresARealPageActivation`
- ant_go `TestKeyPouchOpenCreatesOneWindowForBackgroundChromium`
- ant_node `a lost response after a submit cannot submit the same stage again`

**轮换、清空、撤销及中途更改**

- key_go `TestAntOpenCredentialsRotationClearingAndRevocation`
- ant_go `TestKeyPouchCredentialRefreshBindsRecordAndClearsRemovedFields`
- ant_go `TestLocalGPTCredentialEncryptedBoundAndRenewed`
- ant_go `TestLocalGPTCredentialConcurrentSnapshots`
- ant_node `refreshing or clearing this instance during an in-flight login stops the old attempt before the next submission`
- ant_node `local metadata hides secrets, and each login reads a complete fresh snapshot of the same site and record`

**未同步实例的原插件授权与登录会话**

- key_go `TestGPTPluginGrantConsentOwnershipAndRebinding`
- key_go `TestGPTPluginSignedRequestsAndMinimalSecrets`
- key_go `TestGPTPluginGrantIdentityChangesAndCredentialRotation`
- key_web `明确授权当前账号给指定插件，可按实例指纹撤销`
- key_web `切换记录会清空授权码并丢弃旧记录的延迟响应`
- ant_node `an unsynced toolbar action keeps the existing KeyPouch connection flow`
- ant_node `uses the profile session with a credentialed read-only request; 401 allows sign-in and refresh`
- ant_node `a paired account requests a fresh code only for its authenticator challenge`

**本地加密、目标实例、扩展身份及工具栏手势**

- ant_go `TestLocalGPTCredentialEncryptedBoundAndRenewed`
- ant_go `TestGPTCredentialDestinationMatchesBundledExtension`
- ant_go `TestKeyPouchExtensionFollowsDefaultAndProfileOverrides`
- ant_go `TestGPTCredentialDeliveryChecksProfileAndUnpackedExtension/ok`
- ant_go `TestGPTCredentialDeliveryChecksProfileAndUnpackedExtension/different_profile`
- ant_go `TestGPTCredentialDeliveryChecksProfileAndUnpackedExtension/different_extension`
- ant_go `TestGPTSharedProfileCannotReuseOrStopCredentialedRuntime`
- ant_go `TestGPTStaleDebugPortFileCannotAdoptAnotherProcess`
- ant_go `TestGPTRecoveryClearsMarkedSessionBeforeRegisteringProcess`
- ant_go `TestGPTDeliveryMarksDirectoryWithoutRecordingCredential`
- ant_node `desktop handoff and toolbar apply the same record identity checks`
- ant_node `toolbar clicks can be consumed once, only by their own extension top-level accounts tab and before expiry`
- ant_node `restoring or reloading the accounts page just shows local metadata. A toolbar click in its own tab starts exactly one login`

**登录页边界、其他账号、缺失字段及人工挑战**

- ant_go `TestKeyPouchCredentialRefreshBindsRecordAndClearsRemovedFields`
- ant_node `absent local storage, malformed snapshots and read failures are distinguished without exposing input`
- ant_node `one normal email, password and authenticator submission, then verify the visible settings email`
- ant_node `a combined password form uses the selected email, while an existing other session is retained`
- ant_node `login stops at third-party, insecure, wrong-port or opaque documents`
- ant_node `a different current email, unsafe actions, other verifications and disabled auto-login forms never receive a secret`
- ant_node `a post-fill change to the form destination is rechecked before submit`
- ant_node `an email step alone cannot authorize a password form that does not identify its account`
- ant_node `the entire displayed Unicode address must match before any password reaches the document`
- ant_node `missing or invalid KeyPouch TOTP seeds leave the authenticator input for the user`
- ant_node `a replaced inspected document is never injected by tab ID with its credentials`

**显式克隆、独立更新及实例生命周期**

- ant_go `TestLocalGPTExplicitCloneAndRecycle`
- ant_go `TestKeyPouchOpenResolvesStableProfileAndKeepsTheTemplateInPlace`

**加密迁移、错误/破损不覆盖、旧包、普通包与全量备份**

- ant_go `TestLocalGPTCredentialArchivesAndMigration`
- ant_go `TestPortableProfileSessionRoundTrip`
- ant_go `TestBackupRepairRecognizesLegacyBundledExtensionSource`
- ant_go `TestPortableProfileSessionPasswordValidation`
- ant_go `TestOpenProfilePackageContentsDetectsPortableSession`
- ant_go `TestProfilePackageV1OverwriteMovesTargetToTrash`

</details>

## 交付

KeyPouch 本次提交 `e3e27a61cb943aef7daf90ebd66a7207608930e7` 已推送至 `origin/main`。重新执行 `git fetch --no-tags origin main` 后，祖先关系检查成功。Ant 1.9.4-7 保存在当前本地工作区。

升级时先按 KeyPouch 文档备份服务数据并部署 1.3.0，再升级 Ant 并配置 KeyPouch 的 HTTPS 根地址。现有配对继续使用。

- [KeyPouch 操作、升级与回退](/home/czc/projects/working/KeyPouch/docs/ant-browser.md)
- [Ant 配置与登录说明](/home/czc/projects/working/Ant-Browser/README.md#在-chromium-实例中登录-gpt)
- [已确认的目标](/home/czc/projects/working/Ant-Browser/.codex/goals/2026-11-21-keypouch-ant-gpt-login.md)
