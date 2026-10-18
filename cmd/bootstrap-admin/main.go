package main

import (
	"context"
	"flag"
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
	// 初始化仅允许在首个管理员不存在时执行，后续账号管理统一走 API。
	username := flag.String("username", "admin", "initial administrator username")
	password := flag.String("password", "", "initial administrator password; prefer BOOTSTRAP_ADMIN_PASSWORD")
	flag.Parse()

	// 优先读取环境变量，避免密码暴露在进程命令行中。
	plainPassword := *password
	if plainPassword == "" {
		plainPassword = os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	}
	if strings.TrimSpace(*username) == "" {
		log.Fatal("username is required")
	}
	if err := auth.ValidatePassword(plainPassword); err != nil {
		log.Fatal(err)
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
	// 防止该工具悄悄创建第二个高权限恢复账号。
	var count uint
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role=? AND is_active=TRUE`, model.RoleAdmin).Scan(&count); err != nil {
		log.Fatal(err)
	}
	if count > 0 {
		log.Fatal("an active administrator already exists; use the administrator password reset endpoint instead")
	}

	hash, err := auth.HashPassword(plainPassword)
	if err != nil {
		log.Fatal(err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`, strings.TrimSpace(*username), hash, model.RoleAdmin)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			log.Fatal("username already exists; use an existing administrator to update it")
		}
		log.Fatal(err)
	}
	id, _ := res.LastInsertId()
	log.Printf("initial administrator %q created with ID %d", *username, id)
}
