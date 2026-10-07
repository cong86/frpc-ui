package observe

import (
	"context"
	"os"
	"strings"
	"time"
)

type RuntimeDiscovery struct {
	Candidates []Target
	Issues     []string
}

// Only check the requested default name, never list arbitrary containers,
// inspect their environment, or infer that a registered service is running.
func DetectExistingRuntime(ctx context.Context, name string) RuntimeDiscovery {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return detectExistingRuntime(ctx, name, command)
}

func detectExistingRuntime(ctx context.Context, name string, run Runner) RuntimeDiscovery {
	result := RuntimeDiscovery{}
	if name != "frps" && name != "nginx" {
		result.Issues = append(result.Issues, "自动识别仅检查默认名称；请手动选择运行方式和目标。")
		return result
	}
	raw, e := run(ctx, "docker", "container", "ls", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if e == nil && strings.TrimSpace(string(raw)) == name {
		result.Candidates = append(result.Candidates, Target{Kind: "docker", Name: name})
	} else if e != nil && !os.IsNotExist(e) {
		result.Issues = append(result.Issues, "无法确认默认 Docker 容器是否存在，请检查 Docker 权限或手动选择。")
	}
	raw, e = run(ctx, "systemctl", "show", name+".service", "--property=LoadState", "--value")
	state := strings.TrimSpace(string(raw))
	if e == nil && state == "loaded" {
		result.Candidates = append(result.Candidates, Target{Kind: "systemd", Name: name + ".service"})
	} else if state != "not-found" && !os.IsNotExist(e) {
		result.Issues = append(result.Issues, "无法确认默认原生服务是否存在，请手动选择运行方式。")
	}
	return result
}
