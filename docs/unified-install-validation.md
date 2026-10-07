# 统一安装入口验证（2026-10-07）

本阶段将 `curl | bash` 扩展为新安装与已有 FRPS/Nginx 只读管理服务安装。目标 Debian/Ubuntu + systemd，amd64/arm64。没有连接或修改生产云服务器、PVE LXC、主备或生产反代。

## 自动化检查

- Windows Go 包测试与 go vet；设置官方 FRP 0.71.0 Windows 路径，配置 verify 集成测试实际执行。
- Linux amd64 安装器测试：拒绝不安全路径/目标、计划绑定、独立单元、EOF 取消、默认日志登记、UI session 证据；入口 16 项测试含真实伪终端管道分支、摘要错误拒绝、已有模式不下载 FRP；Gitee 同步 11 项测试。
- Linux amd64/arm64 交叉编译。未改动前端；发布流水线再次执行前端类型检查/构建。

## Linux amd64 隔离运行

WSL Ubuntu 24.04 systemd 使用随机命名专属目录、用户、单元和回环端口；官方 FRPS/FRPC 0.71.0 与测试后端建立真实 TCP 业务隧道。Nginx 使用磁盘 fixture，日志含敏感字段用于检查脱敏。

真实伪终端运行完整 `curl | bash` 接入流程：下载传输由 fixture 替代，归档含实际构建的 Go 程序并执行真实摘要检查，选择接入、填写路径、显示计划、输入完整 ID，自动安装 UI/采集单元/timer。确认只请求 Console 归档和摘要，没有请求 FRP_VERSION 或官方 FRP 归档。此项证明交互与安装，单独的公网下载检查才证明镜像可用。

新安装分支也通过真实伪终端：取消预览无部署副作用；选择两端并确认后实际创建独立 FRPS、FRPC、Console 服务，官方管理 API 验证客户端认证连接，UI session 正常，Token 输入与预览不回显。停止 Console 后两端 FRP PID 不变。此测试不从认证推导业务代理成功；实际业务隧道由已有模式 fixture 单独覆盖。

- 低权限 UI 用户不能读取原 root 私有 FRPS 配置，网页不含 Token/API 密码。
- 受限 root oneshot 完成初始采集，timer 刷新快照、配置/反代/日志摘要与区间平均速率；网页写入接口拒绝修改已有实例。
- 成功的原计划重复执行不改变 UI PID；错误确认无安装副作用。
- 原 FRPS 配置预览后变化时拒绝安装，不创建新目录或用户。
- 模拟本次新采集单元启动失败：安装记录为 failed_retained，只删除本次新增管理单元，保留新目录/用户供检查。
- 原 FRPS PID、FRPS/Nginx/日志文件 SHA-256 和业务响应保持不变；停止 Console 时 timer 与原隧道继续运行。
- 快照超过 120 秒后，进程/反代健康与速率结论失效。

测试清理只针对本次随机创建的资源，保留用户既有文件与服务。

## 发布包计时修复

preview.6 的 GitHub 公网下载与摘要通过，但发布二进制复测遇到 10 秒 timer 未在 25 秒检查窗口刷新。目标 systemd 手册确认 AccuracySec 默认 1 分钟；已有本地测试恰好落在较早触发窗口，不能覆盖这项调度差异。preview.7 显式设置 AccuracySec=1s，保留 OnUnitInactiveSec，以避免一分钟合并窗口；增加生成单元检查并重跑真实 timer。原 preview.6 附件保留，不覆盖已发布文件。

## 验收边界

Docker 仅模拟固定读取命令，Nginx 仅解析磁盘配置与日志 fixture；没有真实 Nginx worker 验收、图形交互、arm64 实机或生产权限测试。已有模式不自动开启 FRPS 管理 API、不迁移旧 INI、不写入原配置、不执行反代加载。完整写接管和升级仍未完成。

## preview.7 公网发布核验

GitHub [Release](https://github.com/cong86/frpc-ui/releases/tag/v0.1.0-preview.7) 与 [发布流水线](https://github.com/cong86/frpc-ui/actions/runs/37592603475) 成功，BUILD_INFO 绑定 `a67374fdd55a5cc9e4ed6d01f7181b7506f0466a`。发布清单全部摘要核对通过，两架构 Console 实际下载；官方 FRP 原归档与固定摘要匹配。实际发布 amd64 二进制再次完整验证新安装取消/两端安装/认证，以及已有接入、timer 刷新、AccuracyUSec=1s、低权限和原隧道不变。

Gitee 原生镜像 main 与 preview.7 标签已匹配此提交。GitHub [附件同步工作流](https://github.com/cong86/frpc-ui/actions/runs/37592696382) 在等待标签的 API GET 阶段因 URLError 失败，未上传附件。本机使用已授权凭据补齐 6 个受支持附件，并实际从 Gitee 下载，逐项确认 SHA-256 与 GitHub 字节一致；不是自动附件同步成功。Gitee 不接受官方 FRP 归档的限制保持不变。

Linux 实际执行 GitHub 与 Gitee 的 `curl | bash -s -- --verify-only`：接入模式只下载 Console，新安装模式使用已核对官方本地缓存，均通过摘要检查。测试网络通过本机代理访问，不证明用户云服务器网络可达；目标云服务器尚未部署。
