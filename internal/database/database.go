package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"library-management/internal/config"
)

// Open 创建并校验共享的 MySQL 连接池。
// 连接上限保护本地数据库免受无限并发影响，Ping 让凭据、服务或数据库不可用时尽早启动失败。
func Open(cfg config.Database, multiStatements bool) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN(multiStatements))
	if err != nil {
		return nil, fmt.Errorf("open mysql connection pool: %w", err)
	}

	// 本地部署刻意使用较小的连接池，避免单个进程占满数据库连接。
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	return db, nil
}
