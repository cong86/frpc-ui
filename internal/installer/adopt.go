package installer

import (
	"context"
	"crypto/sha256"
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
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/filelock"
	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/cong86/frpc-ui/internal/observe"
	"github.com/cong86/frpc-ui/internal/templates"
)

// Adoption adds only Console and collection units; existing services remain independent.
type AdoptionRequest struct {
	Name     string          `json:"name"`
	Root     string          `json:"root"`
	Listen   string          `json:"listen"`
	Interval int             `json:"intervalSeconds"`
	Profile  observe.Profile `json:"profile"`
}
type AdoptionPlan struct {
	ID             string           `json:"id"`
	Created        int64            `json:"created"`
	Request        AdoptionRequest  `json:"request"`
	Arch           string           `json:"arch"`
	ConsoleHash    string           `json:"consoleHash"`
	ConfigRevision string           `json:"configRevision"`
	Files          []templates.File `json:"files"`
	Warnings       []string         `json:"warnings"`
}

func adoptionID(p AdoptionPlan) string {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func checkAdoption(r AdoptionRequest) error {
	if !nameRE.MatchString(r.Name) || !strings.HasPrefix(r.Name, "frp-console") || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root || r.Root == "/" || strings.ContainsAny(r.Root, " \t\r\n\"'\\%$") {
		return errors.New("请使用安全的绝对路径、独立目录及安装名称")
	}
	host, port, e := net.SplitHostPort(r.Listen)
	n, pe := strconv.Atoi(port)
	if e != nil || pe != nil || n < 1024 || n > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("管理页面须使用明确的回环 IP 地址，端口范围为 1024～65535")
	}
	if r.Interval < 10 || r.Interval > 60 {
		return errors.New("采集间隔必须为 10～60 秒")
	}
	return r.Profile.Validate()
}
func adoptionPaths(r AdoptionRequest) []string {
	return []string{filepath.Join("/etc/systemd/system", r.Name+".service"), filepath.Join("/etc/systemd/system", r.Name+"-collect.service"), filepath.Join("/etc/systemd/system", r.Name+"-collect.timer")}
}
func adoptionPreflight(ctx context.Context, r AdoptionRequest) error {
	if e := checkAdoption(r); e != nil {
		return e
	}
	if runtime.GOOS != "linux" || Checksums[runtime.GOARCH] == "" || os.Geteuid() != 0 {
		return errors.New("接入安装需要 Linux amd64/arm64 和 root 权限")
	}
	raw, e := os.ReadFile("/etc/os-release")
	if e != nil || (!strings.Contains(string(raw), "ID=debian") && !strings.Contains(string(raw), "ID=ubuntu")) {
		return errors.New("接入安装支持 Debian 和 Ubuntu")
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		return errors.New("需要正在运行的 systemd")
	}
	for _, p := range append([]string{r.Root}, adoptionPaths(r)...) {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			return errors.New("接入目录或服务单元已存在；不会覆盖原文件")
		}
	}
	if _, e = os.Lstat(filepath.Join("/var/lib/frp-console-installer", r.Name)); !os.IsNotExist(e) {
		return errors.New("此名称已有安装历史；请使用原计划，或选择新的独立名称")
	}
	parent := filepath.Dir(r.Root)
	for {
		if _, e = os.Lstat(parent); e == nil {
			break
		}
		if !os.IsNotExist(e) || parent == filepath.Dir(parent) {
			return errors.New("接入目录的父目录不可用")
		}
		parent = filepath.Dir(parent)
	}
	if managed.TrustedDirectory(parent) != nil || managed.TrustedDirectory("/etc/systemd/system") != nil {
		return errors.New("接入父目录必须受保护且归 root 所有")
	}
	for _, p := range adoptionPaths(r) {
		b, e := exec.CommandContext(ctx, "systemctl", "show", filepath.Base(p), "--property=LoadState", "--value").Output()
		if e != nil || strings.TrimSpace(string(b)) != "not-found" {
			return errors.New("计划中的 systemd 单元已登记")
		}
	}
	if exec.CommandContext(ctx, "id", "-u", r.Name).Run() == nil {
		return errors.New("接入使用的用户已存在")
	}
	l, e := net.Listen("tcp", r.Listen)
	if e != nil {
		return errors.New("管理页面端口不可用")
	}
	l.Close()
	if managed.TrustedFile(r.Profile.Config) != nil {
		return errors.New("已有 FRPS 配置必须受保护且归 root 所有；不会修改原权限")
	}
	return nil
}
func adoptionFiles(r AdoptionRequest) []templates.File {
	profile := filepath.Join(r.Root, "observe", "profile.json")
	snapshot := filepath.Join(r.Root, "observe", "snapshot.json")
	web := fmt.Sprintf("[Unit]\nDescription=FRP 控制台已有部署管理页面\nAfter=network-online.target\n\n[Service]\nUser=%s\nGroup=%s\nExecStart=%s/bin/frp-console --data %s/data --observed-snapshot %s --listen %s\nRestart=on-failure\nRestartSec=2\nNoNewPrivileges=true\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n", r.Name, r.Name, r.Root, r.Root, snapshot, r.Listen)
	collector := fmt.Sprintf("[Unit]\nDescription=FRP 控制台只读采集\n\n[Service]\nType=oneshot\nUser=root\nExecStart=%s/bin/frp-console observe --profile %s --out %s\nTimeoutStartSec=55\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nReadWritePaths=%s/observe\nUMask=0077\n", r.Root, profile, snapshot, r.Root)
	timer := fmt.Sprintf("[Unit]\nDescription=FRP 控制台定时采集\n\n[Timer]\nOnBootSec=10\nOnUnitInactiveSec=%ds\nAccuracySec=1s\nUnit=%s-collect.service\n\n[Install]\nWantedBy=timers.target\n", r.Interval, r.Name)
	paths := adoptionPaths(r)
	b, _ := json.MarshalIndent(r.Profile, "", "  ")
	return []templates.File{{Path: profile, Content: string(b)}, {Path: paths[0], Content: web}, {Path: paths[1], Content: collector}, {Path: paths[2], Content: timer}}
}
func NewAdoptionPlan(ctx context.Context, r AdoptionRequest, console string) (AdoptionPlan, error) {
	if e := adoptionPreflight(ctx, r); e != nil {
		return AdoptionPlan{}, e
	}
	m := config.Manager{Instances: map[string]config.Instance{"frps": {ID: "frps", Role: "frps", Path: r.Profile.Config}}}
	d, e := m.Read("frps")
	if e != nil {
		return AdoptionPlan{}, errors.New("已有 FRPS TOML 配置无法解析")
	}
	hash, e := managed.Digest(console)
	if e != nil {
		return AdoptionPlan{}, e
	}
	p := AdoptionPlan{Created: time.Now().Unix(), Request: r, Arch: runtime.GOARCH, ConsoleHash: hash, ConfigRevision: config.Revision(d.Raw), Files: adoptionFiles(r)}
	observed, e := observe.Collect(ctx, r.Profile, nil)
	if e != nil || observed.Config.Revision != p.ConfigRevision {
		return AdoptionPlan{}, errors.New("原配置在预检期间发生变化或采集失败")
	}
	p.Warnings = []string{}
	if observed.Runtime.Layers["process"].Status != "passed" {
		p.Warnings = append(p.Warnings, "FRPS 进程未验证；安装管理页面不会启动原 FRPS")
	}
	if observed.Runtime.Layers["authentication"].Status != "passed" {
		p.Warnings = append(p.Warnings, "FRPS 认证未验证；管理接口可能不可用，或尚无客户端连接")
	}
	if r.Profile.Nginx != nil && observed.Nginx.Status != "read" {
		status := map[string]string{"read": "读取成功", "partial": "部分读取", "unavailable": "无法读取", "not_configured": "未登记"}[observed.Nginx.Status]
		if status == "" {
			status = "未知"
		}
		p.Warnings = append(p.Warnings, "Nginx 磁盘配置读取结果："+status)
		p.Warnings = append(p.Warnings, observed.Nginx.Issues...)
	}
	for _, log := range observed.Logs {
		if !log.Available {
			format := map[string]string{"frps": "FRPS 服务", "nginx-access": "Nginx 访问", "nginx-error": "Nginx 错误"}[log.Format]
			p.Warnings = append(p.Warnings, "已登记的日志来源不可读取："+format)
		}
	}
	p.ID = adoptionID(p)
	return p, nil
}
func ApplyAdoption(ctx context.Context, p AdoptionPlan, confirmation, console string) (Journal, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return Journal{}, errors.New("接入安装需要 Linux root 权限")
	}
	if p.ID == "" || confirmation != p.ID || p.ID != adoptionID(p) || p.Arch != runtime.GOARCH {
		return Journal{}, errors.New("接入计划完整性或确认标识不匹配")
	}
	if e := checkAdoption(p.Request); e != nil {
		return Journal{}, e
	}
	if managed.TrustedDirectory("/run") != nil {
		return Journal{}, errors.New("安装锁目录不可用")
	}
	release, e := filelock.Acquire(filepath.Join("/run", p.Request.Name+"-install.lock"))
	if e != nil {
		return Journal{}, e
	}
	defer release()
	journalPath := filepath.Join("/var/lib/frp-console-installer", p.Request.Name, "adoption.json")
	if _, e = os.Lstat(journalPath); e == nil {
		if managed.TrustedFile(journalPath) != nil {
			return Journal{}, errors.New("接入记录未通过文件可信检查")
		}
		var j Journal
		if ReadJSON(journalPath, &j) != nil || j.ID != p.ID {
			return Journal{}, errors.New("接入记录属于另一份计划")
		}
		if j.State == "installed" {
			return j, nil
		}
		return j, errors.New("上次接入未完成；重试前请检查保留的文件")
	} else if !os.IsNotExist(e) {
		return Journal{}, errors.New("接入记录不可读取")
	}
	if time.Now().Unix()-p.Created > 900 || p.Created > time.Now().Unix() {
		return Journal{}, errors.New("接入计划已过期")
	}
	hash, e := managed.Digest(console)
	if e != nil || hash != p.ConsoleHash {
		return Journal{}, errors.New("管理程序在预览后发生变化")
	}
	if e = adoptionPreflight(ctx, p.Request); e != nil {
		return Journal{}, e
	}
	m := config.Manager{Instances: map[string]config.Instance{"frps": {ID: "frps", Role: "frps", Path: p.Request.Profile.Config}}}
	d, e := m.Read("frps")
	if e != nil || config.Revision(d.Raw) != p.ConfigRevision {
		return Journal{}, errors.New("已有 FRPS 配置在预览后发生变化")
	}
	parent := filepath.Dir(journalPath)
	if e = deploymentParents(parent); e != nil {
		return Journal{}, e
	}
	if managed.TrustedDirectory(parent) != nil {
		return Journal{}, errors.New("操作记录目录未通过可信检查")
	}
	j := Journal{ID: p.ID, State: "installing", At: time.Now().Unix(), Note: "仅安装新增管理资源；原 FRPS/Nginx 保持不变"}
	if e = WriteJSON(journalPath, j); e != nil {
		return Journal{}, e
	}
	save := func() error {
		b, _ := json.MarshalIndent(j, "", "  ")
		temp := journalPath + ".tmp"
		if e := os.WriteFile(temp, b, 0600); e != nil {
			return e
		}
		return os.Rename(temp, journalPath)
	}
	created := []string{}
	success := false
	defer func() {
		if success {
			return
		}
		// Stop the timer before its oneshot service so it cannot restart collection.
		for i := len(created) - 1; i >= 0; i-- {
			unit := created[i]
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = fixedCommand(cleanup, "systemctl", "disable", "--now", unit)
			cancel()
			_ = os.Remove(filepath.Join("/etc/systemd/system", unit))
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = fixedCommand(cleanup, "systemctl", "daemon-reload")
		j.State = "failed_retained"
		j.Note = "仅移除本次新增管理单元；新文件和用户保留供检查"
		_ = save()
	}()
	r := p.Request
	if e = fixedCommand(ctx, "useradd", "--system", "--user-group", "--home-dir", filepath.Join(r.Root, "data"), "--shell", "/usr/sbin/nologin", r.Name); e != nil {
		return j, e
	}
	b, e := os.ReadFile(console)
	if e != nil {
		return j, e
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != p.ConsoleHash {
		return j, errors.New("管理程序在复制前发生变化")
	}
	if e = put(filepath.Join(r.Root, "bin", "frp-console"), b, 0755); e != nil {
		return j, e
	}
	if e = deploymentParents(filepath.Join(r.Root, "data")); e != nil {
		return j, e
	}
	if e = fixedCommand(ctx, "chown", r.Name+":"+r.Name, filepath.Join(r.Root, "data")); e != nil {
		return j, e
	}
	if e = os.Chmod(filepath.Join(r.Root, "data"), 0700); e != nil {
		return j, e
	}
	for _, file := range adoptionFiles(r) {
		mode := os.FileMode(0644)
		if filepath.Ext(file.Path) == ".json" {
			mode = 0600
		}
		if e = put(file.Path, []byte(file.Content), mode); e != nil {
			return j, e
		}
		if strings.HasPrefix(file.Path, "/etc/systemd/system/") {
			created = append(created, filepath.Base(file.Path))
		}
	}
	// Generate initial evidence before starting the low-privilege UI.
	if e = observe.CLI([]string{"--profile", filepath.Join(r.Root, "observe", "profile.json"), "--out", filepath.Join(r.Root, "observe", "snapshot.json")}); e != nil {
		return j, e
	}
	initial, e := observe.LoadSnapshot(filepath.Join(r.Root, "observe", "snapshot.json"))
	if e != nil || initial.Config.Revision != p.ConfigRevision {
		return j, errors.New("原配置在接入期间发生变化")
	}
	if e = fixedCommand(ctx, "systemctl", "daemon-reload"); e != nil {
		return j, e
	}
	// Verify that the collector can operate inside its systemd sandbox.
	if e = fixedCommand(ctx, "systemctl", "start", r.Name+"-collect.service"); e != nil {
		return j, e
	}
	for _, unit := range []string{r.Name + "-collect.timer", r.Name + ".service"} {
		if e = fixedCommand(ctx, "systemctl", "enable", "--now", unit); e != nil {
			return j, e
		}
		if e = fixedCommand(ctx, "systemctl", "is-active", "--quiet", unit); e != nil {
			return j, e
		}
	}
	if e = checkAdoptionUI(ctx, r.Listen); e != nil {
		return j, e
	}
	j.State = "installed"
	j.Note = "只读页面与采集服务已启动；原 FRPS/Nginx 未修改，认证和业务需要独立证据"
	if e = save(); e != nil {
		return j, e
	}
	success = true
	return j, nil
}

func checkAdoptionUI(ctx context.Context, listen string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		req, e := http.NewRequestWithContext(ctx, "GET", "http://"+listen+"/api/session", nil)
		if e != nil {
			return errors.New("管理页面验证地址无效")
		}
		req.Header.Set("X-FRP-Console", "1")
		response, e := client.Do(req)
		if e == nil {
			var session struct {
				Initialized   *bool `json:"initialized"`
				Authenticated *bool `json:"authenticated"`
			}
			err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&session)
			response.Body.Close()
			if response.StatusCode == 200 && err == nil && session.Initialized != nil && session.Authenticated != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("新增管理页面未通过本机会话接口检查；已回滚新增管理单元")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
