package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"library-management/internal/api"
	"library-management/internal/config"
	"library-management/internal/database"
)

func main() {
	// 使用权限最小化的应用账号；配置无效时拒绝启动。
	dbConfig, err := config.LoadDatabase("")
	if err != nil {
		log.Fatal(err)
	}

	db, err := database.Open(dbConfig, false)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 显式设置 HTTP 超时，防止慢客户端无限占用连接。
	server := &http.Server{
		Addr:              config.ListenAddress(),
		Handler:           api.NewServer(db).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 主协程等待 Ctrl+C 时，通过通道接收意外的监听失败。
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("API listening on http://%s", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	// 关闭时最多给正在处理的请求十秒完成时间。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}
}
