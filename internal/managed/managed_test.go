package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/cong86/frpc-ui/internal/config"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessIsNotBusinessAndProxyFailureIsSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "admin" || password != "private-test-password" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tcp":[{"name":"one","status":"running"}],"udp":[{"name":"two","status":"start error","err":"private-test-password"}]}`)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	binary := filepath.Join(t.TempDir(), "frpc")
	_ = os.WriteFile(binary, []byte("test binary"), 0600)
	hash, _ := Digest(binary)
	d, e := config.Parse(fmt.Sprintf("webServer.addr = \"127.0.0.1\"\nwebServer.port = %d\nwebServer.user = \"admin\"\nwebServer.password = \"private-test-password\"\n[[proxies]]\nname = \"one\"\ntype = \"tcp\"\n[[proxies]]\nname = \"two\"\ntype = \"udp\"\n", port), "frpc")
	if e != nil {
		t.Fatal(e)
	}
	c := Collector{Manifest: &Manifest{Instances: []Instance{{ID: "frpc", Role: "frpc", Binary: binary, BinaryHash: hash, Unit: "test-frpc.service"}}}, Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=42\n"), nil
	}}
	o := c.Observe(context.Background(), "frpc", d, true)
	if o.Layers["process"].Status != "passed" || o.Layers["authentication"].Status != "passed" || o.Layers["registration"].Status != "failed" || o.Layers["business"].Status != "not_checked" {
		t.Fatalf("wrong layer conclusions: %+v", o.Layers)
	}
	b, _ := json.Marshal(o)
	if strings.Contains(string(b), "private-test-password") {
		t.Fatal("runtime leaked password")
	}
	_ = os.WriteFile(binary, []byte("changed"), 0600)
	o = c.Observe(context.Background(), "frpc", d, true)
	if o.Layers["installation"].Status != "failed" || o.Layers["process"].Status != "not_checked" {
		t.Fatal("changed binary accepted")
	}
}
func TestHTTPBodyDigestAndRedirectCannotFakeBusiness(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", 302)
			return
		}
		fmt.Fprint(w, "actual business")
	}))
	defer s.Close()
	sum := sha256.Sum256([]byte("actual business"))
	p := Probe{Scope: "business", Kind: "http", Address: s.Listener.Addr().String(), Path: "/ok", SHA256: hex.EncodeToString(sum[:]), Status: 200}
	if e := ProbeOnce(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	p.Path = "/redirect"
	if ProbeOnce(context.Background(), p) == nil {
		t.Fatal("redirect accepted")
	}
	p.Path = "/ok"
	p.SHA256 = strings.Repeat("0", 64)
	if ProbeOnce(context.Background(), p) == nil {
		t.Fatal("wrong content accepted")
	}
}
func TestUDPRequiresApplicationResponse(t *testing.T) {
	s, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	go func() {
		b := make([]byte, 1024)
		for {
			n, a, e := s.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = s.WriteTo(append([]byte("reply:"), b[:n]...), a)
		}
	}()
	p := Probe{Scope: "business", Kind: "udp", Address: s.LocalAddr().String(), Prefix: "reply:"}
	if e = ProbeOnce(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	p.Prefix = "wrong:"
	if ProbeOnce(context.Background(), p) == nil {
		t.Fatal("wrong UDP payload accepted")
	}
}
func TestBrowserCannotSupplyUnsafeProbeTargets(t *testing.T) {
	for _, p := range []Probe{{Scope: "business", Kind: "tcp", Address: "127.0.0.1:80"}, {Scope: "business", Kind: "http", Address: "metadata.invalid:80"}, {Scope: "business", Kind: "udp", Address: "127.0.0.1:80"}} {
		if ValidateProbe(p) == nil {
			t.Fatal("unsafe/unverifiable probe accepted")
		}
	}
}
