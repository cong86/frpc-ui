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
		return errors.New("invalid runtime target")
	}
	if t.Kind == "systemd" && filepath.Ext(t.Name) != ".service" {
		return errors.New("systemd observation requires a service unit")
	}
	return nil
}
func (p Profile) Validate() error {
	if p.Version != 1 || !filepath.IsAbs(p.Config) || len(p.Logs) > 12 {
		return errors.New("invalid observation profile")
	}
	if e := checkTarget(p.Runtime); e != nil {
		return e
	}
	if n := p.Nginx; n != nil {
		if n.Context != "" && n.Context != "http-fragments" {
			return errors.New("unsupported nginx observation context")
		}
		if !filepath.IsAbs(n.Entry) || !filepath.IsAbs(n.Prefix) || len(n.Roots) == 0 || len(n.Roots) > 16 || len(n.Mounts) > 16 {
			return errors.New("invalid nginx read scope")
		}
		for _, r := range n.Roots {
			if !filepath.IsAbs(r) {
				return errors.New("nginx roots must be absolute")
			}
		}
		for _, m := range n.Mounts {
			if !filepath.IsAbs(m.Inside) || !filepath.IsAbs(m.Host) {
				return errors.New("nginx mounts must be absolute")
			}
		}
		if e := checkTarget(n.Runtime); e != nil {
			return e
		}
	}
	for _, l := range p.Logs {
		if l.Format != "frps" && l.Format != "nginx-access" && l.Format != "nginx-error" {
			return errors.New("unsupported log format")
		}
		if l.Kind == "file" {
			if !filepath.IsAbs(l.Name) {
				return errors.New("log path must be absolute")
			}
		} else if e := checkTarget(Target{l.Kind, l.Name}); e != nil || l.Kind == "" {
			return errors.New("invalid log target")
		}
	}
	return nil
}
func readJSON(path string, out any) error {
	if !filepath.IsAbs(path) || managed.TrustedFile(path) != nil {
		return errors.New("observation metadata must be protected root-owned files")
	}
	f, e := os.Open(path)
	if e != nil {
		return errors.New("observation file unavailable")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() > 4<<20 {
		return errors.New("observation file too large")
	}
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errors.New("invalid observation JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid trailing observation data")
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
		return nil, errors.New("read-only command unavailable")
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
	profile := fs.String("profile", "", "protected administrator profile")
	out := fs.String("out", "", "protected redacted snapshot path")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *profile == "" || *out == "" {
		return errors.New("use observe --profile /absolute/profile.json --out /absolute/snapshot.json")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("observation collection requires Linux root")
	}
	p, e := LoadProfile(*profile)
	if e != nil {
		return e
	}
	if !filepath.IsAbs(*out) || filepath.Clean(*out) == filepath.Clean(*profile) || filepath.Clean(*out) == filepath.Clean(p.Config) {
		return errors.New("invalid snapshot output")
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
		return errors.New("snapshot collection already in progress")
	}
	defer release()
	var previous *Snapshot
	if _, e = os.Lstat(*out); e == nil {
		v, e := LoadSnapshot(*out)
		if e != nil {
			return errors.New("existing output is not an observation snapshot")
		}
		previous = &v
	} else if !os.IsNotExist(e) {
		return errors.New("snapshot output unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, e := Collect(ctx, p, command)
	if e != nil {
		return e
	}
	current, e := LoadProfile(*profile)
	if e != nil || profileRevision(current) != profileRevision(p) {
		return errors.New("observation profile changed during collection")
	}
	s.ProfileRevision = profileRevision(p)
	if previous != nil {
		s.Traffic.WithPrevious(s, *previous)
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return errors.New("snapshot encoding failed")
	}
	if len(b) > 4<<20 {
		return errors.New("redacted snapshot exceeds size limit")
	}
	f, e := os.CreateTemp(filepath.Dir(*out), ".frp-observation-*")
	if e != nil {
		return errors.New("snapshot output unavailable")
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
		return errors.New("snapshot write failed")
	}
	if e = os.Rename(name, *out); e != nil {
		return errors.New("snapshot replacement failed")
	}
	return nil
}
func checkDirectory(dir string) error {
	if managed.TrustedDirectory(dir) != nil {
		return errors.New("snapshot directory must be protected and root-owned")
	}
	return nil
}
func profileRevision(p Profile) string { b, _ := json.Marshal(p); return config.Revision(string(b)) }
