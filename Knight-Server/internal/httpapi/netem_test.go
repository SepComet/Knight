package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"knight-server/internal/kcp2k"
)

func postNetEmJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNetEmDisabledByDefault(t *testing.T) {
	// Default server has DebugAPI disabled even if NetEm is attached.
	ne := kcp2k.NewNetEm()
	defer ne.Close()

	srv := NewServer(nil, nil, nil)
	srv.SetNetEm(ne)
	h := srv.Handler()

	rec := get(t, h, "/api/v1/debug/netem")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "debug api disabled") {
		t.Fatalf("GET body = %s, want debug api disabled", rec.Body.String())
	}

	rec = postNetEmJSON(t, h, "/api/v1/debug/netem", `{"delay_ms":100}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "debug api disabled") {
		t.Fatalf("POST body = %s, want debug api disabled", rec.Body.String())
	}
	if ne.Config().DelayMs != 0 {
		t.Fatalf("netem config must not mutate when debug api disabled: %+v", ne.Config())
	}
}

func TestNetEmUnavailable(t *testing.T) {
	srv := NewServer(nil, nil, nil)
	srv.SetDebugEnabled(true)
	h := srv.Handler()

	rec := get(t, h, "/api/v1/debug/netem")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET code = %d, want 503", rec.Code)
	}

	rec = postNetEmJSON(t, h, "/api/v1/debug/netem", `{"delay_ms":100}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST code = %d, want 503", rec.Code)
	}
}

func TestNetEmGetAndPost(t *testing.T) {
	ne := kcp2k.NewNetEm()
	defer ne.Close()

	srv := NewServer(nil, nil, nil)
	srv.SetDebugEnabled(true)
	srv.SetNetEm(ne)
	h := srv.Handler()

	// Default state: disabled, all 0, direction=both.
	rec := get(t, h, "/api/v1/debug/netem")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"overflow_dropped_packets":0`) {
		t.Fatalf("expected overflow_dropped_packets in JSON response, got %s", rec.Body.String())
	}
	var snap kcp2k.NetEmSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if snap.Enabled || snap.DelayMs != 0 || snap.JitterMs != 0 || snap.LossPercent != 0 || snap.Direction != kcp2k.NetEmDirectionBoth {
		t.Fatalf("unexpected default snapshot: %+v", snap)
	}

	// Trigger one packet to verify stats appear.
	ne.HandleInbound(func() {})

	// Update config.
	rec = postNetEmJSON(t, h, "/api/v1/debug/netem", `{"delay_ms":150,"jitter_ms":20,"loss_percent":3.5,"direction":"outbound"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if !snap.Enabled || snap.DelayMs != 150 || snap.JitterMs != 20 || snap.LossPercent != 3.5 || snap.Direction != kcp2k.NetEmDirectionOutbound {
		t.Fatalf("unexpected updated snapshot: %+v", snap)
	}
	if snap.Stats.TotalPackets != 1 {
		t.Fatalf("TotalPackets = %d, want 1", snap.Stats.TotalPackets)
	}

	// Reset stats and restore to disabled with empty fields + reset_stats=true.
	rec = postNetEmJSON(t, h, "/api/v1/debug/netem", `{"reset_stats":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST reset code = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if snap.Enabled || snap.DelayMs != 0 || snap.JitterMs != 0 || snap.LossPercent != 0 || snap.Direction != kcp2k.NetEmDirectionBoth {
		t.Fatalf("expected reset snapshot, got %+v", snap)
	}
	if snap.Stats.TotalPackets != 0 {
		t.Fatalf("expected stats reset to 0, got %+v", snap.Stats)
	}
}

func TestNetEmValidationErrors(t *testing.T) {
	ne := kcp2k.NewNetEm()
	defer ne.Close()
	if err := ne.SetConfig(kcp2k.NetEmConfig{DelayMs: 80, Direction: kcp2k.NetEmDirectionBoth}); err != nil {
		t.Fatalf("seed SetConfig failed: %v", err)
	}

	srv := NewServer(nil, nil, nil)
	srv.SetDebugEnabled(true)
	srv.SetNetEm(ne)
	h := srv.Handler()

	badBodies := []string{
		`not json`,
		`{"delay_ms": -1}`,
		`{"delay_ms": 2001}`,
		`{"jitter_ms": -5}`,
		`{"jitter_ms": 1001}`,
		`{"loss_percent": -0.5}`,
		`{"loss_percent": 50.1}`,
		`{"direction": "invalid"}`,
		`{"loss_rate": 5}`, // unknown field must be rejected
		`{"delay_ms": 10}{"delay_ms": 20}`,
	}
	for _, body := range badBodies {
		rec := postNetEmJSON(t, h, "/api/v1/debug/netem", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: code = %d, want 400 (resp=%s)", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Fatalf("body %q: expected error json, got %s", body, rec.Body.String())
		}
	}

	// Original config must remain intact after rejected requests.
	if got := ne.Config(); got.DelayMs != 80 {
		t.Fatalf("config mutated after failed requests: %+v", got)
	}
}

func TestNetEmMethodNotAllowed(t *testing.T) {
	ne := kcp2k.NewNetEm()
	defer ne.Close()

	srv := NewServer(nil, nil, nil)
	srv.SetDebugEnabled(true)
	srv.SetNetEm(ne)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/debug/netem", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE code = %d, want 405", rec.Code)
	}
}
