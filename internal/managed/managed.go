// Package managed describes exclusively installed instances and reads runtime evidence.
package managed

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
)

type Instance struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Config     string `json:"config"`
	Binary     string `json:"binary"`
	BinaryHash string `json:"binaryHash"`
	Unit       string `json:"unit"`
}
type Probe struct {
	Instance string `json:"instance"`
	Proxy    string `json:"proxy"`
	Scope    string `json:"scope"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Host     string `json:"host,omitempty"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Status   int    `json:"status,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	CAFile   string `json:"caFile,omitempty"`
}
type Manifest struct {
	Version   int        `json:"version"`
	Name      string     `json:"name"`
	Root      string     `json:"root"`
	Listen    string     `json:"listen"`
	Instances []Instance `json:"instances"`
	Probes    []Probe    `json:"probes,omitempty"`
}

func Digest(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Load(path string) (*Manifest, error) {
	if e := trusted(path); e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(raw) > config.MaxSize {
		return nil, errors.New("管理清单超出大小限制")
	}
	var m Manifest
	if e = json.Unmarshal(raw, &m); e != nil {
		return nil, errors.New("管理清单无效")
	}
	if m.Version != 1 || !filepath.IsAbs(m.Root) || len(m.Instances) == 0 || len(m.Instances) > 2 {
		return nil, errors.New("不支持此管理清单")
	}
	seen := map[string]bool{}
	for _, i := range m.Instances {
		if (i.ID != "frpc" && i.ID != "frps") || i.ID != i.Role || seen[i.ID] {
			return nil, errors.New("管理实例角色无效")
		}
		seen[i.ID] = true
		if i.Config != filepath.Join(m.Root, "data", "instances", i.ID, i.ID+".toml") || i.Binary != filepath.Join(m.Root, "frp", i.ID) || i.Unit != m.Name+"-"+i.ID+".service" {
			return nil, errors.New("清单路径或服务单元与安装记录不匹配")
		}
		if e = trusted(i.Binary); e != nil {
			return nil, e
		}
	}
	for _, p := range m.Probes {
		if !seen[p.Instance] {
			return nil, errors.New("探针所属实例未登记")
		}
		if e = ValidateProbe(p); e != nil {
			return nil, e
		}
	}
	return &m, nil
}
func ValidateProbe(p Probe) error {
	host, port, e := net.SplitHostPort(p.Address)
	n, ne := strconv.Atoi(port)
	if e != nil || ne != nil || net.ParseIP(host) == nil || n < 1 || n > 65535 {
		return errors.New("探针必须使用明确的 IP 地址和有效端口")
	}
	if p.Scope != "local" && p.Scope != "business" {
		return errors.New("探针范围无效")
	}
	if p.Kind == "tcp" {
		if p.Scope != "local" {
			return errors.New("仅建立 TCP 连接不能验证业务")
		}
		return nil
	}
	if p.Kind == "udp" {
		if p.Prefix == "" || len(p.Prefix) > 128 {
			return errors.New("UDP 探针必须登记响应前缀")
		}
		return nil
	}
	if p.Kind != "http" && p.Kind != "https" {
		return errors.New("不支持此探针协议")
	}
	if len(p.SHA256) != 64 || p.Status < 100 || p.Status > 599 || !strings.HasPrefix(p.Path, "/") || strings.ContainsAny(p.Path+p.Host, "\r\n") {
		return errors.New("HTTP 探针必须登记响应状态码和响应体 SHA-256")
	}
	if _, e = hex.DecodeString(p.SHA256); e != nil {
		return errors.New("响应摘要无效")
	}
	return nil
}

type Check struct {
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
	At       int64  `json:"checkedAt"`
	Source   string `json:"source"`
}
type Process struct {
	State    string `json:"state"`
	SubState string `json:"subState"`
	PID      int    `json:"pid"`
}
type Proxy struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
}
type Observation struct {
	Revision string           `json:"revision"`
	Layers   map[string]Check `json:"layers"`
	Process  Process          `json:"process"`
	Proxies  []Proxy          `json:"proxies"`
}
type Collector struct {
	Manifest *Manifest
	Run      func(context.Context, string, ...string) ([]byte, error)
}

func Unknown(revision string) Observation {
	o := Observation{Revision: revision, Layers: map[string]Check{}, Proxies: []Proxy{}}
	for _, k := range []string{"installation", "process", "authentication", "registration", "localService", "business"} {
		o.Layers[k] = Check{Status: "not_checked", Evidence: "尚未采集到验证证据"}
	}
	return o
}
func (c *Collector) set(o *Observation, k, status, source, evidence string) {
	o.Layers[k] = Check{status, evidence, time.Now().Unix(), source}
}
func (c *Collector) Observe(ctx context.Context, id string, d *config.Document, probe bool) Observation {
	o := Unknown(config.Revision(d.Raw))
	var inst *Instance
	if c == nil || c.Manifest == nil {
		return o
	}
	for _, i := range c.Manifest.Instances {
		if i.ID == id {
			v := i
			inst = &v
			break
		}
	}
	if inst == nil {
		return o
	}
	digest, e := Digest(inst.Binary)
	if e != nil || digest != inst.BinaryHash {
		c.set(&o, "installation", "failed", "manifest", "官方程序发生变化或不可读取")
		return o
	}
	c.set(&o, "installation", "passed", "安装清单与 SHA-256", "登记的官方程序摘要匹配")
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return exec.CommandContext(ctx, n, a...).Output()
		}
	}
	raw, e := run(ctx, "systemctl", "show", inst.Unit, "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID")
	if e != nil {
		c.set(&o, "process", "failed", "systemd", "无法读取登记的服务单元")
		return o
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		p := strings.SplitN(line, "=", 2)
		if len(p) == 2 {
			fields[p[0]] = p[1]
		}
	}
	pid, _ := strconv.Atoi(fields["MainPID"])
	o.Process = Process{fields["ActiveState"], fields["SubState"], pid}
	if fields["LoadState"] != "loaded" || o.Process.State != "active" || pid == 0 {
		c.set(&o, "process", "failed", "systemd", "登记的服务未启动或没有有效进程 PID")
		return o
	}
	c.set(&o, "process", "passed", "systemd", "登记的服务已启动，且具有有效进程 PID")
	get := func(path string, v any) error { return adminGet(ctx, d.Values, path, v) }
	var response map[string]json.RawMessage
	if inst.Role == "frpc" {
		if e = get("/api/status", &response); e != nil {
			c.set(&o, "registration", "not_checked", "FRPC 回环管理接口", "管理接口不可用或访问被拒绝")
		} else {
			active := 0
			missing := 0
			expected := map[string]string{}
			if rows, ok := d.Values["proxies"].([]any); ok {
				for _, row := range rows {
					p, _ := row.(map[string]any)
					if p["enabled"] == false {
						continue
					}
					n, _ := p["name"].(string)
					t, _ := p["type"].(string)
					expected[n] = t
				}
			}
			for _, kind := range []string{"tcp", "udp", "http", "https"} {
				var entries []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				}
				_ = json.Unmarshal(response[kind], &entries)
				for _, p := range entries {
					o.Proxies = append(o.Proxies, Proxy{p.Name, kind, p.Status})
					if expected[p.Name] == kind && p.Status == "running" {
						active++
						delete(expected, p.Name)
					}
				}
			}
			missing = len(expected)
			if active > 0 {
				c.set(&o, "authentication", "passed", "FRPC 管理接口", "当前已注册且运行中的代理表明控制连接已通过认证")
			} else {
				c.set(&o, "authentication", "not_checked", "FRPC 管理接口", "没有运行中的代理时，此接口不能直接证明客户端认证成功")
			}
			if missing > 0 {
				c.set(&o, "registration", "failed", "FRPC 管理接口", "部分已启用的代理未运行")
			} else if active > 0 {
				c.set(&o, "registration", "passed", "FRPC 管理接口", "配置中所有启用的代理均报告正在运行")
			}
		}
	} else {
		var info struct {
			Clients int `json:"clientCounts"`
		}
		if get("/api/serverinfo", &info) == nil && info.Clients > 0 {
			c.set(&o, "authentication", "passed", "FRPS 管理接口", "已读取到当前通过认证的客户端")
		}
		count := 0
		complete := true
		for _, kind := range []string{"tcp", "udp", "http", "https"} {
			var reply struct {
				Proxies []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"proxies"`
			}
			if get("/api/proxy/"+kind, &reply) != nil {
				complete = false
				continue
			}
			for _, p := range reply.Proxies {
				o.Proxies = append(o.Proxies, Proxy{p.Name, kind, p.Status})
				if p.Status == "online" {
					count++
				}
			}
		}
		if complete && count > 0 {
			c.set(&o, "registration", "passed", "FRPS 管理接口", "已读取到在线代理注册记录")
			c.set(&o, "authentication", "passed", "FRPS 管理接口", "在线代理表明有客户端已通过认证")
		}
	}
	// Probe targets are immutable installation metadata, never arbitrary browser URLs.
	if probe {
		for _, scope := range []string{"local", "business"} {
			total, failed := 0, 0
			for _, p := range c.Manifest.Probes {
				if p.Instance != id || p.Scope != scope {
					continue
				}
				total++
				if ProbeOnce(ctx, p) != nil {
					failed++
				}
			}
			key := scope
			if scope == "local" {
				key = "localService"
			}
			if total > 0 {
				status := "passed"
				if failed > 0 {
					status = "failed"
				}
				c.set(&o, key, status, "已登记的明确探针", fmt.Sprintf("%d/%d 项协议检查通过", total-failed, total))
			}
		}
	}
	for n := range o.Proxies {
		o.Proxies[n].Name = d.RedactText(o.Proxies[n].Name)
		switch o.Proxies[n].Status {
		case "running", "online", "offline", "closed", "new", "start error", "wait start":
		default:
			o.Proxies[n].Status = "unknown"
		}
	}
	return o
}
func number(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func adminGet(ctx context.Context, values map[string]any, path string, out any) error {
	v, _ := values["webServer"].(map[string]any)
	address, _ := v["addr"].(string)
	if address == "" {
		address = "127.0.0.1"
	}
	ip := net.ParseIP(address)
	port := number(v["port"])
	if ip == nil || !ip.IsLoopback() || port < 1 || port > 65535 || v["tls"] != nil {
		return errors.New("当前仅支持不使用 TLS 的 FRP 回环管理接口")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(address, strconv.Itoa(port))+path, nil)
	if e != nil {
		return e
	}
	user, _ := v["user"].(string)
	password, _ := v["password"].(string)
	req.SetBasicAuth(user, password)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("管理接口不可用")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("管理接口拒绝访问")
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// ReadServerAPI is deliberately limited to read-only, fixed FRPS routes.
func ReadServerAPI(ctx context.Context, d *config.Document, path string, out any) error {
	switch path {
	case "/api/serverinfo", "/api/clients", "/api/proxy/tcp", "/api/proxy/udp", "/api/proxy/http", "/api/proxy/https":
		return adminGet(ctx, d.Values, path, out)
	default:
		return errors.New("不支持此采集接口路径")
	}
}

// TrustedFile verifies administrator-controlled metadata without following links.
func TrustedFile(path string) error { return trusted(path) }
func ProbeOnce(ctx context.Context, p Probe) error {
	if e := ValidateProbe(p); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if p.Kind == "tcp" || p.Kind == "udp" {
		conn, e := (&net.Dialer{}).DialContext(ctx, p.Kind, p.Address)
		if e != nil {
			return errors.New("服务不可达")
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
		if p.Kind == "tcp" {
			return nil
		}
		nonce := make([]byte, 16)
		if _, e = io.ReadFull(strings.NewReader(config.Revision(strconv.FormatInt(time.Now().UnixNano(), 10))), nonce); e != nil {
			return e
		}
		if _, e = conn.Write(nonce); e != nil {
			return e
		}
		buf := make([]byte, 1024)
		n, e := conn.Read(buf)
		if e != nil || string(buf[:n]) != p.Prefix+string(nonce) {
			return errors.New("UDP 协议响应不匹配")
		}
		return nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
	if p.CAFile != "" {
		pem, e := os.ReadFile(p.CAFile)
		if e != nil {
			return errors.New("CA 证书不可读取")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("CA 证书无效")
		}
		tlsConfig.RootCAs = pool
	}
	req, e := http.NewRequestWithContext(ctx, "GET", p.Kind+"://"+p.Address+p.Path, nil)
	if e != nil {
		return e
	}
	if p.Host != "" {
		req.Host = p.Host
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("HTTP/TLS 请求失败")
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || len(body) > 65536 {
		return errors.New("响应超出验证大小限制")
	}
	hash := sha256.Sum256(body)
	if resp.StatusCode != p.Status || hex.EncodeToString(hash[:]) != p.SHA256 {
		return errors.New("HTTP 状态码或响应摘要不匹配")
	}
	return nil
}
