// Package dao contains persistence-only data access objects.
//
// DAOs own table names, column mappings, and query construction. Domain
// packages should depend on these methods instead of issuing SQL directly.
package dao

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/uptrace/bun"
)

// CronJobRecord is the database representation of a scheduled job. Times are
// stored as RFC3339 strings to preserve the existing SQLite schema and data.
type CronJobRecord struct {
	bun.BaseModel `bun:"table:cron_jobs"`
	ID            string `bun:"id,pk"`
	SessionID     string `bun:"session_id"`
	Name          string `bun:"name"`
	Prompt        string `bun:"prompt"`
	Schedule      string `bun:"schedule"`
	OneShot       bool   `bun:"oneshot"`
	Mode          string `bun:"mode"`
	WorkDir       string `bun:"work_dir"`
	A2ATarget     string `bun:"a2a_target"`
	A2AToken      string `bun:"a2a_token"`
	Enabled       bool   `bun:"enabled"`
	CreatedAt     string `bun:"created_at"`
	LastRun       string `bun:"last_run"`
	NextRun       string `bun:"next_run"`
	RunCount      int    `bun:"run_count"`
	LastStatus    string `bun:"last_status"`
	LastError     string `bun:"last_error"`
}

// CronDAO provides Bun-backed access to cron_jobs.
type CronDAO struct {
	db *bun.DB
}

func NewCronDAO(db *bun.DB) *CronDAO {
	return &CronDAO{db: db}
}

func (d *CronDAO) List(ctx context.Context) ([]CronJobRecord, error) {
	var records []CronJobRecord
	err := d.db.NewSelect().Model(&records).
		OrderExpr("created_at DESC, id ASC").
		Scan(ctx)
	return records, err
}

func (d *CronDAO) Get(ctx context.Context, id string) (*CronJobRecord, error) {
	record := new(CronJobRecord)
	err := d.db.NewSelect().Model(record).Where("id = ?", id).Limit(1).Scan(ctx)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (d *CronDAO) Create(ctx context.Context, record *CronJobRecord) error {
	if record == nil {
		return fmt.Errorf("cron job record is nil")
	}
	_, err := d.db.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *CronDAO) Update(ctx context.Context, record *CronJobRecord) error {
	if record == nil {
		return fmt.Errorf("cron job record is nil")
	}
	result, err := d.db.NewUpdate().Model(record).
		Column("session_id", "name", "prompt", "schedule", "oneshot", "mode", "work_dir", "a2a_target", "a2a_token", "enabled", "created_at", "last_run", "next_run", "run_count", "last_status", "last_error").
		WherePK().Exec(ctx)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *CronDAO) Delete(ctx context.Context, id string) error {
	result, err := d.db.NewDelete().Model((*CronJobRecord)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ClaimDue atomically claims an enabled job whose schedule is due or whose
// previous running lease has expired.
func (d *CronDAO) ClaimDue(ctx context.Context, id, now, staleBefore string) (bool, error) {
	result, err := d.db.NewUpdate().Model((*CronJobRecord)(nil)).
		Set("last_status = ?", "running").
		Set("last_run = ?", now).
		Set("last_error = ?", "").
		Where("id = ?", id).
		Where("enabled = 1").
		Where("(last_status = 'running' AND last_run != '' AND last_run <= ?) OR (last_status != 'running' AND ((next_run != '' AND next_run <= ?) OR (next_run = '' AND last_run = '')))", staleBefore, now).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
