// Package commondb owns the process-wide SQLite connection lifecycle.
package commondb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
)

type Migrator func(*sql.DB) error

var state = struct {
	sync.Mutex
	dbs map[string]*sql.DB
}{dbs: make(map[string]*sql.DB)}

func CanonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	return abs, nil
}

func Open(path string, migrate Migrator) (*sql.DB, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	state.Lock()
	defer state.Unlock()
	if db := state.dbs[canonical]; db != nil {
		return db, nil
	}
	db, err := open(canonical, migrate)
	if err != nil {
		return nil, err
	}
	state.dbs[canonical] = db
	return db, nil
}

// OpenStandalone opens an uncached connection for tools that explicitly own
// its lifecycle, such as offline integrity checks.
func OpenStandalone(path string, migrate Migrator) (*sql.DB, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	return open(canonical, migrate)
}

func Query(path string, migrate Migrator, fn func(*sql.DB) error) error {
	db, err := Open(path, migrate)
	if err != nil {
		return err
	}
	return fn(db)
}

func Write(ctx context.Context, path string, migrate Migrator, fn func(*sql.Tx) error) error {
	db, err := Open(path, migrate)
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

func CloseAll() error {
	state.Lock()
	defer state.Unlock()
	var errs []error
	for path, db := range state.dbs {
		var busy, logFrames, checkpointed int
		if err := db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
			errs = append(errs, fmt.Errorf("checkpoint %s: %w", path, err))
		}
		if err := db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", path, err))
		}
		delete(state.dbs, path)
	}
	return errors.Join(errs...)
}

func open(path string, migrate Migrator) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sqlite connection: %w", err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("run sqlite integrity check: %w", err)
		}
		return nil, fmt.Errorf("sqlite integrity check failed: %s", integrity)
	}
	if err := enableWAL(db); err != nil {
		db.Close()
		return nil, err
	}
	if migrate != nil {
		if err := migrate(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply database migration: %w", err)
		}
	}
	return db, nil
}

func dsn(path string) string {
	return DSNForOS(path, runtime.GOOS == "windows")
}

// DSNForOS returns the configured SQLite file URI. It is exported for
// platform-specific integration tests.
func DSNForOS(path string, windows bool) string {
	uriPath := filepath.ToSlash(path)
	if windows && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	q.Set("_dqs", "false")
	u.RawQuery = q.Encode()
	return u.String()
}

func enableWAL(db *sql.DB) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		var mode string
		err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil {
			if strings.EqualFold(mode, "wal") {
				return nil
			}
			return fmt.Errorf("sqlite journal mode is %q, want WAL", mode)
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || (sqliteErr.Code()&0xff != 5 && sqliteErr.Code()&0xff != 6) || time.Now().After(deadline) {
			return fmt.Errorf("enable sqlite WAL mode: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
