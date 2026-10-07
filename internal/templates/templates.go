package templates

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type InstallRequest struct {
	Role   string `json:"role"`
	Mode   string `json:"mode"`
	Server string `json:"server"`
	Port   int    `json:"port"`
}
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type InstallPlan struct {
	Files              []File   `json:"files"`
	ExecutionAvailable bool     `json:"executionAvailable"`
	Notes              []string `json:"notes"`
}

var hostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

func Install(r InstallRequest) (InstallPlan, error) {
	if r.Role != "frpc" && r.Role != "frps" {
		return InstallPlan{}, errors.New("请选择客户端或服务端")
	}
	if r.Mode != "systemd" && r.Mode != "compose" {
		return InstallPlan{}, errors.New("不支持此部署方式")
	}
	if !hostname.MatchString(r.Server) || strings.Contains(r.Server, "..") {
		return InstallPlan{}, errors.New("服务端主机名或 IPv4 地址无效")
	}
	if r.Port < 1024 || r.Port > 65535 {
		return InstallPlan{}, errors.New("当前阶段端口范围必须为 1024～65535")
	}
	config := fmt.Sprintf("bindAddr = \"0.0.0.0\"\nbindPort = %d\n\n[auth]\nmethod = \"token\"\ntoken = \"__SET_TOKEN_ON_HOST__\"\n\n[webServer]\naddr = \"127.0.0.1\"\nport = 17400\nuser = \"__SET_API_USER__\"\npassword = \"__SET_API_PASSWORD__\"\n", r.Port)
	if r.Role == "frpc" {
		config = fmt.Sprintf("serverAddr = %q\nserverPort = %d\n\n[auth]\nmethod = \"token\"\ntoken = \"__SET_TOKEN_ON_HOST__\"\n\n[webServer]\naddr = \"127.0.0.1\"\nport = 17401\nuser = \"__SET_API_USER__\"\npassword = \"__SET_API_PASSWORD__\"\n", r.Server, r.Port)
	}
	base := "/etc/frp-console/instances/" + r.Role
	files := []File{{Path: base + "/" + r.Role + ".toml", Content: config}}
	if r.Mode == "systemd" {
		files = append(files, File{Path: "/etc/systemd/system/frp-console-" + r.Role + ".service", Content: fmt.Sprintf("[Unit]\nDescription=独立官方 %s 服务\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nUser=frp\nGroup=frp\nExecStart=/opt/frp/0.71.0/%s -c %s/%s.toml\nRestart=on-failure\nRestartSec=5s\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\n\n[Install]\nWantedBy=multi-user.target\n", r.Role, r.Role, base, r.Role)})
	} else {
		files = append(files, File{Path: base + "/compose.yaml", Content: fmt.Sprintf("name: frp-console-%s\nservices:\n  %s:\n    image: ghcr.io/fatedier/%s:v0.71.0\n    restart: unless-stopped\n    network_mode: host\n    command: [\"-c\", \"/etc/frp/%s.toml\"]\n    volumes:\n      - ./:/etc/frp:ro\n    read_only: true\n    security_opt:\n      - no-new-privileges:true\n    cap_drop: [ALL]\n", r.Role, r.Role, r.Role, r.Role)})
	}
	return InstallPlan{Files: files, ExecutionAvailable: false, Notes: []string{"仅生成预览，未安装或修改服务。", "执行前需要 Linux amd64/arm64、官方 FRP 摘要校验、独立 frp 用户、凭据及端口检查。", "容器镜像摘要固定和自动安装将在后续阶段实现。", "FRP 不依赖管理程序；卸载管理程序应保留 FRP。"}}, nil
}

type NginxRequest struct {
	Domain    string `json:"domain"`
	Prefix    string `json:"prefix"`
	Upstream  string `json:"upstream"`
	Port      int    `json:"port"`
	TLS       bool   `json:"tls"`
	Cert      string `json:"cert"`
	Key       string `json:"key"`
	WebSocket bool   `json:"websocket"`
}

var prefix = regexp.MustCompile(`^/(?:[A-Za-z0-9_./-]*/)?$`)
var path = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func Nginx(r NginxRequest) (File, error) {
	if !hostname.MatchString(r.Domain) || strings.Contains(r.Domain, "..") {
		return File{}, errors.New("域名无效")
	}
	if r.Prefix == "" {
		r.Prefix = "/"
	}
	if !prefix.MatchString(r.Prefix) || strings.Contains(r.Prefix, "..") {
		return File{}, errors.New("路径前缀无效")
	}
	if !hostname.MatchString(r.Upstream) || strings.Contains(r.Upstream, "..") {
		return File{}, errors.New("上游地址无效")
	}
	if r.Port < 1 || r.Port > 65535 {
		return File{}, errors.New("上游端口无效")
	}
	listen := "listen 80;"
	tls := ""
	if r.TLS {
		if !path.MatchString(r.Cert) || !path.MatchString(r.Key) || strings.Contains(r.Cert, "..") || strings.Contains(r.Key, "..") {
			return File{}, errors.New("证书和私钥必须使用安全的绝对路径")
		}
		listen = "listen 443 ssl;"
		tls = fmt.Sprintf("    ssl_certificate %s;\n    ssl_certificate_key %s;\n", r.Cert, r.Key)
	}
	ws := ""
	if r.WebSocket {
		ws = "        proxy_http_version 1.1;\n        proxy_set_header Upgrade $http_upgrade;\n        proxy_set_header Connection \"upgrade\";\n        proxy_read_timeout 300s;\n"
	}
	content := fmt.Sprintf("# 仅生成预览；加载前请在完整 Nginx 配置中校验。\n# 已有站点请先检查冲突，再按需加入路径配置。\nserver {\n    %s\n    server_name %s;\n%s    location %s {\n        proxy_pass http://%s:%d;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n%s    }\n}\n", listen, r.Domain, tls, r.Prefix, r.Upstream, r.Port, ws)
	return File{Path: "frp-console-" + r.Domain + ".conf", Content: content}, nil
}
