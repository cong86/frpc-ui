package observe

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/managed"
)

type Route struct {
	Location  string   `json:"location"`
	Upstream  string   `json:"upstream"`
	Targets   []string `json:"targets"`
	WebSocket bool     `json:"websocket"`
	FRPHint   string   `json:"frpHint"`
	Redirect  string   `json:"redirect,omitempty"`
}
type Site struct {
	File         string   `json:"file"`
	Names        []string `json:"names"`
	Listen       []string `json:"listen"`
	Certificates []string `json:"certificates"`
	TLS          bool     `json:"tls"`
	Routes       []Route  `json:"routes"`
}
type NginxState struct {
	Status       string          `json:"status"`
	Files        int             `json:"files"`
	Sites        []Site          `json:"sites"`
	Issues       []string        `json:"issues"`
	Process      managed.Process `json:"process"`
	ProcessCheck managed.Check   `json:"processCheck"`
	Validated    bool            `json:"validated"`
	Loaded       bool            `json:"loaded"`
}
type directive struct {
	name     string
	args     []string
	children []directive
	file     string
}
type token struct {
	text   string
	symbol bool
}

func lex(raw string) ([]token, error) {
	var out []token
	for i := 0; i < len(raw); {
		if strings.ContainsRune(" \t\r\n", rune(raw[i])) {
			i++
			continue
		}
		if raw[i] == '#' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			continue
		}
		if strings.ContainsRune("{};", rune(raw[i])) {
			out = append(out, token{string(raw[i]), true})
			i++
			continue
		}
		var b strings.Builder
		quote := byte(0)
		for i < len(raw) {
			c := raw[i]
			if quote != 0 {
				if c == quote {
					quote = 0
					i++
					continue
				}
				if c == '\\' && i+1 < len(raw) {
					if raw[i+1] != quote && raw[i+1] != '\\' {
						b.WriteByte('\\')
					}
					i++
					b.WriteByte(raw[i])
					i++
					continue
				}
				b.WriteByte(c)
				i++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				i++
				continue
			}
			if c == '\\' && i+1 < len(raw) {
				if !strings.ContainsRune("\\\"' \t\r\n{};#", rune(raw[i+1])) {
					b.WriteByte('\\')
				}
				i++
				b.WriteByte(raw[i])
				i++
				continue
			}
			if c == '$' && i+1 < len(raw) && raw[i+1] == '{' {
				end := strings.IndexByte(raw[i+2:], '}')
				if end < 0 {
					return nil, errors.New("unsupported variable")
				}
				end += i + 3
				b.WriteString(raw[i:end])
				i = end
				continue
			}
			if strings.ContainsRune(" \t\r\n{};#", rune(c)) {
				break
			}
			b.WriteByte(c)
			i++
		}
		if quote != 0 {
			return nil, errors.New("unterminated quote")
		}
		out = append(out, token{b.String(), false})
		if len(out) > 100000 {
			return nil, errors.New("too many directives")
		}
	}
	return out, nil
}
func parse(raw, file string) ([]directive, error) {
	t, e := lex(raw)
	if e != nil {
		return nil, e
	}
	i := 0
	var block func(bool, int) ([]directive, error)
	block = func(nested bool, depth int) ([]directive, error) {
		if depth > 32 {
			return nil, errors.New("configuration nesting limit")
		}
		var result []directive
		for i < len(t) {
			if t[i].symbol && t[i].text == "}" {
				if !nested {
					return nil, errors.New("unexpected closing block")
				}
				i++
				return result, nil
			}
			if t[i].symbol {
				return nil, errors.New("missing directive name")
			}
			d := directive{name: t[i].text, file: file}
			i++
			for i < len(t) && !t[i].symbol {
				d.args = append(d.args, t[i].text)
				i++
			}
			if i == len(t) {
				return nil, errors.New("incomplete directive")
			}
			s := t[i].text
			i++
			if s == "{" {
				d.children, e = block(true, depth+1)
				if e != nil {
					return nil, e
				}
			} else if s != ";" {
				return nil, errors.New("invalid directive termination")
			}
			result = append(result, d)
		}
		if nested {
			return nil, errors.New("unclosed block")
		}
		return result, nil
	}
	return block(false, 0)
}

type nginxReader struct {
	profile NginxProfile
	files   map[string]string
	bytes   int
	issues  []string
	trust   func(string) error
}

func within(path, root string) bool {
	r, e := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func (r *nginxReader) resolve(path string) (string, error) {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
		path = filepath.Join(r.profile.Prefix, path)
	}
	best := -1
	host := ""
	for _, m := range r.profile.Mounts {
		if rel, ok := virtualRelative(path, m.Inside); ok && len(m.Inside) > best {
			host = filepath.Join(m.Host, rel)
			best = len(m.Inside)
		}
	}
	if best >= 0 {
		path = host
	}
	for _, root := range r.profile.Roots {
		if within(path, root) {
			return filepath.Clean(path), nil
		}
	}
	return "", errors.New("include outside registered roots")
}

func virtualRelative(value, root string) (string, bool) {
	v, r := path.Clean(filepath.ToSlash(value)), path.Clean(filepath.ToSlash(root))
	if v == r {
		return "", true
	}
	if strings.HasPrefix(v, r+"/") {
		return strings.TrimPrefix(v, r+"/"), true
	}
	return "", false
}
func (r *nginxReader) read(path string, stack map[string]bool, depth int) ([]directive, error) {
	if depth > 12 || len(r.files) >= 128 {
		return nil, errors.New("include limit exceeded")
	}
	path, e := r.resolve(path)
	if e != nil {
		return nil, e
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, errors.New("include unavailable")
	}
	allowed := false
	for _, root := range r.profile.Roots {
		if within(resolved, root) {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("symlink outside registered roots")
	}
	if stack[resolved] {
		return nil, errors.New("include cycle")
	}
	// Configuration reads are administrator-controlled; log and key paths are not opened.
	if r.trust(resolved) != nil {
		return nil, errors.New("include is not a protected root-owned file")
	}
	st, e := os.Stat(resolved)
	if e != nil || st.Size() > config.MaxSize || r.bytes+int(st.Size()) > 4<<20 {
		return nil, errors.New("configuration size limit")
	}
	b, e := readConfigBytes(resolved)
	if e != nil {
		return nil, errors.New("include unreadable")
	}
	r.files[resolved] = config.Revision(string(b))
	r.bytes += len(b)
	stack[resolved] = true
	defer delete(stack, resolved)
	nodes, e := parse(string(b), resolved)
	if e != nil {
		return nil, e
	}
	return r.expand(nodes, stack, depth)
}
func (r *nginxReader) expand(nodes []directive, stack map[string]bool, depth int) ([]directive, error) {
	var out []directive
	for _, d := range nodes {
		if d.name == "include" {
			if len(d.args) != 1 || strings.Contains(d.args[0], "$") {
				r.issues = append(r.issues, "Unsupported include expression")
				continue
			}
			p, e := r.resolve(d.args[0])
			if e != nil {
				r.issues = append(r.issues, "Include outside registered roots")
				continue
			}
			matches, e := filepath.Glob(p)
			if e != nil || len(matches) > 128 {
				r.issues = append(r.issues, "Invalid or oversized include pattern")
				continue
			}
			if len(matches) == 0 && !strings.ContainsAny(p, "*?[") {
				r.issues = append(r.issues, "Required include unavailable")
			}
			for _, file := range matches {
				children, e := r.read(file, stack, depth+1)
				if e != nil {
					r.issues = append(r.issues, "Include unreadable, unsupported or outside scope")
					continue
				}
				out = append(out, children...)
			}
			continue
		}
		if d.children != nil {
			children, e := r.expand(d.children, stack, depth+1)
			if e != nil {
				return nil, e
			}
			d.children = children
		}
		out = append(out, d)
	}
	return out, nil
}
func args(nodes []directive, name string) []string {
	var out []string
	for _, n := range nodes {
		if n.name == name {
			out = append(out, n.args...)
		}
	}
	return out
}
func display(d *config.Document, s string) string {
	if len(s) > 512 {
		s = s[:512]
	}
	s = d.RedactText(s)
	if u, e := url.Parse(s); e == nil && u.Scheme != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	return s
}
func safeArgs(d *config.Document, v []string) []string {
	out := []string{}
	for _, x := range v {
		out = append(out, display(d, x))
	}
	return out
}
func readConfigBytes(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, config.MaxSize+1))
	if e != nil || len(b) > config.MaxSize {
		return nil, errors.New("configuration read limit exceeded")
	}
	return b, nil
}
func ReadNginx(p NginxProfile, d *config.Document) NginxState {
	return readNginx(p, d, managed.TrustedFile)
}
func readNginx(p NginxProfile, d *config.Document, trust func(string) error) NginxState {
	s := NginxState{Status: "unavailable", Sites: []Site{}, Issues: []string{}}
	r := nginxReader{profile: p, files: map[string]string{}, trust: trust}
	var nodes []directive
	var e error
	if p.Context == "http-fragments" {
		pattern, err := r.resolve(p.Entry)
		if err != nil {
			e = err
		} else {
			files, err := filepath.Glob(pattern)
			if err != nil || len(files) == 0 || len(files) > 128 {
				e = errors.New("fragment selection unavailable")
			} else {
				var fragments []directive
				for _, file := range files {
					v, err := r.read(file, map[string]bool{}, 0)
					if err != nil {
						r.issues = append(r.issues, "Site fragment unreadable or unsupported")
						continue
					}
					fragments = append(fragments, v...)
				}
				nodes = []directive{{name: "http", children: fragments}}
				r.issues = append(r.issues, "HTTP site fragments only; main configuration and inherited directives were not collected")
			}
		}
	} else {
		nodes, e = r.read(p.Entry, map[string]bool{}, 0)
	}
	if e != nil {
		s.Issues = []string{"Nginx entry unreadable, unsupported or outside scope"}
		return s
	}
	// Parse disk configuration only: never invoke nginx -t/-T/reload or read certificate keys.
	for path, rev := range r.files {
		b, e := readConfigBytes(path)
		if e != nil || config.Revision(string(b)) != rev {
			s.Issues = []string{"Nginx configuration changed during collection"}
			return s
		}
	}
	s.Files = len(r.files)
	s.Issues = append(s.Issues, r.issues...)
	s.Status = "read"
	if len(s.Issues) > 0 {
		s.Status = "partial"
	}
	for _, http := range nodes {
		if http.name != "http" {
			continue
		}
		upstreams := map[string][]string{}
		for _, n := range http.children {
			if n.name == "upstream" && len(n.args) == 1 {
				for _, v := range n.children {
					if v.name == "server" && len(v.args) > 0 {
						upstreams[n.args[0]] = append(upstreams[n.args[0]], v.args[0])
					}
				}
			}
		}
		for _, server := range http.children {
			if server.name != "server" {
				continue
			}
			site := Site{File: display(d, server.file), Names: safeArgs(d, args(server.children, "server_name")), Listen: safeArgs(d, args(server.children, "listen")), Certificates: safeArgs(d, args(server.children, "ssl_certificate")), Routes: []Route{}}
			if len(site.Certificates) == 0 {
				site.Certificates = safeArgs(d, args(http.children, "ssl_certificate"))
			}
			for _, v := range site.Listen {
				if v == "ssl" {
					site.TLS = true
				}
			}
			var routes func([]directive, string)
			routes = func(children []directive, location string) {
				for _, n := range children {
					if n.name == "return" && len(n.args) > 0 {
						site.Routes = append(site.Routes, Route{Location: display(d, location), Redirect: strings.Join(safeArgs(d, n.args), " "), Targets: []string{}})
					}
					if n.name == "location" {
						routes(n.children, strings.Join(n.args, " "))
						continue
					}
					if n.name == "proxy_pass" && len(n.args) == 1 {
						v := Route{Location: display(d, location), Upstream: display(d, n.args[0]), Targets: []string{}}
						for _, header := range children {
							if header.name == "proxy_set_header" && len(header.args) > 1 && strings.EqualFold(header.args[0], "Upgrade") {
								v.WebSocket = true
							}
						}
						u, e := url.Parse(n.args[0])
						if e == nil {
							targets := []string{u.Host}
							if list, ok := upstreams[u.Host]; ok {
								targets = list
							}
							for _, target := range targets {
								v.Targets = append(v.Targets, display(d, target))
								host, port, e := net.SplitHostPort(target)
								if e != nil {
									continue
								}
								ip := net.ParseIP(host)
								if host == "localhost" || (ip != nil && ip.IsLoopback()) {
									n, _ := strconv.Atoi(port)
									if n > 0 && n == intNumber(d.Values["vhostHTTPPort"]) {
										v.FRPHint = "Possible FRP HTTP vhost route; business not verified"
									}
									web, _ := d.Values["webServer"].(map[string]any)
									if n > 0 && n == intNumber(web["port"]) {
										v.FRPHint = "Possible FRPS management route; business not verified"
									}
								}
							}
						}
						site.Routes = append(site.Routes, v)
					}
					// if/limit_except blocks can contain conditional proxy_pass directives.
					if n.name != "location" && n.children != nil {
						routes(n.children, location)
					}
				}
			}
			routes(server.children, "/")
			s.Sites = append(s.Sites, site)
		}
	}
	if len(s.Sites) > 512 {
		s.Sites = s.Sites[:512]
		s.Status = "partial"
		s.Issues = append(s.Issues, "Site display limit reached")
	}
	sort.SliceStable(s.Sites, func(i, j int) bool { return s.Sites[i].File < s.Sites[j].File })
	if s.Issues == nil {
		s.Issues = []string{}
	}
	s.ProcessCheck = managed.Check{Status: "not_checked", At: time.Now().Unix(), Evidence: "No runtime target registered"}
	return s
}
func intNumber(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}
