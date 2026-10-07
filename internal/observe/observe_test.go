package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/managed"
)

func TestExistingFRPSOnlyUsesReadAPIsAndNeverClaimsBusiness(t *testing.T) {
	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if u, p, ok := r.BasicAuth(); !ok || u != "observer" || p != "private-api-secret" {
			t.Error("credentials unavailable")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/serverinfo":
			fmt.Fprint(w, `{"version":"0.71.0","clientCounts":1,"totalTrafficIn":1000,"totalTrafficOut":2000,"curConns":2}`)
		case "/api/clients":
			fmt.Fprint(w, `[{"key":"private-client-id","clientIP":"private-address","version":"0.71.0","online":true}]`)
		case "/api/proxy/http":
			fmt.Fprint(w, `{"proxies":[{"name":"private-frp-token","status":"online","conf":{"customDomains":["site.test"],"locations":["/"]},"todayTrafficIn":123,"todayTrafficOut":456,"curConns":1}]}`)
		default:
			fmt.Fprint(w, `{"proxies":[]}`)
		}
	}))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	d, e := config.Parse(fmt.Sprintf("# unrelated comment password=do-not-export-comment\nbindPort=17000\n[auth]\ntoken='private-frp-token'\n[webServer]\naddr='127.0.0.1'\nport=%d\nuser='observer'\npassword='private-api-secret'\n", port), "frps")
	if e != nil {
		t.Fatal(e)
	}
	p := Profile{Version: 1, Config: "/config/frps.toml", Runtime: Target{"docker", "existing-frps"}, Logs: []LogSource{{"docker", "existing-frps", "frps"}}}
	var commands []string
	run := func(_ context.Context, n string, args ...string) ([]byte, error) {
		commands = append(commands, n+" "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "inspect" {
			return []byte(`{"Status":"running","Running":true,"Pid":42}`), nil
		}
		return []byte("2026-10-07 12:00:00 [I] client login private-api-secret\n"), nil
	}
	s := collectDocument(context.Background(), p, d, run)
	if s.Config.Editable || s.Runtime.Layers["authentication"].Status != "passed" || s.Runtime.Layers["registration"].Status != "passed" || s.Runtime.Layers["business"].Status != "not_checked" || s.Runtime.Layers["installation"].Status != "not_checked" {
		t.Fatal("wrong observation guarantees", s.Runtime.Layers)
	}
	if !s.Traffic.Available || s.Traffic.In != 1000 || len(s.Proxies) != 1 || s.Proxies[0].TrafficOut != 456 || !s.ClientsAvailable {
		t.Fatal("missing official data")
	}
	b, _ := json.Marshal(s)
	for _, secret := range []string{"private-api-secret", "private-frp-token", "private-client-id", "private-address", "do-not-export-comment"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("snapshot leaked", secret)
		}
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatal("non-read request", request)
		}
	}
	if len(requests) != 6 || len(commands) != 2 || !strings.HasPrefix(commands[0], "docker inspect ") || !strings.HasPrefix(commands[1], "docker logs ") {
		t.Fatal("unexpected read operations", requests, commands)
	}
	if managed.ReadServerAPI(context.Background(), d, "/api/config", &map[string]any{}) == nil {
		t.Fatal("non-observation API allowed")
	}
}

func TestUnknownProcessDoesNotHideAPIAndUnavailableAPIIsNotZeroTraffic(t *testing.T) {
	d, _ := config.Parse("bindPort=17000\n[webServer]\naddr='example.test'\nport=8056\n", "frps")
	s := collectDocument(context.Background(), Profile{Version: 1, Config: "/config/frps.toml"}, d, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected command")
		return nil, nil
	})
	if s.Traffic.Available || s.Runtime.Layers["process"].Status != "not_checked" || s.Runtime.Layers["authentication"].Status != "not_checked" {
		t.Fatal("unsupported API claimed available")
	}
}

func TestProfileRejectsCommandInjectionAndInvalidScopes(t *testing.T) {
	p := Profile{Version: 1, Config: filepath.Join(t.TempDir(), "frps.toml"), Runtime: Target{"docker", "frps"}}
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, v := range []Target{{"docker", "--help"}, {"docker", "frps;reboot"}, {"systemd", "frps"}, {"shell", "frps"}} {
		p.Runtime = v
		if p.Validate() == nil {
			t.Fatal("unsafe target allowed", v)
		}
	}
	p.Runtime = Target{}
	p.Nginx = &NginxProfile{Entry: "/nginx/nginx.conf", Prefix: "/etc/nginx", Roots: []string{"relative"}}
	if p.Validate() == nil {
		t.Fatal("relative roots allowed")
	}
	p.Nginx = nil
	p.Logs = []LogSource{{"file", "relative", "frps"}}
	if p.Validate() == nil {
		t.Fatal("relative log path allowed")
	}
}

func TestRateRequiresSameProcessProfileRevisionAndMonotonicCounters(t *testing.T) {
	previous := Snapshot{CollectedAt: 100, ProfileRevision: "a", Config: config.Snapshot{Revision: "r"}, Runtime: managed.Observation{Process: managed.Process{PID: 42}}, Traffic: Traffic{Available: true, In: 100, Out: 200}}
	next := previous
	next.CollectedAt = 110
	next.Traffic = Traffic{Available: true, In: 300, Out: 500}
	next.Traffic.WithPrevious(next, previous)
	if !next.Traffic.RateAvailable || next.Traffic.InPerSecond != 20 || next.Traffic.OutPerSecond != 30 {
		t.Fatal("wrong average rate")
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Runtime.Process.PID = 43 }, func(s *Snapshot) { s.CollectedAt = 300 }, func(s *Snapshot) { s.Traffic.In = 1 }, func(s *Snapshot) { s.ProfileRevision = "different" }, func(s *Snapshot) { s.Config.Revision = "different" }} {
		v := previous
		v.CollectedAt = 110
		v.Traffic = Traffic{Available: true, In: 300, Out: 500}
		mutate(&v)
		v.Traffic.WithPrevious(v, previous)
		if v.Traffic.RateAvailable {
			t.Fatal("invalid interval accepted")
		}
	}
}

func TestNginxIncludesMountMappingUpstreamsTLSAndSecrets(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "conf.d")
	if e := os.Mkdir(conf, 0700); e != nil {
		t.Fatal(e)
	}
	entry := filepath.Join(root, "nginx.conf")
	_ = os.WriteFile(entry, []byte("http { upstream frp_backend { server 127.0.0.1:8080; } include /etc/nginx/conf.d/*.conf; }"), 0600)
	_ = os.WriteFile(filepath.Join(conf, "site.conf"), []byte(`server { listen 443 ssl; server_name site.test; ssl_certificate /certs/public.crt; ssl_certificate_key /certs/private.key; set $secret 'hidden-directive-secret'; location / { proxy_pass http://user:private-api-secret@frp_backend/?key=hidden-query; proxy_set_header Upgrade $http_upgrade; } }`), 0600)
	d, _ := config.Parse("vhostHTTPPort=8080\n[auth]\ntoken='private-api-secret'", "frps")
	p := NginxProfile{Entry: entry, Prefix: "/etc/nginx", Roots: []string{root}, Mounts: []Mount{{"/etc/nginx/conf.d", conf}}}
	s := readNginx(p, d, func(string) error { return nil })
	if s.Status != "read" || s.Files != 2 || len(s.Sites) != 1 || !s.Sites[0].TLS || s.Validated || s.Loaded {
		t.Fatal("wrong nginx evidence", s)
	}
	r := s.Sites[0].Routes[0]
	if !r.WebSocket || len(r.Targets) != 1 || r.FRPHint == "" {
		t.Fatal("route association missing", r)
	}
	b, _ := json.Marshal(s)
	for _, secret := range []string{"private-api-secret", "hidden-directive-secret", "hidden-query", "private.key", "user:"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("nginx leaked", secret)
		}
	}
	if strings.Contains(string(b), `"issues":null`) {
		t.Fatal("empty issues must remain a JSON array for the UI")
	}
	for _, path := range []string{entry, filepath.Join(conf, "site.conf")} {
		if _, e := os.Stat(path); e != nil {
			t.Fatal("configuration changed")
		}
	}
}

func TestNginxCycleScopeEscapesAndMalformedSyntaxArePartial(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "nginx.conf")
	d, _ := config.Parse("bindPort=17000", "frps")
	_ = os.WriteFile(entry, []byte("http { include nginx.conf; include /outside/*.conf; server { listen 80; server_name retained.test; } }"), 0600)
	s := readNginx(NginxProfile{Entry: entry, Prefix: root, Roots: []string{root}}, d, func(string) error { return nil })
	if s.Status != "partial" || len(s.Issues) != 2 || len(s.Sites) != 1 {
		t.Fatal("scope/cycle not reported", s)
	}
	_ = os.WriteFile(entry, []byte("http { server { listen 80;"), 0600)
	s = readNginx(NginxProfile{Entry: entry, Prefix: root, Roots: []string{root}}, d, func(string) error { return nil })
	if s.Status != "unavailable" || len(s.Sites) != 0 {
		t.Fatal("malformed syntax accepted")
	}
}

func TestLexerDoesNotInterpretQuotedBracesCommentsAndVariables(t *testing.T) {
	n, e := parse(`http { server { server_name "a#b.test"; location ~ "^/x{1,2}$" { proxy_pass http://${backend}; } } }`, "fixture")
	if e != nil || n[0].children[0].children[0].args[0] != "a#b.test" {
		t.Fatal("quoted syntax lost", e)
	}
	if _, e = parse(`server { set x 'unclosed; }`, "fixture"); e == nil {
		t.Fatal("invalid quoted syntax accepted")
	}
}

func TestLogSummariesStripURLsHeadersSecretsAndCountWindowBytes(t *testing.T) {
	raw := `10.0.0.1 - secret-user [07/Oct/2026:12:00:00 +0800] "GET /token/private?password=secret HTTP/1.1" 200 123 "https://ref/?key=secret" "secret-agent"`
	w := ParseLogs(raw, "nginx-access")
	if w.Parsed != 1 || w.Bytes != 123 || w.Events[0].Method != "GET" || w.Events[0].Status != 200 {
		t.Fatal("access statistics missing")
	}
	b, _ := json.Marshal(w)
	for _, secret := range []string{"10.0.0.1", "secret-user", "/token", "password=", "secret-agent", "ref/"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("access log leaked", secret)
		}
	}
	w = ParseLogs("2026-10-07 12:00:00 [E] password=unknown-secret client login\n", "frps")
	b, _ = json.Marshal(w)
	if strings.Contains(string(b), "unknown-secret") || w.Events[0].Level != "error" {
		t.Fatal("service log leaked")
	}
	w = ParseLogs(strings.Repeat(raw+"\n", 150), "nginx-access")
	if w.Parsed != 100 || w.Bytes != 12300 {
		t.Fatal("unbounded log window")
	}
	if w = ParseLogs("custom format cannot be parsed", "nginx-access"); w.Skipped != 1 || w.Bytes != 0 {
		t.Fatal("custom log format misreported")
	}
}

func TestFileLogsTailOnlyAndNeverMutate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	raw := strings.Repeat("junk\n", 30000) + `127.0.0.1 - - [07/Oct/2026:12:00:00 +0800] "POST /private HTTP/1.1" 404 10` + "\n"
	_ = os.WriteFile(path, []byte(raw), 0600)
	w := ReadLogs(context.Background(), LogSource{"file", path, "nginx-access"}, nil)
	if !w.Available || w.Parsed != 1 || w.Bytes != 10 {
		t.Fatal("tail read failed")
	}
	b, _ := os.ReadFile(path)
	if string(b) != raw {
		t.Fatal("log modified")
	}
}

func TestCollectionTimestampIsEvidenceTime(t *testing.T) {
	d, _ := config.Parse("bindPort=17000", "frps")
	s := collectDocument(context.Background(), Profile{Version: 1, Config: "/frps.toml"}, d, func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	if s.CollectedAt < time.Now().Unix()-2 || s.Config.Editable || s.Runtime.Layers["business"].Status != "not_checked" {
		t.Fatal("wrong snapshot guarantees")
	}
}

func TestHTTPFragmentsAndRedirectsDoNotClaimFullNginxConfig(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "redirect.conf")
	_ = os.WriteFile(file, []byte(`server { listen 80; server_name a.test; return 301 https://a.test$request_uri; }`), 0600)
	d, _ := config.Parse("bindPort=17000", "frps")
	s := readNginx(NginxProfile{Context: "http-fragments", Entry: filepath.Join(root, "*.conf"), Prefix: root, Roots: []string{root}}, d, func(string) error { return nil })
	if s.Status != "partial" || len(s.Sites) != 1 || len(s.Sites[0].Routes) != 1 || !strings.HasPrefix(s.Sites[0].Routes[0].Redirect, "301 https://") || s.Validated || s.Loaded {
		t.Fatal("fragment incorrectly represented", s)
	}
}

func TestMissingTrafficCountersAndSecretTimestampAreNotReported(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"0.71.0"}`) }))
	defer api.Close()
	port := api.Listener.Addr().(*net.TCPAddr).Port
	d, _ := config.Parse(fmt.Sprintf("webServer.port=%d", port), "frps")
	s := collectDocument(context.Background(), Profile{}, d, func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	if s.Traffic.Available {
		t.Fatal("missing counters presented as zero traffic")
	}
	w := ParseLogs(`[secret-timestamp] "GET / HTTP/1.1" 200 123`, "nginx-access")
	if w.Parsed != 0 || w.Skipped != 1 {
		t.Fatal("arbitrary timestamp leaked")
	}
}
