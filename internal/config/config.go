package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Database 保存 API 或迁移账号连接 MySQL 所需的配置。
// 凭据仅从进程环境变量读取，不能写入源码仓库。
type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
}

// LoadDatabase 读取 DB_* 环境变量；prefix 用于区分 API 和迁移账号。
func LoadDatabase(prefix string) (Database, error) {
	port, err := strconv.Atoi(getenv(prefix+"DB_PORT", "3306"))
	if err != nil || port < 1 || port > 65535 {
		return Database{}, fmt.Errorf("%sDB_PORT must be a valid TCP port", prefix)
	}

	cfg := Database{
		Host:     getenv(prefix+"DB_HOST", "127.0.0.1"),
		Port:     port,
		Name:     getenv(prefix+"DB_NAME", "library_management"),
		User:     os.Getenv(prefix + "DB_USER"),
		Password: os.Getenv(prefix + "DB_PASSWORD"),
	}

	if cfg.User == "" {
		return Database{}, fmt.Errorf("%sDB_USER is required", prefix)
	}
	if cfg.Password == "" {
		return Database{}, fmt.Errorf("%sDB_PASSWORD is required", prefix)
	}

	return cfg, nil
}

// DSN 生成带网络超时的 MySQL 驱动连接串。
// 仅迁移程序允许多语句执行，正常 API 请求始终关闭该能力。
func (c Database) DSN(multiStatements bool) string {
	return (&mysql.Config{
		User:            c.User,
		Passwd:          c.Password,
		Net:             "tcp",
		Addr:            net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		DBName:          c.Name,
		ParseTime:       true,
		Loc:             time.Local,
		Timeout:         5 * time.Second,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		MultiStatements: multiStatements,
		// 本地 MySQL 8.4 为兼容已有本地账号保留 native-password 登录支持。
		AllowNativePasswords: true,
	}).FormatDSN()
}

// ListenAddress 返回 HTTP 服务监听的本地地址。
func ListenAddress() string {
	return getenv("APP_ADDR", "127.0.0.1:8080")
}

// getenv 读取可选环境变量；未设置时使用已约定的默认值。
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
