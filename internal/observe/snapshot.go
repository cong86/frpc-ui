package observe

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/pelletier/go-toml/v2"
)

type Client struct {
	Online          bool   `json:"online"`
	Version         string `json:"version"`
	LastConnectedAt int64  `json:"lastConnectedAt"`
}
type Proxy struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	RemotePort  int      `json:"remotePort"`
	Domains     []string `json:"domains"`
	Locations   []string `json:"locations"`
	TrafficIn   int64    `json:"trafficIn"`
	TrafficOut  int64    `json:"trafficOut"`
	Connections int      `json:"connections"`
}
type Traffic struct {
	Available     bool    `json:"available"`
	In            int64   `json:"in"`
	Out           int64   `json:"out"`
	Connections   int     `json:"connections"`
	Clients       int     `json:"clients"`
	RateAvailable bool    `json:"rateAvailable"`
	InPerSecond   float64 `json:"inPerSecond"`
	OutPerSecond  float64 `json:"outPerSecond"`
	Interval      int64   `json:"interval"`
}
type Snapshot struct {
	Version          int                 `json:"version"`
	ProfileRevision  string              `json:"profileRevision"`
	CollectedAt      int64               `json:"collectedAt"`
	Stale            bool                `json:"stale"`
	Config           config.Snapshot     `json:"config"`
	Runtime          managed.Observation `json:"runtime"`
	FRPSVersion      string              `json:"frpsVersion"`
	Clients          []Client            `json:"clients"`
	ClientsAvailable bool                `json:"clientsAvailable"`
	Proxies          []Proxy             `json:"proxies"`
	Traffic          Traffic             `json:"traffic"`
	Nginx            NginxState          `json:"nginx"`
	Logs             []LogWindow         `json:"logs"`
}

func LoadSnapshot(path string) (Snapshot, error) {
	var s Snapshot
	if e := readJSON(path, &s); e != nil {
		return s, e
	}
	if s.Version != 1 || len(s.ProfileRevision) != 64 || s.CollectedAt <= 0 || s.Config.Editable || s.Runtime.Layers == nil {
		return s, errors.New("采集快照无效")
	}
	if s.CollectedAt > time.Now().Unix()+5 {
		return s, errors.New("快照时间晚于当前时间")
	}
	s.Stale = time.Now().Unix()-s.CollectedAt > 120
	if s.Stale {
		for _, k := range []string{"installation", "process", "authentication", "registration", "localService", "business"} {
			s.Runtime.Layers[k] = managed.Check{Status: "not_checked", Evidence: "快照已过期；请在主机重新采集", At: s.CollectedAt, Source: "只读快照"}
		}
		s.Traffic.RateAvailable = false
		s.Nginx.ProcessCheck = managed.Check{Status: "not_checked", Evidence: "快照已过期；请在主机重新采集", At: s.CollectedAt, Source: "只读快照"}
	}
	return s, nil
}
func (t *Traffic) WithPrevious(s, previous Snapshot) {
	dt := s.CollectedAt - previous.CollectedAt
	if !t.Available || !previous.Traffic.Available || dt <= 0 || dt > 120 || s.ProfileRevision != previous.ProfileRevision || s.Config.Revision != previous.Config.Revision || s.Runtime.Process.PID == 0 || s.Runtime.Process.PID != previous.Runtime.Process.PID || t.In < previous.Traffic.In || t.Out < previous.Traffic.Out {
		return
	}
	t.Interval = dt
	t.InPerSecond = float64(t.In-previous.Traffic.In) / float64(dt)
	t.OutPerSecond = float64(t.Out-previous.Traffic.Out) / float64(dt)
	t.RateAvailable = true
}
func readProcess(ctx context.Context, t Target, run Runner) (managed.Process, managed.Check) {
	c := managed.Check{Status: "not_checked", Evidence: "未登记运行目标", At: time.Now().Unix(), Source: "管理员采集配置"}
	var p managed.Process
	if t.Kind == "" {
		return p, c
	}
	var b []byte
	var e error
	if t.Kind == "docker" {
		b, e = run(ctx, "docker", "inspect", "--format", "{{json .State}}", t.Name)
		if e == nil {
			var v struct {
				Status  string
				Running bool
				Pid     int
			}
			e = json.Unmarshal(b, &v)
			p = managed.Process{State: v.Status, PID: v.Pid}
			if !v.Running {
				p.PID = 0
			}
		}
	} else {
		b, e = run(ctx, "systemctl", "show", t.Name, "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID")
		if e == nil {
			m := map[string]string{}
			for _, line := range strings.Split(string(b), "\n") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					m[parts[0]] = parts[1]
				}
			}
			p = managed.Process{State: m["ActiveState"], SubState: m["SubState"]}
			p.PID, _ = strconv.Atoi(m["MainPID"])
			if m["LoadState"] != "loaded" {
				p.PID = 0
			}
		}
	}
	c.Source = t.Kind + "只读元数据"
	if e != nil {
		c.Evidence = "运行元数据不可读取"
		return managed.Process{}, c
	}
	c.Status = "failed"
	c.Evidence = "登记的进程未运行"
	if p.PID > 0 && (p.State == "running" || p.State == "active") {
		c.Status = "passed"
		c.Evidence = "登记的进程报告正在运行，且具有进程 PID"
	}
	// Never return arbitrary daemon strings.
	switch p.State {
	case "running", "active", "inactive", "failed", "exited", "created", "paused", "restarting", "dead":
	default:
		p.State = "unknown"
	}
	p.SubState = ""
	return p, c
}
func Collect(ctx context.Context, p Profile, run Runner) (Snapshot, error) {
	if e := p.Validate(); e != nil {
		return Snapshot{}, e
	}
	if run == nil {
		run = command
	}
	if managed.TrustedFile(p.Config) != nil {
		return Snapshot{}, errors.New("FRPS 配置必须是受保护且归 root 所有的普通文件")
	}
	m := config.Manager{Instances: map[string]config.Instance{"frps": {ID: "frps", Role: "frps", Path: p.Config}}}
	d, e := m.Read("frps")
	if e != nil {
		return Snapshot{}, errors.New("FRPS TOML 配置无法读取或解析")
	}
	s := collectDocument(ctx, p, d, run)
	// Discard this collection if the authoritative configuration changed.
	after, e := m.Read("frps")
	if e != nil || config.Revision(after.Raw) != s.Config.Revision {
		return Snapshot{}, config.ErrConflict
	}
	return s, nil
}
func collectDocument(ctx context.Context, p Profile, d *config.Document, run Runner) Snapshot {
	s := Snapshot{Version: 1, ProfileRevision: profileRevision(p), CollectedAt: time.Now().Unix(), Config: d.Snapshot(), Runtime: managed.Unknown(config.Revision(d.Raw)), Clients: []Client{}, Proxies: []Proxy{}, Logs: []LogWindow{}}
	s.Config.Editable = false
	s.Config.Reason = "已有部署仅支持只读采集；不能修改原配置或服务。"
	// Reconstruct from scrubbed values; comments and arbitrary raw text never leave the collector.
	if b, e := toml.Marshal(s.Config.Values); e == nil {
		s.Config.Text = string(b)
	} else {
		s.Config.Text = "# 只读配置预览暂不可用"
	}
	s.Runtime.Process, s.Runtime.Layers["process"] = readProcess(ctx, p.Runtime, run)
	set := func(k, status, evidence string) {
		s.Runtime.Layers[k] = managed.Check{Status: status, Evidence: evidence, At: s.CollectedAt, Source: "FRPS 回环管理接口"}
	}
	var info struct {
		Version         string `json:"version"`
		TotalTrafficIn  *int64 `json:"totalTrafficIn"`
		TotalTrafficOut *int64 `json:"totalTrafficOut"`
		CurConns        *int   `json:"curConns"`
		ClientCounts    *int   `json:"clientCounts"`
	}
	if managed.ReadServerAPI(ctx, d, "/api/serverinfo", &info) == nil {
		s.FRPSVersion = d.RedactText(info.Version)
		if info.TotalTrafficIn != nil && info.TotalTrafficOut != nil && info.CurConns != nil && info.ClientCounts != nil && *info.TotalTrafficIn >= 0 && *info.TotalTrafficOut >= 0 && *info.CurConns >= 0 && *info.ClientCounts >= 0 {
			s.Traffic = Traffic{Available: true, In: *info.TotalTrafficIn, Out: *info.TotalTrafficOut, Connections: *info.CurConns, Clients: *info.ClientCounts}
		}
		if info.ClientCounts != nil && *info.ClientCounts > 0 {
			set("authentication", "passed", "已读取到通过认证的客户端数量")
		}
	}
	if managed.ReadServerAPI(ctx, d, "/api/clients", &s.Clients) == nil {
		s.ClientsAvailable = true
	} else {
		s.Clients = []Client{}
	}
	for i := range s.Clients {
		s.Clients[i].Version = d.RedactText(s.Clients[i].Version)
	}
	complete, online := true, 0
	for _, kind := range []string{"tcp", "udp", "http", "https"} {
		var response struct {
			Proxies []struct {
				Name            string `json:"name"`
				Status          string `json:"status"`
				TodayTrafficIn  int64  `json:"todayTrafficIn"`
				TodayTrafficOut int64  `json:"todayTrafficOut"`
				CurConns        int    `json:"curConns"`
				Conf            struct {
					RemotePort    int      `json:"remotePort"`
					CustomDomains []string `json:"customDomains"`
					Locations     []string `json:"locations"`
				} `json:"conf"`
			} `json:"proxies"`
		}
		if managed.ReadServerAPI(ctx, d, "/api/proxy/"+kind, &response) != nil {
			complete = false
			continue
		}
		for _, v := range response.Proxies {
			status := "unknown"
			if v.Status == "online" {
				status = "online"
				online++
			} else if v.Status == "offline" {
				status = "offline"
			}
			r := Proxy{Name: d.RedactText(v.Name), Type: kind, Status: status, RemotePort: v.Conf.RemotePort, TrafficIn: v.TodayTrafficIn, TrafficOut: v.TodayTrafficOut, Connections: v.CurConns}
			for _, x := range v.Conf.CustomDomains {
				r.Domains = append(r.Domains, d.RedactText(x))
			}
			for _, x := range v.Conf.Locations {
				r.Locations = append(r.Locations, d.RedactText(x))
			}
			s.Proxies = append(s.Proxies, r)
			s.Runtime.Proxies = append(s.Runtime.Proxies, managed.Proxy{Name: r.Name, Type: kind, Status: status})
		}
	}
	if complete && online > 0 {
		set("registration", "passed", "已读取到在线代理；仅查询四种支持的代理类型")
		set("authentication", "passed", "在线代理表明有客户端已通过认证")
	}
	if p.Nginx != nil {
		s.Nginx = ReadNginx(*p.Nginx, d)
		s.Nginx.Process, s.Nginx.ProcessCheck = readProcess(ctx, p.Nginx.Runtime, run)
	} else {
		s.Nginx = NginxState{Status: "not_configured", Sites: []Site{}, Issues: []string{}}
	}
	for _, source := range p.Logs {
		s.Logs = append(s.Logs, ReadLogs(ctx, source, run))
	}
	return s
}
