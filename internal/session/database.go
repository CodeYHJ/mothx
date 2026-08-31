package session

import (
	"context"

	"github.com/startvibecoding/mothx/internal/dao"
	database "github.com/startvibecoding/mothx/internal/db"
)

// OpenBunDatabase returns the process-wide Bun connection for path. New data
// access code should use this entry point and put queries in a DAO.
func OpenBunDatabase(path string) (*dao.Database, error) {
	db, err := database.Open(path, EnsureCurrentSchema)
	if err != nil {
		return nil, err
	}
	return dao.WrapDatabase(db), nil
}

// RootDatabasePath returns the shared sessions.db path for a session root.
// Keeping path derivation here prevents adapters and DAOs from duplicating
// session directory rules.
func RootDatabasePath(sessionDir string) string {
	return rootDBPath(sessionDir)
}

// QueryRootDatabase runs a read operation against a session root's DAO-owned
// database. The callback must not retain the handle after it returns.
func QueryRootDatabase(sessionDir string, fn func(*dao.Database) error) error {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	return fn(db)
}

// WriteRootDatabase runs a write transaction against a session root's DAO-owned
// database. The callback receives the Bun transaction wrapper.
func WriteRootDatabase(ctx context.Context, sessionDir string, fn func(*dao.Tx) error) error {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
