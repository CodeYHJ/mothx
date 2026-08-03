package session

import (
	"context"
	"database/sql"

	"github.com/startvibecoding/mothx/internal/commondb"
)

// OpenSharedDatabase returns the process-wide shared connection for path.
// Callers must not close the returned connection; CloseDatabases owns it.
func OpenSharedDatabase(path string) (*sql.DB, error) {
	return cachedDB(path)
}

// QueryDatabase runs a read operation through the process-wide shared
// connection for path. Callers must not retain db after fn returns.
func QueryDatabase(path string, fn func(*sql.DB) error) error {
	return commondb.Query(path, EnsureCurrentSchema, fn)
}

// WriteDatabase runs a write operation in one transaction through the
// process-wide shared connection for path.
func WriteDatabase(ctx context.Context, path string, fn func(*sql.Tx) error) error {
	return commondb.Write(ctx, path, EnsureCurrentSchema, fn)
}

// QueryRootDatabase runs a read operation against a session root's shared DB.
func QueryRootDatabase(sessionDir string, fn func(*sql.DB) error) error {
	return QueryDatabase(rootDBPath(sessionDir), fn)
}

// WriteRootDatabase runs a write transaction against a session root's shared DB.
func WriteRootDatabase(ctx context.Context, sessionDir string, fn func(*sql.Tx) error) error {
	return WriteDatabase(ctx, rootDBPath(sessionDir), fn)
}
