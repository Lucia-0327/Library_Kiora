package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// migrationFilePattern 仅匹配带版本号的正向迁移文件。
// down 迁移仅用于人工恢复，应用程序不会自动执行它们。
var migrationFilePattern = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.up\.sql$`)

// Migration 表示从单个 .up.sql 文件读取的一项有序 SQL 变更。
type Migration struct {
	Version string
	Name    string
	SQL     string
}

// Apply 按版本字典序执行所有尚未应用的 .up.sql 迁移。
// SQL 成功后才记录版本，因此失败的迁移可以在修复后重试。
func Apply(ctx context.Context, db *sql.DB, directory string) error {
	if err := ensureTrackingTable(ctx, db); err != nil {
		return err
	}

	migrations, err := read(directory)
	if err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}

		if _, err := db.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply migration %s_%s: %w", migration.Version, migration.Name, err)
		}

		if _, err := db.ExecContext(
			ctx,
			`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
			migration.Version,
			migration.Name,
		); err != nil {
			return fmt.Errorf("record migration %s_%s: %w", migration.Version, migration.Name, err)
		}
	}

	return nil
}

// ensureTrackingTable 在读取已应用版本前创建迁移台账。
func ensureTrackingTable(ctx context.Context, db *sql.DB) error {
	const statement = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

// read 校验迁移文件名、拒绝重复版本，并返回排好序的 SQL 列表。
func read(directory string) ([]Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory %q: %w", directory, err)
	}

	var migrations []Migration
	seenVersions := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}

		version, name := matches[1], matches[2]
		if seenVersions[version] {
			return nil, fmt.Errorf("duplicate migration version %q", version)
		}
		seenVersions[version] = true

		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}

		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			SQL:     string(contents),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// appliedVersions 将持久化迁移台账转换为可快速查询的版本集合。
func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("load applied migrations: %w", err)
	}
	defer rows.Close()

	versions := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		versions[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}

	return versions, nil
}
