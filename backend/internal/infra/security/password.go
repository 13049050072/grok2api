package security

import "golang.org/x/crypto/bcrypt"

// 管理员密码的有效字节长度。MaxPasswordBytes 是 bcrypt 可处理的上限，更长的密码会被
// HashPassword 拒绝。
const (
	MinPasswordBytes = 8
	MaxPasswordBytes = 72
)

// HashPassword 使用 bcrypt 生成管理员密码哈希。
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// VerifyPassword 校验管理员密码。
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
