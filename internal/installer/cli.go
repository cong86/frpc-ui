package installer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/term"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/cli"
)

func CLI(args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "adopt-") {
		return AdoptionCLI(args)
	}
	if len(args) == 0 {
		return errors.New("请使用 install wizard、install plan 或 install apply 命令")
	}
	fs := flag.NewFlagSet("install "+args[0], flag.ContinueOnError)
	cli.Configure(fs)
	request := fs.String("request", "", "私有安装请求 JSON")
	output := fs.String("out", "", "私有预览计划 JSON")
	planFile := fs.String("plan", "", "私有安装计划 JSON")
	confirmation := fs.String("confirm", "", "用于确认的完整计划标识")
	archive := fs.String("archive", "", "已校验的官方 FRP 归档，作为向导默认路径")
	if e := fs.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return errors.New(cli.Text(e.Error()))
	}
	if fs.NArg() != 0 {
		return errors.New("参数中包含多余参数")
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch args[0] {
	case "wizard":
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("向导需要交互终端；自动化请使用私有请求 JSON")
		}
		r, e := wizard(*archive)
		if e != nil {
			return e
		}
		if *output == "" {
			*output = "/root/frp-console-install-plan.json"
		}
		p, e := NewPlan(ctx, r, exe)
		if e != nil {
			return e
		}
		if e = WriteJSON(*output, p); e != nil {
			return e
		}
		printPreview(p)
		fmt.Print("输入完整计划 ID 执行安装，留空退出: ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			fmt.Println("计划已保留，未安装。")
			return nil
		}
		j, e := Apply(ctx, p, strings.TrimSpace(line), exe)
		if e != nil {
			return e
		}
		b, _ := json.Marshal(j)
		fmt.Println(string(b))
		fmt.Println("管理页面：http://"+r.Listen, "（首次访问设置管理员；远程访问使用 SSH 隧道）")
		return nil
	case "plan":
		if *request == "" || *output == "" {
			return errors.New("plan 需要 --request 和 --out 参数")
		}
		var r Request
		if e = ReadJSON(*request, &r); e != nil {
			return e
		}
		p, e := NewPlan(ctx, r, exe)
		if e != nil {
			return e
		}
		if e = WriteJSON(*output, p); e != nil {
			return e
		}
		printPreview(p)
		return nil
	case "apply":
		if *planFile == "" {
			return errors.New("apply 需要 --plan 和 --confirm 参数")
		}
		var p Plan
		if e = ReadJSON(*planFile, &p); e != nil {
			return e
		}
		j, e := Apply(ctx, p, *confirmation, exe)
		if e != nil {
			return e
		}
		b, _ := json.Marshal(j)
		fmt.Println(string(b))
		return nil
	default:
		return errors.New("不支持此安装命令")
	}
}
func printPreview(p Plan) {
	b, _ := json.MarshalIndent(struct {
		ID            string   `json:"id"`
		Arch          string   `json:"arch"`
		FRPVersion    string   `json:"frpVersion"`
		ArchiveSHA256 string   `json:"archiveSHA256"`
		Files         any      `json:"files"`
		Notes         []string `json:"notes"`
	}{p.ID, p.Arch, Version, Checksums[p.Arch], p.Files, []string{"仅用于新安装，不接管已有部署。", "FRP 与管理程序使用独立的 systemd 服务；管理页面以非 root 用户运行。", "进程正在运行不能证明认证连接或业务访问成功。", "管理程序备份中的凭据会加密保存；安装器的私有输入和计划包含原始配置，权限必须保持 0600。"}}, "", "  ")
	fmt.Println(string(b))
}
func wizard(defaultArchive string) (Request, error) {
	r := Request{Configs: map[string]string{}}
	reader := bufio.NewReader(os.Stdin)
	ask := func(label, def string) string {
		fmt.Printf("%s [%s]: ", label, def)
		v, _ := reader.ReadString('\n')
		v = strings.TrimSpace(v)
		if v == "" {
			return def
		}
		return v
	}
	r.Name = ask("独立安装名称", "frp-console")
	r.Root = ask("新安装目录", "/opt/frp-console")
	r.Listen = ask("管理页面回环地址", "127.0.0.1:18745")
	role := choice(ask("安装角色：1 客户端 / 2 服务端 / 3 两者", "1"), map[string]string{"1": "frpc", "客户端": "frpc", "2": "frps", "服务端": "frps", "3": "both", "两者": "both"})
	if role != "frpc" && role != "frps" && role != "both" {
		return r, errors.New("安装角色无效")
	}
	server := ask("FRPS 地址（客户端连接地址）", "127.0.0.1")
	bind := ask("FRPS 监听地址", "0.0.0.0")
	port, e := strconv.Atoi(ask("FRP 连接端口", "17000"))
	if e != nil || port < 1024 || port > 65535 {
		return r, errors.New("端口无效")
	}
	r.Archive = ask("已校验官方归档路径（留空由安装器下载）", defaultArchive)
	fmt.Print("两端一致的认证令牌（Token，不回显）：")
	token, e := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if e != nil {
		return r, e
	}
	secret := make([]byte, 24)
	if _, e = rand.Read(secret); e != nil {
		return r, e
	}
	apiPassword := hex.EncodeToString(secret)
	auth := fmt.Sprintf("\n[auth]\nmethod = \"token\"\ntoken = %q\n", string(token))
	if role == "frpc" || role == "both" {
		r.Configs["frpc"] = fmt.Sprintf("serverAddr = %q\nserverPort = %d\nwebServer.addr = \"127.0.0.1\"\nwebServer.port = 17401\nwebServer.user = \"console-api\"\nwebServer.password = %q\n", server, port, apiPassword) + auth
	}
	if role == "frps" || role == "both" {
		r.Configs["frps"] = fmt.Sprintf("bindAddr = %q\nbindPort = %d\nwebServer.addr = \"127.0.0.1\"\nwebServer.port = 17400\nwebServer.user = \"console-api\"\nwebServer.password = %q\n", bind, port, apiPassword) + auth
	}
	return r, nil
}

// Preserve existing CLI inputs while offering numbered and Chinese choices.
func choice(value string, aliases map[string]string) string {
	if canonical, ok := aliases[value]; ok {
		return canonical
	}
	return value
}
