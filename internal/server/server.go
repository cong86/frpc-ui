package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/state"
	"github.com/cong86/frpc-ui/internal/templates"
	"golang.org/x/crypto/bcrypt"
)

//go:embed assets
var assets embed.FS

type Server struct {
	State    *state.Store
	Manager  *config.Manager
	Host     string
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func New(st *state.Store, m *config.Manager, host string) *Server {
	return &Server{State: st, Manager: m, Host: host, attempts: map[string][]time.Time{}}
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, e error) {
	code := http.StatusBadRequest
	if errors.Is(e, config.ErrConflict) {
		code = http.StatusConflict
	}
	reply(w, code, map[string]string{"error": e.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON content type required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, config.MaxSize+65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("invalid request")
	}
	return nil
}

type session struct {
	User  string
	CSRF  string
	Token string
}

func (s *Server) session(r *http.Request) (session, error) {
	c, e := r.Cookie("frp_console")
	if e != nil {
		return session{}, e
	}
	var user, csrf string
	var expires int64
	e = s.State.DB.QueryRow("SELECT user,csrf,expires FROM sessions WHERE hash=?", hash(c.Value)).Scan(&user, &csrf, &expires)
	if e != nil || expires < time.Now().Unix() {
		return session{}, errors.New("session expired")
	}
	return session{User: user, CSRF: csrf, Token: c.Value}, nil
}
func (s *Server) limited(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	old := s.attempts[addr]
	keep := []time.Time{}
	for _, t := range old {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	s.attempts[addr] = append(keep, now)
	return len(keep) >= 5
}
func (s *Server) Handler() http.Handler {
	files, _ := fs.Sub(assets, "assets")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != s.Host {
			http.Error(w, "untrusted host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			static.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" {
			o := r.Header.Get("Origin")
			if o != "" {
				u, e := url.Parse(o)
				if e != nil || u.Host != r.Host || u.Scheme != "http" {
					http.Error(w, "untrusted origin", http.StatusForbidden)
					return
				}
			}
			if r.Header.Get("X-FRP-Console") != "1" {
				http.Error(w, "missing request guard", http.StatusForbidden)
				return
			}
		}
		if r.URL.Path == "/api/session" && r.Method == "GET" {
			var count int
			_ = s.State.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			v, e := s.session(r)
			reply(w, 200, map[string]any{"initialized": count > 0, "authenticated": e == nil, "user": v.User, "csrf": v.CSRF})
			return
		}
		if (r.URL.Path == "/api/bootstrap" || r.URL.Path == "/api/login") && r.Method == "POST" {
			s.login(w, r)
			return
		}
		sess, e := s.session(r)
		if e != nil {
			reply(w, 401, map[string]string{"error": "login required"})
			return
		}
		if r.Method != "GET" && r.Header.Get("X-CSRF-Token") != sess.CSRF {
			reply(w, 403, map[string]string{"error": "invalid CSRF token"})
			return
		}
		s.api(w, r, sess)
	})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	addr, _, _ := net.SplitHostPort(r.RemoteAddr)
	if s.limited(addr) {
		reply(w, 429, map[string]string{"error": "too many attempts; retry after one minute"})
		return
	}
	var in struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &in); e != nil {
		fail(w, e)
		return
	}
	if len(in.User) < 1 || len(in.User) > 64 || strings.ContainsAny(in.User, "\r\n") {
		fail(w, errors.New("invalid username"))
		return
	}
	if r.URL.Path == "/api/bootstrap" {
		if len(in.Password) < 12 || len(in.Password) > 72 {
			fail(w, errors.New("password must contain 12..72 bytes"))
			return
		}
		p, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if e != nil {
			fail(w, errors.New("password hashing failed"))
			return
		}
		// INSERT predicate makes bootstrap one-shot even for simultaneous requests.
		res, e := s.State.DB.Exec("INSERT INTO users(name,password) SELECT ?,? WHERE NOT EXISTS(SELECT 1 FROM users)", in.User, p)
		if e != nil {
			fail(w, errors.New("initialization failed"))
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			reply(w, 409, map[string]string{"error": "administrator already initialized"})
			return
		}
	} else {
		var p []byte
		e := s.State.DB.QueryRow("SELECT password FROM users WHERE name=?", in.User).Scan(&p)
		if e != nil {
			p = []byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5Js94rgK8LNX.QJJJkrfz6/gQ./jN6u")
		}
		if bcrypt.CompareHashAndPassword(p, []byte(in.Password)) != nil || e != nil {
			_ = s.State.Audit(in.User, "login", "", "denied")
			reply(w, 401, map[string]string{"error": "invalid credentials"})
			return
		}
	}
	token, csrf := state.ID(), state.ID()
	_, e := s.State.DB.Exec("INSERT INTO sessions(hash,user,csrf,expires) VALUES(?,?,?,?)", hash(token), in.User, csrf, time.Now().Add(8*time.Hour).Unix())
	if e != nil {
		fail(w, errors.New("session creation failed"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "frp_console", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	_ = s.State.Audit(in.User, "login", "", "success")
	reply(w, 200, map[string]string{"user": in.User, "csrf": csrf})
}
func (s *Server) api(w http.ResponseWriter, r *http.Request, sess session) {
	path := r.URL.Path
	if path == "/api/logout" && r.Method == "POST" {
		_, _ = s.State.DB.Exec("DELETE FROM sessions WHERE hash=?", hash(sess.Token))
		http.SetCookie(w, &http.Cookie{Name: "frp_console", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if path == "/api/instances" && r.Method == "GET" {
		out := []any{}
		for _, i := range s.Manager.Instances {
			d, e := s.Manager.Read(i.ID)
			if e != nil {
				out = append(out, map[string]any{"instance": i, "error": "configuration unavailable"})
				continue
			}
			snap := d.Snapshot()
			if !i.Managed {
				snap.Editable = false
				snap.Reason = "Imported deployment is read-only in this milestone."
			}
			out = append(out, map[string]any{"instance": i, "config": snap, "verification": map[string]string{"installation": "not_checked", "process": "not_checked", "authentication": "not_checked", "registration": "not_checked", "localService": "not_checked", "business": "not_checked"}})
		}
		reply(w, 200, out)
		return
	}
	if path == "/api/plans" && r.Method == "POST" {
		var in struct {
			Instance string `json:"instance"`
			Revision string `json:"revision"`
			Text     string `json:"text"`
		}
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return
		}
		p, e := s.Manager.NewPlan(in.Instance, sess.User, in.Revision, in.Text)
		if e != nil {
			fail(w, e)
			return
		}
		_ = s.State.Audit(sess.User, "preview", in.Instance, "official_binary_verified")
		reply(w, 200, p)
		return
	}
	if path == "/api/proxy-plan" && r.Method == "POST" {
		var in struct {
			Instance string         `json:"instance"`
			Revision string         `json:"revision"`
			Name     string         `json:"name"`
			Remove   bool           `json:"remove"`
			Fields   map[string]any `json:"fields"`
		}
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return
		}
		p, e := s.Manager.ProxyPlan(in.Instance, sess.User, in.Revision, in.Name, in.Fields, in.Remove)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, p)
		return
	}
	if strings.HasPrefix(path, "/api/plans/") && r.Method == "POST" {
		parts := strings.Split(strings.TrimPrefix(path, "/api/plans/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		var result any
		var e error
		switch parts[1] {
		case "apply":
			result, e = s.Manager.Apply(parts[0], sess.User)
		case "cancel":
			e = s.Manager.Cancel(parts[0], sess.User)
			result = "canceled"
		case "restore":
			result, e = s.Manager.RestorePlan(parts[0], sess.User)
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"result": result})
		return
	}
	if path == "/api/operations" && r.Method == "GET" {
		rows, e := s.State.DB.Query("SELECT id,instance,state,created,result,CASE WHEN backup IS NULL THEN 0 ELSE 1 END FROM plans ORDER BY created DESC,rowid DESC LIMIT 100")
		if e != nil {
			fail(w, errors.New("journal unavailable"))
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var id, inst, status, result string
			var created int64
			var backup int
			if e = rows.Scan(&id, &inst, &status, &created, &result, &backup); e != nil {
				fail(w, errors.New("journal unavailable"))
				return
			}
			out = append(out, map[string]any{"id": id, "instance": inst, "state": status, "created": created, "result": result, "hasBackup": backup == 1})
		}
		reply(w, 200, out)
		return
	}
	if path == "/api/audit" && r.Method == "GET" {
		rows, e := s.State.DB.Query("SELECT at,actor,action,instance,result FROM audit ORDER BY id DESC LIMIT 100")
		if e != nil {
			fail(w, errors.New("audit unavailable"))
			return
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var at int64
			var actor, action, inst, result string
			if e = rows.Scan(&at, &actor, &action, &inst, &result); e != nil {
				fail(w, errors.New("audit unavailable"))
				return
			}
			out = append(out, map[string]any{"at": at, "actor": actor, "action": action, "instance": inst, "result": result})
		}
		reply(w, 200, out)
		return
	}
	if path == "/api/install/plan" && r.Method == "POST" {
		var in templates.InstallRequest
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return
		}
		p, e := templates.Install(in)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, p)
		return
	}
	if path == "/api/nginx/preview" && r.Method == "POST" {
		var in templates.NginxRequest
		if e := decode(w, r, &in); e != nil {
			fail(w, e)
			return
		}
		p, e := templates.Nginx(in)
		if e != nil {
			fail(w, e)
			return
		}
		reply(w, 200, map[string]any{"file": p, "validated": false, "loaded": false})
		return
	}
	http.NotFound(w, r)
}
