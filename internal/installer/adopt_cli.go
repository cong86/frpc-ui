package installer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/cli"
	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/cong86/frpc-ui/internal/observe"
	"golang.org/x/term"
)

func AdoptionCLI(args []string) error {
	fs := flag.NewFlagSet("install "+args[0], flag.ContinueOnError)
	cli.Configure(fs)
	request := fs.String("request", "", "受保护的私有接入请求")
	out := fs.String("out", "", "私有接入预览文件")
	planFile := fs.String("plan", "", "受保护的接入计划")
	confirm := fs.String("confirm", "", "完整计划标识")
	if e := fs.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return errors.New(cli.Text(e.Error()))
	}
	if fs.NArg() != 0 {
		return errors.New("接入参数中包含多余参数")
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	printResult := func(j Journal, r AdoptionRequest) {
		b, _ := json.Marshal(j)
		fmt.Println(string(b))
		fmt.Println("管理页面：http://" + r.Listen + " （首次访问初始化管理员；远程访问使用 SSH 隧道）")
		fmt.Printf("定时采集: %s-collect.timer · 每 %d 秒\n", r.Name, r.Interval)
		fmt.Printf("停用新增管理服务: systemctl disable --now %s.service %s-collect.timer\n", r.Name, r.Name)
	}
	switch args[0] {
	case "adopt-wizard":
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("接入向导需要交互终端")
		}
		reader := bufio.NewReader(os.Stdin)
		r, e := adoptionWizard(reader, os.Stdout)
		if e != nil {
			return e
		}
		if *out == "" {
			*out = "/root/frp-console-adoption-plan.json"
		}
		p, e := NewAdoptionPlan(ctx, r, exe)
		if e != nil {
			return e
		}
		if e = WriteJSON(*out, p); e != nil {
			return e
		}
		printAdoptionPreview(p)
		fmt.Print("输入完整计划 ID 安装独立管理服务，留空取消: ")
		line, e := reader.ReadString('\n')
		if e != nil {
			return errors.New("未获得接入确认；未执行安装")
		}
		if strings.TrimSpace(line) == "" {
			fmt.Println("计划已保留，原服务及配置未修改。")
			return nil
		}
		j, e := ApplyAdoption(ctx, p, strings.TrimSpace(line), exe)
		if e != nil {
			return e
		}
		printResult(j, r)
		return nil
	case "adopt-plan":
		if *request == "" || *out == "" {
			return errors.New("adopt-plan 需要 --request 和 --out 参数")
		}
		if managed.TrustedFile(*request) != nil {
			return errors.New("接入请求必须受保护且归 root 所有")
		}
		var r AdoptionRequest
		if e = ReadJSON(*request, &r); e != nil {
			return e
		}
		p, e := NewAdoptionPlan(ctx, r, exe)
		if e != nil {
			return e
		}
		if e = WriteJSON(*out, p); e != nil {
			return e
		}
		printAdoptionPreview(p)
		return nil
	case "adopt-apply":
		if *planFile == "" || managed.TrustedFile(*planFile) != nil {
			return errors.New("接入计划必须受保护且归 root 所有")
		}
		var p AdoptionPlan
		if e = ReadJSON(*planFile, &p); e != nil {
			return e
		}
		j, e := ApplyAdoption(ctx, p, *confirm, exe)
		if e != nil {
			return e
		}
		printResult(j, p.Request)
		return nil
	default:
		return errors.New("请使用 adopt-wizard、adopt-plan 或 adopt-apply 命令")
	}
}
func printAdoptionPreview(p AdoptionPlan) {
	b, _ := json.MarshalIndent(struct {
		Plan  AdoptionPlan `json:"plan"`
		Notes []string     `json:"notes"`
	}{p, []string{"原 FRPS 和 Nginx 保持独立；不会下载 FRP、写入原配置、修改原权限、重载或重启原服务。", "仅安装选定的新管理目录、用户、网页服务及采集定时器。", "完成前验证初次采集和受限采集服务；接口、日志及业务的可用性分别呈现证据。", "安装失败时仅删除本次新增管理单元，保留新数据和用户供检查。"}}, "", "  ")
	fmt.Println(string(b))
}
func adoptionWizard(reader *bufio.Reader, out io.Writer) (AdoptionRequest, error) {
	r := AdoptionRequest{Profile: observe.Profile{Version: 1}}
	var inputError error
	ask := func(label, def string) string {
		if inputError != nil {
			return ""
		}
		fmt.Fprintf(out, "%s [%s]: ", label, def)
		v, e := reader.ReadString('\n')
		if e != nil {
			inputError = errors.New("接入输入已结束；未执行安装")
			return ""
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return def
		}
		return v
	}
	r.Name = ask("独立管理服务名称", "frp-console-observer")
	r.Root = ask("新的管理程序目录", "/opt/frp-console-observer")
	r.Listen = ask("管理页面回环地址", "127.0.0.1:18745")
	r.Interval, _ = strconv.Atoi(ask("采集间隔秒数（10..60）", "30"))
	r.Profile.Config = ask("已有 FRPS TOML 的宿主机绝对路径", "")
	askRuntime := func(label string) observe.Target {
		kind := choice(ask(label+"方式：1 原生服务 / 2 Docker 容器 / 3 未知", "1"), map[string]string{"1": "systemd", "原生服务": "systemd", "2": "docker", "容器": "docker", "3": "unknown", "未知": "unknown"})
		if kind == "unknown" {
			return observe.Target{}
		}
		name := "frps.service"
		if strings.HasPrefix(label, "Nginx") {
			name = "nginx.service"
		}
		if kind == "docker" {
			name = strings.TrimSuffix(name, ".service")
		}
		return observe.Target{Kind: kind, Name: ask(label+"单元或容器名称", name)}
	}
	r.Profile.Runtime = askRuntime("FRPS 运行")
	frpsLog := ask("FRPS 日志文件（空值使用已登记服务日志）", "")
	if frpsLog != "" {
		r.Profile.Logs = append(r.Profile.Logs, observe.LogSource{Kind: "file", Name: frpsLog, Format: "frps"})
	} else if r.Profile.Runtime.Kind != "" {
		r.Profile.Logs = append(r.Profile.Logs, observe.LogSource{Kind: r.Profile.Runtime.Kind, Name: r.Profile.Runtime.Name, Format: "frps"})
	}
	entry := ask("Nginx 宿主机配置入口或站点通配路径（留空不读取）", "")
	if entry != "" {
		context := choice(ask("Nginx 配置类型：1 完整主配置 / 2 HTTP 站点片段", "1"), map[string]string{"1": "main", "完整主配置": "main", "2": "http-fragments", "站点片段": "http-fragments"})
		if context == "main" {
			context = ""
		}
		n := &observe.NginxProfile{Context: context, Entry: entry, Prefix: ask("Nginx 运行环境配置前缀", "/etc/nginx")}
		host := filepath.Dir(entry)
		rootText := ask("允许读取的宿主机根目录（逗号分隔）", host)
		for _, root := range strings.Split(rootText, ",") {
			n.Roots = append(n.Roots, strings.TrimSpace(root))
		}
		inside := n.Prefix
		if context == "http-fragments" {
			inside = filepath.Join(n.Prefix, "conf.d")
		}
		inside = ask("该宿主机目录对应的 Nginx 内部路径", inside)
		n.Mounts = []observe.Mount{{Inside: inside, Host: host}}
		n.Runtime = askRuntime("Nginx 运行")
		r.Profile.Nginx = n
		for _, format := range []string{"nginx-access", "nginx-error"} {
			label := map[string]string{"nginx-access": "Nginx 访问", "nginx-error": "Nginx 错误"}[format]
			path := ask(label+"日志文件（空值使用已登记服务日志；运行方式未知则跳过）", "")
			if path != "" {
				r.Profile.Logs = append(r.Profile.Logs, observe.LogSource{Kind: "file", Name: path, Format: format})
			} else if n.Runtime.Kind != "" {
				r.Profile.Logs = append(r.Profile.Logs, observe.LogSource{Kind: n.Runtime.Kind, Name: n.Runtime.Name, Format: format})
			}
		}
	}
	if inputError != nil {
		return r, inputError
	}
	return r, checkAdoption(r)
}
