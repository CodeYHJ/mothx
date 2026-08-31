package dao

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type AttachmentRecord struct {
	bun.BaseModel `bun:"table:session_attachments"`
	ID            string `bun:"id,pk"`
	SessionID     string `bun:"session_id"`
	RunID         string `bun:"run_id"`
	Origin        string `bun:"origin"`
	Kind          string `bun:"kind"`
	Filename      string `bun:"filename"`
	MediaType     string `bun:"media_type"`
	Bytes         int64  `bun:"byte_size"`
	SHA256        string `bun:"sha256"`
	StorageKey    string `bun:"storage_key"`
	Status        string `bun:"status"`
	CreatedAt     string `bun:"created_at"`
	ExpiresAt     string `bun:"expires_at"`
	Metadata      string `bun:"metadata"`
}

type AttachmentDAO struct{ db *bun.DB }

func NewAttachmentDAO(db *bun.DB) *AttachmentDAO { return &AttachmentDAO{db: db} }

func (d *AttachmentDAO) Insert(ctx context.Context, executor bun.IDB, record *AttachmentRecord) error {
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *AttachmentDAO) Find(ctx context.Context, sessionID, attachmentID string) (*AttachmentRecord, error) {
	record := new(AttachmentRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ? AND id = ?", sessionID, attachmentID).Limit(1).Scan(ctx)
	return record, err
}

func (d *AttachmentDAO) Expired(ctx context.Context, executor bun.IDB, now string) ([]AttachmentRecord, error) {
	var records []AttachmentRecord
	err := executor.NewSelect().Model(&records).Column("id", "storage_key").Where("expires_at <= ?", now).Scan(ctx)
	return records, err
}

func (d *AttachmentDAO) MarkExpired(ctx context.Context, executor bun.IDB, now string) error {
	_, err := executor.NewUpdate().Model((*AttachmentRecord)(nil)).Set("status = ?", "expired").Where("expires_at <= ? AND status != ?", now, "expired").Exec(ctx)
	return err
}

func (d *AttachmentDAO) SetStatus(ctx context.Context, executor bun.IDB, sessionID, attachmentID, status string) (int64, error) {
	result, err := executor.NewUpdate().Model((*AttachmentRecord)(nil)).Set("status = ?", status).Where("session_id = ? AND id = ?", sessionID, attachmentID).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func IsNoRowsAttachment(err error) bool { return err == sql.ErrNoRows }
