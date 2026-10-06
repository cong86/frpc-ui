package server

import (
	"bytes"
	"encoding/json"
	"github.com/cong86/frpc-ui/internal/config"
	"github.com/cong86/frpc-ui/internal/state"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAuthHostOriginCSRFAndBootstrap(t *testing.T) {
	st, e := state.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.DB.Close()
	s := New(st, &config.Manager{State: st, Instances: map[string]config.Instance{}}, "127.0.0.1:18745")
	h := s.Handler()
	request := func(path, host, origin string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		method := "POST"
		if body == nil {
			method = "GET"
		}
		r := httptest.NewRequest(method, "http://"+host+path, bytes.NewReader(b))
		r.Host = host
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-FRP-Console", "1")
		r.Header.Set("X-CSRF-Token", csrf)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("/api/instances", "127.0.0.1:18745", "", nil, nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated access", w.Code)
	}
	if w := request("/api/session", "attacker.test", "", nil, nil, ""); w.Code != 403 {
		t.Fatal("host not checked")
	}
	input := map[string]string{"user": "admin", "password": "test-password-long"}
	if w := request("/api/bootstrap", "127.0.0.1:18745", "https://attacker.test", input, nil, ""); w.Code != 403 {
		t.Fatal("origin not checked")
	}
	w := request("/api/bootstrap", "127.0.0.1:18745", "", input, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	var session map[string]string
	json.Unmarshal(w.Body.Bytes(), &session)
	if w = request("/api/bootstrap", "127.0.0.1:18745", "", input, nil, ""); w.Code != 409 {
		t.Fatal("bootstrap repeated", w.Code)
	}
	if w = request("/api/logout", "127.0.0.1:18745", "", map[string]any{}, cookie, "wrong"); w.Code != 403 {
		t.Fatal("CSRF not checked")
	}
	if w = request("/api/instances", "127.0.0.1:18745", "", nil, cookie, ""); w.Code != 200 {
		t.Fatal("session rejected")
	}
	if w = request("/api/logout", "127.0.0.1:18745", "", map[string]any{}, cookie, session["csrf"]); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = request("/api/instances", "127.0.0.1:18745", "", nil, cookie, ""); w.Code != 401 {
		t.Fatal("logout did not invalidate session")
	}
}

func TestPasswordBootstrapWithoutLengthRestriction(t *testing.T) {
	for _, password := range []string{"", "1", "中文 密码🔑", strings.Repeat("长密码", 100)} {
		t.Run("bytes_"+strconv.Itoa(len(password)), func(t *testing.T) {
			st, err := state.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.DB.Close()
			s := New(st, &config.Manager{State: st, Instances: map[string]config.Instance{}}, "127.0.0.1:18745")
			request := func(path, value string) *httptest.ResponseRecorder {
				body, _ := json.Marshal(map[string]string{"user": "admin", "password": value})
				r := httptest.NewRequest("POST", "http://"+s.Host+path, bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-FRP-Console", "1")
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w
			}
			w := request("/api/bootstrap", password)
			if password == "" {
				if w.Code != 400 {
					t.Fatal("empty password accepted")
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("bootstrap: %d", w.Code)
			}
			if w := request("/api/login", password); w.Code != 200 {
				t.Fatalf("login: %d", w.Code)
			}
			if w := request("/api/login", password+"different"); w.Code != 401 {
				t.Fatal("different password accepted")
			}
		})
	}
}

func TestLegacyPasswordStillWorks(t *testing.T) {
	p, err := bcrypt.GenerateFromPassword([]byte("legacy-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkPassword(p, "legacy-password"); err != nil {
		t.Fatal(err)
	}
	if checkPassword(p, "wrong-password") == nil {
		t.Fatal("different legacy password accepted")
	}
}

func TestTwoConsolesUseIndependentCookieNamespaces(t *testing.T) {
	a := &Server{Host: "127.0.0.1:18752"}
	b := &Server{Host: "127.0.0.1:18759"}
	if a.cookieName() == b.cookieName() {
		t.Fatal("ports share a cookie namespace")
	}
	if a.cookieName() != (&Server{Host: a.Host}).cookieName() {
		t.Fatal("cookie namespace is not stable")
	}
}
