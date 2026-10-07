package installer

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cong86/frpc-ui/internal/observe"
)

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
