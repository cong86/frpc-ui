package templates

import (
	"strings"
	"testing"
)

func TestRejectNginxInjection(t *testing.T) {
	for _, domain := range []string{"site.test;include /etc/passwd;", "x\nserver {}", "../site"} {
		if _, e := Nginx(NginxRequest{Domain: domain, Upstream: "127.0.0.1", Port: 8080}); e == nil {
			t.Fatal("injection accepted")
		}
	}
	f, e := Nginx(NginxRequest{Domain: "site.test", Upstream: "127.0.0.1", Port: 8080, WebSocket: true})
	if e != nil || !strings.Contains(f.Content, "proxy_set_header Upgrade") {
		t.Fatal("WebSocket template missing", e)
	}
}
func TestDeploymentPreviewIsIndependent(t *testing.T) {
	for _, role := range []string{"frpc", "frps"} {
		for _, mode := range []string{"systemd", "compose"} {
			p, e := Install(InstallRequest{Role: role, Mode: mode, Server: "127.0.0.1", Port: 17000})
			if e != nil {
				t.Fatal(e)
			}
			if p.ExecutionAvailable || len(p.Files) != 2 {
				t.Fatal("preview misrepresented")
			}
			for _, f := range p.Files {
				if strings.Contains(f.Content, "Requires=frp-console") || strings.Contains(f.Content, "PartOf=") {
					t.Fatal("FRP incorrectly coupled to console")
				}
			}
		}
	}
}
