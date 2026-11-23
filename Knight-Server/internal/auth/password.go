package auth

import (
	"golang.org/x/crypto/bcrypt"
)

// HashPassword 生成 bcrypt 加盐哈希（默认 cost 10）。
// bcrypt 输入上限 72 字节，调用方（HTTP 层）负责限制密码长度。
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword 校验明文密码与哈希是否匹配（常数时间比较）。
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
