FRP Console v0.1.0-preview.8：统一一键新安装与已有 FRPS/Nginx 只读管理安装入口。第一版尚未完成，提供嵌入前端的 Linux amd64/arm64 程序。

本次将安装菜单、交互与错误提示、预检证据及网页状态、审计、配置说明统一为中文。角色、运行方式和 Nginx 配置类型支持中文编号选择，原英文输入和机器接口保持兼容。命令参数、协议和配置字段保留原标识。

`curl | bash` 先选择新安装或只读接入，也可传 `--mode new` / `--mode adopt`。新安装继续支持原生 FRPC/FRPS/两者，原版官方 FRP 校验、配置预览和明确确认。已有部署模式仅下载 Console，向导登记原 FRPS TOML、systemd/Docker 目标、Nginx 配置路径/挂载/日志，自动安装独立低权限 UI 和 root 只读采集 timer；不重装 FRP，不修改原配置或权限，不重载或重启原服务。

预检显示 API/日志不可用与配置读取范围警告，执行前重新核对原配置修订。验证新增受限采集服务、timer 与 UI session 接口；同一成功计划重试不重启 UI。安装失败只清理本次新增管理单元，保留新数据和用户供检查。管理员由首次网页访问设置，UI 回环监听、远程使用 SSH 隧道。安装成功与原 FRPS 认证、代理注册、业务访问分层显示。

只读页面包含脱敏 FRPS 配置、进程、代理/客户端、累计流量和采集间隔平均速率，Nginx HTTP 站点/include/upstream、HTTPS/WebSocket 配置及有限日志摘要。网页只读脱敏快照，不持有 Docker socket；快照过期撤销健康和速率结论。

验证：Go 测试与 go vet、16 项入口测试、11 项 Gitee 同步测试、两架构构建；Linux amd64 隔离环境实际官方 FRPS/FRPC 隧道和 systemd 管理服务验证。真实终端管道经过完整接入向导、确认、安装和 timer 刷新；原服务 PID、配置哈希和测试隧道保持正常，模拟新增服务启动失败时只移除新增单元。Docker 为固定命令模拟测试，Nginx 为磁盘 fixture 解析，arm64 仅构建，不代表实机或生产验收。详见 docs/unified-install-validation.md。

默认 Console 国内下载使用 Gitee；新安装的官方 FRP 从官方 GitHub 下载或传 `--frp-archive` 原版本地缓存，仍强制校验。Gitee 不接受官方 FRP 附件，完整国内在线镜像尚未实现。只读接入不需要官方 FRP 下载。源码/标签采用 Gitee 原生 Pull 镜像，发布附件独立同步。

现有 Console 不会自动升级。完整写接管、配置运行应用/自动回滚、Compose 自动安装、Nginx 校验/加载和升级/卸载尚未开放。旧 preview.5 提供手动只读接入，使用本次入口需下载 preview.8。

preview.7 修正接入 timer 的计时精度，显式设置 AccuracySec=1s，避免 systemd 默认一分钟合并窗口影响 10～60 秒采集间隔。preview.6 已被此版本替代；附件不覆盖。
