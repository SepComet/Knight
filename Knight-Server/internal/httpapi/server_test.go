package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(ctx context.Context) error { return f.err }

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthzDBUp(t *testing.T) {
	h := NewServer(fakePinger{}, nil, nil).Handler()
	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	want := `{"status":"ok","db":"up"}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHealthzDBDown(t *testing.T) {
	h := NewServer(fakePinger{err: errors.New("connection refused")}, nil, nil).Handler()
	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"degraded"`) || !strings.Contains(body, `"db":"down"`) {
		t.Fatalf("body = %q, want degraded/down", body)
	}
}

func TestHealthzDBDisabled(t *testing.T) {
	h := NewServer(nil, nil, nil).Handler()
	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"db":"disabled"`) {
		t.Fatalf("body = %q, want db disabled", got)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	h := NewServer(nil, nil, nil).Handler()
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
