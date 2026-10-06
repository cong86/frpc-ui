# PVE LXC 基础阶段验证

验证日期：2026-10-07（Asia/Singapore）。代码基线：`e7de1f64e022e50ab09973bdd3fa073219816d31`。
本报告记录隔离测试结果，不代表第一版全部功能完成，也不代表现有生产 FRP 状态。

## 环境与部署

- 两个新建 Debian 12 amd64 非特权 LXC，分别运行官方 FRPS 与 FRPC。
- 每个容器 2 核、1 GiB 内存、256 MiB swap、8 GiB 根盘；DHCP；关闭容器开机自启。
- systemd 252 初始出现 mount namespace 权限错误；仅在两个新测试容器启用 nesting 并重启后，系统服务状态恢复 running。未使用 privileged 或 unconfined。
- 官方 FRP 0.71.0；预先校验官方下载包 SHA-256，SSH 二进制传输后再次核对。
- FRP 与 Console 分别作为低权限用户运行独立 systemd 单元，FRP 单元没有依赖 Console。
- Console SHA-256：`3269e5f70ac815cecd277f295624c06df6b163d5ba896e20d15176b4c2bbb896`。
- 本轮手动部署验证服务；未通过尚未实现的自动安装器执行。
- UI 与 FRP 管理 API 仅监听容器 loopback；UI 通过本机 SSH 隧道访问。SSH Host Key 经已认证 PVE 链路读取并固定。
- Token、FRP 管理密码随机生成，配置及临时验证材料在私有目录中；未写入本仓库。临时 Console 测试管理员在交付前移除，可以重新初始化用户管理员。

## 六层证据

| 层级 | 实测结果 | 依据 |
| --- | --- | --- |
| 安装就绪 | 通过 | 两端官方二进制版本、文件哈希、官方 verify |
| 进程运行 | 通过 | 独立 systemd 单元 active，容器系统 running |
| 认证连接 | 通过 | FRPC 登录成功、FRPS 接收测试客户端，以及实际代理访问 |
| 代理注册 | 通过 | TCP、UDP、HTTP、HTTPS 四代理 start proxy success |
| 本地服务 | 通过 | 客户端本机 HTTP、TLS、UDP 测试后端响应；先出现过本地后端不可达，修正测试脚本权限和变量遮蔽后再验收 |
| 实际业务 | 通过，局域网范围 | PVE 宿主机 TCP/HTTP 固定响应内容、UDP 随机 nonce 原样响应、HTTPS SNI 路由及测试 CA 证书验证；另从 Windows LAN 验证 TCP/HTTP |

HTTPS 使用 30 天自签测试证书并显式信任该测试 CA，未关闭 TLS 验证。HTTP 与 HTTPS 使用测试域名 `frp-console.test`，没有修改实际 DNS 或生产域名。
TCP 代理将 HTTP 测试请求转发至客户端本地服务；UDP 必须收到应用响应，未仅用端口状态判断成功。
未测试公网访问、生产域名、公网证书或 WAN 故障。

## 两端 Console 配置闭环

FRPS 与 FRPC 均通过实际 HTTP API 完成：

- 一次性管理员初始化、登录、配置读取；Token 和 FRP 管理密码在 API 输出中脱敏。
- 候选配置经目标官方二进制 verify。
- 取消预览后文件 SHA-256 不变。
- 非法 TOML 被拒绝，配置文件不变。
- 预览后模拟外部编辑，应用返回 HTTP 409，外部编辑内容保留；仅恢复本轮测试生成的原始文件。
- 离线保存产生备份与审计；重复应用同一计划幂等。
- 从加密备份生成恢复计划，再应用，配置 SHA-256 与原文件完全一致。
- 客户端代理删除预览取消，配置不变。

## 运行变更与恢复

1. 客户端 TCP remotePort 从 16000 修改为 16002，并通过 Console 保存。
2. 保存返回 `saved_offline`：原端口仍可访问，新端口尚未开放。
3. 验证脚本显式重启独立 FRPC 单元：新端口可访问，原端口关闭。
4. 通过 Console 恢复计划恢复原配置，哈希一致；显式重启后原端口恢复，新端口关闭。
5. 服务端 allowPorts 从 16000–16010 缩小至仅 16000，再显式重启：FRPS 仍 active，允许的 TCP 可用，UDP 注册/访问失败。
6. 通过备份恢复服务端策略并显式重启：UDP 应用响应恢复。

这些运行操作由隔离验证脚本执行。当前 Console 没有自动 reload/restart 或业务探测功能，API 中的 verification 仍为 `not_checked`，不能把本报告结果误认为 UI 已实现自动分层验证。

## Console 独立性

停止两端 Console 后，从 PVE 宿主机再次访问四种代理，全部响应正确；FRPS/FRPC MainPID 均未变化。
随后重新启动 Console，配置和审计保留。
这证明本次 systemd 部署的管理服务停止不会中断隧道，不代表升级、崩溃恢复或 Compose 场景已全部验收。

## 交付与边界

测试容器保留运行，容器开机自启保持关闭；FRP、后端与 Console 单元在容器启动后可启动。
用户可经已配置的本机 SSH 隧道访问两端 UI，并初始化自己的管理员；没有向 LAN 暴露管理 UI。
基础阶段 `--demo` 会登记两种配置角色，实际只运行所在容器对应的 FRP 单元；页面中另一角色不代表该容器运行了另一个实例。后续实例选择与运行状态适配需消除这一限制。

未修改现有 FRPC/FRPS、生产反代或主备容器。未验证 Docker Compose、arm64 实机、自动安装、UI 运行操作、INI 接管、Nginx 加载、升级/卸载及全部故障矩阵。

配置字段依据：[官方代理配置](https://gofrp.org/en/docs/reference/proxy/)、[官方服务端配置](https://gofrp.org/en/docs/reference/server-configures/)。
