package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/uptrace/bun"
)

func TestOpenCachesBunConnectionAndRunsTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "test.db")
	migrate := func(sqlDB *sql.DB) error {
		_, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS db_test_values (value TEXT NOT NULL)`)
		return err
	}
	first, err := Open(path, migrate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(filepath.Join(filepath.Dir(path), ".", filepath.Base(path)), migrate)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Open returned separate connections for the same canonical path")
	}

	if err := Write(context.Background(), path, migrate, func(_ context.Context, tx bun.Tx) error {
		row := struct {
			bun.BaseModel `bun:"table:db_test_values"`
			Value         string `bun:"value"`
		}{Value: "bun"}
		_, err := tx.NewInsert().Model(&row).Exec(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := first.NewSelect().Table("db_test_values").Column("value").Limit(1).Scan(context.Background(), &value); err != nil {
		t.Fatal(err)
	}
	if value != "bun" {
		t.Fatalf("value = %q, want bun", value)
	}
	if err := CloseAll(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, migrate)
	if err != nil {
		t.Fatal(err)
	}
	if reopened == first {
		t.Fatal("CloseAll returned the closed cached connection")
	}
	if err := CloseAll(); err != nil {
		t.Fatal(err)
	}
}
