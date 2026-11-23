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
	c := FromEnv()
	if c.HTTPPort != 8080 || c.MySQLHost != "db.internal" || c.MySQLPort != 3307 {
		t.Fatalf("env override failed: %+v", c)
	}
	if c.TickRate != 60 {
		t.Fatalf("TickRate = %d, want 60", c.TickRate)
	}
}
