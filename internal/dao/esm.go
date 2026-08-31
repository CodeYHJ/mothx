package dao

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/uptrace/bun"
)

// ESMObjectiveRecord is the persistence representation of one supervised
// objective. JSON and timestamp values intentionally remain strings to match
// the existing SQLite schema and replay format.
type ESMObjectiveRecord struct {
	bun.BaseModel `bun:"table:session_esm_objectives"`
	SessionID     string `bun:"session_id,pk"`
	ESMID         string `bun:"esm_id"`
	Objective     string `bun:"objective"`
	Status        string `bun:"status"`
	TokenBudget   *int64 `bun:"token_budget"`
	TokensUsed    int64  `bun:"tokens_used"`
	TimeUsedMS    int64  `bun:"time_used_ms"`
	BlockedCount  int    `bun:"blocked_count"`
	BlockedReason string `bun:"blocked_reason"`
	BlockedRunID  string `bun:"blocked_run_id"`

	CompletionReason string `bun:"completion_reason"`
	CompletionRunID  string `bun:"completion_run_id"`
	CompletionReview string `bun:"completion_review"`
	Phase            string `bun:"phase"`
	ProgressSummary  string `bun:"progress_summary"`
	RemainingWork    string `bun:"remaining_work"`

	RejectionCount int    `bun:"completion_rejection_count"`
	RejectionRunID string `bun:"completion_rejection_run_id"`
	RecoveryCount  int    `bun:"recovery_count"`
	RecoveryReason string `bun:"recovery_reason"`
	CreatedAt      string `bun:"created_at"`
	UpdatedAt      string `bun:"updated_at"`
}

// ESMDAO provides Bun-backed access to session_esm_objectives.
type ESMDAO struct {
	db *bun.DB
}

func NewESMDAO(db *bun.DB) *ESMDAO {
	return &ESMDAO{db: db}
}

func (d *ESMDAO) Get(ctx context.Context, sessionID string) (*ESMObjectiveRecord, error) {
	return d.GetFrom(ctx, d.db, sessionID)
}

func (d *ESMDAO) GetFrom(ctx context.Context, executor bun.IDB, sessionID string) (*ESMObjectiveRecord, error) {
	if executor == nil {
		return nil, fmt.Errorf("esm database executor is nil")
	}
	record := new(ESMObjectiveRecord)
	err := executor.NewSelect().Model(record).Where("session_id = ?", sessionID).Limit(1).Scan(ctx)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (d *ESMDAO) Insert(ctx context.Context, executor bun.IDB, record *ESMObjectiveRecord) error {
	if executor == nil {
		return fmt.Errorf("esm database executor is nil")
	}
	if record == nil || record.SessionID == "" {
		return fmt.Errorf("esm objective record is invalid")
	}
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *ESMDAO) Update(ctx context.Context, executor bun.IDB, record *ESMObjectiveRecord) error {
	if executor == nil {
		return fmt.Errorf("esm database executor is nil")
	}
	if record == nil || record.SessionID == "" {
		return fmt.Errorf("esm objective record is invalid")
	}
	result, err := executor.NewUpdate().Model(record).
		Column("esm_id", "objective", "status", "token_budget", "tokens_used", "time_used_ms",
			"blocked_count", "blocked_reason", "blocked_run_id", "completion_reason", "completion_run_id",
			"completion_review", "phase", "progress_summary", "remaining_work", "completion_rejection_count",
			"completion_rejection_run_id", "recovery_count", "recovery_reason", "updated_at").
		Where("session_id = ?", record.SessionID).Exec(ctx)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *ESMDAO) Delete(ctx context.Context, executor bun.IDB, sessionID string) error {
	if executor == nil {
		return fmt.Errorf("esm database executor is nil")
	}
	_, err := executor.NewDelete().Model((*ESMObjectiveRecord)(nil)).Where("session_id = ?", sessionID).Exec(ctx)
	return err
}

func (d *ESMDAO) ListRunnable(ctx context.Context) ([]string, error) {
	var sessionIDs []string
	err := d.db.NewSelect().Table("session_esm_objectives").Column("session_id").
		Where("status IN (?, ?)", "active", "complete_candidate").OrderExpr("session_id").Scan(ctx, &sessionIDs)
	return sessionIDs, err
}
