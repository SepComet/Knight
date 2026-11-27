package config

import (
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.HTTPPort != 37780 {
		t.Fatalf("HTTPPort = %d, want 37780", c.HTTPPort)
	}
	if c.TickRate != 30 {
		t.Fatalf("TickRate = %d, want 30", c.TickRate)
	}
	if c.DebugAPIEnabled {
		t.Fatal("DebugAPIEnabled = true, want false by default")
	}
	if c.NetEmDelayMs != 0 || c.NetEmJitterMs != 0 || c.NetEmLossPercent != 0 || c.NetEmDirection != "both" {
		t.Fatalf("unexpected netem defaults: %+v", c)
	}
	if c.MySQLHost != "127.0.0.1" || c.MySQLPort != 3306 || c.MySQLDB != "knight" {
		t.Fatalf("unexpected mysql defaults: %+v", c)
	}
}

func TestMySQLDSNString(t *testing.T) {
	c := Defaults()
	want := "knight:knight@tcp(127.0.0.1:3306)/knight?parseTime=true&charset=utf8mb4"
	if got := c.MySQLDSNString(); got != want {
		t.Fatalf("dsn = %q, want %q", got, want)
	}

	// 显式 DSN 优先于分项拼接
	c.MySQLDSN = "user:pass@tcp(db:3306)/other"
	if got := c.MySQLDSNString(); got != c.MySQLDSN {
		t.Fatalf("dsn = %q, want explicit %q", got, c.MySQLDSN)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("KNIGHT_HTTP_PORT", "8080")
	t.Setenv("KNIGHT_MYSQL_HOST", "db.internal")
	t.Setenv("KNIGHT_MYSQL_PORT", "3307")
	t.Setenv("KNIGHT_TICK_RATE", "60")
	t.Setenv("KNIGHT_DEBUG_API_ENABLED", "true")
	t.Setenv("KNIGHT_NETEM_DELAY_MS", "150")
	t.Setenv("KNIGHT_NETEM_JITTER_MS", "25")
	t.Setenv("KNIGHT_NETEM_LOSS_PERCENT", "3.5")
	t.Setenv("KNIGHT_NETEM_DIRECTION", "outbound")
	c := FromEnv()
	if c.HTTPPort != 8080 || c.MySQLHost != "db.internal" || c.MySQLPort != 3307 {
		t.Fatalf("env override failed: %+v", c)
	}
	if c.TickRate != 60 {
		t.Fatalf("TickRate = %d, want 60", c.TickRate)
	}
	if !c.DebugAPIEnabled {
		t.Fatal("DebugAPIEnabled = false, want true")
	}
	if c.NetEmDelayMs != 150 || c.NetEmJitterMs != 25 || c.NetEmLossPercent != 3.5 || c.NetEmDirection != "outbound" {
		t.Fatalf("netem env override failed: %+v", c)
	}
}

func TestFromEnvDebugAPIEnabledValues(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"0", false},
		{"invalid", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
	}
	for _, tc := range cases {
		t.Setenv("KNIGHT_DEBUG_API_ENABLED", tc.raw)
		if got := FromEnv().DebugAPIEnabled; got != tc.want {
			t.Fatalf("KNIGHT_DEBUG_API_ENABLED=%q -> %v, want %v", tc.raw, got, tc.want)
		}
	}
}
