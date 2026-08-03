package session

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

const currentSchemaVersion = 19

type schemaMigration struct {
	version int
	name    string
	apply   func(*sql.Tx) error
}

var schemaMigrations = []schemaMigration{
	{version: 16, name: "add_channel_binding_columns", apply: func(tx *sql.Tx) error {
		for _, table := range []string{"sessions", "sub_session"} {
			exists, err := tableExists(tx, table)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			if err := addColumnIfMissing(tx, table, "channel_type", "TEXT NOT NULL DEFAULT 'local'"); err != nil {
				return err
			}
			if err := addColumnIfMissing(tx, table, "channel_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_wechat_binding ON sessions(channel_type, channel_id) WHERE channel_type = 'wechat' AND channel_id <> ''`); err != nil {
			return err
		}
		_, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_feishu_binding ON sessions(channel_type, channel_id) WHERE channel_type = 'feishu' AND channel_id <> ''`)
		return err
	}},
	{version: 17, name: "create_session_channel_tools", apply: func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS session_channel_tools (
				session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				tool_name TEXT NOT NULL,
				enabled INTEGER NOT NULL DEFAULT 1,
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (session_id, tool_name)
			)`); err != nil {
			return fmt.Errorf("create session channel tools table: %w", err)
		}
		_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_session_channel_tools_session_id ON session_channel_tools(session_id)`)
		return err
	}},
	{version: 18, name: "create_session_runs", apply: func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS session_runs (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			work_dir TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			started_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			finished_at TEXT,
			error TEXT NOT NULL DEFAULT '',
			usage_json TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
			return fmt.Errorf("create session runs table: %w", err)
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_session_runs_session_id ON session_runs(session_id)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_session_runs_status ON session_runs(status)`); err != nil {
			return err
		}
		_, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_session_runs_active_session ON session_runs(session_id) WHERE status IN ('created', 'queued', 'running', 'cancelling', 'terminalizing')`)
		return err
	}},
	{version: 19, name: "create_response_runtime_tables", apply: func(tx *sql.Tx) error {
		if err := createResponseRuntimeTables(tx); err != nil {
			return err
		}
		return nil
	}},
}

func createResponseRuntimeTables(tx *sql.Tx) error {
	statements := []struct {
		name string
		sql  string
	}{
		{"response_turns", `CREATE TABLE IF NOT EXISTS response_turns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			local_turn_id TEXT NOT NULL,
			message_id INTEGER,
			request_id TEXT,
			response_id TEXT,
			previous_response_id TEXT,
			conversation_id TEXT,
			provider TEXT NOT NULL,
			api TEXT NOT NULL,
			model TEXT NOT NULL,
			state_mode TEXT NOT NULL,
			status TEXT NOT NULL,
			incomplete_reason TEXT,
			request_summary_json BLOB,
			response_summary_json BLOB,
			created_at DATETIME NOT NULL,
			completed_at DATETIME,
			UNIQUE(session_id, local_turn_id)
		)`},
		{"idx_response_turns_session_id", `CREATE INDEX IF NOT EXISTS idx_response_turns_session_id ON response_turns(session_id)`},
		{"idx_response_turns_response_id", `CREATE INDEX IF NOT EXISTS idx_response_turns_response_id ON response_turns(response_id)`},
		{"response_items", `CREATE TABLE IF NOT EXISTS response_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			local_turn_id TEXT NOT NULL,
			response_id TEXT,
			item_id TEXT,
			output_index INTEGER NOT NULL,
			item_type TEXT NOT NULL,
			item_status TEXT,
			sanitized_json BLOB NOT NULL,
			created_at DATETIME NOT NULL
		)`},
		{"idx_response_items_session_turn", `CREATE INDEX IF NOT EXISTS idx_response_items_session_turn ON response_items(session_id, local_turn_id)`},
		{"idx_response_items_response_id", `CREATE INDEX IF NOT EXISTS idx_response_items_response_id ON response_items(response_id)`},
		{"tool_execution_records", `CREATE TABLE IF NOT EXISTS tool_execution_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			local_turn_id TEXT NOT NULL,
			execution_key TEXT NOT NULL,
			provider TEXT NOT NULL,
			api TEXT NOT NULL,
			response_id TEXT,
			provider_call_id TEXT,
			tool_kind TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			args_hash TEXT NOT NULL,
			execution_state TEXT NOT NULL,
			result_summary_json BLOB,
			provider_metadata_json BLOB,
			side_effecting BOOLEAN NOT NULL,
			created_at DATETIME NOT NULL,
			completed_at DATETIME,
			UNIQUE(execution_key)
		)`},
		{"idx_tool_execution_records_session_turn", `CREATE INDEX IF NOT EXISTS idx_tool_execution_records_session_turn ON tool_execution_records(session_id, local_turn_id)`},
		{"idx_tool_execution_records_provider_call", `CREATE INDEX IF NOT EXISTS idx_tool_execution_records_provider_call ON tool_execution_records(provider, api, provider_call_id)`},
		{"response_runs", `CREATE TABLE IF NOT EXISTS response_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			local_run_id TEXT NOT NULL,
			response_id TEXT,
			provider TEXT NOT NULL,
			api TEXT NOT NULL,
			state TEXT NOT NULL,
			polling_url TEXT,
			last_event_sequence INTEGER,
			cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(session_id, local_run_id)
		)`},
		{"idx_response_runs_session_id", `CREATE INDEX IF NOT EXISTS idx_response_runs_session_id ON response_runs(session_id)`},
		{"idx_response_runs_state", `CREATE INDEX IF NOT EXISTS idx_response_runs_state ON response_runs(state)`},
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt.sql); err != nil {
			return fmt.Errorf("create %s: %w", stmt.name, err)
		}
	}
	return nil
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
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureSchemaMigrationsTable(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		columns, err := tableColumns(tx, "schema_migrations")
		if err != nil {
			return err
		}
		if columns["name"] && columns["applied_at"] && !columns["version"] {
			if _, err := tx.Exec(`ALTER TABLE schema_migrations ADD COLUMN version INTEGER`); err != nil {
				return err
			}
		} else if !columns["version"] || !columns["name"] || !columns["applied_at"] {
			legacy := "schema_migrations_legacy_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			if _, err := tx.Exec("ALTER TABLE schema_migrations RENAME TO " + legacy); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	return tx.Commit()
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
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
		var byVersion, byName int
		if err := tx.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", migration.version).Scan(&byVersion); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", migration.name).Scan(&byName); err != nil {
			tx.Rollback()
			return err
		}
		if byVersion != 0 || byName != 0 {
			if byVersion == 0 {
				if _, err := tx.Exec("UPDATE schema_migrations SET version = ? WHERE name = ? AND version IS NULL", migration.version, migration.name); err != nil {
					tx.Rollback()
					return err
				}
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			continue
		}
		if err := migration.apply(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply schema migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)", migration.version, migration.name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
