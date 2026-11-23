package auth

import (
	"testing"
	"time"
)

func TestTokenCreateLookup(t *testing.T) {
	s := NewTokenStore(time.Hour)
	tok := s.Create(42, "alice")

	uid, name, ok := s.Lookup(tok)
	if !ok || uid != 42 || name != "alice" {
		t.Fatalf("lookup = %d %q %v", uid, name, ok)
	}

	// 未知令牌
	if _, _, ok := s.Lookup("no-such-token"); ok {
		t.Fatal("unknown token should not lookup")
	}
}

func TestTokenExpiry(t *testing.T) {
	s := NewTokenStore(10 * time.Millisecond)
	tok := s.Create(1, "bob")

	time.Sleep(20 * time.Millisecond)
	if _, _, ok := s.Lookup(tok); ok {
		t.Fatal("expired token should not lookup")
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d, expired entry should be purged", s.Len())
	}
}

func TestTokenUniqueness(t *testing.T) {
	s := NewTokenStore(time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := s.Create(uint64(i), "u")
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
	if s.Len() != 100 {
		t.Fatalf("len = %d, want 100", s.Len())
	}
}

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret-密码")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "s3cret-密码" || len(h) < 50 {
		t.Fatal("hash should be salted and long")
	}
	if !VerifyPassword(h, "s3cret-密码") {
		t.Fatal("correct password should verify")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatal("wrong password should not verify")
	}

	// 同一密码两次哈希结果不同（盐随机）
	h2, _ := HashPassword("s3cret-密码")
	if h == h2 {
		t.Fatal("hashes should differ across salts")
	}
}
