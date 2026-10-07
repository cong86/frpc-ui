package installer

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cong86/frpc-ui/internal/observe"
)

func TestAdoptionWizardConfirmsFoundConfigAndSeparateNginxMounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths verified on Linux")
	}
	input := "\n\n\n\n\n2\n\n1\n\n\n2\n\n1\n\n\n"
	root := t.TempDir()
	var roles []string
	discover := func(_ context.Context, role string, target observe.Target) observe.Discovery {
		roles = append(roles, role)
		if target.Kind != "docker" || target.Name != role {
			t.Fatal("wrong lookup target", target)
		}
		if role == "frps" {
			return observe.Discovery{Candidates: []observe.DiscoveredConfig{{Path: filepath.Join(root, "frps.toml"), Source: "test mount"}}}
		}
		n := &observe.NginxProfile{Entry: filepath.Join(root, "nginx.conf"), Prefix: "/etc/nginx", Roots: []string{root}, Mounts: []observe.Mount{{Inside: "/etc/nginx/nginx.conf", Host: filepath.Join(root, "nginx.conf")}, {Inside: "/etc/nginx/conf.d", Host: filepath.Join(root, "conf.d")}}, Runtime: target}
		return observe.Discovery{Candidates: []observe.DiscoveredConfig{{Path: n.Entry, Nginx: n, Logs: []observe.LogSource{{Kind: "file", Name: filepath.Join(root, "access.log"), Format: "nginx-access"}}}}}
	}
	var output strings.Builder
	r, e := adoptionWizardWithDiscovery(bufio.NewReader(strings.NewReader(input)), &output, discover)
	if e != nil || len(roles) != 2 || r.Profile.Config != filepath.Join(root, "frps.toml") || r.Profile.Nginx == nil || len(r.Profile.Nginx.Mounts) != 2 || len(r.Profile.Logs) != 3 || r.Profile.Logs[1].Kind != "file" || r.Profile.Logs[2].Kind != "docker" {
		t.Fatal(r, e, output.String())
	}
	if !strings.Contains(output.String(), "确认使用") || !strings.Contains(output.String(), "配置挂载") {
		t.Fatal("scope not shown")
	}
}

func TestAdoptionWizardAutomaticallyRecognizesBothDockerTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths verified on Linux")
	}
	input := "\n\n\n\n\n\n\n1\n\n\n\n\n1\n\n\n"
	var output strings.Builder
	lookedUp := []string{}
	detect := func(_ context.Context, name string) observe.RuntimeDiscovery {
		lookedUp = append(lookedUp, name)
		return observe.RuntimeDiscovery{Candidates: []observe.Target{{Kind: "docker", Name: name}}}
	}
	discover := func(_ context.Context, role string, target observe.Target) observe.Discovery {
		if target.Kind != "docker" || target.Name != role {
			t.Fatal("auto-selected wrong runtime", target)
		}
		if role == "frps" {
			return observe.Discovery{Candidates: []observe.DiscoveredConfig{{Path: "/home/frp/frps.toml"}}}
		}
		n := &observe.NginxProfile{Entry: "/home/nginx/nginx.conf", Prefix: "/etc/nginx", Roots: []string{"/home/nginx"}, Runtime: target}
		return observe.Discovery{Candidates: []observe.DiscoveredConfig{{Path: n.Entry, Nginx: n}}}
	}
	r, e := adoptionWizardWithLookups(bufio.NewReader(strings.NewReader(input)), &output, discover, detect)
	if e != nil || len(lookedUp) != 2 || r.Profile.Runtime.Kind != "docker" || r.Profile.Nginx == nil || r.Profile.Nginx.Runtime.Kind != "docker" || !strings.Contains(output.String(), "已识别默认目标") {
		t.Fatal("automatic runtime selection failed", e, output.String())
	}
}

func TestAdoptionWizardDoesNotGuessBetweenRegisteredRuntimes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths verified on Linux")
	}
	input := "\n\n\n\n/home/frp/frps.toml\n\n2\ncustom-frps\n\n0\n"
	var output strings.Builder
	r, e := adoptionWizardWithLookups(bufio.NewReader(strings.NewReader(input)), &output,
		func(context.Context, string, observe.Target) observe.Discovery { return observe.Discovery{} },
		func(context.Context, string) observe.RuntimeDiscovery {
			return observe.RuntimeDiscovery{Candidates: []observe.Target{{Kind: "docker", Name: "frps"}, {Kind: "systemd", Name: "frps.service"}}}
		})
	if e != nil || r.Profile.Runtime.Name != "custom-frps" || r.Profile.Runtime.Kind != "docker" || !strings.Contains(output.String(), "未找到唯一") {
		t.Fatal("ambiguous runtimes silently selected", e, output.String())
	}
}
func TestAdoptionWizardMissingPathFallsBackAndDoesNotAcceptBlank(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths verified on Linux")
	}
	input := "\n\n\n\n\n2\n\n\nrelative.toml\n/srv/frp/frps.toml\n\n0\n"
	var output strings.Builder
	r, e := adoptionWizardWithDiscovery(bufio.NewReader(strings.NewReader(input)), &output, func(context.Context, string, observe.Target) observe.Discovery {
		return observe.Discovery{Issues: []string{"未找到配置"}}
	})
	if e != nil || r.Profile.Config != "/srv/frp/frps.toml" || r.Profile.Nginx != nil || strings.Count(output.String(), "FRPS 配置是必填项") != 3 {
		t.Fatal(r, e, output.String())
	}
	_, e = adoptionWizardWithDiscovery(bufio.NewReader(strings.NewReader("\n\n\n\n\n2\n\n")), &output, func(context.Context, string, observe.Target) observe.Discovery { return observe.Discovery{} })
	if e == nil {
		t.Fatal("EOF accepted as installation input")
	}
}

func TestAdoptionWizardMultipleCandidatesRequireSelection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths verified on Linux")
	}
	input := "\n\n\n\n\n2\n\n9\n2\n\n0\n"
	var output strings.Builder
	r, e := adoptionWizardWithDiscovery(bufio.NewReader(strings.NewReader(input)), &output, func(context.Context, string, observe.Target) observe.Discovery {
		return observe.Discovery{Candidates: []observe.DiscoveredConfig{{Path: "/first/frps.toml"}, {Path: "/selected/frps.toml"}}}
	})
	if e != nil || r.Profile.Config != "/selected/frps.toml" || !strings.Contains(output.String(), "编号无效") {
		t.Fatal(r, e, output.String())
	}
}

func adoptionRequest() AdoptionRequest {
	return AdoptionRequest{Name: "frp-console-observer", Root: "/opt/frp-console-observer", Listen: "127.0.0.1:18745", Interval: 30, Profile: observe.Profile{Version: 1, Config: "/srv/frp/frps.toml", Runtime: observe.Target{Kind: "docker", Name: "frps"}}}
}
func TestAdoptionRejectsUnsafeScopeAndArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX adoption inputs tested on Linux")
	}
	r := adoptionRequest()
	if checkAdoption(r) != nil {
		t.Fatal("valid adoption rejected")
	}
	for _, mutate := range []func(*AdoptionRequest){func(r *AdoptionRequest) { r.Root = "/opt/unsafe name" }, func(r *AdoptionRequest) { r.Listen = "0.0.0.0:18745" }, func(r *AdoptionRequest) { r.Listen = "127.0.0.1:bad" }, func(r *AdoptionRequest) { r.Name = "frps" }, func(r *AdoptionRequest) { r.Interval = 0 }, func(r *AdoptionRequest) { r.Profile.Runtime.Name = "frps; reboot" }} {
		r = adoptionRequest()
		mutate(&r)
		if checkAdoption(r) == nil {
			t.Fatal("unsafe adoption accepted")
		}
	}
}
func TestAdoptionPlanBindsSourceRevisionDestinationBinaryAndTimer(t *testing.T) {
	p := AdoptionPlan{Request: adoptionRequest(), ConsoleHash: strings.Repeat("a", 64), ConfigRevision: strings.Repeat("b", 64)}
	id := adoptionID(p)
	for _, mutate := range []func(*AdoptionPlan){func(p *AdoptionPlan) { p.Request.Profile.Config += ".other" }, func(p *AdoptionPlan) { p.ConsoleHash = "changed" }, func(p *AdoptionPlan) { p.ConfigRevision = "changed" }, func(p *AdoptionPlan) { p.Request.Interval = 60 }, func(p *AdoptionPlan) { p.Request.Root += "-other" }} {
		copy := p
		mutate(&copy)
		if adoptionID(copy) == id {
			t.Fatal("plan did not bind adoption input")
		}
	}
}
func TestAdoptionCreatesNoFRPOrNginxControlUnit(t *testing.T) {
	r := adoptionRequest()
	files := adoptionFiles(r)
	if len(files) != 4 {
		t.Fatal("unexpected new resources")
	}
	for _, file := range files {
		if strings.HasSuffix(file.Path, ".json") {
			continue
		}
		if strings.Contains(file.Content, " restart ") || strings.Contains(file.Content, "reload") || strings.Contains(file.Content, "/frp/frps") || strings.Contains(file.Content, "--manifest") {
			t.Fatal("existing service control in adoption")
		}
	}
	if !strings.Contains(files[1].Content, "--observed-snapshot") || !strings.Contains(files[2].Content, "User=root") || !strings.Contains(files[3].Content, "OnUnitInactiveSec=30s") || !strings.Contains(files[3].Content, "AccuracySec=1s") {
		t.Fatal("incorrect isolation or refresh")
	}
}
func TestAdoptionWizardEOFStopsWithoutAnImplicitConfirmation(t *testing.T) {
	if _, e := adoptionWizard(bufio.NewReader(strings.NewReader("\n")), io.Discard); e == nil {
		t.Fatal("truncated wizard accepted")
	}
}
func TestAdoptionWizardUsesRegisteredRuntimeLogsWhenPathsAreEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX adoption inputs tested on Linux")
	}
	input := "\n\n\n\n/srv/frp/frps.toml\nsystemd\n\n\n/etc/nginx/nginx.conf\nmain\n\n\n\n docker\nnginx\n\n\n"
	r, e := adoptionWizard(bufio.NewReader(strings.NewReader(input)), io.Discard)
	if e != nil || len(r.Profile.Logs) != 3 {
		t.Fatal("registered service logs unavailable", e)
	}
	for i, expected := range []observe.LogSource{{Kind: "systemd", Name: "frps.service", Format: "frps"}, {Kind: "docker", Name: "nginx", Format: "nginx-access"}, {Kind: "docker", Name: "nginx", Format: "nginx-error"}} {
		if r.Profile.Logs[i] != expected {
			t.Fatal("wrong default log source")
		}
	}
}
func TestChineseNumberedAdoptionChoicesPreserveRegisteredTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX adoption inputs tested on Linux")
	}
	input := "\n\n\n\n/srv/frp/frps.toml\n1\n\n\n/etc/nginx/conf.d/*.conf\n2\n\n\n\n2\n\n\n\n"
	r, e := adoptionWizard(bufio.NewReader(strings.NewReader(input)), io.Discard)
	if e != nil || r.Profile.Runtime.Kind != "systemd" || r.Profile.Nginx == nil || r.Profile.Nginx.Context != "http-fragments" || r.Profile.Nginx.Runtime.Kind != "docker" || r.Profile.Nginx.Runtime.Name != "nginx" || len(r.Profile.Logs) != 3 {
		t.Fatal("numbered choices changed registration", e)
	}
}
func TestAdoptionUIRequiresSessionEvidenceNotJustHTTP200(t *testing.T) {
	for _, body := range []string{`{"initialized":false,"authenticated":false}`, `{"ok":true}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/session" || r.Header.Get("X-FRP-Console") != "1" {
				t.Error("wrong UI probe")
			}
			_, _ = io.WriteString(w, body)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		e := checkAdoptionUI(ctx, strings.TrimPrefix(s.URL, "http://"))
		cancel()
		s.Close()
		if (e == nil) != (strings.Contains(body, "initialized")) {
			t.Fatal("wrong session evidence outcome", e)
		}
	}
}
