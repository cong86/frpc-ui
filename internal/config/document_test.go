package config

import (
	"strings"
	"testing"
)

const fixture = `# unrelated comment retained
serverAddr = "127.0.0.1"
serverPort = 17000
[auth]
token = "a-long-test-secret"

[[proxies]]
name = "one"
type = "tcp"
localPort = 8080
remotePort = 16000
enabled = false
opaque_field = "keep-me"

[[proxies]]
name = "two"
type = "udp"
localPort = 8081
remotePort = 16001
`

func TestMaskedEditRoundtrip(t *testing.T) {
	d, e := Parse(fixture, "frpc")
	if e != nil {
		t.Fatal(e)
	}
	s := d.Snapshot()
	if !s.Editable || strings.Contains(s.Text, "a-long-test-secret") {
		t.Fatal("unsafe snapshot")
	}
	raw, e := d.RestoreMasks(s.Text)
	if e != nil || raw != fixture {
		t.Fatalf("roundtrip did not preserve source: %v", e)
	}
}
func TestPatchPreservesUnknownAndOtherProxy(t *testing.T) {
	d, _ := Parse(fixture, "frpc")
	raw, e := d.PatchProxy("one", map[string]any{"localPort": float64(8090), "enabled": true}, false)
	if e != nil {
		t.Fatal(e)
	}
	next, e := Parse(raw, "frpc")
	if e != nil {
		t.Fatal(e)
	}
	rows := next.Values["proxies"].([]any)
	if rows[0].(map[string]any)["localPort"] != int64(8090) {
		t.Fatal("port was serialized as a float")
	}
	if !strings.Contains(raw, `opaque_field = "keep-me"`) || !strings.HasSuffix(raw, strings.Split(fixture, "[[proxies]]")[2]) || !strings.HasPrefix(raw, "# unrelated comment retained") {
		t.Fatal("unrelated content changed")
	}
}
func TestRemovalRetainsFollowingCommonTable(t *testing.T) {
	raw := "[[proxies]]\nname='one'\ntype='tcp'\nlocalPort=8080\nremotePort=16000\n[auth]\ntoken='another-long-secret'\n"
	d, _ := Parse(raw, "frpc")
	next, e := d.PatchProxy("one", nil, true)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(next, "[auth]") || strings.Contains(next, "[[proxies]]") {
		t.Fatal("removal touched common configuration")
	}
}
func TestComplexCredentialsNeverExposeRaw(t *testing.T) {
	for _, raw := range []string{"auth = { token = 'a-long-test-secret' }\n", "[auth]\ntoken = '''a-long-test-secret'''\n", "# a-long-test-secret in a comment\n[auth]\ntoken='a-long-test-secret'\n"} {
		d, e := Parse(raw, "frpc")
		if e != nil {
			t.Fatal(e)
		}
		s := d.Snapshot()
		if s.Editable || strings.Contains(s.Text, "a-long-test-secret") {
			t.Fatal("complex credential leaked or became editable")
		}
	}
}
func TestSecretAliasesAndEscapesAreRedacted(t *testing.T) {
	raw := "description='a-long-test-secret'\n[auth]\ntoken='a-long-test-secret'\n"
	d, e := Parse(raw, "frpc")
	if e != nil {
		t.Fatal(e)
	}
	s := d.Snapshot()
	if strings.Contains(s.Text, "a-long-test-secret") || s.Values["description"] == "a-long-test-secret" {
		t.Fatal("credential alias leaked")
	}
}
func TestNestedSecretTableIsNotLeaked(t *testing.T) {
	d, e := Parse("[secret]\nvalue='nested-test-secret'\n", "frpc")
	if e != nil {
		t.Fatal(e)
	}
	s := d.Snapshot()
	if s.Editable || strings.Contains(s.Text, "nested-test-secret") {
		t.Fatal("nested sensitive value leaked")
	}
}
func TestInlineCommentPreserved(t *testing.T) {
	raw := strings.Replace(fixture, "localPort = 8080", "localPort = 8080 # keep port comment", 1)
	d, _ := Parse(raw, "frpc")
	out, e := d.PatchProxy("one", map[string]any{"localPort": int64(8090)}, false)
	if e != nil || !strings.Contains(out, "localPort = 8090 # keep port comment") {
		t.Fatal("inline comment lost", e)
	}
}
func TestRejectPlaceholderExfiltration(t *testing.T) {
	d, _ := Parse(fixture, "frpc")
	masked := d.Snapshot().Text
	for _, raw := range []string{strings.Replace(masked, "serverAddr = \"127.0.0.1\"", "serverAddr = \"__FRP_REDACTED_0__\"", 1), masked + "\n# __FRP_REDACTED_0__", strings.ReplaceAll(masked, "__FRP_REDACTED_0__", "__FRP_REDACTED_999__")} {
		if _, e := d.RestoreMasks(raw); e == nil {
			t.Fatal("placeholder misuse accepted")
		}
	}
}
func TestIncludesAndTemplatesReadOnly(t *testing.T) {
	for _, raw := range []string{"includes=['./*.toml']\n", `serverAddr="{{ .Envs.HOST }}"`} {
		d, e := Parse(raw, "frpc")
		if e != nil {
			t.Fatal(e)
		}
		if d.Snapshot().Editable {
			t.Fatal("untracked dependency editable")
		}
	}
}
func TestRejectDuplicateNamesAndInvalidPorts(t *testing.T) {
	if _, e := Parse(fixture+"\n[[proxies]]\nname='one'\ntype='tcp'\n", "frpc"); e == nil {
		t.Fatal("duplicate proxy accepted")
	}
	d, _ := Parse(fixture, "frpc")
	for _, port := range []any{0.1, -1.0, 65536.0, "8080"} {
		if _, e := d.PatchProxy("one", map[string]any{"localPort": port}, false); e == nil {
			t.Fatal("invalid port accepted")
		}
	}
}
