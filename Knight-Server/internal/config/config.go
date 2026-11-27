// Package config 汇总服务端运行配置。
// 全部通过环境变量覆盖；MySQL 连接参数与 docker-compose.yml 中的 mysql 服务默认值对应。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 服务端全部运行配置。
type Config struct {
	HTTPPort int // HTTP 监听端口（TCP）
	KCPPort  int // KCP 游戏通道监听端口（UDP）

	// 会话层（Docs/protocol.md §4）
	ProtoVersion     uint32        // 握手版本校验值（knight.proto PROTO_VERSION）
	SessionLimit     int           // 同时在线会话上限
	HeartbeatTimeout time.Duration // 无任何 Envelope 的踢出时限
	DevTokenBypass   bool          // 开发后门：接受任意非空 token（默认关）

	// 账号（Docs/protocol.md §3）
	TokenTTL time.Duration // 登录令牌有效期（内存态，进程重启失效）

	// 雪花 ID（uid / match_id 生成）
	SnowflakeWorkerID int // worker ID（0~1023）；多实例部署时各实例必须不同

	// 大厅（Docs/protocol.md §5.2）
	MaxPlayers int // 单房间人数上限（协议 4）
	MinPlayers int // 开局最少人数（协议默认 2，开发期可配 1）
	MaxRooms   int // 同时存在房间数上限

	// 模拟循环（Docs/server-tick-loop.md）
	TickRate int // 固定步长模拟频率（KNIGHT_TICK_RATE），1~120；越界由 tick.NewRunner 拒绝启动

	// 弱网模拟初始配置（默认全 0 关闭，可通过 HTTP /api/v1/debug/netem 热修改）
	DebugAPIEnabled  bool    // KNIGHT_DEBUG_API_ENABLED，默认 false；仅开发/演示环境开启调试接口
	NetEmDelayMs     int     // KNIGHT_NETEM_DELAY_MS，0~2000
	NetEmJitterMs    int     // KNIGHT_NETEM_JITTER_MS，0~1000
	NetEmLossPercent float64 // KNIGHT_NETEM_LOSS_PERCENT，0~50
	NetEmDirection   string  // KNIGHT_NETEM_DIRECTION，both/outbound/inbound

	// MySQL 连接参数；DSN 非空时优先使用完整 DSN，否则由分项拼接
	MySQLDSN      string
	MySQLHost     string
	MySQLPort     int
	MySQLUser     string
	MySQLPassword string
	MySQLDB       string
}

// Defaults 返回默认配置，与 docker-compose.yml 的 mysql 服务保持一致。
func Defaults() Config {
	return Config{
		HTTPPort:         37780,
		KCPPort:          37777,
		ProtoVersion:     5, // 与 Knight-Proto/knight.proto 头部 PROTO_VERSION 同步
		SessionLimit:     256,
		HeartbeatTimeout: 10 * time.Second,
		DevTokenBypass:   false,
		TokenTTL:         24 * time.Hour,
		SnowflakeWorkerID: 0,
		MaxPlayers:       4,
		MinPlayers:       2,
		MaxRooms:         64,
		TickRate:         30,
		DebugAPIEnabled:  false,
		NetEmDelayMs:     0,
		NetEmJitterMs:    0,
		NetEmLossPercent: 0,
		NetEmDirection:   "both",
		MySQLHost:        "127.0.0.1",
		MySQLPort:        3306,
		MySQLUser:        "knight",
		MySQLPassword:    "knight",
		MySQLDB:          "knight",
	}
}

// FromEnv 在默认值基础上应用环境变量覆盖。
func FromEnv() Config {
	c := Defaults()
	c.HTTPPort = envInt("KNIGHT_HTTP_PORT", c.HTTPPort)
	c.KCPPort = envInt("KNIGHT_KCP_PORT", c.KCPPort)
	c.ProtoVersion = uint32(envInt("KNIGHT_PROTO_VERSION", int(c.ProtoVersion)))
	c.SessionLimit = envInt("KNIGHT_SESSION_LIMIT", c.SessionLimit)
	c.HeartbeatTimeout = envDur("KNIGHT_HEARTBEAT_TIMEOUT", c.HeartbeatTimeout)
	c.DevTokenBypass = envStr("KNIGHT_DEV_TOKEN_BYPASS", "") == "1"
	c.TokenTTL = envDur("KNIGHT_TOKEN_TTL", c.TokenTTL)
	c.SnowflakeWorkerID = envInt("KNIGHT_SNOWFLAKE_WORKER_ID", c.SnowflakeWorkerID)
	c.MaxPlayers = envInt("KNIGHT_MAX_PLAYERS", c.MaxPlayers)
	c.MinPlayers = envInt("KNIGHT_MIN_PLAYERS_TO_START", c.MinPlayers)
	c.MaxRooms = envInt("KNIGHT_MAX_ROOMS", c.MaxRooms)
	c.TickRate = envInt("KNIGHT_TICK_RATE", c.TickRate)
	c.DebugAPIEnabled = envBool("KNIGHT_DEBUG_API_ENABLED", c.DebugAPIEnabled)
	c.NetEmDelayMs = envInt("KNIGHT_NETEM_DELAY_MS", c.NetEmDelayMs)
	c.NetEmJitterMs = envInt("KNIGHT_NETEM_JITTER_MS", c.NetEmJitterMs)
	c.NetEmLossPercent = envFloat("KNIGHT_NETEM_LOSS_PERCENT", c.NetEmLossPercent)
	c.NetEmDirection = envStr("KNIGHT_NETEM_DIRECTION", c.NetEmDirection)
	c.MySQLDSN = os.Getenv("KNIGHT_MYSQL_DSN")
	c.MySQLHost = envStr("KNIGHT_MYSQL_HOST", c.MySQLHost)
	c.MySQLPort = envInt("KNIGHT_MYSQL_PORT", c.MySQLPort)
	c.MySQLUser = envStr("KNIGHT_MYSQL_USER", c.MySQLUser)
	c.MySQLPassword = envStr("KNIGHT_MYSQL_PASSWORD", c.MySQLPassword)
	c.MySQLDB = envStr("KNIGHT_MYSQL_DB", c.MySQLDB)
	return c
}

// MySQLDSNString 组装 go-sql-driver/mysql 的 DSN。
// parseTime：TIME/DATETIME 扫描为 time.Time；utf8mb4：中文与 emoji 兼容。
func (c Config) MySQLDSNString() string {
	if c.MySQLDSN != "" {
		return c.MySQLDSN
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&charset=utf8mb4",
		c.MySQLUser, c.MySQLPassword, c.MySQLHost, c.MySQLPort, c.MySQLDB)
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
