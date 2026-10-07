package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const MaxSize = 1 << 20

var ErrConflict = errors.New("配置已被修改；请刷新并重新生成计划")
var keyLine = regexp.MustCompile(`^([ \t]*[A-Za-z0-9_."'-]+[ \t]*=[ \t]*)(.*)$`)
var header = regexp.MustCompile(`^[ \t]*\[\[proxies\]\][ \t]*(?:#.*)?$`)
var sensitive = regexp.MustCompile(`(?i)(token|password|passwd|secret|authorization|proxyurl|groupkey|privatekey)`)
var marker = regexp.MustCompile(`__FRP_REDACTED_[0-9]+__`)

type Document struct {
	Raw    string
	Values map[string]any
	Role   string
}
type Snapshot struct {
	Revision string         `json:"revision"`
	Text     string         `json:"text"`
	Values   map[string]any `json:"values"`
	Editable bool           `json:"editable"`
	Reason   string         `json:"reason,omitempty"`
}

func Revision(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }

// RedactText also protects runtime labels returned by official management APIs.
func (d *Document) RedactText(text string) string {
	values := secrets(d.Values)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if value != "" {
			text = strings.ReplaceAll(text, value, "***")
		}
	}
	return text
}
func Parse(raw, role string) (*Document, error) {
	if len(raw) > MaxSize {
		return nil, errors.New("配置超出 1 MiB 大小限制")
	}
	if strings.ContainsRune(raw, 0) {
		return nil, errors.New("配置不能包含空字符")
	}
	v := map[string]any{}
	if err := toml.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errors.New("TOML 配置无效；请检查语法，避免暴露凭据")
	}
	if role != "frpc" && role != "frps" {
		return nil, errors.New("实例角色无效")
	}
	if p, ok := v["proxies"]; ok {
		if role != "frpc" {
			return nil, errors.New("服务端配置不能包含客户端代理定义")
		}
		rows, ok := p.([]any)
		if !ok {
			return nil, errors.New("代理定义必须是数组表格")
		}
		seen := map[string]bool{}
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				return nil, errors.New("代理定义无效")
			}
			n, _ := m["name"].(string)
			t, _ := m["type"].(string)
			if n == "" || seen[n] {
				return nil, errors.New("代理名称不能为空或重复")
			}
			seen[n] = true
			switch t {
			case "tcp", "udp", "http", "https":
			default: /* retained; unsupported forms remain visible */
			}
		}
	}
	return &Document{Raw: raw, Values: v, Role: role}, nil
}
func (d *Document) Editability() string {
	if strings.Contains(d.Raw, `"""`) || strings.Contains(d.Raw, `'''`) {
		return "当前阶段多行字符串仅支持只读查看"
	}
	if strings.Contains(d.Raw, "{{") {
		return "环境模板需要结合依赖关系编辑，当前仅支持只读查看"
	}
	if _, ok := d.Values["includes"]; ok {
		return "包含外部配置的部署需要多文件事务，当前仅支持只读查看"
	}
	if _, ok := d.Values["start"]; ok {
		return "已有 start 过滤配置需要按版本迁移，当前仅支持只读查看"
	}
	masked, _ := d.masked()
	for _, secret := range secrets(d.Values) {
		quoted := strconv.Quote(secret)
		if secret != "" && (strings.Contains(masked, secret) || strings.Contains(masked, quoted[1:len(quoted)-1])) {
			return "存在简单赋值之外的凭据写法，当前仅支持只读查看"
		}
	}
	return ""
}
func secrets(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if sensitive.MatchString(k) {
				out = append(out, secretStrings(val)...)
			}
			out = append(out, secrets(val)...)
		}
	case []any:
		for _, val := range x {
			out = append(out, secrets(val)...)
		}
	}
	return out
}
func secretStrings(v any) []string {
	out := []string{}
	switch x := v.(type) {
	case string:
		return []string{x}
	case map[string]any:
		for _, val := range x {
			out = append(out, secretStrings(val)...)
		}
	case []any:
		for _, val := range x {
			out = append(out, secretStrings(val)...)
		}
	}
	return out
}
func commentSuffix(value string) string {
	quote := rune(0)
	escaped := false
	for i, ch := range value {
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '#' {
			return value[i:]
		}
	}
	return ""
}
func (d *Document) masked() (string, map[string]string) {
	// All managed candidates are single-line TOML. Complex imported documents
	// get a canonical redacted projection instead of a potentially leaking raw view.
	masks := map[string]string{}
	i := 0
	lines := strings.Split(d.Raw, "\n")
	for j, l := range lines {
		m := keyLine.FindStringSubmatch(l)
		if m != nil && sensitive.MatchString(strings.SplitN(m[1], "=", 2)[0]) {
			tag := fmt.Sprintf("__FRP_REDACTED_%d__", i)
			i++
			masks[tag] = m[2]
			lines[j] = m[1] + `"` + tag + `"`
		}
	}
	return strings.Join(lines, "\n"), masks
}
func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if sensitive.MatchString(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redactValue(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactValue(val)
		}
		return out
	default:
		return v
	}
}
func scrubValues(v any, secretValues []string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = scrubValues(val, secretValues)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = scrubValues(val, secretValues)
		}
		return out
	case string:
		for _, secret := range secretValues {
			if secret != "" {
				x = strings.ReplaceAll(x, secret, "[REDACTED]")
			}
		}
		return x
	default:
		return v
	}
}
func (d *Document) Snapshot() Snapshot {
	reason := d.Editability()
	text, _ := d.masked()
	values := scrubValues(redactValue(d.Values), secrets(d.Values)).(map[string]any)
	if reason != "" {
		b, err := toml.Marshal(values)
		if err == nil {
			text = string(b)
		} else {
			text = "# 预览暂不可用"
		}
	}
	return Snapshot{Revision: Revision(d.Raw), Text: text, Values: values, Editable: reason == "", Reason: reason}
}
func (d *Document) RestoreMasks(candidate string) (string, error) {
	if d.Editability() != "" {
		return "", errors.New(d.Editability())
	}
	_, masks := d.masked()
	counts := map[string]int{}
	for _, line := range strings.Split(candidate, "\n") {
		for _, tag := range marker.FindAllString(line, -1) {
			match := keyLine.FindStringSubmatch(line)
			if match == nil || !sensitive.MatchString(strings.SplitN(match[1], "=", 2)[0]) || strings.TrimSpace(match[2]) != `"`+tag+`"` {
				return "", errors.New("凭据占位符必须独立作为敏感字段的值")
			}
			counts[tag]++
			if counts[tag] > 1 {
				return "", errors.New("凭据占位符不能重复")
			}
		}
	}
	for _, tag := range marker.FindAllString(candidate, -1) {
		if _, ok := masks[tag]; !ok {
			return "", errors.New("未知凭据占位符")
		}
	}
	for tag, value := range masks {
		quoted := `"` + tag + `"`
		if strings.Contains(candidate, tag) && !strings.Contains(candidate, quoted) {
			return "", errors.New("凭据占位符不能嵌入其他值")
		}
		candidate = strings.ReplaceAll(candidate, quoted, value)
	}
	return candidate, nil
}
func (d *Document) PatchProxy(name string, fields map[string]any, remove bool) (string, error) {
	if d.Role != "frpc" || d.Editability() != "" {
		return "", errors.New("此实例不支持代理表单")
	}
	allowed := map[string]bool{"name": true, "type": true, "localIP": true, "localPort": true, "remotePort": true, "customDomains": true, "locations": true, "enabled": true}
	for k := range fields {
		if !allowed[k] {
			return "", errors.New("不支持此代理字段")
		}
	}
	for _, k := range []string{"localPort", "remotePort"} {
		if v, ok := fields[k]; ok {
			switch n := v.(type) {
			case float64:
				if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < 1 || n > 65535 {
					return "", errors.New("代理端口必须为 1～65535 的整数")
				}
				fields[k] = int64(n)
			case int, int64:
			default:
				return "", errors.New("代理端口必须为整数")
			}
		}
	}
	lines := strings.Split(d.Raw, "\n")
	starts := []int{}
	for i, l := range lines {
		if header.MatchString(l) {
			starts = append(starts, i)
		}
	}
	starts = append(starts, len(lines))
	begin, end := -1, -1
	for i := 0; i < len(starts)-1; i++ {
		block := strings.Join(lines[starts[i]:starts[i+1]], "\n")
		var parsed map[string]any
		if toml.Unmarshal([]byte(block), &parsed) != nil {
			continue
		}
		rows, _ := parsed["proxies"].([]any)
		if len(rows) > 0 {
			m, _ := rows[0].(map[string]any)
			if m["name"] == name {
				begin, end = starts[i], starts[i+1]
				for j := begin + 1; j < end; j++ {
					l := strings.TrimSpace(lines[j])
					if strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "[proxies.") && !strings.HasPrefix(l, "[[proxies.") {
						end = j
						break
					}
				}
				break
			}
		}
	}
	if remove {
		if begin < 0 {
			return "", errors.New("代理已不存在")
		}
		return strings.Join(append(lines[:begin], lines[end:]...), "\n"), nil
	}
	if begin < 0 {
		if name != "" {
			return "", errors.New("代理已不存在")
		}
		b, e := toml.Marshal(map[string]any{"proxies": []any{fields}})
		if e != nil {
			return "", e
		}
		return strings.TrimRight(d.Raw, "\n") + "\n\n" + string(b), nil
	}
	// Nested proxy tables can contain identical keys. Do not guess field ownership.
	for _, l := range lines[begin+1 : end] {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			return "", errors.New("嵌套代理表格需要使用高级编辑")
		}
	}
	replacement := append([]string{}, lines[begin:end]...)
	remaining := map[string]any{}
	for k, v := range fields {
		remaining[k] = v
	}
	for i, l := range replacement {
		m := keyLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		k := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), "="))
		k = strings.Trim(k, `"'`)
		if v, ok := remaining[k]; ok {
			b, e := toml.Marshal(map[string]any{k: v})
			if e != nil {
				return "", e
			}
			replacement[i] = strings.TrimRight(string(b), "\n")
			if comment := commentSuffix(m[2]); comment != "" {
				replacement[i] += " " + comment
			}
			delete(remaining, k)
		}
	}
	keys := []string{}
	for k := range remaining {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b, e := toml.Marshal(map[string]any{k: remaining[k]})
		if e != nil {
			return "", e
		}
		replacement = append(replacement, strings.TrimRight(string(b), "\n"))
	}
	result := append([]string{}, lines[:begin]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return strings.Join(result, "\n"), nil
}
