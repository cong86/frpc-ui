# systemd 新安装与验证

支持 Debian/Ubuntu、systemd、amd64/arm64 的原生新安装；不接管已有部署。

## 从零下载安装

```sh
curl -fsSL https://gitee.com/wangcong886/frpc-ui/raw/main/scripts/install.sh | sudo bash
```

root 可直接 `| bash`。入口默认固定 `v0.1.0-preview.3`，可通过 `bash -s -- --version vX.Y.Z` 选择仓库已发布版本。默认 `--source gitee`；可选 `--source github`，只使用本项目固定仓库。`--verify-only` 仅下载并检查归档，不启动向导。preview.3 开始包含官方 FRP 缓存和 FRP_VERSION；旧预览版没有这些文件，使用其原版入口。

Console/FRP 归档与 SHA256SUMS 从同一选定 Release 经 HTTPS 下载，摘要检查用于发现文件损坏或不匹配，不是独立签名证明。Console 归档只允许单个普通 `frp-console` 文件；FRP 还由 Go 安装器核对官方固定摘要。无终端、缺少依赖、不支持系统/架构或校验失败时停止，不安装系统依赖、不修改已有服务。

向导标准输入重新连接 `/dev/tty`，因此 `curl | bash` 可以正常交互并隐藏输入 Token。每次使用独立私有临时目录，退出清理下载文件；私有计划保留在 `/root/frp-console-install-*.json`（0600），取消仍不安装。管理员在完成安装后首次网页访问设置。

发布流水线由 `.github/release-version` 更新触发，构建两架构 Console 归档，并校验/附带原版官方 FRP 归档、FRP_VERSION、SHA256SUMS、install.sh 和 BUILD_INFO；全部上传后才公开 GitHub 预览 Release，不覆盖已发布版本。Gitee 自动同步需配置专用 GITEE_TOKEN，见 [同步说明](gitee-sync.md)。arm64 仅构建/下载校验，不表示实机运行。

## 已有程序启动向导

```sh
sudo ./frp-console install wizard
```

向导选择 frpc/frps/both、独立名称和新目录、UI 回环地址、FRP 地址/端口及官方缓存归档。Token 通过终端隐藏输入，FRP 管理 API 密码随机生成。留空归档路径时下载官方 FRP 0.71.0，amd64/arm64 SHA-256 固定在安装器中。

预检检查系统、systemd、架构、目录/服务/用户冲突和目标端口；校验归档后提取官方二进制，再调用 verify。预览包含脱敏配置及单元文件，输入完整计划 ID 才执行，留空退出。安装后从 loopback UI 初始化 Console 管理员。

向导不创建 PVE 容器、不调整 nesting、防火墙、DNS、Nginx 或其他部署。UI 不绑定 LAN，远程使用 SSH 隧道。网页安装页面仍仅生成模板。

## 私有请求

自动化使用 mode 0600 的 JSON，包含 name、root、listen、configs、可选 archive/probes：name 以 frp-console 开头；root 为新建专属绝对目录；listen 为字面 loopback 地址；configs 为 frpc/frps 完整 TOML 文本，从私有配置读取；archive 为官方缓存 tar.gz，仍须匹配内置摘要。

```sh
sudo ./frp-console install plan --request /root/request.json --out /root/plan.json
sudo ./frp-console install apply --plan /root/plan.json --confirm FULL_PLAN_ID
```

请求和磁盘计划含原始凭据，必须在私有目录且权限 0600，不能提交或分享。终端预览已脱敏。确认绑定配置、Console 摘要、部署内容和 15 分钟有效期；执行前再次预检、校验摘要与 verify。

成功的同一计划重复执行返回原始记录，不覆盖配置、不重复启动，也不代表新的实时验证。未完成或失败记录禁止自动重放。服务启用失败时停止/禁用新单元并删除其文件，保留目录、配置、用户和记录，供检查。中断后查看 `/var/lib/frp-console-installer/<name>/operation.json`；尚无完整恢复、升级或卸载命令。

## 归属与权限

官方二进制、清单、systemd 单元归 root；清单和二进制父目录也必须为受保护的 root 目录。数据目录归安装用户、权限 0700，配置及 SQLite/密钥为 0600。Web 不执行特权操作。

清单登记配置路径、二进制摘要和固定单元名。Console 使用 `--manifest`，不能与 demo/导入混用。客户端/服务端可同装，FRPS 先启动。FRP 与 Console 无单元依赖，关闭 Console 不影响隧道。

## 分层验证

页面读取登记二进制摘要、systemd 状态/PID和 FRP loopback API，点击“验证登记目标”才发起协议探针。

- 安装：官方二进制摘要匹配。
- 进程：登记单元 loaded/active 且 PID 非零。
- 认证：FRPS 当前客户端数或运行代理的间接证据；FRPC 没有运行代理时保持未验证。
- 注册：官方 API 代理状态；名称/状态不证明所有运行参数与文件相同。
- 本地服务：登记后端探针；服务端不能代替客户端的本地检查。
- 业务：登记目标的协议响应，不从 running 推导。

探针由 root 在安装清单登记，浏览器不能传入任意目标。每项包含 instance、proxy、scope（local/business）、kind（tcp/http/https/udp）、address（字面 IP:port）。TCP connect 仅允许 local。HTTP/HTTPS 指定 path、status、sha256，可指定 Host/SNI 与 CA 文件；不跟随重定向、不关闭 TLS 校验。UDP 必须指定 prefix，要求 prefix + 本次 nonce 的精确响应。正文最多 64 KiB，结果不返回正文、凭据或原始错误。

六层记录来源和时间；本地/业务按配置修订与 PID 缓存最多 2 分钟，Console 重启后重新检查。探针在所在 Console 节点执行，不代替外网验收。结果描述固定登记目标，**不证明离线配置已应用**。网页保存/恢复仍只写文件，运行应用将在下一阶段接入。

接口依据：[FRPC 路由](https://github.com/fatedier/frp/blob/v0.71.0/client/api_router.go)、[FRPS 路由](https://github.com/fatedier/frp/blob/v0.71.0/server/api_router.go)。
