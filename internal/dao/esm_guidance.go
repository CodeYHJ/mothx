package dao

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type ESMGuidanceRecord struct {
	bun.BaseModel    `bun:"table:session_esm_guidance"`
	ID               string  `bun:"id,pk"`
	SessionID        string  `bun:"session_id"`
	ObjectiveVersion string  `bun:"objective_version"`
	Guidance         string  `bun:"guidance"`
	Status           string  `bun:"status"`
	CreatedAt        string  `bun:"created_at"`
	ConsumedAt       *string `bun:"consumed_at,nullzero"`
}

type ESMGuidanceDAO struct{ db *bun.DB }

func NewESMGuidanceDAO(db *bun.DB) *ESMGuidanceDAO { return &ESMGuidanceDAO{db: db} }

func (d *ESMGuidanceDAO) Insert(ctx context.Context, executor bun.IDB, record *ESMGuidanceRecord) error {
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *ESMGuidanceDAO) List(ctx context.Context, sessionID, status string, limit int) ([]ESMGuidanceRecord, error) {
	var records []ESMGuidanceRecord
	query := d.db.NewSelect().Model(&records).Where("session_id = ?", sessionID)
	if status != "" {
		query.Where("status = ?", status)
	}
	err := query.OrderExpr("created_at ASC").Limit(limit).Scan(ctx)
	return records, err
}

func (d *ESMGuidanceDAO) Consume(ctx context.Context, executor bun.IDB, sessionID, id, consumedAt string) error {
	_, err := executor.NewUpdate().Model((*ESMGuidanceRecord)(nil)).
		Set("status = ?", "consumed").Set("consumed_at = ?", consumedAt).
		Where("id = ? AND session_id = ? AND status = ?", id, sessionID, "pending").Exec(ctx)
	return err
}

func IsNoRowsGuidance(err error) bool { return err == sql.ErrNoRows }
