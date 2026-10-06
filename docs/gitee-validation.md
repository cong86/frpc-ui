# Gitee 下载与同步验证

验证日期：2026-10-07，Asia/Singapore。发布版本 `v0.1.0-preview.4`，发布源码 `902bc3083afdd01c43635ca205ee23844bed7aec`。GitHub 为发布来源，Gitee 为源码及 Console 下载镜像。

## 实际结果

- GitHub [preview.4 构建](https://github.com/cong86/frpc-ui/actions/runs/37520472483) 成功，发布八个附件，包含原版官方 FRP 0.71.0 两架构归档。Go、前端、静态检查、安装入口及镜像测试由该流水线执行。
- Gitee main 与 preview.4 标签同步至同一源码提交。Gitee 的六个镜像附件已使用本机凭据上传：Console 两架构归档、FRP_VERSION、BUILD_INFO.txt、install.sh、SHA256SUMS。
- LXC 102 直接下载并校验这六个公开附件；SHA256SUMS 与 GitHub 发布摘要一致，两个 Console ELF 的架构分别为 amd64/arm64。验证时移除了代理环境变量。
- 本机对 preview.4 再次执行镜像，六个现有附件摘要均匹配并跳过，未创建重复附件或覆盖已发布内容。
- 两台 Debian 12 amd64 测试容器分别通过 13 项安装入口测试。本机通过八项镜像测试；测试覆盖远端独立提交/标签保护、摘要和路径拒绝、认证头不随跳转转发，以及不上传被平台拒绝的 FRP 文件。
- 两端均从公开 Gitee main 入口执行真实 `curl | bash`，使用 `--frp-archive /root/frp.tar.gz`。该缓存为官方 amd64 原版归档，SHA-256 为 `84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716`，入口和 Go 安装器均校验；不需要访问 GitHub。
- 完整计划 ID 确认后，安装记录为 installed、Console/FRP 单元 active、UI session 接口可访问。Token 输入时终端 ECHO 关闭，输出不含 Token，私有计划及 FRP 配置为 0600，部署根目录为 0755。
- 新 FRPS 的本机认证 API 确认 `clientCounts=1`。关闭两端新 Console 后，FRP PID 未改变，客户端仍保持认证连接。新实例未配置代理，因此没有新代理注册、本地服务或实际业务访问验收。

## Gitee 平台限制

官方 FRP 原版 amd64 归档的 API 上传实际返回 HTTP 403，响应为 `malicious file detected and rejected`；两个 Console 归档被接受。这是平台文件检查结果，不能解释为密码或普通下载网络故障。

没有改包或拆分 FRP 绕过平台检查。Gitee 镜像只上传六个可接受的附件，保留原始完整 SHA256SUMS；清单含 FRP 摘要不意味着 Gitee 托管了 FRP 文件。

preview.4 选 Gitee 时，Console/元数据从 Gitee 下载，FRP 明确从官方 GitHub 下载。GitHub 不可达时必须提供原版本地归档。完整国内在线安装仍需另一个已授权且接受官方 FRP 文件的托管源。preview.3 原始入口假设完整 Gitee 附件，使用 preview.4 替代，未覆盖旧版本附件。

## 自动同步方案更新

2026-10-07 后续确认：用户已解决源码同步，采用 Gitee 自带 Pull 自动镜像。管理页面截图显示源仓库 cong86/frpc-ui，最近请求 03:46:20、最近完成 03:48:29。该时间早于本次后续工作流调整；当前方案和验收入口见 [同步说明](gitee-sync.md)。

原 Sync Gitee 的源码 push 触发与 Git 推送步骤已移除。后续用户提供 GitHub API 访问后，已验证仓库管理权限、Secrets 公钥及 GITEE_TOKEN 元数据 HTTP 200；GitHub 令牌未持久保存。工作流改为在 Preview release 成功后仅同步附件，并保留手动入口；等待原生镜像标签匹配，新增账号及标签校验测试，本机十项镜像测试通过。

后续 Webhook 检查发现：GitHub 的 Gitee push 回调处于启用状态，19:46:21 UTC 的投递成功（HTTP 200），19:51:46 UTC 投递失败（HTTP 500），一次重投在 20:04:36 UTC 返回 HTTP 504。此前截图证明上次镜像完成，不能证明后续每次自动同步成功；在最新 main 提交实际到达 Gitee 前，源码自动同步仍需确认。本次没有更改用户的镜像设置、回调 URL 或其凭据，也没有为掩盖该结果直接推送 Gitee。

原手动工作流 37522769922 在重复 Git 源码访问步骤停留较久，已取消，改用仅附件 API 的任务验收。本页以下保留早期方案的实际检查结果。

### 后续验收通过

2026-10-07 04:24（Asia/Singapore）检查：GitHub/Gitee main 均为 `572fb07f25bc28f9030f394d53df54205b85cc0e`。此阶段没有人工推送 Gitee，确认原生 Pull 镜像已完成后续源码更新；早期回调失败不能代表镜像最终未完成。

用户提供的 GitHub API 凭据读取目标仓库和 Secrets 公钥均为 HTTP 200，目标仓库权限包括 admin/push。GITEE_TOKEN 元数据读取成功，Actions 的 Gitee 账号认证通过。没有把 GitHub 令牌持久保存到 Git、文件或仓库。

[附件同步运行 37524072976](https://github.com/cong86/frpc-ui/actions/runs/37524072976) 重跑最终 success：十一项同步测试、账号认证、原生标签与 GitHub 发布提交核对、完整 GitHub 附件下载及 preview.4 六个 Gitee 附件逐字节复查均通过。途中 Release 列表请求失败后改为按标签查询；一次大附件读取超时后，后续严格复查成功。凭据认证与下载网络结果分别记录。

当前工作流在 Preview release 成功后仅处理附件，不进行 Git 源码推送。CI 使用显式 manifest-marker 模式：已有最终 SHA256SUMS 必须与原始清单一致且六个附件名齐全，才能跳过该已完成发布；日志明确归档内容未审计。缺失附件或清单不同立即拒绝。该模式也已在本机使用真实 API 验证；安装器和不带该参数的严格复查始终校验实际归档字节。尚未创建新版本以验证首次自动事件及新附件上传，此阶段验收对象是已发布的 preview.4。

### 原 GitHub Actions 方案的历史检查

`Sync Gitee` 已提交，main 更新触发源码/标签同步；成功的 Preview release 通过 workflow_run 触发可接受附件的镜像。同步为普通推送，不强推、不删除远端独有内容；完整 GitHub 产物先校验，SHA256SUMS 最后上传。

用户已明确授权将本机现有 Gitee 凭据配置为此 GitHub 仓库的 Actions Secret `GITEE_TOKEN`。但当前本机 GitHub 认证访问 Secrets API 返回 403，GitHub 连接器不提供 Secrets API，浏览器控制也未能连接。因此本次尚未保存 Secret；[当前同步运行](https://github.com/cong86/frpc-ui/actions/runs/37520630782) 的实际日志为 `Configure repository Actions secret GITEE_TOKEN first`。本次 Gitee 发布为本机执行的镜像，不能报告自动同步已启用。

手动输入 `release_tag=v0.1.0-preview.4` 可核验附件补同步；源码原生镜像与附件任务需分别确认。

## 测试实例与边界

- LXC 102：`frp-console-gitee-srv`，安装根 `/opt/frp-console-gitee-srv`，UI 回环 18768，FRPS 17104。
- LXC 103：`frp-console-gitee-cli`，安装根 `/opt/frp-console-gitee-cli`，UI 回环 18769。
- 认证与独立运行验证后，已停止并禁用上述四个新单元，保留配置、数据、用户及安装记录供审阅。
- 原有两端 `frp-console-stage3` 与其 FRP 服务保持 active，FRP PID 与安装前保存值一致。未修改现有生产 FRPC/FRPS、反代或主备服务。
- arm64 仅完成构建、公开下载摘要与 ELF 架构检查，没有 arm64 实机运行证明。普通开发未写入 Homelab Knowledge 或 Memory。
