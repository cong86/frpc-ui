# GitHub 到 Gitee 自动同步

GitHub 为开发与发布来源；Gitee 为国内源码和下载镜像。目标固定为 `wangcong886/frpc-ui`，只同步 main 和版本标签，不删除远端独有分支/标签，不强推。远端有独立提交或同名不同标签时停止，先人工合并差异。

## 一次设置，之后自动同步

在 GitHub 仓库 Settings → Secrets and variables → Actions 中添加 repository secret `GITEE_TOKEN`。使用 Gitee 的专用自动同步 Token，需能够推送此仓库并创建 Release/上传附件。不要把 Token 写进 URL、代码、Issue 或日志。

专用 Token 由用户授权配置；已有凭据只在本机针对此目标使用，不自动迁移到 GitHub。

- main 更新：`Sync Gitee` 自动同步源码和标签。
- `Preview release` 成功完成：`workflow_run` 触发同步，下载已发布 GitHub 附件，验证 SHA256SUMS 后上传到 Gitee。
- GitHub GITHUB_TOKEN 发布 Release 不触发普通 release/push 标签工作流，因此用 workflow_run 连接发布和镜像。
- 首次补同步或重跑：Actions → Sync Gitee → Run workflow，`release_tag` 填已发布版本；留空只同步源码。

未配置 Secret 会明确失败，不报告同步成功。并发同步串行执行，避免旧任务覆盖新状态。失败不自动回滚另一端已成功的推送；超时写入不盲目重试，先查看远端状态。重复镜像核对现有附件摘要，匹配则跳过，不匹配则拒绝覆盖。

## 国内下载

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash
```

默认 `--source gitee`。显式 GitHub：`bash -s -- --source github`。每个新预览版包含 Console amd64/arm64、原版官方 FRP 两架构归档、FRP_VERSION、BUILD_INFO、install.sh 和 SHA256SUMS。

GitHub 构建流水线从官方 FRP 下载归档，并核对 Go 安装器中固定的 SHA-256，镜像不重新打包 FRP。入口从选择的同一源下载 Console/FRP 并核对发布摘要，把 FRP 缓存作为向导默认值；Go 安装器再次核对官方固定摘要。无需在目标机再访问 GitHub 获取 FRP。

Gitee 附件同步最后上传 SHA256SUMS，缺少就绪文件时入口停止。选定源校验失败不会悄悄切换其他源。`--verify-only` 检查下载，不安装；首次实际安装依然要预览并输入完整 ID 确认。取消后私有计划用于审阅，临时归档被清理，重新安装需重新生成计划。

接口参考：[Gitee SDK 仓库 API](https://gitee.com/sdk/gitee5j/blob/main/docs/RepositoriesApi.md)、[官方附件短地址说明](https://blog.gitee.com/2022/08/18/update/)、[GitHub GITHUB_TOKEN 事件规则](https://docs.github.com/en/actions/concepts/security/github_token)。
