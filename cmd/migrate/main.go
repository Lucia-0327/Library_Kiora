package main

import (
	"context"
	"flag"
	"log"
	"time"

	"library-management/internal/config"
	"library-management/internal/database"
	"library-management/internal/migration"
)

func main() {
	// 目录参数使迁移可在检出的项目目录或 CI 工作区中运行。
	directory := flag.String("dir", "migrations", "directory containing .up.sql migration files")
	flag.Parse()

	// 迁移使用独立账号，因为 DDL 所需权限高于普通 API。
	dbConfig, err := config.LoadDatabase("MIGRATION_")
	if err != nil {
		log.Fatal(err)
	}

	db, err := database.Open(dbConfig, true)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migration.Apply(ctx, db, *directory); err != nil {
		log.Fatal(err)
	}

	log.Println("migrations are up to date")
}
