//go:build browserqa

package server

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/state"
	"golang.org/x/crypto/bcrypt"
)

// TestBrowserPreview is a disposable browser fixture, excluded from normal
// builds. It does not initialize the user's Console data or administrator.
func TestBrowserPreview(t *testing.T) {
	binaryDir := os.Getenv("FRP_TEST_DIR")
	if binaryDir == "" {
		t.Skip("official binary directory required")
	}
	root := t.TempDir()
	st, e := state.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer st.DB.Close()
	password, e := bcrypt.GenerateFromPassword([]byte("test-password-long"), bcrypt.DefaultCost)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec("INSERT INTO users(name,password) VALUES(?,?)", "qa-admin", password); e != nil {
		t.Fatal(e)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	m := &config.Manager{State: st, Instances: map[string]config.Instance{}}
	configs := map[string]string{
		"frpc": "serverAddr='127.0.0.1'\nserverPort=17000\n[auth]\ntoken='qa-frp-token-only'\n[[proxies]]\nname='local-example'\ntype='tcp'\nlocalIP='127.0.0.1'\nlocalPort=18080\nremotePort=16000\nenabled=false\n",
		"frps": "bindAddr='127.0.0.1'\nbindPort=17000\n[auth]\ntoken='qa-frp-token-only'\n",
	}
	for role, raw := range configs {
		path := filepath.Join(root, role+".toml")
		if e = os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		m.Instances[role] = config.Instance{ID: role, Role: role, Managed: true, Path: path, Binary: filepath.Join(binaryDir, role+suffix)}
	}
	host := "127.0.0.1:18746"
	listener, e := net.Listen("tcp", host)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{}, 1)
	handler := New(st, m, host).Handler()
	fixture := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == host && r.URL.Path == "/qa/done" && r.Method == "POST" {
			select {
			case done <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	go fixture.Serve(listener)
	defer fixture.Close()
	t.Log("Disposable browser QA at http://" + host + "; no production or user preview data is accessed")
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("browser fixture expired")
	}
}
