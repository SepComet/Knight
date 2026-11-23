package session

import (
	"fmt"
	"hash/fnv"
)

// DenyAllValidator 全部拒绝：本阶段尚无登录接口与 token 签发方，
// 默认行为是诚实地拒绝所有握手（ERR_INVALID_TOKEN）。
type DenyAllValidator struct{}

// Validate 实现 TokenValidator。
func (DenyAllValidator) Validate(string) (uint64, string, bool) { return 0, "", false }

// DevBypassValidator 开发后门：接受任意非空 token。
// uid 由 token 哈希派生、name 带 dev 前缀——仅用于登录接口落地前的链路联调
// （KNIGHT_DEV_TOKEN_BYPASS=1 时由 main 启用）。派生的账号不落库，与真实账号体系无关。
type DevBypassValidator struct{}

// Validate 实现 TokenValidator。
func (v DevBypassValidator) Validate(token string) (uint64, string, bool) {
	if token == "" {
		return 0, "", false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	uid := h.Sum64()
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return uid, fmt.Sprintf("dev-%s", prefix), true
}
