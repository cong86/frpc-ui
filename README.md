# FRP Console

FRP Console 第一版开发仓库。Go + Vue 3/TypeScript，前端嵌入程序；SQLite 保存用户、审计与操作元数据。官方 FRP 独立运行。

**当前版本为第一阶段基础实现，不是完成的第一版，也不能部署接管生产。**

## 当前可以使用

- 首次初始化管理员、登录/退出、HttpOnly 会话、CSRF/Host/Origin 防护、登录限速。
- 客户端与服务端页面；独立 FRPC/FRPS 隔离 TOML 配置。
- TCP/UDP/HTTP/HTTPS 代理表单；配置文件中的启用状态与运行状态分开显示。
- 脱敏配置、高级编辑、代理候选校验、变更前后预览。
- 官方 FRP `verify` 通过后才生成写入计划；取消不修改配置。
- 修订号冲突拒绝、操作幂等、系统文件锁、原子写入、加密快照、恢复预览。
- 启动时核对未完成写入记录，不盲目重放。
- systemd/Compose 部署资源预览、Nginx HTTPS/WebSocket 模板生成。

## 当前限制

- 只监听字面 IP 回环地址；无公网登录、TLS 终止或正式反代部署。
- **仅 `--demo` 创建的隔离配置可写**，且保存只修改文件。没有启动、重载或控制 FRP。
- `--frpc-config` / `--frps-config` 导入 TOML 始终只读；不迁移生产配置，不执行主备或同步脚本。
- 多行字符串、includes、环境模板、已有 start 过滤与无法可靠脱敏的写法只读。
- 嵌套代理表格不支持表单修改；可在支持脱敏的高级编辑视图中编辑。
- 旧 INI 导入/迁移、多实例注册、实际运行状态与日志、自动安装、Nginx 校验/加载、完整升级/卸载尚未实现。
- 部署模板含明确凭据占位符，Compose 镜像摘要固定待实现；不能直接用于生产。
- 进程、认证、代理注册、本地服务、业务访问均显示未验证。没有模拟成功状态。

完整规格与开发清单见 [docs/v1-spec.md](docs/v1-spec.md)。

## 构建

需要 Go 1.26+、Node.js 22.12+、pnpm 11+。开发依赖只允许 esbuild 的安装脚本，见 `pnpm-workspace.yaml`。Go 依赖固定到本轮核实的稳定版本，具体版本与校验记录见 `go.mod` / `go.sum`。

```sh
pnpm install --frozen-lockfile
pnpm build
go build -o bin/frp-console ./cmd/frp-console
```

Windows 输出名使用 `bin/frp-console.exe`。前端构建写入 `internal/server/assets`，Go embed 读取同一目录。无需运行独立前端服务。

## 隔离启动

先从官方 FRP Release 下载对应平台的 frpc/frps，核对官方 SHA-256，再传入所在目录。Console 仅调用 `verify`。

```sh
./bin/frp-console --demo --data ./data --frp-dir /path/to/official/frp
```

浏览器打开 `http://127.0.0.1:18745`，首次访问创建管理员。Token、密码不得写入命令参数。没有 `--frp-dir` 仍能读取配置，但不能生成可写计划。

只读导入示例：

```sh
./bin/frp-console --data ./data --frpc-config /path/to/frpc.toml
```

不要把 `--data` 指向共享或公开目录。Linux 目录权限 0700、敏感文件 0600；Windows 本地预览应使用当前用户受保护的目录，POSIX mode 不是 Windows ACL 保证。

`backup.key` 与 `console.db` 必须一起保留；丢失密钥会拒绝启动，不生成新密钥覆盖。数据库保存 AES-GCM 加密的候选/旧配置，账户密码使用 bcrypt；会话仅保存随机令牌哈希。操作与审计响应不包含快照密文或凭据。

## 验证

```sh
go test ./...
FRP_TEST_DIR=/path/to/official/frp go test -count=1 ./...
go vet ./...
pnpm build
```

未设置 `FRP_TEST_DIR` 时，官方二进制集成测试会明确 skip，不能当成全链路通过。该测试只调用 verify，不建立隧道。Linux amd64/arm64 可交叉编译，但交叉编译不等于目标系统实机验收。

## 生产边界

没有接入 Homelab SSH、生产 FRPC/FRPS、Nginx、主备仲裁或配置同步。管理程序不拥有 FRP 子进程。后续生产部署须独立授权并完成隔离环境验收。
