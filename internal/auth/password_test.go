package auth

import "testing"

// TestHashAndVerifyPassword 确认密码不会以明文保存且错误密码被拒绝。
func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("a-secure-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if hash == "a-secure-password" {
		t.Fatal("hash must not equal the plaintext password")
	}
	if err := VerifyPassword(hash, "a-secure-password"); err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if err := VerifyPassword(hash, "incorrect-password"); err == nil {
		t.Fatal("VerifyPassword() accepted wrong password")
	}
}

// TestValidatePassword 覆盖最短密码策略。
func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Fatal("short password should fail")
	}
	if err := ValidatePassword("valid-password"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}
