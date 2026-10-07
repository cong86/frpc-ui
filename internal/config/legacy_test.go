package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyServer = `# comments may contain long-token-fixture
[common]
bind_addr = 127.0.0.1
bind_port = 17000
token = long-token-fixture
dashboard_addr = 127.0.0.1
dashboard_port = 17500
dashboard_user = "observer"
dashboard_pwd = "long-password-fixture#still-password"
allow_ports = 16000-16010, 17001
custom_field = preserve-me
alias = long-token-fixture
[plugin.fixture]
addr = 127.0.0.1:19000
secret = plugin-secret-fixture
`

func TestLegacyFRPSContentDetectionPreservesAuthoritativeBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frps.toml")
	for _, raw := range []string{legacyServer, strings.ReplaceAll(legacyServer, "\n", "\r\n"), "\ufeff" + legacyServer} {
		if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		m := Manager{Instances: map[string]Instance{"server": {ID: "server", Role: "frps", Path: path}}}
		d, e := m.Read("server")
		if e != nil {
			t.Fatal(e)
		}
		s := d.Snapshot()
		encoded, _ := json.Marshal(s)
		if d.Format != "ini" || s.Format != "ini" || s.Editable || d.Raw != raw || s.Revision != Revision(raw) {
			t.Fatal("legacy source authority or read-only boundary lost")
		}
		for _, secret := range []string{"long-token-fixture", "long-password-fixture#still-password", "plugin-secret-fixture"} {
			if strings.Contains(string(encoded), secret) || strings.Contains(d.RedactText(secret), secret) {
				t.Fatal("legacy secret leaked")
			}
		}
		web := d.Values["webServer"].(map[string]any)
		if web["addr"] != "127.0.0.1" || web["port"] != int64(17500) || web["user"] != "observer" || web["password"] != "long-password-fixture#still-password" {
			t.Fatal("legacy API projection changed credentials or endpoint")
		}
		if !strings.Contains(s.Text, "preserve-me") || len(s.Values["allowPorts"].([]any)) != 2 {
			t.Fatal("unknown fields or allowed ranges omitted")
		}
		if _, e := d.RestoreMasks(s.Text); e == nil {
			t.Fatal("legacy source became writable")
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("original file changed")
		}
	}
}

func TestLegacyAPIUnknownDefaultsAndTLSRemainUnavailable(t *testing.T) {
	d, e := Parse("[common]\nbind_port=7000\ndashboard_port=7500\n", "frps")
	if e != nil || d.Values["webServer"].(map[string]any)["addr"] != "0.0.0.0" {
		t.Fatal("invented loopback dashboard")
	}
	for _, extra := range []string{"dashboard_tls_mode=true", "dashboard_tls_cert_file=/cert.crt", "dashboard_tls_key_file=/key.pem"} {
		d, e = Parse("[common]\ndashboard_addr=127.0.0.1\ndashboard_port=7500\n"+extra+"\n", "frps")
		if e != nil || d.Values["webServer"].(map[string]any)["tls"] == nil {
			t.Fatal("legacy HTTPS silently downgraded")
		}
	}
}

func TestConfigurationErrorsNeverEchoRawCredentials(t *testing.T) {
	for _, raw := range []string{
		"[common]\nbind_port=long-token-fixture\n",
		"[common]\nallow_ports=long-token-fixture\n",
		"[common]\ndashboard_tls_mode=long-token-fixture\n",
		"[common]\ntoken={{ .Envs.SECRET }}\n",
		"[common]\ndashboard_pwd=%(token)s\n",
		"bindPort=long-token-fixture\n",
	} {
		_, e := Parse(raw, "frps")
		if e == nil || strings.Contains(e.Error(), "long-token-fixture") {
			t.Fatal("unsafe or missing diagnostic")
		}
	}
	if _, e := Parse(legacyServer, "frpc"); e == nil {
		t.Fatal("server projection reused for old client")
	}
	_, e := Parse("bindPort=long-token-fixture\n", "frps")
	if !strings.Contains(e.Error(), "第 1 行") {
		t.Fatal("safe syntax position omitted")
	}
}
