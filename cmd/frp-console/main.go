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

	"github.com/cong86/frpc-ui/internal/cli"
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
			if e == flag.ErrHelp {
				return
			}
			log.Fatal(e)
		}
		fmt.Println("只读采集快照已更新；原服务保持不变。")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if e := installer.CLI(os.Args[2:]); e != nil {
			log.Fatal(e)
		}
		return
	}
	listen := flag.String("listen", "127.0.0.1:18745", "本机管理地址，当前仅允许回环 IP")
	data := flag.String("data", "./data", "私有数据目录")
	demo := flag.Bool("demo", false, "创建隔离的本机 FRP 配置，不启动隧道")
	binaries := flag.String("frp-dir", "", "官方 frpc/frps 程序目录，用于离线校验")
	client := flag.String("frpc-config", "", "只读导入客户端 TOML 配置")
	service := flag.String("frps-config", "", "只读导入服务端 TOML 配置")
	manifestPath := flag.String("manifest", "", "root 所有的专属 systemd 安装清单")
	observedPath := flag.String("observed-snapshot", "", "root 所有的已有 FRPS/Nginx 脱敏采集快照")
	cli.Configure(flag.CommandLine)
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "用法：frp-console [参数]\n安装向导：frp-console install wizard\n只读接入：frp-console install adopt-wizard\n只读采集：frp-console observe --help")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *observedPath != "" {
		if *manifestPath != "" || *demo || *client != "" || *service != "" {
			log.Fatal("采集快照不能与安装清单、演示或导入配置同时使用")
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
			log.Fatal("安装清单模式不能与演示配置或导入配置同时使用")
		}
		if filepath.Clean(*data) != filepath.Join(installed.Root, "data") {
			log.Fatal("数据目录必须与安装清单一致")
		}
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("当前阶段必须监听明确的回环 IP 地址")
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
		log.Fatal("数据目录已被其他管理程序占用")
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
	fmt.Printf("FRP 控制台：http://%s\n官方 FRP 独立运行。保存配置不会自动应用；已登记的 systemd 实例提供运行状态与分层验证。\n", *listen)
	if e = httpServer.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Print(e)
	}
}
