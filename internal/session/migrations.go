package session

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

const currentSchemaVersion = 16

type schemaMigration struct {
	version int
	name    string
	apply   func(*sql.Tx) error
}

var schemaMigrations = []schemaMigration{
	{
		version: currentSchemaVersion,
		name:    "add_channel_binding_columns",
		apply: func(tx *sql.Tx) error {
			for _, table := range []string{"sessions", "sub_session"} {
				if exists, err := tableExists(tx, table); err != nil {
					return err
				} else if !exists {
					continue
				}
				if err := addColumnIfMissing(tx, table, "channel_type", "TEXT NOT NULL DEFAULT 'local'"); err != nil {
					return err
				}
				if err := addColumnIfMissing(tx, table, "channel_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_wechat_binding
			ON sessions(channel_type, channel_id)
			WHERE channel_type = 'wechat' AND channel_id <> ''`); err != nil {
				return fmt.Errorf("create wechat binding index: %w", err)
			}
			if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_feishu_binding
			ON sessions(channel_type, channel_id)
			WHERE channel_type = 'feishu' AND channel_id <> ''`); err != nil {
				return fmt.Errorf("create feishu binding index: %w", err)
			}
			return nil
		},
	},
}

func tableExists(tx *sql.Tx, table string) (bool, error) {
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	return count != 0, nil
}

func addColumnIfMissing(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("scan %s schema: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s schema: %w", table, err)
	}
	if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureSchemaMigrationsTable(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema migrations setup: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect schema migrations table: %w", err)
	}
	if exists != 0 {
		columns, err := tableColumns(tx, "schema_migrations")
		if err != nil {
			return err
		}
		// Databases created by the previous migration runner use
		// (name, applied_at). Preserve that history and add the numeric
		// version column used by the current runner.
		if columns["name"] && columns["applied_at"] && !columns["version"] {
			if _, err := tx.Exec(`ALTER TABLE schema_migrations ADD COLUMN version INTEGER`); err != nil {
				return fmt.Errorf("upgrade legacy schema migrations table: %w", err)
			}
			columns["version"] = true
		}
		if !columns["version"] || !columns["name"] || !columns["applied_at"] {
			legacy := "schema_migrations_legacy_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			if _, err := tx.Exec("ALTER TABLE schema_migrations RENAME TO " + legacy); err != nil {
				return fmt.Errorf("rename incompatible schema migrations table: %w", err)
			}
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	return tx.Commit()
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func applySchemaMigrations(db *sql.DB) error {
	if err := ensureSchemaMigrationsTable(db); err != nil {
		return err
	}
	for _, migration := range schemaMigrations {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin schema migration %d: %w", migration.version, err)
		}
		var appliedByVersion, appliedByName int
		err = tx.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migration.version).Scan(&appliedByVersion)
		if err == nil {
			err = tx.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", migration.name).Scan(&appliedByName)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check schema migration %d: %w", migration.version, err)
		}
		if appliedByVersion != 0 || appliedByName != 0 {
			// Legacy migration tables tracked applications by name only. Backfill
			// the numeric version when the name is already present, so future
			// opens can use the versioned lookup without a duplicate-name insert.
			if appliedByVersion == 0 && appliedByName != 0 {
				if _, err := tx.Exec("UPDATE schema_migrations SET version = ? WHERE name = ? AND version IS NULL", migration.version, migration.name); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("backfill schema migration %d: %w", migration.version, err)
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("finish schema migration %d: %w", migration.version, err)
			}
			continue
		}
		if err := migration.apply(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply schema migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)", migration.version, migration.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d: %w", migration.version, err)
		}
	}
	return nil
}
