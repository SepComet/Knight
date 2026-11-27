package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"knight-server/internal/kcp2k"
)

// NetEmController 运行时弱网控制抽象（*kcp2k.NetEm 满足）。
type NetEmController interface {
	SetConfig(cfg kcp2k.NetEmConfig) error
	Snapshot() kcp2k.NetEmSnapshot
	ResetStats()
}

type netemSetReq struct {
	DelayMs     int                  `json:"delay_ms"`
	JitterMs    int                  `json:"jitter_ms"`
	LossPercent float64              `json:"loss_percent"`
	Direction   kcp2k.NetEmDirection `json:"direction"`
	ResetStats  bool                 `json:"reset_stats"`
}

// guardNetEm 弱网调试路由前置：未启用调试开关返回 403，未装配 NetEmController 返回 503。
func (s *Server) guardNetEm(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.debugEnabled {
			writeError(w, http.StatusForbidden, "debug api disabled")
			return
		}
		if s.netem == nil {
			writeError(w, http.StatusServiceUnavailable, "netem not available")
			return
		}
		next(w, r)
	}
}

// handleGetNetEm GET /api/v1/debug/netem → 200 NetEmSnapshot
func (s *Server) handleGetNetEm(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.netem.Snapshot())
}

// handleSetNetEm POST /api/v1/debug/netem → 200 NetEmSnapshot / 400
func (s *Server) handleSetNetEm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req netemSetReq
	if err := dec.Decode(&req); err != nil {
		slog.Warn("invalid netem request body", "err", err)
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	cfg := kcp2k.NetEmConfig{
		DelayMs:     req.DelayMs,
		JitterMs:    req.JitterMs,
		LossPercent: req.LossPercent,
		Direction:   req.Direction,
	}
	if err := s.netem.SetConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ResetStats {
		s.netem.ResetStats()
	}

	snap := s.netem.Snapshot()
	slog.Info("netem config updated",
		"enabled", snap.Enabled,
		"delay_ms", snap.DelayMs,
		"jitter_ms", snap.JitterMs,
		"loss_percent", snap.LossPercent,
		"direction", snap.Direction,
		"reset_stats", req.ResetStats,
	)
	writeJSON(w, http.StatusOK, snap)
}
