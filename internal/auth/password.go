package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 在本地登录延迟和离线破解防护之间取得平衡。
// bcrypt 会把成本写入每条哈希，因此日后提高成本不需要修改数据表。
const bcryptCost = 12

// HashPassword 先校验明文密码，再生成 bcrypt 哈希。
// 明文密码绝不能持久化或写入日志。
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 比较输入密码与已存储的 bcrypt 哈希。
// 比较由 bcrypt 实现完成，不会暴露哈希内容。
func VerifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// ValidatePassword 统一执行账号创建、自助修改和管理员强制重置所用的密码策略。
func ValidatePassword(password string) error {
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}
	if len(strings.TrimSpace(password)) == 0 {
		return fmt.Errorf("password must not be blank")
	}
	return nil
}

// NewToken 返回交给客户端的令牌和用于存储的 SHA-256 摘要。
// MySQL 仅保存摘要，避免数据库泄露直接变成可用会话泄露。
func NewToken() (rawToken, tokenHash string, err error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	rawToken = hex.EncodeToString(bytes)
	sum := sha256.Sum256([]byte(rawToken))
	return rawToken, hex.EncodeToString(sum[:]), nil
}

// TokenHash 为请求中的令牌生成与数据库一致的查询摘要。
func TokenHash(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
