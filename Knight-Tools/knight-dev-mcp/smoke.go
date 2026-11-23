package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// toolSmoke 对运行中的 knight-server 跑 HTTP 账号冒烟，走 internal/httpapi 契约
// （Docs/protocol.md §3）：GET /healthz → POST /api/v1/register → POST /api/v1/login →
// GET /api/v1/users/{uid}（Bearer）。默认地址 127.0.0.1:37780（容器栈映射的 HTTP 端口）。
// 部署工具跑完后用它确认服务与数据库可用。
func toolSmoke(args map[string]any) (string, error) {
	addr := argString(args, "addr", "127.0.0.1:37780")
	timeoutSec := argNumber(args, "timeout_sec", 60)
	base := normalizeHTTPBase(addr)
	client := &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))}

	start := time.Now()
	output, ok := runHTTPSmoke(base, client)
	elapsed := time.Since(start).Seconds()

	var sb strings.Builder
	if ok {
		fmt.Fprintf(&sb, "冒烟通过（%.1fs，%s）\n%s", elapsed, base, output)
		return sb.String(), nil
	}
	fmt.Fprintf(&sb, "冒烟失败（%.1fs，%s）\n%s", elapsed, base, output)
	sb.WriteString("\n提示：确认容器栈在运行（deploy 工具），HTTP 默认 127.0.0.1:37780（compose 映射）；" +
		"healthz 报 db down 是 MySQL 未就绪，register 报 internal 通常是容器内二进制与数据库 schema 不匹配，需重新 deploy。\n")
	return sb.String(), nil
}

// runHTTPSmoke 顺序执行四步冒烟，逐步输出 ✓/✗；任一步失败即止，返回输出与整体结论。
func runHTTPSmoke(base string, client *http.Client) (string, bool) {
	var sb strings.Builder
	ok := true
	step := func(format string, a ...any) { fmt.Fprintf(&sb, "✓ "+format+"\n", a...) }
	fail := func(format string, a ...any) { fmt.Fprintf(&sb, "✗ "+format+"\n", a...); ok = false }

	// 1) healthz：服务存活 + MySQL 连通（注册/登录依赖数据库，db down 直接止步）。
	var hz smokeHealthz
	code, srvErr, err := smokeCall(client, http.MethodGet, base+"/healthz", "", nil, &hz)
	switch {
	case err != nil:
		fail("healthz: %v", err)
	case code != http.StatusOK || hz.Status != "ok" || hz.DB != "up":
		reason := hz.Error
		if reason == "" {
			reason = srvErr
		}
		fail("healthz: HTTP %d status=%s db=%s %s", code, hz.Status, hz.DB, reason)
	default:
		step("healthz: 服务 ok，数据库 up")
	}
	if !ok {
		return sb.String(), false
	}

	// 2) register：唯一用户名（纳秒后缀）避开历史数据冲突。
	suffix := time.Now().UnixNano()
	name := fmt.Sprintf("smoke-%d", suffix)
	password := fmt.Sprintf("smoke-pass-%d", suffix)
	var reg smokeAuthResp
	body := fmt.Sprintf(`{"name":%q,"password":%q}`, name, password)
	code, srvErr, err = smokeCall(client, http.MethodPost, base+"/api/v1/register", body, nil, &reg)
	switch {
	case err != nil:
		fail("register: %v", err)
	case code == http.StatusConflict:
		fail("register: 用户名冲突（%s）——冒烟用户名按纳秒生成，冲突说明时钟回拨，重试即可", name)
	case code != http.StatusOK || reg.UID == "":
		fail("register: HTTP %d %s uid=%q", code, srvErr, reg.UID)
	default:
		step("register: uid=%s", reg.UID)
	}
	if !ok {
		return sb.String(), false
	}

	// 3) login：同凭据换 token，uid 必须与注册一致。
	var login smokeAuthResp
	code, srvErr, err = smokeCall(client, http.MethodPost, base+"/api/v1/login", body, nil, &login)
	switch {
	case err != nil:
		fail("login: %v", err)
	case code != http.StatusOK || login.Token == "":
		fail("login: HTTP %d %s", code, srvErr)
	case login.UID != reg.UID:
		fail("login: uid=%s 与注册 uid=%s 不一致", login.UID, reg.UID)
	default:
		step("login: uid=%s name=%s token 已签发", login.UID, login.Name)
	}
	if !ok {
		return sb.String(), false
	}

	// 4) Bearer 查用户：token 有效 + 显示名与登录一致。
	var user smokeUserResp
	header := map[string]string{"Authorization": "Bearer " + login.Token}
	code, srvErr, err = smokeCall(client, http.MethodGet, base+"/api/v1/users/"+login.UID, "", header, &user)
	switch {
	case err != nil:
		fail("users/{uid}: %v", err)
	case code != http.StatusOK:
		fail("users/{uid}: HTTP %d %s", code, srvErr)
	case user.Name != login.Name:
		fail("users/{uid}: name=%q 与登录 name=%q 不一致", user.Name, login.Name)
	default:
		step("users/{uid}: name=%s ✓", user.Name)
	}
	return sb.String(), ok
}

// smokeHealthz /healthz 响应体（error 字段仅 degraded 时出现）。
type smokeHealthz struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Error  string `json:"error"`
}

// smokeAuthResp register/login 响应体（uid 字符串化，超 2^53 雪花 ID）。
type smokeAuthResp struct {
	UID   string `json:"uid"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// smokeUserResp GET /users/{uid} 响应体的冒烟关注字段（coins/created_at 忽略）。
type smokeUserResp struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// smokeCall 执行一次 HTTP 请求并把响应体解析进 out（无论状态码，out 可为 nil）。
// 返回：状态码；srvErr 为服务端业务错误描述（非 2xx 时取 {"error": ...} 或原始体前缀，2xx 时为空）；
// err 仅代表传输/解析层面的失败。
func smokeCall(client *http.Client, method, url, body string, header map[string]string, out any) (code int, srvErr string, err error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	code = resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return code, "", err
	}
	if out != nil && len(data) > 0 {
		if uerr := json.Unmarshal(data, out); uerr != nil && code >= 200 && code <= 299 {
			return code, "", uerr
		}
	}
	if code < 200 || code > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return code, e.Error, nil
		}
		return code, fmt.Sprintf("%.200s", string(data)), nil
	}
	return code, "", nil
}

// normalizeHTTPBase 把 host:port 或完整 URL 统一成不带尾斜杠的 http base URL。
func normalizeHTTPBase(addr string) string {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}
