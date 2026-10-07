package observe

import (
	"context"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type LogEvent struct {
	Time   string `json:"time"`
	Level  string `json:"level"`
	Event  string `json:"event"`
	Method string `json:"method,omitempty"`
	Status int    `json:"status,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}
type LogWindow struct {
	Format    string     `json:"format"`
	Source    string     `json:"source"`
	Available bool       `json:"available"`
	Events    []LogEvent `json:"events"`
	Parsed    int        `json:"parsed"`
	Skipped   int        `json:"skipped"`
	Bytes     int64      `json:"bytes"`
	Note      string     `json:"note"`
}

var accessLine = regexp.MustCompile(`\[([0-9]{2}/[A-Za-z]{3}/[0-9]{4}:[0-9]{2}:[0-9]{2}:[0-9]{2} [+-][0-9]{4})\]\s+"([A-Z]{1,16}) [^"]*"\s+([1-5][0-9]{2})\s+([0-9]+|-)`)
var logTime = regexp.MustCompile(`(?:[0-9]{4}[-/][0-9]{2}[-/][0-9]{2}[ T][0-9:.+-]{8,24}|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.+-]+Z)`)

func ParseLogs(raw, format string) LogWindow {
	w := LogWindow{Format: format, Available: true, Events: []LogEvent{}, Note: "仅采集有限的近期日志；不返回原始行、地址、请求网址或头部。字节数表示窗口内响应体字节合计，不是实时速率。"}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) > 100 {
		lines = lines[len(lines)-100:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" {
			continue
		}
		v := LogEvent{}
		if format == "nginx-access" {
			m := accessLine.FindStringSubmatch(line)
			if m == nil {
				w.Skipped++
				continue
			}
			v.Time = m[1]
			v.Method = m[2]
			v.Status, _ = strconv.Atoi(m[3])
			v.Bytes, _ = strconv.ParseInt(m[4], 10, 64)
			v.Level = "access"
			v.Event = "HTTP 访问"
			w.Bytes += v.Bytes
		} else {
			v.Time = logTime.FindString(line)
			v.Level = "info"
			lower := strings.ToLower(line)
			for _, level := range []string{"error", "warn", "fatal", "debug", "info"} {
				if strings.Contains(lower, "["+level+"]") || strings.Contains(lower, "["+string(level[0])+"]") {
					v.Level = level
					break
				}
			}
			switch {
			case strings.Contains(lower, "client login") || strings.Contains(lower, "login to server success"):
				v.Event = "客户端认证事件"
			case strings.Contains(lower, "new proxy") || strings.Contains(lower, "start proxy") || strings.Contains(lower, "proxy added"):
				v.Event = "代理注册事件"
			case strings.Contains(lower, "proxy removed") || strings.Contains(lower, "proxy closed"):
				v.Event = "代理移除事件"
			case strings.Contains(lower, "upstream timed out"):
				v.Event = "上游响应超时"
			case strings.Contains(lower, "connect() failed"):
				v.Event = "上游连接失败"
			case strings.Contains(lower, "ssl") || strings.Contains(lower, "certificate"):
				v.Event = "TLS 事件"
			case v.Level == "error" || v.Level == "fatal":
				v.Event = "错误事件（详细内容已隐藏）"
			default:
				v.Event = "服务日志事件（详细内容已隐藏）"
			}
		}
		w.Events = append(w.Events, v)
		w.Parsed++
	}
	return w
}
func ReadLogs(ctx context.Context, s LogSource, run Runner) LogWindow {
	w := LogWindow{Format: s.Format, Source: s.Kind, Events: []LogEvent{}, Note: "日志来源不可读取；不返回原始错误信息"}
	var b []byte
	var e error
	switch s.Kind {
	case "file":
		// Do not follow links or read devices, pipes, sockets or giant full files.
		st, err := os.Lstat(s.Name)
		if err != nil || !st.Mode().IsRegular() {
			return w
		}
		f, err := os.Open(s.Name)
		if err != nil {
			return w
		}
		defer f.Close()
		start := st.Size() - (128 << 10)
		if start < 0 {
			start = 0
		}
		if _, e = f.Seek(start, io.SeekStart); e != nil {
			return w
		}
		b, e = io.ReadAll(io.LimitReader(f, 128<<10))
		if start > 0 {
			if n := strings.IndexByte(string(b), '\n'); n >= 0 {
				b = b[n+1:]
			}
		}
	case "docker":
		b, e = run(ctx, "docker", "logs", "--timestamps", "--tail", "100", s.Name)
	case "systemd":
		b, e = run(ctx, "journalctl", "--unit", s.Name, "--no-pager", "--lines", "100", "--output", "short-iso")
	default:
		return w
	}
	if e != nil {
		return w
	}
	w = ParseLogs(string(b), s.Format)
	w.Source = s.Kind
	return w
}
