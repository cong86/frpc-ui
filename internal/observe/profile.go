// Package observe collects existing deployments without changing their services.
// Only the administrator CLI accesses Docker, privileged files or journals.
package observe

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/cong86/frpc-ui/internal/cli"
	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/filelock"
	"github.com/cong86/frpc-ui/internal/managed"
)

type Target struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}
type Mount struct {
	Inside string `json:"inside"`
	Host   string `json:"host"`
}
type NginxProfile struct {
	Context string   `json:"context,omitempty"`
	Entry   string   `json:"entry"`
	Prefix  string   `json:"prefix"`
	Roots   []string `json:"roots"`
	Mounts  []Mount  `json:"mounts,omitempty"`
	Runtime Target   `json:"runtime"`
}
type LogSource struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Format string `json:"format"`
}
type Profile struct {
	Version int           `json:"version"`
	Config  string        `json:"frpsConfig"`
	Runtime Target        `json:"frpsRuntime"`
	Nginx   *NginxProfile `json:"nginx,omitempty"`
	Logs    []LogSource   `json:"logs,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)

func checkTarget(t Target) error {
	if t.Kind == "" && t.Name == "" {
		return nil
	}
	if (t.Kind != "docker" && t.Kind != "systemd") || !identifier.MatchString(t.Name) {
		return errors.New("运行目标无效")
	}
	if t.Kind == "systemd" && filepath.Ext(t.Name) != ".service" {
		return errors.New("systemd 采集目标必须是服务单元")
	}
	return nil
}
func (p Profile) Validate() error {
	if p.Version != 1 || !filepath.IsAbs(p.Config) || len(p.Logs) > 12 {
		return errors.New("采集配置无效")
	}
	if e := checkTarget(p.Runtime); e != nil {
		return e
	}
	if n := p.Nginx; n != nil {
		if n.Context != "" && n.Context != "http-fragments" {
			return errors.New("不支持此 Nginx 采集类型")
		}
		if !filepath.IsAbs(n.Entry) || !filepath.IsAbs(n.Prefix) || len(n.Roots) == 0 || len(n.Roots) > 16 || len(n.Mounts) > 16 {
			return errors.New("Nginx 读取范围无效")
		}
		for _, r := range n.Roots {
			if !filepath.IsAbs(r) {
				return errors.New("Nginx 根目录必须是绝对路径")
			}
		}
		for _, m := range n.Mounts {
			if !filepath.IsAbs(m.Inside) || !filepath.IsAbs(m.Host) {
				return errors.New("Nginx 挂载路径必须是绝对路径")
			}
		}
		if e := checkTarget(n.Runtime); e != nil {
			return e
		}
	}
	for _, l := range p.Logs {
		if l.Format != "frps" && l.Format != "nginx-access" && l.Format != "nginx-error" {
			return errors.New("不支持此日志格式")
		}
		if l.Kind == "file" {
			if !filepath.IsAbs(l.Name) {
				return errors.New("日志路径必须是绝对路径")
			}
		} else if e := checkTarget(Target{l.Kind, l.Name}); e != nil || l.Kind == "" {
			return errors.New("日志目标无效")
		}
	}
	return nil
}
func readJSON(path string, out any) error {
	if !filepath.IsAbs(path) || managed.TrustedFile(path) != nil {
		return errors.New("采集元数据必须是受保护且归 root 所有的文件")
	}
	f, e := os.Open(path)
	if e != nil {
		return errors.New("采集文件不可读取")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() > 4<<20 {
		return errors.New("采集文件超出大小限制")
	}
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errors.New("采集 JSON 无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("采集文件包含无效的尾随数据")
	}
	return nil
}
func LoadProfile(path string) (Profile, error) {
	var p Profile
	if e := readJSON(path, &p); e != nil {
		return p, e
	}
	return p, p.Validate()
}

type Runner func(context.Context, string, ...string) ([]byte, error)

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	// Fixed executable paths and argv; no shell or browser-selected command.
	cmd := exec.CommandContext(ctx, "/usr/bin/"+name, args...)
	var b limitedBuffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	if cmd.Run() != nil || b.Overflow {
		return nil, errors.New("只读命令不可用")
	}
	return b.Data, nil
}

type limitedBuffer struct {
	Data     []byte
	Overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (4 << 20) - len(b.Data)
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		b.Overflow = true
		p = p[:remaining]
	}
	b.Data = append(b.Data, p...)
	return n, nil
}

func CLI(args []string) error {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	cli.Configure(fs)
	profile := fs.String("profile", "", "受保护的管理员采集配置")
	out := fs.String("out", "", "受保护的脱敏快照路径")
	if e := fs.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return e
		}
		return errors.New(cli.Text(e.Error()))
	}
	if fs.NArg() != 0 || *profile == "" || *out == "" {
		return errors.New("用法：observe --profile /absolute/profile.json --out /absolute/snapshot.json")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("采集需要 Linux root 权限")
	}
	p, e := LoadProfile(*profile)
	if e != nil {
		return e
	}
	if !filepath.IsAbs(*out) || filepath.Clean(*out) == filepath.Clean(*profile) || filepath.Clean(*out) == filepath.Clean(p.Config) {
		return errors.New("快照输出路径无效")
	}
	// Verify the output directory before collection and never replace an unrelated file.
	if e = managed.TrustedFile(*profile); e != nil {
		return e
	}
	if e = checkDirectory(filepath.Dir(*out)); e != nil {
		return e
	}
	release, e := filelock.Acquire(*out + ".collect-lock")
	if e != nil {
		return errors.New("快照采集正在进行中")
	}
	defer release()
	var previous *Snapshot
	if _, e = os.Lstat(*out); e == nil {
		v, e := LoadSnapshot(*out)
		if e != nil {
			return errors.New("已有输出文件不是采集快照")
		}
		previous = &v
	} else if !os.IsNotExist(e) {
		return errors.New("快照输出不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, e := Collect(ctx, p, command)
	if e != nil {
		return e
	}
	current, e := LoadProfile(*profile)
	if e != nil || profileRevision(current) != profileRevision(p) {
		return errors.New("采集配置在采集期间发生变化")
	}
	s.ProfileRevision = profileRevision(p)
	if previous != nil {
		s.Traffic.WithPrevious(s, *previous)
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return errors.New("快照编码失败")
	}
	if len(b) > 4<<20 {
		return errors.New("脱敏快照超出大小限制")
	}
	f, e := os.CreateTemp(filepath.Dir(*out), ".frp-observation-*")
	if e != nil {
		return errors.New("快照输出不可用")
	}
	name := f.Name()
	defer os.Remove(name)
	// Snapshot is redacted; 0644 permits the unprivileged Console to read it.
	if e = f.Chmod(0644); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return errors.New("快照写入失败")
	}
	if e = os.Rename(name, *out); e != nil {
		return errors.New("快照替换失败")
	}
	return nil
}
func checkDirectory(dir string) error {
	if managed.TrustedDirectory(dir) != nil {
		return errors.New("快照目录必须受保护且归 root 所有")
	}
	return nil
}
func profileRevision(p Profile) string { b, _ := json.Marshal(p); return config.Revision(string(b)) }
