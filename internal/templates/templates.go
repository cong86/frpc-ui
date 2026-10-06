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
		return InstallPlan{}, errors.New("select frpc or frps")
	}
	if r.Mode != "systemd" && r.Mode != "compose" {
		return InstallPlan{}, errors.New("unsupported deployment mode")
	}
	if !hostname.MatchString(r.Server) || strings.Contains(r.Server, "..") {
		return InstallPlan{}, errors.New("invalid server hostname or IPv4 address")
	}
	if r.Port < 1024 || r.Port > 65535 {
		return InstallPlan{}, errors.New("port must be 1024..65535 in this milestone")
	}
	config := fmt.Sprintf("bindAddr = \"0.0.0.0\"\nbindPort = %d\n\n[auth]\nmethod = \"token\"\ntoken = \"__SET_TOKEN_ON_HOST__\"\n\n[webServer]\naddr = \"127.0.0.1\"\nport = 17400\nuser = \"__SET_API_USER__\"\npassword = \"__SET_API_PASSWORD__\"\n", r.Port)
	if r.Role == "frpc" {
		config = fmt.Sprintf("serverAddr = %q\nserverPort = %d\n\n[auth]\nmethod = \"token\"\ntoken = \"__SET_TOKEN_ON_HOST__\"\n\n[webServer]\naddr = \"127.0.0.1\"\nport = 17401\nuser = \"__SET_API_USER__\"\npassword = \"__SET_API_PASSWORD__\"\n", r.Server, r.Port)
	}
	base := "/etc/frp-console/instances/" + r.Role
	files := []File{{Path: base + "/" + r.Role + ".toml", Content: config}}
	if r.Mode == "systemd" {
		files = append(files, File{Path: "/etc/systemd/system/frp-console-" + r.Role + ".service", Content: fmt.Sprintf("[Unit]\nDescription=Independent official %s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nUser=frp\nGroup=frp\nExecStart=/opt/frp/0.71.0/%s -c %s/%s.toml\nRestart=on-failure\nRestartSec=5s\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\n\n[Install]\nWantedBy=multi-user.target\n", r.Role, r.Role, base, r.Role)})
	} else {
		files = append(files, File{Path: base + "/compose.yaml", Content: fmt.Sprintf("name: frp-console-%s\nservices:\n  %s:\n    image: ghcr.io/fatedier/%s:v0.71.0\n    restart: unless-stopped\n    network_mode: host\n    command: [\"-c\", \"/etc/frp/%s.toml\"]\n    volumes:\n      - ./:/etc/frp:ro\n    read_only: true\n    security_opt:\n      - no-new-privileges:true\n    cap_drop: [ALL]\n", r.Role, r.Role, r.Role, r.Role)})
	}
	return InstallPlan{Files: files, ExecutionAvailable: false, Notes: []string{"Preview only: no installation or service modification has been executed.", "Requires Linux amd64/arm64, FRP checksum verification, a dedicated frp user, credentials, and port checks before execution.", "Container digest pinning and automatic installation are pending the next milestone.", "FRP has no dependency on the Console process. Uninstalling Console should preserve FRP."}}, nil
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
		return File{}, errors.New("invalid domain")
	}
	if r.Prefix == "" {
		r.Prefix = "/"
	}
	if !prefix.MatchString(r.Prefix) || strings.Contains(r.Prefix, "..") {
		return File{}, errors.New("invalid location prefix")
	}
	if !hostname.MatchString(r.Upstream) || strings.Contains(r.Upstream, "..") {
		return File{}, errors.New("invalid upstream")
	}
	if r.Port < 1 || r.Port > 65535 {
		return File{}, errors.New("invalid upstream port")
	}
	listen := "listen 80;"
	tls := ""
	if r.TLS {
		if !path.MatchString(r.Cert) || !path.MatchString(r.Key) || strings.Contains(r.Cert, "..") || strings.Contains(r.Key, "..") {
			return File{}, errors.New("certificate and key must be safe absolute paths")
		}
		listen = "listen 443 ssl;"
		tls = fmt.Sprintf("    ssl_certificate %s;\n    ssl_certificate_key %s;\n", r.Cert, r.Key)
	}
	ws := ""
	if r.WebSocket {
		ws = "        proxy_http_version 1.1;\n        proxy_set_header Upgrade $http_upgrade;\n        proxy_set_header Connection \"upgrade\";\n        proxy_read_timeout 300s;\n"
	}
	content := fmt.Sprintf("# Generated preview only; test in the full nginx configuration before loading.\n# For an existing server block, include only the location after reviewing conflicts.\nserver {\n    %s\n    server_name %s;\n%s    location %s {\n        proxy_pass http://%s:%d;\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n%s    }\n}\n", listen, r.Domain, tls, r.Prefix, r.Upstream, r.Port, ws)
	return File{Path: "frp-console-" + r.Domain + ".conf", Content: content}, nil
}
