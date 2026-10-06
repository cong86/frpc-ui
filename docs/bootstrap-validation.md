# 一键入口验证

2026-10-07（Asia/Singapore），范围为两个既有隔离 Debian 12 amd64 LXC；生产 FRP、主备及反代未修改。

当前入口：`curl -fsSL https://raw.githubusercontent.com/cong86/frpc-ui/main/scripts/install.sh | sudo bash`。默认固定 `v0.1.0-preview.2`，不是完整第一版。

## 已验证

- GitHub Actions 完成 Vue 构建、全部 Go 测试、go vet、脚本语法检查、8 项入口测试和两架构打包，全部成功。[流水线](https://github.com/cong86/frpc-ui/actions/runs/37516456621)。
- 两 LXC 执行入口测试：两架构选择、错误摘要、异常归档成员、缺失摘要、无终端、非法版本、不支持架构和管道下终端保留。
- 实际 Linux 回归测试验证：调用者使用 umask 077 时，新部署目录和程序仍为 0755，私有配置 0600，已有父目录权限不变。
- 从公开 GitHub 地址下载预览归档及 SHA256SUMS；两架构摘要、归档单一成员和 ELF 架构检查通过。
- 实际 `curl | bash` + PTY：下载 Console、在线下载官方 FRP、隐藏 Token、脱敏计划、完整 ID 确认，分别完成新服务端及客户端安装。
- 两端独立 systemd 服务与 HTTP UI 可用，新管理员未初始化；新客户端连接到新服务端，FRPS API 显示一个已认证客户端。向导没有创建业务代理，此项不是业务隧道验收。
- 新测试实例随后停用、配置数据保留；原有两个 Console/FRP 仍正常，原 FRP PID 未受清理操作影响。服务端原 FRPS 启动时间早于本轮安装。临时代理 SSH 通道已移除。

## 修复及边界

第一次完整安装发现 umask 077 将目录/可执行文件权限收紧，低权限服务无法启动。失败安装撤销新增单元，保留数据与 failed_retained 记录，未自动重放。修复安装器显式权限后发布 preview.2；preview.1 已标记 superseded，已发布归档没有覆盖。

测试最初紧接 systemctl active 请求 UI，遇到监听就绪时序；等待实际 HTTP 响应后验证通过，不以 active 替代 HTTP 可用。

LXC 直连 raw 脚本成功，直连 GitHub Release 遇到 HTTP/2 中断及连接超时。公开文件和安装验证使用已有代理，通过仅测试用途的临时 SSH 转发及进程级 HTTPS_PROXY；未改持久网络设置、未换第三方镜像。网络受限环境仍需能访问 GitHub Release 和官方 FRP 下载地址。

arm64 只验证构建、下载摘要及 ELF 架构，未在 arm64 硬件运行。无 Go/Node 本机依赖；Bash/curl/tar/sha256sum/systemd 依赖须已有，入口不自动运行 apt。安装后的 UI 仍回环监听；Compose、生产接管、Nginx 加载、运行应用、升级及卸载未实现。
