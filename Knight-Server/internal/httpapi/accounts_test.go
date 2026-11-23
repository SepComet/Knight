package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAccounts 可编程的账号业务假实现。
type fakeAccounts struct {
	registerErr error
	loginErr    error
	user        AccountUser
	userErr     error
	matches     []MatchEntry

	registered [][2]string // 记录调用入参
}

func (f *fakeAccounts) Register(_ context.Context, name, password string) (uint64, error) {
	f.registered = append(f.registered, [2]string{name, password})
	if f.registerErr != nil {
		return 0, f.registerErr
	}
	return 7, nil
}

func (f *fakeAccounts) Login(_ context.Context, _, _ string) (uint64, string, string, error) {
	if f.loginErr != nil {
		return 0, "", "", f.loginErr
	}
	return 7, "alice", "tok-123", nil
}

func (f *fakeAccounts) User(_ context.Context, uid uint64) (AccountUser, error) {
	if f.userErr != nil {
		return AccountUser{}, f.userErr
	}
	return AccountUser{UID: uid, Name: "alice", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, nil
}

func (f *fakeAccounts) Matches(_ context.Context, _ uint64) ([]MatchEntry, error) {
	return f.matches, nil
}

type fakeTokens struct{ ok bool }

func (f fakeTokens) Lookup(string) (uint64, string, bool) { return 7, "alice", f.ok }

func newAccountsServer(t *testing.T, acc *fakeAccounts, tok TokenLookup) http.Handler {
	t.Helper()
	return NewServer(nil, acc, tok).Handler()
}

func postJSON(t *testing.T, h http.Handler, path, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getJSON(t *testing.T, h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegisterSuccess(t *testing.T) {
	acc := &fakeAccounts{}
	h := newAccountsServer(t, acc, fakeTokens{ok: true})
	rec := postJSON(t, h, "/api/v1/register", `{"name":"alice","password":"pw123456"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	// uid 为雪花 ID：JSON 序列化为字符串
	if got := rec.Body.String(); !strings.Contains(got, `"uid":"7"`) {
		t.Fatalf("body = %q", got)
	}
	if len(acc.registered) != 1 || acc.registered[0][0] != "alice" {
		t.Fatalf("registered = %+v", acc.registered)
	}
}

func TestRegisterNameTaken(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{registerErr: ErrNameTaken}, fakeTokens{ok: true})
	rec := postJSON(t, h, "/api/v1/register", `{"name":"alice","password":"pw"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{}, fakeTokens{ok: true})
	for _, body := range []string{
		`{"name":"","password":"pw"}`,
		`{"name":"a","password":""}`,
		`not-json`,
		`{"name":"` + strings.Repeat("x", 65) + `","password":"pw"}`,
	} {
		rec := postJSON(t, h, "/api/v1/register", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: code = %d, want 400", body, rec.Code)
		}
	}
}

func TestLoginSuccessAndFailure(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{}, fakeTokens{ok: true})

	rec := postJSON(t, h, "/api/v1/login", `{"name":"alice","password":"pw"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["uid"].(string) != "7" || resp["name"] != "alice" || resp["token"] != "tok-123" {
		t.Fatalf("resp = %v", resp)
	}

	h2 := newAccountsServer(t, &fakeAccounts{loginErr: ErrBadCredentials}, fakeTokens{ok: true})
	rec = postJSON(t, h2, "/api/v1/login", `{"name":"alice","password":"bad"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestUserRequiresBearer(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{}, fakeTokens{ok: false})
	rec := getJSON(t, h, "/api/v1/users/7", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header: code = %d", rec.Code)
	}

	rec = getJSON(t, h, "/api/v1/users/7", map[string]string{"Authorization": "Bearer bad"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: code = %d", rec.Code)
	}
}

func TestUserSuccess(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{}, fakeTokens{ok: true})
	hdr := map[string]string{"Authorization": "Bearer tok"}
	rec := getJSON(t, h, "/api/v1/users/7", hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var u AccountUser
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if u.UID != 7 || u.Name != "alice" || !u.CreatedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("user = %+v", u)
	}
}

func TestUserNotFound(t *testing.T) {
	h := newAccountsServer(t, &fakeAccounts{userErr: ErrAccountNotFound}, fakeTokens{ok: true})
	hdr := map[string]string{"Authorization": "Bearer tok"}
	if rec := getJSON(t, h, "/api/v1/users/99", hdr); rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestMatchesListAndNotFound(t *testing.T) {
	hdr := map[string]string{"Authorization": "Bearer tok"}

	// 有战绩（含角色/武器快照；match_id 字符串化）
	h := newAccountsServer(t, &fakeAccounts{matches: []MatchEntry{{
		MatchID: 1, Outcome: 1, DurationSec: 120, CharacterID: 3, WeaponID: 9,
		Kills: 3, DamageDealt: 300, DamageTaken: 80, Alive: true,
		FinishedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}, fakeTokens{ok: true})
	rec := getJSON(t, h, "/api/v1/users/7/matches", hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"match_id":"1"`) ||
		!strings.Contains(rec.Body.String(), `"outcome":1`) ||
		!strings.Contains(rec.Body.String(), `"character_id":3`) ||
		!strings.Contains(rec.Body.String(), `"weapon_id":9`) {
		t.Fatalf("body = %q", rec.Body)
	}

	// 无战绩 → 200 []
	h2 := newAccountsServer(t, &fakeAccounts{}, fakeTokens{ok: true})
	rec = getJSON(t, h2, "/api/v1/users/7/matches", hdr)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("code = %d body = %q", rec.Code, rec.Body)
	}

	// 用户不存在 → 404
	h3 := newAccountsServer(t, &fakeAccounts{userErr: ErrAccountNotFound}, fakeTokens{ok: true})
	rec = getJSON(t, h3, "/api/v1/users/99/matches", hdr)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAccountsNotWired(t *testing.T) {
	h := NewServer(nil, nil, nil).Handler()
	rec := postJSON(t, h, "/api/v1/register", `{"name":"a","password":"b"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rec.Code)
	}
}
