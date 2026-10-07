package observe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Discovery returns paths only, never configuration contents or credentials.
type DiscoveredConfig struct {
	Path   string
	Source string
	Nginx  *NginxProfile
	Logs   []LogSource
}
type Discovery struct {
	Candidates []DiscoveredConfig
	Issues     []string
}
type discoveredMount struct{ Type, Source, Destination string }
type dockerDiscovery struct {
	Prefixes   []string
	Configs    []string
	WorkingDir string
	Mounts     []discoveredMount
}

// Filter argv at Docker's output boundary; do not read Env or full argv.
const discoveryFormat = `{"Configs":[{{ $sep := "" }}{{ $next := false }}{{ range .Args }}{{ if $next }}{{ $sep }}{{ json . }}{{ $sep = "," }}{{ $next = false }}{{ else if or (eq . "-c") (eq . "--config") }}{{ $next = true }}{{ else if and (ge (len .) 9) (eq (slice . 0 9) "--config=") }}{{ $sep }}{{ json (slice . 9) }}{{ $sep = "," }}{{ end }}{{ end }}],"Prefixes":[{{ $sep := "" }}{{ $next := false }}{{ range .Args }}{{ if $next }}{{ $sep }}{{ json . }}{{ $sep = "," }}{{ $next = false }}{{ else if eq . "-p" }}{{ $next = true }}{{ end }}{{ end }}],"WorkingDir":{{json .Config.WorkingDir}},"Mounts":[{{range $i,$m := .Mounts}}{{if $i}},{{end}}{"Type":{{json $m.Type}},"Source":{{json $m.Source}},"Destination":{{json $m.Destination}}}{{end}}]}`

func DiscoverExisting(ctx context.Context, role string, target Target) Discovery {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return discoverExisting(ctx, role, target, command, os.Stat)
}

func discoverExisting(ctx context.Context, role string, target Target, run Runner, stat func(string) (os.FileInfo, error)) Discovery {
	result := Discovery{}
	if (role != "frps" && role != "nginx") || target.Kind == "" || checkTarget(target) != nil {
		result.Issues = append(result.Issues, "未登记有效运行目标，无法自动查找；请手动填写宿主机路径。")
		return result
	}
	regular := func(p string) bool { s, e := stat(p); return e == nil && s.Mode().IsRegular() }
	directory := func(p string) bool { s, e := stat(p); return e == nil && s.IsDir() }
	seen := map[string]bool{}
	add := func(p, source string, n *NginxProfile, logs []LogSource) {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) || seen[p] || (!regular(p) && (n == nil || n.Context != "http-fragments" || !directory(filepath.Dir(p)))) {
			return
		}
		seen[p] = true
		result.Candidates = append(result.Candidates, DiscoveredConfig{Path: p, Source: source, Nginx: n, Logs: logs})
	}
	if target.Kind == "docker" {
		raw, e := run(ctx, "docker", "inspect", target.Name, "--format", discoveryFormat)
		var d dockerDiscovery
		if e != nil || json.Unmarshal(raw, &d) != nil || len(d.Mounts) > 128 || len(d.Configs) > 16 {
			result.Issues = append(result.Issues, "容器挂载信息无法读取；请检查容器名称和 Docker 权限，或手动填写路径。")
			return result
		}
		var mounts []discoveredMount
		for _, m := range d.Mounts {
			if (m.Type == "bind" || m.Type == "volume") && filepath.IsAbs(m.Source) && filepath.IsAbs(m.Destination) && filepath.Clean(m.Source) != "/" && filepath.Clean(m.Destination) != "/" {
				m.Source, m.Destination = filepath.Clean(m.Source), filepath.Clean(m.Destination)
				mounts = append(mounts, m)
			}
		}
		sort.SliceStable(mounts, func(i, j int) bool { return len(mounts[i].Destination) > len(mounts[j].Destination) })
		hostPath := func(inside string) string {
			if !filepath.IsAbs(inside) {
				if !filepath.IsAbs(d.WorkingDir) {
					return ""
				}
				inside = filepath.Join(d.WorkingDir, inside)
			}
			inside = filepath.Clean(inside)
			for _, m := range mounts {
				if rel, ok := virtualRelative(inside, m.Destination); ok && (rel == "" || directory(m.Source)) {
					return filepath.Join(m.Source, rel)
				}
			}
			return ""
		}
		if role == "frps" {
			for _, p := range d.Configs {
				add(hostPath(p), "容器配置参数与挂载映射（需确认）", nil, nil)
			}
			if len(d.Configs) == 0 {
				for _, m := range mounts {
					if filepath.Base(m.Destination) == "frps.toml" || filepath.Base(m.Destination) == "frps.ini" {
						add(m.Source, "容器挂载中的候选配置（需确认）", nil, nil)
					}
					if directory(m.Source) && (m.Destination == "/frp" || m.Destination == "/etc/frp" || m.Destination == "/etc/frps") {
						add(filepath.Join(m.Source, "frps.toml"), "容器配置目录中的候选文件（需确认）", nil, nil)
						add(filepath.Join(m.Source, "frps.ini"), "容器配置目录中的候选文件（旧 INI 仅只读；需确认）", nil, nil)
					}
				}
			}
		} else {
			if len(d.Prefixes) > 0 {
				result.Issues = append(result.Issues, "检测到自定义 Nginx 配置前缀（-p），请手动登记主配置及前缀。")
				return result
			}
			entries := d.Configs
			if len(entries) == 0 {
				entries = []string{"/etc/nginx/nginx.conf"}
			}
			for _, entry := range entries {
				if !filepath.IsAbs(entry) && filepath.IsAbs(d.WorkingDir) {
					entry = filepath.Join(d.WorkingDir, entry)
				}
				prefix := filepath.Dir(entry)
				if !filepath.IsAbs(prefix) {
					continue
				}
				if prefix != "/etc/nginx" {
					result.Issues = append(result.Issues, "Nginx 主配置位于非标准位置，无法可靠推断配置前缀；请手动登记主配置和前缀。")
					continue
				}
				n := &NginxProfile{Entry: hostPath(entry), Prefix: prefix, Runtime: target}
				roots := map[string]bool{}
				for _, m := range mounts {
					if _, ok := virtualRelative(m.Destination, prefix); !ok {
						continue
					}
					base := filepath.Base(m.Destination)
					if m.Destination != entry && m.Destination != prefix && base != "conf.d" && base != "stream.d" && base != "sites-enabled" && base != "sites-available" && filepath.Ext(base) != ".conf" {
						continue
					}
					root := m.Source
					if !directory(root) {
						root = filepath.Dir(root)
					}
					if root == "/" {
						continue
					}
					if !roots[root] {
						n.Roots = append(n.Roots, root)
						roots[root] = true
					}
					n.Mounts = append(n.Mounts, Mount{Inside: m.Destination, Host: m.Source})
				}
				if len(n.Roots) > 16 || len(n.Mounts) > 16 {
					result.Issues = append(result.Issues, "Nginx 挂载范围较多，请手动登记读取范围。")
					continue
				}
				var logs []LogSource
				for _, item := range []struct{ file, format string }{{"access.log", "nginx-access"}, {"error.log", "nginx-error"}} {
					if p := hostPath("/var/log/nginx/" + item.file); regular(p) {
						logs = append(logs, LogSource{Kind: "file", Name: p, Format: item.format})
					}
				}
				if regular(n.Entry) {
					add(n.Entry, "容器主配置及配置挂载（需确认）", n, logs)
				} else if len(d.Configs) == 0 {
					if p := hostPath(filepath.Join(prefix, "conf.d")); directory(p) && len(n.Roots) > 0 {
						n.Entry, n.Context = filepath.Join(p, "*.conf"), "http-fragments"
						add(n.Entry, "仅找到站点片段；不代表完整主配置（需确认）", n, logs)
					}
				}
			}
		}
	} else {
		raw, e := run(ctx, "systemctl", "show", target.Name, "--property=ExecStart", "--value")
		if e != nil {
			result.Issues = append(result.Issues, "无法读取服务启动信息，请手动填写路径。")
			return result
		}
		if role == "nginx" && regexp.MustCompile(`(?:^|\s)-p(?:\s|$)`).Match(raw) {
			result.Issues = append(result.Issues, "检测到自定义 Nginx 配置前缀（-p），请手动登记主配置及前缀。")
			return result
		}
		paths := configArguments(string(raw))
		if len(paths) == 0 {
			if role == "frps" {
				paths = []string{"/etc/frp/frps.toml", "/etc/frps/frps.toml", "/etc/frps.toml", "/etc/frp/frps.ini", "/etc/frps/frps.ini", "/etc/frps.ini"}
			} else {
				paths = []string{"/etc/nginx/nginx.conf"}
			}
		}
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				wd, err := run(ctx, "systemctl", "show", target.Name, "--property=WorkingDirectory", "--value")
				if err != nil || !filepath.IsAbs(strings.TrimSpace(string(wd))) {
					continue
				}
				p = filepath.Join(strings.TrimSpace(string(wd)), p)
			}
			var n *NginxProfile
			if role == "nginx" {
				if filepath.Dir(p) != "/etc/nginx" {
					result.Issues = append(result.Issues, "Nginx 主配置位于非标准位置，无法可靠推断配置前缀；请手动登记主配置和前缀。")
					continue
				}
				n = &NginxProfile{Entry: p, Prefix: filepath.Dir(p), Roots: []string{filepath.Dir(p)}, Runtime: target}
			}
			add(p, "服务启动参数或标准配置位置（需确认）", n, nil)
		}
	}
	if len(result.Candidates) == 0 {
		result.Issues = append(result.Issues, "未找到可读取的宿主机配置；配置可能仅在容器内部、使用非标准路径或由启动脚本决定，请手动填写。")
	}
	return result
}

var configArg = regexp.MustCompile(`(?:^|\s)(?:-c\s+|--config(?:=|\s+))(?:"([^"]+)"|'([^']+)'|([^\s;]+))`)

func configArguments(raw string) []string {
	var paths []string
	for _, match := range configArg.FindAllStringSubmatch(raw, 16) {
		for _, value := range match[1:] {
			if value != "" {
				paths = append(paths, value)
				break
			}
		}
	}
	return paths
}
