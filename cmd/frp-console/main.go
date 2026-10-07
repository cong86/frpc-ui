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
	"github.com/cong86/frpc-ui/internal/installer"
	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/cong86/frpc-ui/internal/observe"
	"github.com/cong86/frpc-ui/internal/server"
	"github.com/cong86/frpc-ui/internal/state"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "observe" {
		if e := observe.CLI(os.Args[2:]); e != nil {
			log.Fatal(e)
		}
		fmt.Println("Read-only observation snapshot updated; existing services unchanged.")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if e := installer.CLI(os.Args[2:]); e != nil {
			log.Fatal(e)
		}
		return
	}
	listen := flag.String("listen", "127.0.0.1:18745", "local management address (loopback only in this milestone)")
	data := flag.String("data", "./data", "private state directory")
	demo := flag.Bool("demo", false, "create isolated local FRP configurations; no tunnels started")
	binaries := flag.String("frp-dir", "", "directory containing official frpc/frps for offline verify")
	client := flag.String("frpc-config", "", "read-only imported client TOML")
	service := flag.String("frps-config", "", "read-only imported server TOML")
	manifestPath := flag.String("manifest", "", "root-owned manifest for exclusively installed systemd instances")
	observedPath := flag.String("observed-snapshot", "", "root-owned redacted snapshot of an existing FRPS/Nginx deployment")
	flag.Parse()
	if *observedPath != "" {
		if *manifestPath != "" || *demo || *client != "" || *service != "" {
			log.Fatal("observed snapshot cannot be combined with managed/demo/import configurations")
		}
		if _, e := observe.LoadSnapshot(*observedPath); e != nil {
			log.Fatal(e)
		}
	}
	var installed *managed.Manifest
	if *manifestPath != "" {
		var e error
		installed, e = managed.Load(*manifestPath)
		if e != nil {
			log.Fatal(e)
		}
		if *demo || *client != "" || *service != "" {
			log.Fatal("managed manifest cannot be combined with demo or imports")
		}
		if filepath.Clean(*data) != filepath.Join(installed.Root, "data") {
			log.Fatal("data directory must match installed manifest")
		}
	}
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
		if installed != nil {
			continue
		}
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
	if installed != nil {
		for _, i := range installed.Instances {
			m.Instances[i.ID] = config.Instance{ID: i.ID, Role: i.Role, Path: i.Config, Managed: true, Binary: i.Binary}
		}
	}
	if e = m.Recover(); e != nil {
		log.Fatal(e)
	}
	s := server.New(st, m, *listen)
	s.Runtime = &managed.Collector{Manifest: installed}
	s.ObservedPath = *observedPath
	httpServer := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()
	fmt.Printf("FRP Console: http://%s\nFRP runs independently. Configuration saves are offline; installed systemd instances expose observed runtime evidence.\n", *listen)
	if e = httpServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Print(e)
	}
}
