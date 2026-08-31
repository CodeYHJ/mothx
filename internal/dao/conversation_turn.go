package dao

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type EntryRecord struct {
	bun.BaseModel `bun:"table:entries"`
	Seq           int64   `bun:"seq"`
	SessionID     string  `bun:"session_id"`
	ID            string  `bun:"id,pk"`
	Type          string  `bun:"type"`
	ParentID      *string `bun:"parent_id,nullzero"`
	Timestamp     string  `bun:"timestamp"`
	Data          string  `bun:"data"`
}

type ConversationTurnRecord struct {
	bun.BaseModel `bun:"table:conversation_turns"`
	ID            string  `bun:"id,pk"`
	SessionID     string  `bun:"session_id"`
	IntentID      string  `bun:"intent_id"`
	Kind          string  `bun:"kind"`
	Status        string  `bun:"status"`
	StartSeq      int64   `bun:"start_seq"`
	EndSeq        *int64  `bun:"end_seq,nullzero"`
	StartedAt     string  `bun:"started_at"`
	EndedAt       *string `bun:"ended_at,nullzero"`
}

type ConversationTurnState struct {
	IntentID string
	Status   string
	RunID    string
}

type ConversationTurnDAO struct{ db *bun.DB }

func NewConversationTurnDAO(db *bun.DB) *ConversationTurnDAO { return &ConversationTurnDAO{db: db} }

func (d *ConversationTurnDAO) AppendEntry(ctx context.Context, executor bun.IDB, record *EntryRecord) (int64, error) {
	var seq int64
	if err := executor.NewInsert().Model(record).ExcludeColumn("seq").Returning("seq").Scan(ctx, &seq); err != nil {
		return 0, err
	}
	return seq, nil
}

func (d *ConversationTurnDAO) Entry(ctx context.Context, executor bun.IDB, id string) (*EntryRecord, error) {
	record := new(EntryRecord)
	err := executor.NewSelect().Model(record).Where("id = ?", id).Limit(1).Scan(ctx)
	return record, err
}

func (d *ConversationTurnDAO) CurrentLeaf(ctx context.Context, executor bun.IDB, sessionID, excludedType string) (string, error) {
	var id string
	err := executor.NewSelect().Table("entries").Column("id").Where("session_id = ? AND type <> ?", sessionID, excludedType).OrderExpr("seq DESC").Limit(1).Scan(ctx, &id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (d *ConversationTurnDAO) State(ctx context.Context, executor bun.IDB, sessionID, turnID string) (ConversationTurnState, error) {
	var state ConversationTurnState
	err := executor.NewSelect().TableExpr("conversation_turns AS ct").
		ColumnExpr("ct.intent_id, ct.status, COALESCE((SELECT json_extract(e.data, '$.runId') FROM entries e WHERE e.session_id = ct.session_id AND e.type = 'turn_start' AND json_extract(e.data, '$.turnId') = ct.id ORDER BY e.seq DESC LIMIT 1), '') AS run_id").
		Where("ct.id = ? AND ct.session_id = ?", turnID, sessionID).Limit(1).Scan(ctx, &state)
	return state, err
}

func (d *ConversationTurnDAO) OpenCount(ctx context.Context, executor bun.IDB, sessionID string) (int, error) {
	return executor.NewSelect().Table("conversation_turns").Where("session_id = ? AND status = ?", sessionID, "open").Count(ctx)
}

func (d *ConversationTurnDAO) Reopen(ctx context.Context, executor bun.IDB, turn *ConversationTurnRecord) error {
	_, err := executor.NewUpdate().Model((*ConversationTurnRecord)(nil)).
		Set("intent_id = ?", turn.IntentID).Set("status = ?", "open").Set("end_seq = NULL").
		Set("started_at = ?", turn.StartedAt).Set("ended_at = NULL").
		Where("id = ? AND session_id = ?", turn.ID, turn.SessionID).Exec(ctx)
	return err
}

func (d *ConversationTurnDAO) Insert(ctx context.Context, executor bun.IDB, turn *ConversationTurnRecord) error {
	_, err := executor.NewInsert().Model(turn).Exec(ctx)
	return err
}

func (d *ConversationTurnDAO) Close(ctx context.Context, executor bun.IDB, sessionID, turnID, status string, endSeq int64, endedAt string) error {
	_, err := executor.NewUpdate().Model((*ConversationTurnRecord)(nil)).
		Set("status = ?", status).Set("end_seq = ?", endSeq).Set("ended_at = ?", endedAt).
		Where("id = ? AND session_id = ? AND status = ?", turnID, sessionID, "open").Exec(ctx)
	return err
}

func (d *ConversationTurnDAO) List(ctx context.Context, sessionID string) ([]ConversationTurnRecord, error) {
	return d.ListFrom(ctx, d.db, sessionID)
}

func (d *ConversationTurnDAO) ListFrom(ctx context.Context, executor bun.IDB, sessionID string) ([]ConversationTurnRecord, error) {
	var records []ConversationTurnRecord
	err := executor.NewSelect().Model(&records).Where("session_id = ?", sessionID).OrderExpr("start_seq").Scan(ctx)
	return records, err
}
