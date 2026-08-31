package dao

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/uptrace/bun"
)

type InputResourceRecord struct {
	bun.BaseModel `bun:"table:input_resources"`
	ID            string `bun:"id,pk"`
	SessionID     string `bun:"session_id"`
	RunID         string `bun:"run_id"`
	Origin        string `bun:"origin"`
	EventID       string `bun:"event_id"`
	ItemIndex     int    `bun:"item_index"`
	ItemKey       string `bun:"item_key"`
	Kind          string `bun:"kind"`
	Filename      string `bun:"filename"`
	MediaType     string `bun:"media_type"`
	Bytes         int64  `bun:"byte_size"`
	SHA256        string `bun:"sha256"`
	RelativePath  string `bun:"relative_path"`
	Status        string `bun:"status"`
	CreatedAt     string `bun:"created_at"`
	Metadata      string `bun:"metadata"`
}

type InputResourceEventRecord struct {
	bun.BaseModel `bun:"table:input_resource_events"`
	ID            string `bun:"id,pk"`
	SessionID     string `bun:"session_id"`
	ResourceID    string `bun:"resource_id"`
	RunID         string `bun:"run_id"`
	EventType     string `bun:"event_type"`
	Status        string `bun:"status"`
	Timestamp     string `bun:"timestamp"`
	Data          string `bun:"data"`
}

type InputResourceDAO struct{ db *bun.DB }

func NewInputResourceDAO(db *bun.DB) *InputResourceDAO { return &InputResourceDAO{db: db} }

func (d *InputResourceDAO) Insert(ctx context.Context, executor bun.IDB, record *InputResourceRecord) error {
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *InputResourceDAO) Find(ctx context.Context, executor bun.IDB, sessionID, resourceID string) (*InputResourceRecord, error) {
	record := new(InputResourceRecord)
	err := executor.NewSelect().Model(record).Where("session_id = ? AND id = ?", sessionID, resourceID).Limit(1).Scan(ctx)
	return record, err
}

func (d *InputResourceDAO) FindByItemKey(ctx context.Context, sessionID, itemKey string) (*InputResourceRecord, error) {
	record := new(InputResourceRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ? AND item_key = ?", sessionID, itemKey).Limit(1).Scan(ctx)
	return record, err
}

func (d *InputResourceDAO) List(ctx context.Context, executor bun.IDB, sessionID string) ([]InputResourceRecord, error) {
	var records []InputResourceRecord
	err := executor.NewSelect().Model(&records).Where("session_id = ?", sessionID).OrderExpr("created_at ASC, id ASC").Scan(ctx)
	return records, err
}

func (d *InputResourceDAO) UpdateAttachment(ctx context.Context, executor bun.IDB, sessionID, resourceID, runID string) error {
	_, err := executor.NewUpdate().Model((*InputResourceRecord)(nil)).Set("run_id = ?", runID).Set("status = ?", "attached").Where("id = ? AND session_id = ?", resourceID, sessionID).Exec(ctx)
	return err
}

func (d *InputResourceDAO) UpdateStatus(ctx context.Context, executor bun.IDB, sessionID, resourceID, status string) error {
	_, err := executor.NewUpdate().Model((*InputResourceRecord)(nil)).Set("status = ?", status).Where("id = ? AND session_id = ?", resourceID, sessionID).Exec(ctx)
	return err
}

func (d *InputResourceDAO) DeleteDraft(ctx context.Context, executor bun.IDB, sessionID, resourceID string) error {
	_, err := executor.NewUpdate().Model((*InputResourceRecord)(nil)).Set("status = ?", "deleted").Where("id = ? AND session_id = ? AND status = ? AND run_id = ?", resourceID, sessionID, "prepared", "").Exec(ctx)
	return err
}

func (d *InputResourceDAO) CreatedAt(ctx context.Context, executor bun.IDB, sessionID, resourceID string) (string, error) {
	var created string
	err := executor.NewSelect().Model((*InputResourceRecord)(nil)).Column("created_at").Where("id = ? AND session_id = ?", resourceID, sessionID).Limit(1).Scan(ctx, &created)
	return created, err
}

func (d *InputResourceDAO) AppendEvent(ctx context.Context, executor bun.IDB, record *InputResourceEventRecord) error {
	_, err := executor.NewInsert().Model(record).On("CONFLICT(id) DO NOTHING").Exec(ctx)
	return err
}

func (d *InputResourceDAO) ListEvents(ctx context.Context, sessionID string) ([]InputResourceEventRecord, error) {
	var records []InputResourceEventRecord
	err := d.db.NewSelect().Model(&records).Where("session_id = ?", sessionID).OrderExpr("timestamp ASC, id ASC").Scan(ctx)
	return records, err
}

func (d *InputResourceDAO) OwnerRun(ctx context.Context, executor bun.IDB, resourceID, sessionID string) (string, string, error) {
	record, err := d.Find(ctx, executor, sessionID, resourceID)
	if err != nil {
		return "", "", err
	}
	return record.RunID, record.Status, nil
}

func (d *InputResourceDAO) OwnerIntent(ctx context.Context, executor bun.IDB, ownerRunID, sessionID string) (string, error) {
	var intentID string
	err := executor.NewSelect().Table("session_runs").Column("intent_id").Where("id = ? AND session_id = ?", ownerRunID, sessionID).Limit(1).Scan(ctx, &intentID)
	return intentID, err
}

func (r InputResourceEventRecord) JSON() json.RawMessage { return json.RawMessage(r.Data) }

func IsNoRowsInput(err error) bool { return err == sql.ErrNoRows }
