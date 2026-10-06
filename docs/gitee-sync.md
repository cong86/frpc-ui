# GitHub 到 Gitee 自动同步

GitHub 为开发与发布来源；Gitee 为国内源码和 Console 下载镜像。源码自动同步采用用户配置的 Gitee 自带 Pull 镜像：`cong86/frpc-ui → wangcong886/frpc-ui`。

## 当前方案：Gitee 自带自动镜像

2026-10-07 用户确认已配置 Gitee 自带自动镜像，提供的管理页面显示 Pull 方向、源仓库 cong86/frpc-ui，以及最近完成时间 03:48:29。两端 main 提交和 preview.4 标签已核对一致。

后续实际验证：04:24 两端 main 自动到达 `572fb07`，该阶段没有人工推送 Gitee；Actions [preview.4 附件补同步](https://github.com/cong86/frpc-ui/actions/runs/37524072976) 成功。完整证据及网络重试边界见 [验证记录](gitee-validation.md)。

以后只向 GitHub 提交；Gitee 根据自身镜像配置同步代码提交、分支和标签。源码同步不需要 GitHub Actions 的 GITEE_TOKEN。官方说明的镜像触发最短间隔为五分钟，不能保证每次推送立即可见；以镜像管理页完成记录和两端实际提交为准。

Gitee 文档列出的镜像内容不含 Release 描述和二进制附件。发布附件由 GitHub `Sync Gitee` 单独处理，不把源码一致当作附件发布完成。

原 GitHub `Sync Gitee` 的源码 push 触发和 Git 推送步骤已移除，避免与用户选定的镜像方案重复传输。用户已授权配置 GITEE_TOKEN，仓库 Secret 元数据读取成功；GitHub 私人令牌仅用于本次内存中的 API 操作，未持久保存。没有修改用户的 Gitee 镜像设置或 Webhook URL。

依据：[Gitee 仓库镜像官方说明](https://help.gitee.com/repository/settings/sync-between-gitee-github)。

## Release 附件同步

自动或手动附件同步使用 repository secret `GITEE_TOKEN`，需能够读取仓库并创建 Release/上传附件。该 Secret 已确认存在；令牌不写入 URL、代码、Issue 或日志。

Token 配置须由用户授权；迁移本机已有凭据到 GitHub Secret 需要独立明确授权。凭据不进入仓库、URL 或日志。

- 成功的 `Preview release` 通过 workflow_run 触发附件同步，仅接受 main 分支的成功发布；从该发布提交读取版本。
- 手动补同步：Actions → Sync Gitee → Run workflow，`release_tag` 必须填已发布版本。
- 先校验凭据所属账号，再等待 Gitee 原生镜像标签匹配 GitHub 发布提交（最长十分钟）。标签指向其他提交时立即拒绝，不通过附件发布改写源码或标签。
- 下载完整 GitHub 附件，验证 SHA256SUMS 后上传 Gitee 可接受的六个附件。未完成镜像中的已有文件逐字节校验后才续传，不同则拒绝覆盖。
- CI 使用显式 `--manifest-marker` 模式：若最后上传的 SHA256SUMS 已存在，必须六个附件名齐全且清单与 GitHub 原始字节一致，才认定该发布已完成并跳过。日志明确不复查既有归档内容，不能当作当前远端文件的逐字节审计。该模式避免海外 runner 重复下载较大的 Gitee 附件超时；原版清单仍必须匹配。
- 本机严格复查：不加 `--manifest-marker` 执行相同 `--tag/--assets-dir` 命令，会下载并校验全部已有附件。下载端安装器始终强制核对实际归档摘要，CI 模式不会跳过安装校验。
- 工作流不再调用 Git 同步。`sync_gitee.py --code` 保留为独立的人工备用命令，使用普通推送，保护远端独有内容并拒绝冲突；这些保护不代表 Gitee 平台镜像本身的行为。

未配置 Secret 会明确失败，不报告同步成功。并发同步串行执行，避免旧任务覆盖新状态。失败不自动回滚另一端已成功的推送；超时写入不盲目重试，先查看远端状态。重复镜像核对现有附件摘要，匹配则跳过，不匹配则拒绝覆盖。

## 国内下载

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash
```

默认 Console 使用 `--source gitee`。显式 GitHub：`bash -s -- --source github`。GitHub 每个新预览版包含 Console amd64/arm64、原版官方 FRP 两架构归档、FRP_VERSION、BUILD_INFO、install.sh 和 SHA256SUMS。

GitHub 构建流水线从官方 FRP 下载归档，并核对 Go 安装器中固定的 SHA-256。2026-10-07 实际 Gitee API 上传官方 amd64 归档返回 HTTP 403：`malicious file detected and rejected`，两个 Console 归档上传成功。同步流程验证完整 GitHub 产物后，仅镜像 Console 和元数据（共六个附件），不改包或拆分 FRP 来绕过平台检查。SHA256SUMS 保持 GitHub 原始内容，其中 FRP 摘要用于验证外部官方归档，不代表该文件也在 Gitee。

preview.4 入口选 Gitee 时明确从官方 GitHub 下载 FRP。GitHub 不可达时，先传入原版官方归档，再执行：

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash -s -- --frp-archive /path/to/frp_0.71.0_linux_amd64.tar.gz
```

入口和 Go 安装器均校验缓存归档；只有此缓存方式可不访问 GitHub。完整国内在线安装仍需一个接受官方 FRP 文件的已授权下载托管源。

Gitee 附件同步最后上传 SHA256SUMS，缺少就绪文件时入口停止。选定源校验失败不会悄悄切换其他源。`--verify-only` 检查下载，不安装；首次实际安装依然要预览并输入完整 ID 确认。取消后私有计划用于审阅，临时归档被清理，重新安装需重新生成计划。

接口参考：[Gitee SDK 仓库 API](https://gitee.com/sdk/gitee5j/blob/main/docs/RepositoriesApi.md)、[官方附件短地址说明](https://blog.gitee.com/2022/08/18/update/)、[GitHub GITHUB_TOKEN 事件规则](https://docs.github.com/en/actions/concepts/security/github_token)。
