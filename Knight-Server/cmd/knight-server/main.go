// knight-server 服务端入口。
// HTTP（/healthz + MySQL）与 KCP 游戏通道（kcp2k 线上协议 + 会话握手）并行运行。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"knight-server/internal/account"
	"knight-server/internal/auth"
	"knight-server/internal/config"
	"knight-server/internal/event"
	"knight-server/internal/httpapi"
	"knight-server/internal/kcp2k"
	"knight-server/internal/lobby"
	"knight-server/internal/session"
	"knight-server/internal/snowflake"
	"knight-server/internal/store"
	"knight-server/internal/tick"
	"knight-server/internal/world"

	knight "knight-server/protogen"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := config.FromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.MySQLDSNString())
	if err != nil {
		slog.Error("open store failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	slog.Info("mysql connected", "host", cfg.MySQLHost, "port", cfg.MySQLPort, "db", cfg.MySQLDB)

	// ---- 账号业务（HTTP）----
	ids, err := snowflake.NewNode(cfg.SnowflakeWorkerID)
	if err != nil {
		slog.Error("snowflake init failed", "err", err)
		os.Exit(1)
	}
	tokens := auth.NewTokenStore(cfg.TokenTTL)
	accounts := account.NewService(st, tokens, ids)

	// ---- KCP 游戏通道 + 会话层 ----
	// 闭包先行捕获 mgr（gate 回调在 Run 前不会触发）
	var mgr *session.Manager
	kcpCfg := kcp2k.DefaultConfig()
	kcpCfg.FastResend = 2 // 游戏协议固定参数（protocol.md §2）
	gate, err := kcp2k.Listen(fmt.Sprintf(":%d", cfg.KCPPort), kcpCfg, kcp2k.ServerCallbacks{
		OnConnect:    func(connID string) { mgr.OnKcpConnect(connID) },
		OnData:       func(connID string, data []byte, _ kcp2k.Channel) { mgr.OnKcpData(connID, data, kcp2k.ChannelReliable) },
		OnDisconnect: func(connID string) { mgr.OnKcpDisconnect(connID) },
		OnError: func(connID string, code kcp2k.ErrorCode, msg string) {
			slog.Warn("kcp transport error", "conn", connID, "code", code, "msg", msg)
		},
	})
	if err != nil {
		slog.Error("kcp listen failed", "port", cfg.KCPPort, "err", err)
		os.Exit(1)
	}
	netemInit := kcp2k.NetEmConfig{
		DelayMs:     cfg.NetEmDelayMs,
		JitterMs:    cfg.NetEmJitterMs,
		LossPercent: cfg.NetEmLossPercent,
		Direction:   kcp2k.NetEmDirection(cfg.NetEmDirection),
	}
	if err := gate.NetEm().SetConfig(netemInit); err != nil {
		slog.Error("invalid initial netem config", "err", err)
		gate.Stop()
		os.Exit(1)
	}
	if netemInit.Enabled() {
		slog.Warn("netem enabled at startup",
			"delay_ms", netemInit.DelayMs,
			"jitter_ms", netemInit.JitterMs,
			"loss_percent", netemInit.LossPercent,
			"direction", netemInit.Normalized().Direction,
		)
	}

	// ---- HTTP ----
	apiSrv := httpapi.NewServer(st, accounts, tokens)
	if cfg.DebugAPIEnabled {
		apiSrv.SetDebugEnabled(true)
		apiSrv.SetNetEm(gate.NetEm())
		slog.Warn("debug api enabled", "endpoint", "/api/v1/debug/netem")
	}
	httpSrv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.HTTPPort),
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// ---- 事件总线（服务端状态变化发布订阅，Docs/server-event-pipeline.md）----
	bus := event.NewBus()

	// ---- 固定步长模拟循环（游戏域 64~127 的时间轴，Docs/server-tick-loop.md）----
	// 现阶段无单元注册，空转验证接线；游戏域落地后经 Add 挂载对局
	runner, err := tick.NewRunner(tick.Config{Rate: cfg.TickRate})
	if err != nil {
		slog.Error("tick runner init failed", "err", err)
		os.Exit(1)
	}

	// ---- 会话层（先于大厅构造：大厅广播需要 mgr 作 UIDSender）----
	mgr = session.NewManager(
		session.Config{
			ProtoVersion:     cfg.ProtoVersion,
			SessionLimit:     cfg.SessionLimit,
			HeartbeatTimeout: cfg.HeartbeatTimeout,
		},
		tokenValidator(cfg, tokens),
		gate,
		bus,
	)

	// ---- 大厅（房间消息域 32~63）----
	lobbyMgr := lobby.NewManager(lobby.Config{
		MaxPlayers:       cfg.MaxPlayers,
		MinPlayers:       cfg.MinPlayers,
		MaxRooms:         cfg.MaxRooms,
		DungeonGenerator: world.GenerateDungeon,
	}, gate, mgr, bus)

	// ---- 战斗世界（游戏域 64~127）----
	worldMgr := world.NewManager(world.Config{
		TickRate: cfg.TickRate,
		Recorder: &mysqlMatchRecorder{store: st, ids: ids},
	}, runner, mgr, mgr, bus)

	// ---- 域级 Router（域段见 knight.proto MsgId 分配表）----
	mgr.SetRouter(session.NewDomainRouter(gate,
		session.DomainRoute{Min: 32, Max: 63, Router: lobbyMgr},
		session.DomainRoute{Min: 64, Max: 127, Router: worldMgr},
	))

	// 心跳超时巡检
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mgr.CheckTimeouts()
			}
		}
	}()

	// ---- 启动 ----
	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "port", cfg.HTTPPort)
		errCh <- httpSrv.ListenAndServe()
	}()
	go func() {
		slog.Info("kcp listening", "port", cfg.KCPPort, "proto_version", cfg.ProtoVersion)
		gate.Run()
	}()
	go func() {
		slog.Info("tick runner started", "rate", cfg.TickRate, "dt", runner.Dt())
		runner.Run(ctx)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
		}
		// 先停模拟循环，等待在途对局结算落库与 GameResult 下发完成，再关会话与传输层（MySQL defer 最后关闭）
		runner.Stop()
		if err := worldMgr.WaitPersist(shutdownCtx); err != nil {
			slog.Warn("wait match persist timeout during shutdown", "err", err)
		}
		mgr.Close()
		gate.Stop()
	}
}

// tokenValidator 按配置选择 KCP 握手的令牌校验器：
// 默认用登录签发的真实令牌表（auth.TokenStore.Lookup 与 session.TokenValidator 同签名）；
// KNIGHT_DEV_TOKEN_BYPASS=1 仅用于登录链路不可用时的临时调试（任意非空 token 放行）。
func tokenValidator(cfg config.Config, tokens *auth.TokenStore) session.TokenValidator {
	if cfg.DevTokenBypass {
		slog.Warn("dev token bypass enabled, any non-empty token is accepted")
		return session.DevBypassValidator{}
	}
	return tokens
}

// mysqlMatchRecorder 将对局结算写入 MySQL matches / match_players 表。
type mysqlMatchRecorder struct {
	store *store.Store
	ids   *snowflake.Node
}

func (r *mysqlMatchRecorder) RecordMatch(ctx context.Context, outcome knight.GameOutcome, durationSec uint32, seed uint64, players []world.MatchPlayerStat) (uint64, error) {
	matchID, err := r.ids.Next()
	if err != nil {
		return 0, err
	}
	rows := make([]store.MatchPlayerRow, 0, len(players))
	for _, p := range players {
		rows = append(rows, store.MatchPlayerRow{
			UID:         p.UID,
			CharacterID: p.CharacterID,
			WeaponID:    p.WeaponID,
			Kills:       p.Kills,
			DamageDealt: p.DamageDealt,
			DamageTaken: p.DamageTaken,
			Alive:       p.Alive,
		})
	}
	if err := r.store.InsertMatchWithSeed(ctx, matchID, uint32(outcome), durationSec, seed, rows); err != nil {
		return 0, err
	}
	return matchID, nil
}
