package dao

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type RuntimeSubmissionRecord struct {
	bun.BaseModel      `bun:"table:runtime_submissions"`
	ID                 string `bun:"id,pk"`
	SessionID          string `bun:"session_id"`
	Scope              string `bun:"scope"`
	KeyHash            string `bun:"key_hash"`
	RequestFingerprint string `bun:"request_fingerprint"`
	IntentID           string `bun:"intent_id"`
	RunID              string `bun:"run_id"`
	CreatedAt          string `bun:"created_at"`
}

type RuntimeSubmissionDAO struct{ db *bun.DB }

func NewRuntimeSubmissionDAO(db *bun.DB) *RuntimeSubmissionDAO {
	return &RuntimeSubmissionDAO{db: db}
}

func (d *RuntimeSubmissionDAO) Find(ctx context.Context, executor bun.IDB, sessionID, scope, keyHash string) (RuntimeSubmissionRecord, error) {
	var record RuntimeSubmissionRecord
	err := executor.NewSelect().Model(&record).
		Where("session_id = ? AND scope = ? AND key_hash = ?", sessionID, scope, keyHash).
		Limit(1).Scan(ctx)
	return record, err
}

func (d *RuntimeSubmissionDAO) Insert(ctx context.Context, executor bun.IDB, record *RuntimeSubmissionRecord) error {
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func IsNoRows(err error) bool { return err == sql.ErrNoRows }
