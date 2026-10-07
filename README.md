# FRP Console

FRP Console 第一版开发仓库。Go + Vue 3/TypeScript，前端嵌入程序；SQLite 保存用户、审计与操作元数据。官方 FRP 独立运行。

**当前支持 systemd 新安装与已有 FRPS/Nginx 只读接入；第一版尚未完成，已有部署写接管仍未开放。**

## 当前可以使用

- 首次初始化管理员、登录/退出、HttpOnly 会话、CSRF/Host/Origin 防护、登录限速。
- 客户端与服务端页面；独立 FRPC/FRPS 隔离 TOML 配置。
- TCP/UDP/HTTP/HTTPS 代理表单；配置文件中的启用状态与运行状态分开显示。
- 脱敏配置、高级编辑、代理候选校验、变更前后预览。
- 官方 FRP `verify` 通过后才生成写入计划；取消不修改配置。
- 修订号冲突拒绝、操作幂等、系统文件锁、原子写入、加密快照、恢复预览。
- 启动时核对未完成写入记录，不盲目重放。
- systemd/Compose 部署资源预览、Nginx HTTPS/WebSocket 模板生成。
- 已有 FRPS 的只读采集与 UI：配置、官方 API 代理/客户端/流量、Docker/systemd 元数据、Nginx 站点/include/upstream 与脱敏日志摘要。管理员生成快照，网页只读快照，不持有 Docker socket。见 [接入说明](docs/existing-frps.md) 和 [验收记录](docs/existing-frps-validation.md)。

## 当前限制

- 只监听字面 IP 回环地址；无公网登录、TLS 终止或正式反代部署。
- **仅 demo 与新安装清单登记的专属配置可写**。网页保存仍为离线保存，不启动或重载 FRP。
- `--frpc-config` / `--frps-config` 导入 TOML 始终只读；不迁移生产配置，不执行主备或同步脚本。
- 多行字符串、includes、环境模板、已有 start 过滤与无法可靠脱敏的写法只读。
- 嵌套代理表格不支持表单修改；可在支持脱敏的高级编辑视图中编辑。
- 旧 INI 迁移、已有部署写接管、同角色多实例、网页安装执行、运行应用、新安装实例日志、Compose 自动安装、Nginx 校验/加载、升级/卸载尚未实现。
- 部署模板含明确凭据占位符，Compose 镜像摘要固定待实现；不能直接用于生产。
- 无清单的 demo/导入保持运行未验证。新安装显示实测层级；未登记探针或证据不足仍未验证。运行结果描述当前进程和固定登记目标，不证明离线修改已应用或全部参数与文件相同。
- 从 preview.7 起，一键入口可安装独立只读 UI 和定时采集服务，显示采集时间、过期状态及分层证据；已有 FRPS、Nginx 配置及运行服务不变。

完整规格与开发清单见 [docs/v1-spec.md](docs/v1-spec.md)。

## 一键下载安装（预览版）

在 Debian/Ubuntu 的交互终端中执行（root 用户可省略 sudo）：

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash
```

入口检测系统和架构，默认下载 preview.7，先选择操作模式，再下载并校验 SHA-256：

- **新安装**：选择 FRPC / FRPS / 两者、填写连接参数与 Token，预览并确认后安装独立 FRP 与 Console 服务。
- **已有 FRPS 只读接入**：填写现有 TOML 路径、systemd 单元或 Docker 容器名、Nginx 配置与日志路径，预览并确认后安装低权限 UI 和定时采集服务。此模式只下载 Console，无需下载或重装官方 FRP。未启用的管理 API 保持未验证，程序不自动修改原配置。

可用 `--mode new` / `--mode adopt` 直接选择模式。默认从 Gitee 下载 Console；新安装的官方 FRP 从官方 GitHub 下载。Gitee 实际拒绝官方 FRP 附件，无法提供完整在线镜像。新安装时 GitHub 不可达，先将对应架构的官方原版 FRP 归档传到目标机，再使用本地缓存：

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash -s -- --frp-archive /path/to/frp_0.71.0_linux_amd64.tar.gz
```

本地归档也必须通过发布摘要与 Go 安装器的官方固定摘要检查；采用缓存时无需访问 GitHub。可用 `--source github` 将 Console 下载源切回 GitHub。选择角色、填写配置、预览并确认后才安装。无需预装 Go、Node.js；需要 Bash、curl、tar、sha256sum 和运行中的 systemd。

现有 Console 不会自动升级。接入安装拒绝覆盖现有目录、用户和服务；成功的原计划可以幂等重试，重新启动向导不等于升级。接入详情见 [接入说明](docs/existing-frps.md)。

只检查下载和摘要、不安装：

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash -s -- --verify-only
```

仅验证接入模式的 Console 下载：在上述命令后加 `--mode adopt --verify-only`，不会下载官方 FRP。

程序临时文件在退出时清理；私有安装计划保存到 `/root/frp-console-install-*.json`，权限 0600。安装后 UI 通过回环地址和 SSH 隧道访问。详情见 [安装说明](docs/systemd-install.md)、[统一入口验证](docs/unified-install-validation.md) 与 [自动同步设置](docs/gitee-sync.md)。

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

`backup.key` 与 `console.db` 必须一起保留；丢失密钥会拒绝启动，不生成新密钥覆盖。数据库保存 AES-GCM 加密的候选/旧配置，新账户密码使用 SHA-256 预哈希后 bcrypt，兼容已有 bcrypt 密码；密码非空即可，没有单独的长度或字符种类限制（请求整体大小仍受限）。会话仅保存随机令牌哈希。操作与审计响应不包含快照密文或凭据。

## 验证

```sh
go test ./...
FRP_TEST_DIR=/path/to/official/frp go test -count=1 ./...
go vet ./...
pnpm build
```

未设置 `FRP_TEST_DIR` 时，官方二进制集成测试会明确 skip，不能当成全链路通过。该测试只调用 verify，不建立隧道。Linux amd64/arm64 可交叉编译，但交叉编译不等于目标系统实机验收。

## 生产边界

没有接入 Homelab SSH、生产 FRPC/FRPS、Nginx、主备仲裁或配置同步。Web 管理程序不拥有 FRP 子进程。root CLI 新安装创建独立 systemd 服务。后续生产部署须独立授权并完成隔离环境验收。

## 原生新安装与验证

Linux CLI：sudo ./bin/frp-console install wizard。向导隐藏输入 Token，预检并校验官方归档和配置，脱敏预览后输入计划 ID 才创建独立服务。只支持新安装，管理员从首次 UI 访问初始化。

安装器不修改 PVE 权限、防火墙、DNS 或已有服务。详见 [原生安装说明](docs/systemd-install.md)、[阶段验证](docs/stage3-validation.md) 与 [Gitee 实际验证](docs/gitee-validation.md)。

Console 以低权限读取 systemd/FRP 本机 API。业务探针检查响应内容、UDP nonce 和 TLS 证书；两端 Cookie 按地址隔离，可同时登录。
