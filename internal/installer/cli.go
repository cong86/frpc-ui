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
)

func CLI(args []string) error {
	if len(args) == 0 {
		return errors.New("use install wizard, install plan, or install apply")
	}
	fs := flag.NewFlagSet("install "+args[0], flag.ContinueOnError)
	request := fs.String("request", "", "private installation request JSON")
	output := fs.String("out", "", "private preview plan JSON")
	planFile := fs.String("plan", "", "private installation plan JSON")
	confirmation := fs.String("confirm", "", "exact plan ID to confirm")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
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
			return errors.New("wizard requires a terminal; use a private request JSON for automation")
		}
		r, e := wizard()
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
		fmt.Println("UI:", r.Listen, "（管理员由首次 UI 访问初始化）")
		return nil
	case "plan":
		if *request == "" || *output == "" {
			return errors.New("plan requires --request and --out")
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
			return errors.New("apply requires --plan and --confirm")
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
		return errors.New("unsupported install command")
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
	}{p.ID, p.Arch, Version, Checksums[p.Arch], p.Files, []string{"Fresh installation only. Existing deployment is not taken over.", "FRP and Console independent systemd services. Console runs without root.", "Process active does not prove authentication or business access.", "Plan contains encrypted-at-rest credentials only in Console backups; the private installer input/plan contains raw config and must remain 0600."}}, "", "  ")
	fmt.Println(string(b))
}
func wizard() (Request, error) {
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
	r.Listen = ask("UI 回环地址", "127.0.0.1:18745")
	role := ask("角色 frpc / frps / both", "frpc")
	if role != "frpc" && role != "frps" && role != "both" {
		return r, errors.New("invalid role")
	}
	server := ask("FRPS 地址（客户端连接地址）", "127.0.0.1")
	bind := ask("FRPS 监听地址", "0.0.0.0")
	port, e := strconv.Atoi(ask("FRP 连接端口", "17000"))
	if e != nil || port < 1024 || port > 65535 {
		return r, errors.New("invalid port")
	}
	r.Archive = ask("已校验官方归档路径（留空由安装器下载）", "")
	fmt.Print("两端一致的 Token（不回显）: ")
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
