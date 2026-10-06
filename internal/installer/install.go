// Package installer implements privileged, previewed, fresh systemd installations.
package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/filelock"
	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/cong86/frpc-ui/internal/templates"
)

const Version = "0.71.0"

var Checksums = map[string]string{"amd64": "84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716", "arm64": "f33c293c275d8fc68c654b6fba8f10b2551d6463d09a9fc9cffb7227eae82266"}
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,27}$`)

type Request struct {
	Name    string            `json:"name"`
	Root    string            `json:"root"`
	Listen  string            `json:"listen"`
	Configs map[string]string `json:"configs"`
	Archive string            `json:"archive,omitempty"`
	Probes  []managed.Probe   `json:"probes,omitempty"`
}
type Plan struct {
	ID          string           `json:"id"`
	Created     int64            `json:"created"`
	Request     Request          `json:"request"`
	Arch        string           `json:"arch"`
	ConsoleHash string           `json:"consoleHash"`
	Files       []templates.File `json:"files"`
}
type Journal struct {
	ID    string `json:"id"`
	State string `json:"state"`
	At    int64  `json:"at"`
	Note  string `json:"note"`
}

func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	return f.Sync()
}
func ReadJSON(path string, v any) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > config.MaxSize {
		return errors.New("private request/plan must be a regular file, mode 0600, below 1 MiB")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return errors.New("invalid private request or plan JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON in private request or plan")
	}
	return nil
}
func checkRequest(r Request) error {
	if !nameRE.MatchString(r.Name) || !strings.HasPrefix(r.Name, "frp-console") || !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root || r.Root == "/" || strings.ContainsAny(r.Root, " \t\r\n\"'\\%$") {
		return errors.New("use a safe absolute dedicated root path and installation name")
	}
	host, _, e := net.SplitHostPort(r.Listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("Console listen must be a literal loopback address")
	}
	if len(r.Configs) == 0 || len(r.Configs) > 2 {
		return errors.New("provide frpc, frps, or both configurations")
	}
	for role, raw := range r.Configs {
		d, e := config.Parse(raw, role)
		if e != nil {
			return e
		}
		if why := d.Editability(); why != "" {
			return errors.New(why)
		}
		auth, _ := d.Values["auth"].(map[string]any)
		token, _ := auth["token"].(string)
		if auth["method"] != "token" || len(token) < 12 || strings.Contains(token, "__SET_") || strings.Contains(token, "DEMO_ONLY") {
			return errors.New("new installation requires an explicitly supplied token of at least 12 bytes")
		}
	}
	if len(r.Probes) > 16 {
		return errors.New("at most 16 explicit probes")
	}
	for _, p := range r.Probes {
		if _, ok := r.Configs[p.Instance]; !ok {
			return errors.New("probe must belong to an installed instance")
		}
		if e = managed.ValidateProbe(p); e != nil {
			return e
		}
	}
	return nil
}
func preflight(r Request) error {
	if runtime.GOOS != "linux" || Checksums[runtime.GOARCH] == "" {
		return errors.New("systemd installation supports Linux amd64/arm64")
	}
	raw, e := os.ReadFile("/etc/os-release")
	if e != nil || (!strings.Contains(string(raw), "ID=debian") && !strings.Contains(string(raw), "ID=ubuntu")) {
		return errors.New("this installer supports Debian and Ubuntu")
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		return errors.New("running systemd required; LXC namespace setup is a host-side prerequisite")
	}
	if _, e = exec.LookPath("systemctl"); e != nil {
		return errors.New("systemctl unavailable")
	}
	if e = checkRequest(r); e != nil {
		return e
	}
	// Existing files/services/users are never silently taken over.
	for _, p := range append([]string{r.Root}, unitPaths(r)...) {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			return errors.New("installation target already exists; only fresh installation is supported")
		}
	}
	for _, p := range unitPaths(r) {
		b, e := exec.Command("systemctl", "show", filepath.Base(p), "--property=LoadState", "--value").Output()
		if e != nil || strings.TrimSpace(string(b)) != "not-found" {
			return errors.New("a planned systemd unit is already registered")
		}
	}
	if exec.Command("id", "-u", r.Name).Run() == nil {
		return errors.New("installation user already exists")
	}
	for _, p := range listenAddresses(r) {
		network := "tcp"
		if strings.HasPrefix(p, "udp:") {
			network = "udp"
			p = strings.TrimPrefix(p, "udp:")
		}
		if network == "tcp" {
			l, e := net.Listen("tcp", p)
			if e != nil {
				return errors.New("a planned listen port is unavailable")
			}
			l.Close()
		} else {
			l, e := net.ListenPacket("udp", p)
			if e != nil {
				return errors.New("a planned UDP port is unavailable")
			}
			l.Close()
		}
	}
	return nil
}
func integer(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func listenAddresses(r Request) []string {
	out := []string{r.Listen}
	for role, raw := range r.Configs {
		d, _ := config.Parse(raw, role)
		v := d.Values
		if w, ok := v["webServer"].(map[string]any); ok {
			a, _ := w["addr"].(string)
			if a == "" {
				a = "127.0.0.1"
			}
			if p := integer(w["port"]); p > 0 {
				out = append(out, net.JoinHostPort(a, fmt.Sprint(p)))
			}
		}
		if role == "frps" {
			a, _ := v["bindAddr"].(string)
			if a == "" {
				a = "0.0.0.0"
			}
			for _, k := range []string{"bindPort", "vhostHTTPPort", "vhostHTTPSPort", "tcpmuxHTTPConnectPort"} {
				if p := integer(v[k]); p > 0 {
					out = append(out, net.JoinHostPort(a, fmt.Sprint(p)))
				}
			}
			for _, k := range []string{"kcpBindPort", "quicBindPort"} {
				if p := integer(v[k]); p > 0 {
					out = append(out, "udp:"+net.JoinHostPort(a, fmt.Sprint(p)))
				}
			}
		}
	}
	return out
}
func unitPaths(r Request) []string {
	out := []string{filepath.Join("/etc/systemd/system", r.Name+".service")}
	for _, role := range roles(r) {
		out = append(out, filepath.Join("/etc/systemd/system", r.Name+"-"+role+".service"))
	}
	return out
}
func roles(r Request) []string {
	a := []string{}
	// A combined install starts the server before its local client.
	for _, role := range []string{"frps", "frpc"} {
		if _, ok := r.Configs[role]; ok {
			a = append(a, role)
		}
	}
	return a
}
func frpUnit(r Request, role string) string {
	return fmt.Sprintf("[Unit]\nDescription=Independent official %s\nAfter=network-online.target\n\n[Service]\nUser=%s\nGroup=%s\nExecStart=%s/frp/%s -c %s/data/instances/%s/%s.toml\nRestart=on-failure\nRestartSec=2\nNoNewPrivileges=true\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n", role, r.Name, r.Name, r.Root, role, r.Root, role, role)
}
func consoleUnit(r Request) string {
	return fmt.Sprintf("[Unit]\nDescription=FRP Console\nAfter=network-online.target\n\n[Service]\nUser=%s\nGroup=%s\nExecStart=%s/bin/frp-console --data %s/data --manifest %s/manifest.json --listen %s\nRestart=on-failure\nRestartSec=2\nNoNewPrivileges=true\nUMask=0077\n\n[Install]\nWantedBy=multi-user.target\n", r.Name, r.Name, r.Root, r.Root, r.Root, r.Listen)
}
func NewPlan(ctx context.Context, r Request, console string) (Plan, error) {
	if e := preflight(r); e != nil {
		return Plan{}, e
	}
	staging, e := os.MkdirTemp("", "frp-install-check-*")
	if e != nil {
		return Plan{}, e
	}
	defer os.RemoveAll(staging)
	if e = prepare(ctx, r.Archive, runtime.GOARCH, staging); e != nil {
		return Plan{}, e
	}
	for _, role := range roles(r) {
		if e = config.Verify(filepath.Join(staging, role), role, r.Configs[role], staging); e != nil {
			return Plan{}, e
		}
	}
	hash, e := managed.Digest(console)
	if e != nil {
		return Plan{}, e
	}
	p := Plan{Created: time.Now().Unix(), Request: r, Arch: runtime.GOARCH, ConsoleHash: hash}
	for _, role := range roles(r) {
		d, _ := config.Parse(r.Configs[role], role)
		p.Files = append(p.Files, templates.File{Path: filepath.Join(r.Root, "data", "instances", role, role+".toml"), Content: d.Snapshot().Text}, templates.File{Path: filepath.Join("/etc/systemd/system", r.Name+"-"+role+".service"), Content: frpUnit(r, role)})
	}
	p.Files = append(p.Files, templates.File{Path: filepath.Join("/etc/systemd/system", r.Name+".service"), Content: consoleUnit(r)})
	p.ID = planID(p)
	return p, nil
}
func planID(p Plan) string {
	p.ID = ""
	b, _ := json.Marshal(p)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func prepare(ctx context.Context, archive, arch, dir string) error {
	expected, ok := Checksums[arch]
	if !ok {
		return errors.New("unsupported architecture")
	}
	if archive == "" {
		archive = filepath.Join(dir, "download.tar.gz")
		req, e := http.NewRequestWithContext(ctx, "GET", "https://github.com/fatedier/frp/releases/download/v"+Version+"/frp_"+Version+"_linux_"+arch+".tar.gz", nil)
		if e != nil {
			return e
		}
		client := &http.Client{Timeout: 90 * time.Second}
		resp, e := client.Do(req)
		if e != nil {
			return errors.New("official FRP download failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("official FRP download rejected")
		}
		f, e := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(f, io.LimitReader(resp.Body, 100<<20))
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	compressed, e := io.ReadAll(io.LimitReader(f, (100<<20)+1))
	f.Close()
	if e != nil || len(compressed) > 100<<20 {
		return errors.New("archive unavailable or too large")
	}
	sum := sha256.Sum256(compressed)
	if hex.EncodeToString(sum[:]) != expected {
		return errors.New("official FRP archive SHA-256 does not match pinned release")
	}
	gz, e := gzip.NewReader(bytes.NewReader(compressed))
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	prefix := "frp_" + Version + "_linux_" + arch + "/"
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		role := strings.TrimPrefix(h.Name, prefix)
		if h.Name != prefix+"frpc" && h.Name != prefix+"frps" {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size > 80<<20 || seen[role] {
			return errors.New("invalid official binary archive entry")
		}
		seen[role] = true
		dest, e := os.OpenFile(filepath.Join(dir, role), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if e != nil {
			return e
		}
		_, e = io.CopyN(dest, tr, h.Size)
		ce := dest.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	if !seen["frpc"] || !seen["frps"] {
		return errors.New("official archive lacks required binaries")
	}
	return nil
}
func fixedCommand(ctx context.Context, name string, args ...string) error {
	if exec.CommandContext(ctx, name, args...).Run() != nil {
		return fmt.Errorf("%s step failed; raw diagnostic suppressed", name)
	}
	return nil
}
func put(path string, b []byte, mode os.FileMode) error {
	if e := deploymentParents(filepath.Dir(path)); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	// The bootstrap runs with umask 077; executable and public manifest permissions
	// must be explicit so the independent unprivileged services can read them.
	if e := f.Chmod(mode); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	ok = true
	return nil
}

func deploymentParents(path string) error {
	missing := []string{}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return errors.New("deployment parent is not a directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, current)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0755); err != nil {
			return err
		}
		if err := os.Chmod(missing[i], 0755); err != nil {
			return err
		}
	}
	return nil
}
func Apply(ctx context.Context, p Plan, confirmation, console string) (Journal, error) {
	if os.Geteuid() != 0 || runtime.GOOS != "linux" {
		return Journal{}, errors.New("apply requires Linux root")
	}
	if p.ID == "" || confirmation != p.ID || p.ID != planID(p) || p.Arch != runtime.GOARCH {
		return Journal{}, errors.New("plan confirmation or integrity mismatch")
	}
	if e := checkRequest(p.Request); e != nil {
		return Journal{}, e
	}
	release, e := filelock.Acquire(filepath.Join("/run", p.Request.Name+"-install.lock"))
	if e != nil {
		return Journal{}, e
	}
	defer release()
	journalPath := filepath.Join("/var/lib/frp-console-installer", p.Request.Name, "operation.json")
	if b, e := os.ReadFile(journalPath); e == nil {
		var prior Journal
		if json.Unmarshal(b, &prior) != nil || prior.ID != p.ID {
			return Journal{}, errors.New("installation journal exists; inspect before another install")
		}
		if prior.State == "installed" {
			return prior, nil
		}
		return prior, errors.New("prior installation incomplete; inspect journal and retained files, no automatic retry")
	}
	if time.Now().Unix()-p.Created > 900 || p.Created > time.Now().Unix() {
		return Journal{}, errors.New("installation plan expired")
	}
	hash, e := managed.Digest(console)
	if e != nil || hash != p.ConsoleHash {
		return Journal{}, errors.New("Console binary changed since preview")
	}
	if e = preflight(p.Request); e != nil {
		return Journal{}, e
	}
	staging, e := os.MkdirTemp("", "frp-install-*")
	if e != nil {
		return Journal{}, e
	}
	defer os.RemoveAll(staging)
	if e = prepare(ctx, p.Request.Archive, p.Arch, staging); e != nil {
		return Journal{}, e
	}
	for _, role := range roles(p.Request) {
		if e = config.Verify(filepath.Join(staging, role), role, p.Request.Configs[role], staging); e != nil {
			return Journal{}, e
		}
	}
	if e = os.MkdirAll(filepath.Dir(journalPath), 0700); e != nil {
		return Journal{}, e
	}
	journal := Journal{ID: p.ID, State: "installing", At: time.Now().Unix(), Note: "Fresh installation; data retained on failure"}
	if e = WriteJSON(journalPath, journal); e != nil {
		return Journal{}, e
	}
	save := func() error {
		b, _ := json.MarshalIndent(journal, "", "  ")
		temp := journalPath + ".tmp"
		if e := os.WriteFile(temp, b, 0600); e != nil {
			return e
		}
		return os.Rename(temp, journalPath)
	}
	r := p.Request
	createdUnits := []string{}
	success := false
	defer func() {
		if !success {
			for _, u := range createdUnits {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = fixedCommand(cleanupCtx, "systemctl", "disable", "--now", u)
				cancel()
				_ = os.Remove(filepath.Join("/etc/systemd/system", u))
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = fixedCommand(cleanupCtx, "systemctl", "daemon-reload")
			cancel()
			journal.State = "failed_retained"
			journal.Note = "New service units removed; configuration and data retained for inspection"
			_ = save()
		}
	}()
	if e = fixedCommand(ctx, "useradd", "--system", "--user-group", "--home-dir", filepath.Join(r.Root, "data"), "--shell", "/usr/sbin/nologin", r.Name); e != nil {
		return journal, e
	}
	executable, e := os.ReadFile(console)
	if e != nil {
		return journal, e
	}
	sum := sha256.Sum256(executable)
	if hex.EncodeToString(sum[:]) != p.ConsoleHash {
		return journal, errors.New("Console changed before binary copy")
	}
	if e = put(filepath.Join(r.Root, "bin", "frp-console"), executable, 0755); e != nil {
		return journal, e
	}
	manifest := managed.Manifest{Version: 1, Name: r.Name, Root: r.Root, Listen: r.Listen, Probes: append([]managed.Probe(nil), r.Probes...)}
	for _, role := range roles(r) {
		binary := filepath.Join(r.Root, "frp", role)
		b, e := os.ReadFile(filepath.Join(staging, role))
		if e != nil {
			return journal, e
		}
		if e = put(binary, b, 0755); e != nil {
			return journal, e
		}
		hash, _ := managed.Digest(binary)
		cfg := filepath.Join(r.Root, "data", "instances", role, role+".toml")
		if e = put(cfg, []byte(r.Configs[role]), 0600); e != nil {
			return journal, e
		}
		manifest.Instances = append(manifest.Instances, managed.Instance{ID: role, Role: role, Config: cfg, Binary: binary, BinaryHash: hash, Unit: r.Name + "-" + role + ".service"})
	}
	for index, p := range manifest.Probes {
		if p.CAFile != "" {
			cert, e := os.ReadFile(p.CAFile)
			if e != nil {
				return journal, errors.New("probe CA unavailable")
			}
			target := filepath.Join(r.Root, "pki", fmt.Sprintf("probe-%d.crt", index))
			if e = put(target, cert, 0644); e != nil {
				return journal, e
			}
			manifest.Probes[index].CAFile = target
		}
	}
	if e = fixedCommand(ctx, "chown", "-R", r.Name+":"+r.Name, filepath.Join(r.Root, "data")); e != nil {
		return journal, e
	}
	if e = os.Chmod(filepath.Join(r.Root, "data"), 0700); e != nil {
		return journal, e
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if e = put(filepath.Join(r.Root, "manifest.json"), b, 0644); e != nil {
		return journal, e
	}
	for _, role := range roles(r) {
		unit := r.Name + "-" + role + ".service"
		if e = put(filepath.Join("/etc/systemd/system", unit), []byte(frpUnit(r, role)), 0644); e != nil {
			return journal, e
		}
		createdUnits = append(createdUnits, unit)
	}
	consoleName := r.Name + ".service"
	if e = put(filepath.Join("/etc/systemd/system", consoleName), []byte(consoleUnit(r)), 0644); e != nil {
		return journal, e
	}
	createdUnits = append(createdUnits, consoleName)
	if e = fixedCommand(ctx, "systemctl", "daemon-reload"); e != nil {
		return journal, e
	}
	for _, u := range createdUnits {
		if e = fixedCommand(ctx, "systemctl", "enable", "--now", u); e != nil {
			return journal, e
		}
		if e = fixedCommand(ctx, "systemctl", "is-active", "--quiet", u); e != nil {
			return journal, e
		}
	}
	journal.State = "installed"
	journal.Note = "Independent services active; authentication and business still require verification"
	if e = save(); e != nil {
		return journal, e
	}
	success = true
	return journal, nil
}
