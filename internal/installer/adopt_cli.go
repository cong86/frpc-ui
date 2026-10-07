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

	"github.com/cong86/frpc-ui/internal/managed"
	"github.com/cong86/frpc-ui/internal/observe"
	"golang.org/x/term"
)

func AdoptionCLI(args []string) error {
	fs := flag.NewFlagSet("install "+args[0], flag.ContinueOnError)
	request := fs.String("request", "", "protected private adoption request")
	out := fs.String("out", "", "private adoption preview")
	planFile := fs.String("plan", "", "protected adoption plan")
	confirm := fs.String("confirm", "", "exact plan ID")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected adoption arguments")
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
		fmt.Println("UI: http://" + r.Listen + " （首次访问初始化管理员；远程访问使用 SSH 隧道）")
		fmt.Printf("定时采集: %s-collect.timer · 每 %d 秒\n", r.Name, r.Interval)
		fmt.Printf("停用新增管理服务: systemctl disable --now %s.service %s-collect.timer\n", r.Name, r.Name)
	}
	switch args[0] {
	case "adopt-wizard":
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("adoption wizard requires a terminal")
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
			return errors.New("adoption confirmation unavailable; no installation performed")
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
			return errors.New("adopt-plan requires --request and --out")
		}
		if managed.TrustedFile(*request) != nil {
			return errors.New("adoption request must be protected and root-owned")
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
			return errors.New("adoption plan must be protected and root-owned")
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
		return errors.New("use adopt-wizard, adopt-plan or adopt-apply")
	}
}
func printAdoptionPreview(p AdoptionPlan) {
	b, _ := json.MarshalIndent(struct {
		Plan  AdoptionPlan `json:"plan"`
		Notes []string     `json:"notes"`
	}{p, []string{"Existing FRPS and Nginx stay independent; no FRP download, config write, permission change, reload or restart.", "Only the selected new Console directory, user, web service and collection timer are installed.", "Initial collection and sandboxed collector are checked before completion; API/log availability and business access are separate evidence.", "Failure removes only newly created management units, retaining private data/account for inspection."}}, "", "  ")
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
			inputError = errors.New("adoption input ended; no installation performed")
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
	r.Listen = ask("UI 回环地址", "127.0.0.1:18745")
	r.Interval, _ = strconv.Atoi(ask("采集间隔秒数（10..60）", "30"))
	r.Profile.Config = ask("已有 FRPS TOML 的宿主机绝对路径", "")
	askRuntime := func(label string) observe.Target {
		kind := ask(label+"类型 docker / systemd / unknown", "systemd")
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
	entry := ask("Nginx 宿主机入口路径或站点 glob（留空不读取）", "")
	if entry != "" {
		context := ask("Nginx 配置类型 main / http-fragments", "main")
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
			path := ask(format+" 宿主机日志文件（空值使用已登记服务日志；unknown 则跳过）", "")
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
