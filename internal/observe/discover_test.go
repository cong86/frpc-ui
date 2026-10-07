package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"
)

func discoveryFiles(t *testing.T) (string, string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX deployment paths verified on Linux")
	}
	root := t.TempDir()
	frps := filepath.Join(root, "frp", "frps.toml")
	nginx := filepath.Join(root, "web", "nginx.conf")
	confd := filepath.Join(root, "web", "conf.d")
	logs := filepath.Join(root, "web", "logs")
	for _, dir := range []string{filepath.Dir(frps), confd, logs} {
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, file := range []string{frps, nginx, filepath.Join(confd, "site.conf"), filepath.Join(logs, "access.log"), filepath.Join(logs, "error.log")} {
		if e := os.WriteFile(file, []byte("private-not-read"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return frps, nginx, confd, logs
}
func discoveryRunner(t *testing.T, d dockerDiscovery) Runner {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "docker" || len(args) != 4 || args[0] != "inspect" || args[2] != "--format" || args[3] != discoveryFormat {
			t.Fatal("unexpected discovery command", name, args)
		}
		return json.Marshal(d)
	}
}
func TestDockerDiscoveryMapsExactFileBeforeDirectory(t *testing.T) {
	frps, _, _, _ := discoveryFiles(t)
	d := dockerDiscovery{Configs: []string{"/frp/frps.toml"}, Mounts: []discoveredMount{{"bind", filepath.Dir(frps), "/frp"}, {"bind", frps, "/frp/frps.toml"}, {"bind", "/", "/host"}}}
	found := discoverExisting(context.Background(), "frps", Target{"docker", "frps"}, discoveryRunner(t, d), os.Stat)
	if len(found.Candidates) != 1 || found.Candidates[0].Path != frps {
		t.Fatal("exact file mapping unavailable", found)
	}
	d.Configs = []string{"/unmounted/frps.toml"}
	if got := discoverExisting(context.Background(), "frps", Target{"docker", "frps"}, discoveryRunner(t, d), os.Stat); len(got.Candidates) != 0 {
		t.Fatal("guessed mounted file despite explicit different config", got)
	}
	d.Configs = nil
	if got := discoverExisting(context.Background(), "frps", Target{"docker", "frps"}, discoveryRunner(t, d), os.Stat); len(got.Candidates) != 1 {
		t.Fatal("mount fallback ambiguous or missing", got)
	}
}
func TestNginxDiscoveryRetainsSeparateConfigurationMountsAndLogs(t *testing.T) {
	_, nginx, confd, logs := discoveryFiles(t)
	d := dockerDiscovery{Mounts: []discoveredMount{{"bind", nginx, "/etc/nginx/nginx.conf"}, {"bind", confd, "/etc/nginx/conf.d"}, {"bind", logs, "/var/log/nginx"}, {"bind", logs, "/etc/nginx/certs"}, {"bind", logs, "/run/php"}}}
	found := discoverExisting(context.Background(), "nginx", Target{"docker", "nginx"}, discoveryRunner(t, d), os.Stat)
	if len(found.Candidates) != 1 {
		t.Fatal(found)
	}
	c := found.Candidates[0]
	if c.Path != nginx || c.Nginx.Context != "" || len(c.Nginx.Mounts) != 2 || len(c.Logs) != 2 {
		t.Fatal("incomplete or excessive scope", c)
	}
	for _, m := range c.Nginx.Mounts {
		if m.Inside == "/etc/nginx/certs" || m.Inside == "/run/php" {
			t.Fatal("unrelated or certificate mount registered", m)
		}
	}
	p := Profile{Version: 1, Config: filepath.Join(filepath.Dir(confd), "frps.toml"), Runtime: Target{"docker", "frps"}, Nginx: c.Nginx, Logs: c.Logs}
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	d.Mounts = d.Mounts[1:]
	found = discoverExisting(context.Background(), "nginx", Target{"docker", "nginx"}, discoveryRunner(t, d), os.Stat)
	if len(found.Candidates) != 1 || found.Candidates[0].Nginx.Context != "http-fragments" || found.Candidates[0].Path != filepath.Join(confd, "*.conf") {
		t.Fatal("fragments claimed complete or missing", found)
	}
	d.Prefixes = []string{"/custom"}
	if got := discoverExisting(context.Background(), "nginx", Target{"docker", "nginx"}, discoveryRunner(t, d), os.Stat); len(got.Candidates) != 0 {
		t.Fatal("custom prefix silently inferred", got)
	}
}
func TestDiscoveryRejectsBroadMountsAndInvalidTargetWithoutCommands(t *testing.T) {
	discoveryFiles(t)
	run := func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unsafe target reached command")
		return nil, nil
	}
	found := discoverExisting(context.Background(), "frps", Target{"docker", "frps; reboot"}, run, os.Stat)
	if len(found.Candidates) != 0 || len(found.Issues) == 0 {
		t.Fatal(found)
	}
	d := dockerDiscovery{Configs: []string{"/etc/frps.toml"}, Mounts: []discoveredMount{{"bind", "/", "/"}}}
	if got := discoverExisting(context.Background(), "frps", Target{"docker", "frps"}, discoveryRunner(t, d), os.Stat); len(got.Candidates) != 0 {
		t.Fatal("filesystem root accepted", got)
	}
}
func TestNativeDiscoveryUsesRelativeConfigAndRegisteredWorkingDirectory(t *testing.T) {
	frps, _, _, _ := discoveryFiles(t)
	var requests []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		requests = append(requests, name+" "+strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "WorkingDirectory") {
			return []byte(filepath.Dir(frps)), nil
		}
		return []byte("{ path=/opt/frps ; argv[]=/opt/frps --config frps.toml --token private-not-returned ; ignore_errors=no ; }"), nil
	}
	found := discoverExisting(context.Background(), "frps", Target{"systemd", "frps.service"}, run, os.Stat)
	if len(found.Candidates) != 1 || found.Candidates[0].Path != frps || len(requests) != 2 {
		t.Fatal(found, requests)
	}
	b, _ := json.Marshal(found)
	if strings.Contains(string(b), "private-not-returned") {
		t.Fatal("command credential returned")
	}
}
func TestDockerDiscoveryTemplateFiltersOtherArgumentsAndEnvironment(t *testing.T) {
	jsonFunc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	tpl, e := template.New("inspect").Funcs(template.FuncMap{"json": jsonFunc}).Parse(discoveryFormat)
	if e != nil {
		t.Fatal(e)
	}
	data := map[string]any{"Args": []string{"-c", "/frp/frps.toml", "--token", "private-token", "--config=/custom.toml", "-p", "/etc/nginx"}, "Config": map[string]any{"WorkingDir": "/frp", "Env": []string{"PASSWORD=private-env"}}, "Mounts": []discoveredMount{{"bind", "/host/frps.toml", "/frp/frps.toml"}}}
	var output bytes.Buffer
	if e = tpl.Execute(&output, data); e != nil {
		t.Fatal(e)
	}
	var d dockerDiscovery
	if json.Unmarshal(output.Bytes(), &d) != nil || len(d.Configs) != 2 || len(d.Prefixes) != 1 {
		t.Fatal(output.String())
	}
	for _, secret := range []string{"private-token", "private-env", "PASSWORD"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("credential field exported")
		}
	}
}
func TestMissingFRPSConfigHasActionableError(t *testing.T) {
	e := (Profile{Version: 1}).Validate()
	if e == nil || !strings.Contains(e.Error(), "FRPS") || !strings.Contains(e.Error(), "宿主机") {
		t.Fatal(e)
	}
}
