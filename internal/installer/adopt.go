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
		return errors.New("use a safe absolute dedicated root path and installation name")
	}
	host, port, e := net.SplitHostPort(r.Listen)
	n, pe := strconv.Atoi(port)
	if e != nil || pe != nil || n < 1024 || n > 65535 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("UI requires a literal loopback address and port 1024..65535")
	}
	if r.Interval < 10 || r.Interval > 60 {
		return errors.New("collection interval must be 10..60 seconds")
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
		return errors.New("adoption installation requires Linux amd64/arm64 root")
	}
	raw, e := os.ReadFile("/etc/os-release")
	if e != nil || (!strings.Contains(string(raw), "ID=debian") && !strings.Contains(string(raw), "ID=ubuntu")) {
		return errors.New("adoption supports Debian and Ubuntu")
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		return errors.New("running systemd required")
	}
	for _, p := range append([]string{r.Root}, adoptionPaths(r)...) {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			return errors.New("adoption destination or unit already exists; original files are never overwritten")
		}
	}
	if _, e = os.Lstat(filepath.Join("/var/lib/frp-console-installer", r.Name)); !os.IsNotExist(e) {
		return errors.New("installation history already exists for this name; use the original plan or a separate name")
	}
	parent := filepath.Dir(r.Root)
	for {
		if _, e = os.Lstat(parent); e == nil {
			break
		}
		if !os.IsNotExist(e) || parent == filepath.Dir(parent) {
			return errors.New("adoption parent unavailable")
		}
		parent = filepath.Dir(parent)
	}
	if managed.TrustedDirectory(parent) != nil || managed.TrustedDirectory("/etc/systemd/system") != nil {
		return errors.New("adoption parents must be protected root-owned directories")
	}
	for _, p := range adoptionPaths(r) {
		b, e := exec.CommandContext(ctx, "systemctl", "show", filepath.Base(p), "--property=LoadState", "--value").Output()
		if e != nil || strings.TrimSpace(string(b)) != "not-found" {
			return errors.New("a planned systemd unit is already registered")
		}
	}
	if exec.CommandContext(ctx, "id", "-u", r.Name).Run() == nil {
		return errors.New("adoption user already exists")
	}
	l, e := net.Listen("tcp", r.Listen)
	if e != nil {
		return errors.New("UI port is unavailable")
	}
	l.Close()
	if managed.TrustedFile(r.Profile.Config) != nil {
		return errors.New("existing FRPS configuration must be protected and root-owned; original permissions are not changed")
	}
	return nil
}
func adoptionFiles(r AdoptionRequest) []templates.File {
	profile := filepath.Join(r.Root, "observe", "profile.json")
	snapshot := filepath.Join(r.Root, "observe", "snapshot.json")
	web := fmt.Sprintf("[Unit]\nDescription=FRP Console existing deployment UI\nAfter=network-online.target\n\n[Service]\nUser=%s\nGroup=%s\nExecStart=%s/bin/frp-console --data %s/data --observed-snapshot %s --listen %s\nRestart=on-failure\nRestartSec=2\nNoNewPrivileges=true\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n", r.Name, r.Name, r.Root, r.Root, snapshot, r.Listen)
	collector := fmt.Sprintf("[Unit]\nDescription=FRP Console read-only collection\n\n[Service]\nType=oneshot\nUser=root\nExecStart=%s/bin/frp-console observe --profile %s --out %s\nTimeoutStartSec=55\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nReadWritePaths=%s/observe\nUMask=0077\n", r.Root, profile, snapshot, r.Root)
	timer := fmt.Sprintf("[Unit]\nDescription=Refresh FRP Console observation\n\n[Timer]\nOnBootSec=10\nOnUnitInactiveSec=%ds\nUnit=%s-collect.service\n\n[Install]\nWantedBy=timers.target\n", r.Interval, r.Name)
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
		return AdoptionPlan{}, errors.New("existing FRPS TOML cannot be parsed")
	}
	hash, e := managed.Digest(console)
	if e != nil {
		return AdoptionPlan{}, e
	}
	p := AdoptionPlan{Created: time.Now().Unix(), Request: r, Arch: runtime.GOARCH, ConsoleHash: hash, ConfigRevision: config.Revision(d.Raw), Files: adoptionFiles(r)}
	observed, e := observe.Collect(ctx, r.Profile, nil)
	if e != nil || observed.Config.Revision != p.ConfigRevision {
		return AdoptionPlan{}, errors.New("existing configuration changed or collection failed during preflight")
	}
	p.Warnings = []string{}
	if observed.Runtime.Layers["process"].Status != "passed" {
		p.Warnings = append(p.Warnings, "FRPS process not verified; installing UI does not start the existing FRPS")
	}
	if observed.Runtime.Layers["authentication"].Status != "passed" {
		p.Warnings = append(p.Warnings, "FRPS authentication not verified; management API may be unavailable or no client connected")
	}
	if r.Profile.Nginx != nil && observed.Nginx.Status != "read" {
		p.Warnings = append(p.Warnings, "Nginx disk observation: "+observed.Nginx.Status)
		p.Warnings = append(p.Warnings, observed.Nginx.Issues...)
	}
	for _, log := range observed.Logs {
		if !log.Available {
			p.Warnings = append(p.Warnings, "Registered log source unavailable: "+log.Format)
		}
	}
	p.ID = adoptionID(p)
	return p, nil
}
func ApplyAdoption(ctx context.Context, p AdoptionPlan, confirmation, console string) (Journal, error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return Journal{}, errors.New("adoption requires Linux root")
	}
	if p.ID == "" || confirmation != p.ID || p.ID != adoptionID(p) || p.Arch != runtime.GOARCH {
		return Journal{}, errors.New("adoption plan integrity or confirmation mismatch")
	}
	if e := checkAdoption(p.Request); e != nil {
		return Journal{}, e
	}
	if managed.TrustedDirectory("/run") != nil {
		return Journal{}, errors.New("installation lock directory unavailable")
	}
	release, e := filelock.Acquire(filepath.Join("/run", p.Request.Name+"-install.lock"))
	if e != nil {
		return Journal{}, e
	}
	defer release()
	journalPath := filepath.Join("/var/lib/frp-console-installer", p.Request.Name, "adoption.json")
	if _, e = os.Lstat(journalPath); e == nil {
		if managed.TrustedFile(journalPath) != nil {
			return Journal{}, errors.New("adoption journal is not trusted")
		}
		var j Journal
		if ReadJSON(journalPath, &j) != nil || j.ID != p.ID {
			return Journal{}, errors.New("adoption journal belongs to a different plan")
		}
		if j.State == "installed" {
			return j, nil
		}
		return j, errors.New("prior adoption incomplete; inspect retained files before retrying")
	} else if !os.IsNotExist(e) {
		return Journal{}, errors.New("adoption journal unavailable")
	}
	if time.Now().Unix()-p.Created > 900 || p.Created > time.Now().Unix() {
		return Journal{}, errors.New("adoption plan expired")
	}
	hash, e := managed.Digest(console)
	if e != nil || hash != p.ConsoleHash {
		return Journal{}, errors.New("Console binary changed since preview")
	}
	if e = adoptionPreflight(ctx, p.Request); e != nil {
		return Journal{}, e
	}
	m := config.Manager{Instances: map[string]config.Instance{"frps": {ID: "frps", Role: "frps", Path: p.Request.Profile.Config}}}
	d, e := m.Read("frps")
	if e != nil || config.Revision(d.Raw) != p.ConfigRevision {
		return Journal{}, errors.New("existing FRPS configuration changed since preview")
	}
	parent := filepath.Dir(journalPath)
	if e = deploymentParents(parent); e != nil {
		return Journal{}, e
	}
	if managed.TrustedDirectory(parent) != nil {
		return Journal{}, errors.New("journal directory is not trusted")
	}
	j := Journal{ID: p.ID, State: "installing", At: time.Now().Unix(), Note: "Only new management resources are installed; existing FRPS/Nginx remain unchanged"}
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
		j.Note = "Only newly created management units removed; files and account retained for inspection"
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
		return j, errors.New("Console binary changed before copy")
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
		return j, errors.New("existing configuration changed during adoption")
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
	j.Note = "Read-only UI and collector active; original FRPS/Nginx untouched, authentication and business require independent evidence"
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
			return errors.New("UI verification address invalid")
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
			return errors.New("new UI did not pass its local session endpoint check; new management units rolled back")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
