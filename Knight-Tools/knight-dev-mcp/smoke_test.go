package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAccountServer 按内部/httpapi 契约搭一个最小假服务：healthz/register/login/users 四路由。
func fakeAccountServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","db":"up"}`))
	})
	registered := ""
	mux.HandleFunc("POST /api/v1/register", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Password == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid body"}`))
			return
		}
		registered = req.Name
		_, _ = w.Write([]byte(`{"uid":"42"}`))
	})
	mux.HandleFunc("POST /api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name != registered || req.Password == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"uid":"42","name":"` + registered + `","token":"tok-1"}`))
	})
	mux.HandleFunc("GET /api/v1/users/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"uid":"42","name":"` + registered + `","coins":7,"created_at":"2026-01-01T00:00:00Z"}`))
	})
	return httptest.NewServer(mux)
}

func TestRunHTTPSmoke_AllStepsPass(t *testing.T) {
	srv := fakeAccountServer(t)
	defer func() { srv.Close() }()

	out, ok := runHTTPSmoke(srv.URL, srv.Client())
	if !ok {
		t.Fatalf("冒烟应通过:\n%s", out)
	}
	for _, want := range []string{"healthz", "register: uid=42", "login: uid=42", "users/{uid}"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "✗") {
		t.Fatalf("通过时不应有失败步骤:\n%s", out)
	}
}

func TestRunHTTPSmoke_StopsOnBadCredentials(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","db":"up"}`))
	})
	mux.HandleFunc("POST /api/v1/register", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"uid":"7"}`))
	})
	mux.HandleFunc("POST /api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
	})
	srv := httptest.NewServer(mux)
	defer func() { srv.Close() }()

	out, ok := runHTTPSmoke(srv.URL, srv.Client())
	if ok {
		t.Fatalf("登录失败时冒烟应不通过:\n%s", out)
	}
	if !strings.Contains(out, "invalid credentials") {
		t.Fatalf("输出应携带服务端错误原因:\n%s", out)
	}
	if strings.Contains(out, "users/{uid}") {
		t.Fatalf("失败后不应继续后续步骤:\n%s", out)
	}
}

func TestRunHTTPSmoke_StopsOnDBDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded","db":"down","error":"dial tcp: connection refused"}`))
	}))
	defer func() { srv.Close() }()

	out, ok := runHTTPSmoke(srv.URL, srv.Client())
	if ok {
		t.Fatalf("数据库不可达时冒烟应不通过:\n%s", out)
	}
	if !strings.Contains(out, "db=down") {
		t.Fatalf("输出应指明数据库不可达:\n%s", out)
	}
}

func TestNormalizeHTTPBase(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:37780":    "http://127.0.0.1:37780",
		"http://127.0.0.1/":  "http://127.0.0.1",
		"https://example.io": "https://example.io",
	}
	for in, want := range cases {
		if got := normalizeHTTPBase(in); got != want {
			t.Fatalf("normalizeHTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
}
