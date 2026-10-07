FRP Console v0.1.0-preview.5：systemd 新安装与已有 FRPS/Nginx 只读观测预览版，第一版尚未完成。提供 Linux amd64/arm64 程序，前端已嵌入。

新增 `observe` 管理员采集 CLI 和 `--observed-snapshot` 模式：显示已有 FRPS 的脱敏配置、进程、认证、代理与客户端、累计流量和采集间隔平均速率；读取 Nginx HTTP 站点/include/upstream、HTTPS/WebSocket 配置，以及有限日志摘要。低权限网页只读脱敏快照，不持有 Docker socket、不重装 FRP、不写源配置、不重载服务。快照超过 120 秒时撤销健康和速率结论。

已有部署接入按仓库 `docs/existing-frps.md` 单独配置，不重新运行新安装向导。旧安装不会自动升级；当前没有自动升级功能。只读接入无需下载新的官方 FRP。

验证：Go 测试、go vet、前端类型检查/构建通过，12 项采集测试在 Windows/Linux amd64 通过，amd64/arm64 构建通过。真实 FRPS/FRPC 隔离隧道验证了低权限登录、脱敏、写入拒绝、源配置/PID 不变、快照过期，以及 Console 退出后隧道持续可用。Docker 目前仅模拟固定命令测试，Nginx 为磁盘 fixture 解析，未验收真实 worker、生产权限或图形交互。

下载入口检测系统及架构，从本 Release 下载对应归档和 SHA256SUMS，校验后启动交互向导。向导预览和确认后创建独立 FRPC/FRPS 与 Console 服务。UI 回环监听，管理员由首次网页访问初始化。

支持短密码和长密码，兼容已有管理员。Console 退出不影响 FRP 隧道。

preview.2 修复私有 umask 下安装目录和程序权限被收紧的问题，确保独立低权限服务能够启动；原始配置和计划仍保持私有。

preview.3 增加国内源和官方 FRP 缓存。实际发布验证发现 Gitee 拒绝官方 FRP 归档；preview.3 原版安装入口不适用于 Gitee。

preview.4 修正国内源兼容：Gitee 镜像源码、标签、Console 两架构归档和元数据，保留 GitHub 原始完整 SHA256SUMS；官方 FRP 归档只在 GitHub 发布。选 Gitee 时 FRP 从官方 GitHub 获取，也可用 `--frp-archive` 指定原版本地归档，仍强制校验。完整国内在线镜像尚未实现。源码自动同步采用用户配置的 Gitee Pull 镜像；附件由 GitHub Sync Gitee 在 Preview release 成功后单独镜像，使用 GITEE_TOKEN，不推送源码。仍提供手动补同步入口。

仍未开放生产接管、配置运行应用/自动回滚、Compose 自动安装、Nginx 加载或升级/卸载。arm64 构建不代表 arm64 实机验收。
