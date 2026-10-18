// smoke-user 仅供 tools/smoke.ps1 创建临时馆员账号，避免依赖现有管理员密码。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"library-management/internal/auth"
	"library-management/internal/config"
	"library-management/internal/database"
	"library-management/internal/model"
)

func main() {
	username := strings.TrimSpace(os.Getenv("SMOKE_USERNAME"))
	password := os.Getenv("SMOKE_PASSWORD")
	if username == "" {
		log.Fatal("SMOKE_USERNAME 不能为空")
	}
	if !strings.HasPrefix(username, "smoke_staff_") {
		log.Fatal("SMOKE_USERNAME 必须使用 smoke_staff_ 前缀")
	}
	if err := auth.ValidatePassword(password); err != nil {
		log.Fatalf("临时馆员密码无效: %v", err)
	}

	cfg, err := config.LoadDatabase("")
	if err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(cfg, false)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("生成临时馆员密码哈希失败: %v", err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,is_active) VALUES(?,?,?,TRUE)`, username, hash, model.RoleLibrarian)
	if err != nil {
		log.Fatalf("创建临时馆员失败: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		log.Fatalf("读取临时馆员 ID 失败: %v", err)
	}
	fmt.Printf("已创建临时馆员 %q，ID 为 %d。\n", username, id)
}
