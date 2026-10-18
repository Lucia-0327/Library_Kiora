package config

import "testing"

// TestLoadDatabaseUsesDefaults 验证本地开发约定的默认连接地址。
func TestLoadDatabaseUsesDefaults(t *testing.T) {
	t.Setenv("DB_USER", "library_app")
	t.Setenv("DB_PASSWORD", "secret")

	cfg, err := LoadDatabase("")
	if err != nil {
		t.Fatalf("LoadDatabase() error = %v", err)
	}

	if cfg.Host != "127.0.0.1" || cfg.Port != 3306 || cfg.Name != "library_management" {
		t.Fatalf("LoadDatabase() defaults = %+v", cfg)
	}
}

// TestLoadDatabaseRequiresPassword 防止 API 在缺少数据库凭据时意外启动。
func TestLoadDatabaseRequiresPassword(t *testing.T) {
	t.Setenv("DB_USER", "library_app")
	t.Setenv("DB_PASSWORD", "")

	if _, err := LoadDatabase(""); err == nil {
		t.Fatal("LoadDatabase() error = nil, want missing password error")
	}
}
