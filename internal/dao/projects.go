package dao

import (
	"context"
	"database/sql"
	"time"

	"github.com/uptrace/bun"
)

type ProjectRecord struct {
	bun.BaseModel `bun:"table:projects"`
	ID            string `bun:"id,pk"`
	Name          string `bun:"name"`
	CreatedAt     string `bun:"created_at"`
	UpdatedAt     string `bun:"updated_at"`
}

type SessionMetadataRecord struct {
	bun.BaseModel `bun:"table:session_metadata"`
	SessionID     string  `bun:"session_id,pk"`
	ProjectID     *string `bun:"project_id,nullzero"`
	Pinned        int     `bun:"pinned"`
	UpdatedAt     string  `bun:"updated_at"`
}

type ProjectDAO struct{ db *bun.DB }

func NewProjectDAO(db *bun.DB) *ProjectDAO { return &ProjectDAO{db: db} }

func (d *ProjectDAO) List(ctx context.Context) ([]ProjectRecord, error) {
	var records []ProjectRecord
	err := d.db.NewSelect().Model(&records).
		OrderExpr("updated_at DESC, name COLLATE NOCASE").Scan(ctx)
	return records, err
}

func (d *ProjectDAO) Insert(ctx context.Context, record *ProjectRecord) error {
	_, err := d.db.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *ProjectDAO) UpdateName(ctx context.Context, id, name, updatedAt string) (int64, error) {
	result, err := d.db.NewUpdate().Model((*ProjectRecord)(nil)).
		Set("name = ?", name).Set("updated_at = ?", updatedAt).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ProjectDAO) Delete(ctx context.Context, id string) error {
	_, err := d.db.NewDelete().Model((*ProjectRecord)(nil)).Where("id = ?", id).Exec(ctx)
	return err
}

func (d *ProjectDAO) Exists(ctx context.Context, id string) (bool, error) {
	var value int
	err := d.db.NewSelect().Model((*ProjectRecord)(nil)).ColumnExpr("1").Where("id = ?", id).Limit(1).Scan(ctx, &value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (d *ProjectDAO) UpsertMetadata(ctx context.Context, record *SessionMetadataRecord) error {
	_, err := d.db.NewInsert().Model(record).
		On("CONFLICT(session_id) DO UPDATE SET project_id = excluded.project_id, pinned = excluded.pinned, updated_at = excluded.updated_at").
		Exec(ctx)
	return err
}

func (d *ProjectDAO) Metadata(ctx context.Context, sessionID string) (*SessionMetadataRecord, error) {
	record := new(SessionMetadataRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ?", sessionID).Limit(1).Scan(ctx)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return record, err
}

func (d *ProjectDAO) LatestSessionInfoData(ctx context.Context, sessionID string) (string, error) {
	var data string
	err := d.db.NewSelect().Table("entries").Column("data").
		Where("session_id = ? AND type = ?", sessionID, "session_info").
		OrderExpr("seq DESC").Limit(1).Scan(ctx, &data)
	return data, err
}

func (d *ProjectDAO) Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
