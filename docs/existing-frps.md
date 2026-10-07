# 已有 FRPS / Nginx 只读接入

此功能增加管理 UI，不重装 FRPS、不接管配置写入、不控制原服务。支持已有 FRPS TOML、非 TLS 回环管理 API、Docker 或 systemd 的进程元数据；不存在的接口、未启用的管理 API 和无证据的层级保持未验证。

## 权限与数据流

管理员在目标 Linux 主机运行 `frp-console observe`，按受保护的固定 Profile 读取配置、官方 API、服务元数据和有限日志，输出脱敏 JSON 快照。Console 使用 `--observed-snapshot` 读取快照；网页不调用采集命令、不持有 Docker socket，也不读取原始 FRPS 配置、Nginx 密钥或原始日志。

Profile、快照及其所有父目录须 root 拥有，不能被组或其他用户写入。Profile 不保存新的 API 密码，采集器从原 FRPS 配置读取已有凭据。原 FRPS TOML 须为受保护的 root 常规文件；其他部署权限需要管理员先评估，程序不自动修改原权限。

原配置不变，快照的配置文本从脱敏字段重建，不包含原始注释。已有部署始终只读；计划/代理修改与恢复接口不会获得其写权限。

## Profile 示例

以下地址和路径是占位示例，应先只读核对实际部署后填写，不代表现有云端设置：

```json
{
  "version": 1,
  "frpsConfig": "/srv/frp/frps.toml",
  "frpsRuntime": {"kind": "docker", "name": "frps"},
  "nginx": {
    "entry": "/srv/nginx/nginx.conf",
    "prefix": "/etc/nginx",
    "roots": ["/srv/nginx"],
    "mounts": [{"inside": "/etc/nginx", "host": "/srv/nginx"}],
    "runtime": {"kind": "docker", "name": "nginx"}
  },
  "logs": [
    {"kind": "docker", "name": "frps", "format": "frps"},
    {"kind": "file", "name": "/srv/nginx/logs/access.log", "format": "nginx-access"},
    {"kind": "file", "name": "/srv/nginx/logs/error.log", "format": "nginx-error"}
  ]
}
```

原生服务的 runtime/log source 可用 `{"kind":"systemd","name":"frps.service"}`。日志还需 `format`，可选 frps、nginx-access、nginx-error。runtime 留空则不推断进程状态，仍可读取已开启的 FRPS API。Docker inspect 只取 `.State`，不读取环境变量、启动参数或挂载凭据；日志只取最后 100 行。没有登记的来源不访问。

如果 Docker 仅将站点片段挂载到宿主机，可将 nginx 设置为 `"context":"http-fragments"`、`"entry":"/srv/nginx/conf.d/*.conf"`，并登记对应 roots/mounts。此模式明确标记 partial：未采集完整主配置、HTTP 全局参数及继承关系。不能把片段列表当成完整 nginx 配置。

## 执行与刷新

管理员在新的受保护目录保存 Profile，例如 `/etc/frp-console-observe/profile.json`（0600），为快照准备 root 拥有的 `/var/lib/frp-console-observe`（0755）。不要使用 Console 用户可写的数据目录保存受信快照。

```sh
# root：仅采集；不执行安装、重载或重启
frp-console observe --profile /etc/frp-console-observe/profile.json \
  --out /var/lib/frp-console-observe/snapshot.json

# 独立低权限用户：data 为该用户专属的私有目录
frp-console --data /path/to/private-console-data \
  --observed-snapshot /var/lib/frp-console-observe/snapshot.json \
  --listen 127.0.0.1:18745
```

采集器序列化同一输出的并发采集，原子替换自己的快照；不会覆盖原配置、Profile 或任意已有文件。首次网页访问初始化独立管理员。现有 API 的 Host/Origin/CSRF 防护和回环限制保持有效，远程访问仍使用 SSH 隧道。

可以由管理员安排一个独立 timer 定期执行上述采集命令（建议 15～30 秒），浏览器每 30 秒读新快照；本程序不会自动安装 timer。快照超过 120 秒时，运行健康层级降为未验证，速率失效，配置和日志按历史快照展示。过期后需重新采集，不以刷新网页冒充重新探测。

## 页面内容与证据边界

- 服务端：脱敏配置、Docker/systemd 进程观测、官方 API 认证与四类注册代理；不证明配置已被当前进程加载。安装与业务层没有专门证据时保持未验证。
- 流量与日志：FRPS 累计流入/流出、连接/客户端数、每代理当日流量；版本支持时返回客户端在线/版本/最近连接摘要，不返回身份 key、来源地址或原始响应。
- 两次采集平均速率：只有同 Profile、配置修订、非零 PID、单调计数，间隔 1～120 秒才计算。进程重启、计数重置、来源变化不跨样本求速率。它是区间平均，不是瞬时实时流量。
- Nginx：只解析登记根目录内磁盘配置与 include，可读取独立 server/location、监听、域名、公开证书路径、return 跳转、HTTP upstream、proxy_pass 和 Upgrade 头。include 越界、循环、缺失或不支持时说明 partial/unavailable。未知指令不原样展示，不打开证书私钥，也不执行 nginx -t/-T/reload。
- FRP 关联只根据回环上游端口匹配 HTTP vhost/管理端口，显示候选提示；不证明请求真的经过该代理，也不展开动态变量、Lua、stream 或复杂继承的运行语义。
- 日志：结构化服务事件和默认 common/combined 格式 Nginx 访问摘要，不返回原始行、URL/查询参数、请求头、IP、用户名或任意错误文本；自定义 log_format 未识别行单独计数。每文件最多读尾部 128 KiB、每来源最多 100 行。窗口响应体字节合计不代表全量 Nginx 流量或速率。

完整写接管、配置应用、日志原文导出、实时连接推送及反代加载仍为后续能力。当前一键安装固定 preview.4；本功能需使用包含该代码的新构建，不能认为旧发布包已更新。
