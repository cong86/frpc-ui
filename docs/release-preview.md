FRP Console systemd 安装与运行观测预览版，第一版尚未完成。仅 Debian/Ubuntu 原生新安装；提供 Linux amd64/arm64 程序，前端已嵌入。

下载入口检测系统及架构，从本 Release 下载对应归档和 SHA256SUMS，校验后启动交互向导。向导预览和确认后创建独立 FRPC/FRPS 与 Console 服务。UI 回环监听，管理员由首次网页访问初始化。

支持短密码和长密码，兼容已有管理员。Console 退出不影响 FRP 隧道。

仍未开放生产接管、配置运行应用/自动回滚、Compose 自动安装、Nginx 加载或升级/卸载。arm64 构建不代表 arm64 实机验收。
