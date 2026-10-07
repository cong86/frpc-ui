# 已有部署只读接入验收

2026-10-07（Asia/Singapore），Windows 构建与 WSL Ubuntu-24.04 隔离 Linux 验证；没有连接或修改云端、生产 LXC、FRPC 主备及既有 Nginx。

## 实际验证

- 原有 Go 测试、官方 FRP 0.71.0 verify 集成检查与 go vet 通过；新增采集测试覆盖只读 GET、凭据与注释脱敏、字段白名单、固定命令、Profile 参数拒绝、速率来源与计数重置、Nginx include/挂载/范围/循环/语法、日志窗口与敏感内容隔离。
- 新包 12 项测试在 Windows amd64 和 Linux amd64 执行；Vue/TypeScript 检查与生产构建通过，Linux amd64/arm64 交叉构建通过。
- 使用已核验的官方 FRPS/FRPC 0.71.0 建立独立回环 TCP 隧道，服务端运行在临时 systemd 单元。管理员 CLI 实际读取原 TOML、FRPS API、systemd 元数据、两个 Nginx 文件和登记日志，生成快照。
- 两次采集得到有效区间平均速率；FRPS 客户端认证与代理注册有独立 API 证据。安装及业务层没有注册专门探针，保持未验证，即使外部测试脚本另外访问隧道成功也不自动提升页面层级。
- Console 以 nobody 运行，无法读取 root-only FRPS 配置，却能登录并读取脱敏快照。对导入实例提交写入计划被拒绝；原配置、Nginx 文件和访问日志哈希一致，FRPS PID 不变。
- 改成过期快照后，网页 API 返回 stale、进程层未验证、速率失效。输出选择原 FRPS 配置或不受保护目录时被拒绝。
- 停止 Console 后，原测试隧道仍响应，FRPS PID 不变。隔离测试完成后临时进程、systemd 单元与专属目录清理，保留源码及测试产物。

## 边界

Docker inspect/logs 路径目前使用固定 argv 的模拟测试，没有真实 Docker 容器或云端接入验收。Nginx 使用磁盘配置 fixture，仅验收解析与范围控制，没有运行 Nginx、验证证书或证明当前 worker 加载一致。日志/流量来自采集快照，非实时推送。

未在 arm64 实机执行、未做生产权限适配，尚未完成图形浏览器交互验收。以上为发布前隔离验证；preview.5 为包含本功能的目标发布版本，原 preview.4 附件保留。发布和下载摘要校验须另行完成，不能从编译结果推断发布成功。只读接入不开放配置写接管、FRP 运行控制、Nginx 加载或主备管理。
