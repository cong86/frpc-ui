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
		return nil, errors.New("manifest too large")
	}
	var m Manifest
	if e = json.Unmarshal(raw, &m); e != nil {
		return nil, errors.New("invalid managed manifest")
	}
	if m.Version != 1 || !filepath.IsAbs(m.Root) || len(m.Instances) == 0 || len(m.Instances) > 2 {
		return nil, errors.New("unsupported managed manifest")
	}
	seen := map[string]bool{}
	for _, i := range m.Instances {
		if (i.ID != "frpc" && i.ID != "frps") || i.ID != i.Role || seen[i.ID] {
			return nil, errors.New("invalid managed role")
		}
		seen[i.ID] = true
		if i.Config != filepath.Join(m.Root, "data", "instances", i.ID, i.ID+".toml") || i.Binary != filepath.Join(m.Root, "frp", i.ID) || i.Unit != m.Name+"-"+i.ID+".service" {
			return nil, errors.New("manifest paths or unit do not match installation")
		}
		if e = trusted(i.Binary); e != nil {
			return nil, e
		}
	}
	for _, p := range m.Probes {
		if !seen[p.Instance] {
			return nil, errors.New("probe instance is not registered")
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
		return errors.New("probe requires literal IP and valid port")
	}
	if p.Scope != "local" && p.Scope != "business" {
		return errors.New("invalid probe scope")
	}
	if p.Kind == "tcp" {
		if p.Scope != "local" {
			return errors.New("TCP connect alone cannot verify business")
		}
		return nil
	}
	if p.Kind == "udp" {
		if p.Prefix == "" || len(p.Prefix) > 128 {
			return errors.New("UDP requires a response prefix")
		}
		return nil
	}
	if p.Kind != "http" && p.Kind != "https" {
		return errors.New("unsupported probe protocol")
	}
	if len(p.SHA256) != 64 || p.Status < 100 || p.Status > 599 || !strings.HasPrefix(p.Path, "/") || strings.ContainsAny(p.Path+p.Host, "\r\n") {
		return errors.New("HTTP probe requires status and body SHA-256")
	}
	if _, e = hex.DecodeString(p.SHA256); e != nil {
		return errors.New("invalid response digest")
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
		o.Layers[k] = Check{Status: "not_checked", Evidence: "No evidence collected"}
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
		c.set(&o, "installation", "failed", "manifest", "Official binary changed or unavailable")
		return o
	}
	c.set(&o, "installation", "passed", "manifest + SHA-256", "Registered official binary digest matches")
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return exec.CommandContext(ctx, n, a...).Output()
		}
	}
	raw, e := run(ctx, "systemctl", "show", inst.Unit, "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID")
	if e != nil {
		c.set(&o, "process", "failed", "systemd", "Unable to read registered unit")
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
		c.set(&o, "process", "failed", "systemd", "Registered unit is not active with a live PID")
		return o
	}
	c.set(&o, "process", "passed", "systemd", "Registered unit active with a live PID")
	get := func(path string, v any) error { return adminGet(ctx, d.Values, path, v) }
	var response map[string]json.RawMessage
	if inst.Role == "frpc" {
		if e = get("/api/status", &response); e != nil {
			c.set(&o, "registration", "not_checked", "FRPC loopback API", "Admin interface unavailable or access denied")
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
				c.set(&o, "authentication", "passed", "FRPC API", "Currently registered running proxy proves an authenticated control connection")
			} else {
				c.set(&o, "authentication", "not_checked", "FRPC API", "Without a running proxy this API does not directly prove client authentication")
			}
			if missing > 0 {
				c.set(&o, "registration", "failed", "FRPC API", "Some enabled proxy definitions are not running")
			} else if active > 0 {
				c.set(&o, "registration", "passed", "FRPC API", "All enabled configured proxies report running")
			}
		}
	} else {
		var info struct {
			Clients int `json:"clientCounts"`
		}
		if get("/api/serverinfo", &info) == nil && info.Clients > 0 {
			c.set(&o, "authentication", "passed", "FRPS API", "Current authenticated clients observed")
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
			c.set(&o, "registration", "passed", "FRPS API", "Online proxy registrations observed")
			c.set(&o, "authentication", "passed", "FRPS API", "Online proxies imply authenticated clients")
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
				c.set(&o, key, status, "registered explicit probes", fmt.Sprintf("%d/%d protocol checks passed", total-failed, total))
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
		return errors.New("only non-TLS loopback FRP API supported")
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
		return errors.New("admin API unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("admin API rejected access")
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// ReadServerAPI is deliberately limited to read-only, fixed FRPS routes.
func ReadServerAPI(ctx context.Context, d *config.Document, path string, out any) error {
	switch path {
	case "/api/serverinfo", "/api/clients", "/api/proxy/tcp", "/api/proxy/udp", "/api/proxy/http", "/api/proxy/https":
		return adminGet(ctx, d.Values, path, out)
	default:
		return errors.New("unsupported observation route")
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
			return errors.New("service unreachable")
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
			return errors.New("UDP protocol response mismatch")
		}
		return nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.Host}
	if p.CAFile != "" {
		pem, e := os.ReadFile(p.CAFile)
		if e != nil {
			return errors.New("CA unavailable")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("CA invalid")
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
		return errors.New("HTTP/TLS request failed")
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || len(body) > 65536 {
		return errors.New("response exceeds verification limit")
	}
	hash := sha256.Sum256(body)
	if resp.StatusCode != p.Status || hex.EncodeToString(hash[:]) != p.SHA256 {
		return errors.New("HTTP status or response digest mismatch")
	}
	return nil
}
