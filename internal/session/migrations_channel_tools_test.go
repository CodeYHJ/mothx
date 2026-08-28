package session

import (
	"path/filepath"
	"testing"
)

func TestChannelToolMigrationsCleanOrphansAndReopen(t *testing.T) {
	sessionDir := t.TempDir()
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		t.Fatalf("open current schema: %v", err)
	}
	defer CloseDatabases()

	// Simulate an unpublished v20 database before migrations 21/22 run.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 21`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE session_channel_tool_generations`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_channel_tools(session_id, tool_name, enabled) VALUES ('orphan', 'browser', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := applySchemaMigrations(db.Bun().DB); err != nil {
		t.Fatalf("apply 21/22: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_channel_tools WHERE session_id = 'orphan'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphan channel tools = %d, want 0", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_channel_tool_generations'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("generation table was not recreated")
	}

	if err := CloseDatabases(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRootDB(filepath.Join(sessionDir, "."))
	if err != nil {
		t.Fatalf("reopen migrated schema: %v", err)
	}
	defer CloseDatabases()
	if err := reopened.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", count, currentSchemaVersion)
	}
}
