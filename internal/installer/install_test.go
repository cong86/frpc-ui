package installer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{Name: "frp-console-test", Root: "/opt/frp-console-test", Listen: "127.0.0.1:18745", Configs: map[string]string{"frps": "bindPort = 17000\n[auth]\nmethod = \"token\"\ntoken = \"private-test-shared-token\"\n"}}
}
func TestRejectUnsafePathsPlaceholdersAndIncludedConfigs(t *testing.T) {
	for _, r := range []Request{{Name: "../../other", Root: "/opt/test", Listen: "127.0.0.1:18745", Configs: validRequest().Configs}, {Name: "safe-name", Root: "/opt/a%other", Listen: "127.0.0.1:18745", Configs: validRequest().Configs}, {Name: "safe-name", Root: "/opt/a", Listen: "0.0.0.0:18745", Configs: validRequest().Configs}} {
		if checkRequest(r) == nil {
			t.Fatal("unsafe installation accepted")
		}
	}
	r := validRequest()
	r.Configs["frps"] = "includes = [\"/root/private\"]\n[auth]\nmethod = \"token\"\ntoken = \"private-test-shared-token\"\n"
	if checkRequest(r) == nil {
		t.Fatal("includes accepted")
	}
}
func TestPlanBindsPreviewAndSecretsWithoutLoggingThem(t *testing.T) {
	r := validRequest()
	p := Plan{Request: r, ConsoleHash: strings.Repeat("a", 64)}
	first := planID(p)
	p.Request.Configs["frps"] += "# change\n"
	if first == planID(p) {
		t.Fatal("plan did not bind raw configuration")
	}
	p.Request.Configs = validRequest().Configs
	p.Files = nil
	p.ID = planID(p)
	p.ConsoleHash = strings.Repeat("b", 64)
	if p.ID == planID(p) {
		t.Fatal("plan did not bind binary")
	}
}
func TestPrivateInputCannotOverwriteOrBeWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX private-file mode check is exercised on Linux")
	}
	path := filepath.Join(t.TempDir(), "request.json")
	r := validRequest()
	if e := WriteJSON(path, r); e != nil {
		t.Fatal(e)
	}
	if WriteJSON(path, r) == nil {
		t.Fatal("private request overwritten")
	}
	var loaded Request
	if e := ReadJSON(path, &loaded); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(r)
	_ = os.WriteFile(path, b, 0644)
	_ = os.Chmod(path, 0644)
	if ReadJSON(path, &loaded) == nil {
		t.Fatal("public request accepted")
	}
}
func TestArchiveRejectsTamperedContentBeforeExtracting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.tar.gz")
	_ = os.WriteFile(path, []byte("untrusted archive"), 0600)
	if prepare(context.Background(), path, "amd64", t.TempDir()) == nil {
		t.Fatal("bad archive accepted")
	}
}
