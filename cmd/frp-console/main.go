package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/filelock"
	"github.com/cong86/frpc-ui/internal/server"
	"github.com/cong86/frpc-ui/internal/state"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18745", "local management address (loopback only in this milestone)")
	data := flag.String("data", "./data", "private state directory")
	demo := flag.Bool("demo", false, "create isolated local FRP configurations; no tunnels started")
	binaries := flag.String("frp-dir", "", "directory containing official frpc/frps for offline verify")
	client := flag.String("frpc-config", "", "read-only imported client TOML")
	service := flag.String("frps-config", "", "read-only imported server TOML")
	flag.Parse()
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("this milestone requires a literal loopback listen address")
	}
	root, e := filepath.Abs(*data)
	if e != nil {
		log.Fatal(e)
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		log.Fatal(e)
	}
	lock := filepath.Join(root, "console-owner.lock")
	release, e := filelock.Acquire(lock)
	if e != nil {
		log.Fatal("data directory is owned by another Console process")
	}
	defer release()
	st, e := state.Open(root)
	if e != nil {
		log.Fatal(e)
	}
	defer st.DB.Close()
	m := &config.Manager{State: st, Instances: map[string]config.Instance{}}
	for _, role := range []string{"frpc", "frps"} {
		p := ""
		if role == "frpc" {
			p = *client
		} else {
			p = *service
		}
		managed := false
		if p == "" && *demo {
			managed = true
			dir := filepath.Join(root, "instances", role)
			if e = os.MkdirAll(dir, 0700); e != nil {
				log.Fatal(e)
			}
			p = filepath.Join(dir, role+".toml")
			if _, e = os.Stat(p); os.IsNotExist(e) {
				raw := "bindAddr = \"127.0.0.1\"\nbindPort = 17000\n\n[auth]\nmethod = \"token\"\ntoken = \"DEMO_ONLY_CHANGE_BEFORE_DEPLOYMENT\"\n"
				if role == "frpc" {
					raw = "serverAddr = \"127.0.0.1\"\nserverPort = 17000\n\n[auth]\nmethod = \"token\"\ntoken = \"DEMO_ONLY_CHANGE_BEFORE_DEPLOYMENT\"\n\n[[proxies]]\nname = \"local-example\"\ntype = \"tcp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = 18080\nremotePort = 16000\nenabled = false\n"
				}
				if e = os.WriteFile(p, []byte(raw), 0600); e != nil {
					log.Fatal(e)
				}
			}
		}
		if p == "" {
			continue
		}
		p, e = filepath.Abs(p)
		if e != nil {
			log.Fatal(e)
		}
		binary := ""
		if *binaries != "" {
			suffix := ""
			if runtime.GOOS == "windows" {
				suffix = ".exe"
			}
			binary, e = filepath.Abs(filepath.Join(*binaries, role+suffix))
			if e != nil {
				log.Fatal(e)
			}
		}
		m.Instances[role] = config.Instance{ID: role, Role: role, Path: p, Managed: managed, Binary: binary}
	}
	if e = m.Recover(); e != nil {
		log.Fatal(e)
	}
	s := server.New(st, m, *listen)
	httpServer := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()
	fmt.Printf("FRP Console foundation: http://%s\nNo FRP process is started or controlled. Imported deployments are read-only.\n", *listen)
	if e = httpServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Print(e)
	}
}
