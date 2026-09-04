package dao

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type ResponseRunRecord struct {
	bun.BaseModel     `bun:"table:response_runs"`
	ID                int64   `bun:"id,pk,nullzero"`
	SessionID         string  `bun:"session_id"`
	LocalRunID        string  `bun:"local_run_id"`
	LocalTurnID       string  `bun:"local_turn_id"`
	MessageID         *int64  `bun:"message_id,nullzero"`
	ResponseID        *string `bun:"response_id,nullzero"`
	Provider          string  `bun:"provider"`
	API               string  `bun:"api"`
	State             string  `bun:"state"`
	PollingURL        *string `bun:"polling_url,nullzero"`
	LastEventSequence *int64  `bun:"last_event_sequence,nullzero"`
	CancelRequested   bool    `bun:"cancel_requested"`
	CreatedAt         string  `bun:"created_at"`
	UpdatedAt         string  `bun:"updated_at"`
}

type ResponseTurnRecord struct {
	bun.BaseModel       `bun:"table:response_turns"`
	ID                  int64   `bun:"id,pk,nullzero"`
	SessionID           string  `bun:"session_id"`
	LocalTurnID         string  `bun:"local_turn_id"`
	MessageID           *int64  `bun:"message_id,nullzero"`
	RequestID           *string `bun:"request_id,nullzero"`
	ResponseID          *string `bun:"response_id,nullzero"`
	PreviousResponseID  *string `bun:"previous_response_id,nullzero"`
	ConversationID      *string `bun:"conversation_id,nullzero"`
	Provider            string  `bun:"provider"`
	API                 string  `bun:"api"`
	Model               string  `bun:"model"`
	StateMode           string  `bun:"state_mode"`
	Status              string  `bun:"status"`
	IncompleteReason    *string `bun:"incomplete_reason,nullzero"`
	RequestSummaryJSON  []byte  `bun:"request_summary_json"`
	ResponseSummaryJSON []byte  `bun:"response_summary_json"`
	CreatedAt           string  `bun:"created_at"`
	CompletedAt         *string `bun:"completed_at,nullzero"`
}

type ResponseItemRecord struct {
	bun.BaseModel `bun:"table:response_items"`
	ID            int64   `bun:"id,pk,nullzero"`
	SessionID     string  `bun:"session_id"`
	LocalTurnID   string  `bun:"local_turn_id"`
	ResponseID    *string `bun:"response_id,nullzero"`
	ItemID        *string `bun:"item_id,nullzero"`
	OutputIndex   int     `bun:"output_index"`
	ItemType      string  `bun:"item_type"`
	ItemStatus    *string `bun:"item_status,nullzero"`
	ItemKey       string  `bun:"item_key"`
	SanitizedJSON []byte  `bun:"sanitized_json"`
	CreatedAt     string  `bun:"created_at"`
	UpdatedAt     *string `bun:"updated_at,nullzero"`
}

type ToolExecutionRecord struct {
	bun.BaseModel        `bun:"table:tool_execution_records"`
	ID                   int64   `bun:"id,pk,nullzero"`
	SessionID            string  `bun:"session_id"`
	LocalTurnID          string  `bun:"local_turn_id"`
	ExecutionKey         string  `bun:"execution_key"`
	Provider             string  `bun:"provider"`
	API                  string  `bun:"api"`
	ResponseID           *string `bun:"response_id,nullzero"`
	ProviderCallID       *string `bun:"provider_call_id,nullzero"`
	ToolKind             string  `bun:"tool_kind"`
	ToolName             string  `bun:"tool_name"`
	ArgsHash             string  `bun:"args_hash"`
	ExecutionState       string  `bun:"execution_state"`
	ResultSummaryJSON    []byte  `bun:"result_summary_json"`
	ProviderMetadataJSON []byte  `bun:"provider_metadata_json"`
	SideEffecting        bool    `bun:"side_effecting"`
	CreatedAt            string  `bun:"created_at"`
	CompletedAt          *string `bun:"completed_at,nullzero"`
}

type ResponseSessionStateRecord struct {
	bun.BaseModel      `bun:"table:response_session_state"`
	SessionID          string  `bun:"session_id,pk"`
	StateMode          string  `bun:"state_mode"`
	PreviousResponseID *string `bun:"previous_response_id,nullzero"`
	ConversationID     *string `bun:"conversation_id,nullzero"`
	Provider           string  `bun:"provider"`
	API                string  `bun:"api"`
	Model              string  `bun:"model"`
	Version            int64   `bun:"version"`
	UpdatedAt          string  `bun:"updated_at"`
}

type ResponseReplayItemRecord struct {
	LocalTurnID   string `bun:"local_turn_id"`
	SanitizedJSON []byte `bun:"sanitized_json"`
}

type ResponseDAO struct{ db *bun.DB }

func NewResponseDAO(db *bun.DB) *ResponseDAO { return &ResponseDAO{db: db} }

func (d *ResponseDAO) LinkedRun(ctx context.Context, executor bun.IDB, sessionID, localRunID string) (*ResponseRunRecord, error) {
	record := new(ResponseRunRecord)
	err := executor.NewSelect().Model(record).Where("session_id = ?", sessionID).Where("(local_turn_id = ? OR substr(local_turn_id, 1, length(?) + 1) = ? || ':')", localRunID, localRunID, localRunID).OrderExpr("updated_at DESC, id DESC").Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) InsertRun(ctx context.Context, executor bun.IDB, record *ResponseRunRecord) error {
	_, err := executor.NewInsert().Model(record).Exec(ctx)
	return err
}

func (d *ResponseDAO) FindRun(ctx context.Context, executor bun.IDB, id int64) (*ResponseRunRecord, error) {
	record := new(ResponseRunRecord)
	err := executor.NewSelect().Model(record).Where("id = ?", id).Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) InsertTurn(ctx context.Context, executor bun.IDB, record *ResponseTurnRecord) error {
	_, err := executor.NewInsert().Model(record).On("CONFLICT(session_id, local_turn_id) DO UPDATE SET message_id = excluded.message_id, request_id = excluded.request_id, response_id = excluded.response_id, previous_response_id = excluded.previous_response_id, conversation_id = excluded.conversation_id, provider = excluded.provider, api = excluded.api, model = excluded.model, state_mode = excluded.state_mode, status = excluded.status, incomplete_reason = excluded.incomplete_reason, request_summary_json = excluded.request_summary_json, response_summary_json = excluded.response_summary_json, completed_at = excluded.completed_at").Exec(ctx)
	return err
}

func (d *ResponseDAO) FindTurn(ctx context.Context, sessionID, localTurnID string) (*ResponseTurnRecord, error) {
	record := new(ResponseTurnRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ? AND local_turn_id = ?", sessionID, localTurnID).Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) UpsertItem(ctx context.Context, executor bun.IDB, record *ResponseItemRecord) error {
	_, err := executor.NewInsert().Model(record).On("CONFLICT(session_id, local_turn_id, item_key) DO UPDATE SET response_id = excluded.response_id, item_id = excluded.item_id, output_index = excluded.output_index, item_type = excluded.item_type, item_status = excluded.item_status, sanitized_json = excluded.sanitized_json, updated_at = excluded.updated_at").Exec(ctx)
	return err
}

func (d *ResponseDAO) ListItems(ctx context.Context, sessionID, localTurnID string) ([]ResponseItemRecord, error) {
	var records []ResponseItemRecord
	err := d.db.NewSelect().Model(&records).Where("session_id = ? AND local_turn_id = ?", sessionID, localTurnID).OrderExpr("id ASC").Scan(ctx)
	return records, err
}

func (d *ResponseDAO) ListReplayItems(ctx context.Context, sessionID string, limit int) ([]ResponseReplayItemRecord, error) {
	var records []ResponseReplayItemRecord
	err := d.db.NewSelect().TableExpr("response_items AS ri").Column("ri.sanitized_json").Join("JOIN response_turns AS rt ON rt.session_id = ri.session_id AND rt.local_turn_id = ri.local_turn_id").Where("ri.session_id = ? AND rt.status IN (?)", sessionID, bun.In([]string{"completed", "incomplete"})).OrderExpr("rt.created_at ASC, ri.output_index ASC, ri.id ASC").Limit(limit).Scan(ctx, &records)
	return records, err
}

func (d *ResponseDAO) ListReplayTurns(ctx context.Context, sessionID string) ([]ResponseReplayItemRecord, error) {
	var records []ResponseReplayItemRecord
	err := d.db.NewSelect().TableExpr("response_turns AS rt").ColumnExpr("rt.local_turn_id, ri.sanitized_json").Join("JOIN response_items AS ri ON ri.session_id = rt.session_id AND ri.local_turn_id = rt.local_turn_id").Where("rt.session_id = ? AND rt.status IN (?)", sessionID, bun.In([]string{"completed", "incomplete"})).OrderExpr("rt.created_at ASC, ri.output_index ASC, ri.id ASC").Scan(ctx, &records)
	return records, err
}

func (d *ResponseDAO) ClaimTool(ctx context.Context, executor bun.IDB, record *ToolExecutionRecord) (*ToolExecutionRecord, int64, error) {
	result, err := executor.NewInsert().Model(record).On("CONFLICT(execution_key) DO NOTHING").Exec(ctx)
	if err != nil {
		return nil, 0, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	stored := new(ToolExecutionRecord)
	if err := executor.NewSelect().Model(stored).Where("execution_key = ?", record.ExecutionKey).Limit(1).Scan(ctx); err != nil {
		return nil, 0, err
	}
	return stored, created, nil
}

func (d *ResponseDAO) FindTool(ctx context.Context, executor bun.IDB, executionKey string) (*ToolExecutionRecord, error) {
	record := new(ToolExecutionRecord)
	err := executor.NewSelect().Model(record).Where("execution_key = ?", executionKey).Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) UpdateTool(ctx context.Context, executor bun.IDB, record *ToolExecutionRecord) (int64, error) {
	result, err := executor.NewUpdate().Model((*ToolExecutionRecord)(nil)).Set("execution_state = ?", record.ExecutionState).Set("result_summary_json = ?", record.ResultSummaryJSON).Set("provider_metadata_json = ?", record.ProviderMetadataJSON).Set("completed_at = ?", record.CompletedAt).Where("execution_key = ?", record.ExecutionKey).Where("execution_state IN (?)", bun.In([]string{"running", "retry_requested"})).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ResponseDAO) ReclaimTool(ctx context.Context, executor bun.IDB, executionKey string, metadata []byte, state string) (int64, error) {
	result, err := executor.NewUpdate().Model((*ToolExecutionRecord)(nil)).Set("execution_state = 'running'").Set("result_summary_json = NULL").Set("provider_metadata_json = ?", metadata).Set("completed_at = NULL").Where("execution_key = ? AND execution_state = ?", executionKey, state).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ResponseDAO) RequestToolRecovery(ctx context.Context, executor bun.IDB, sessionID, localTurnID string, providerCallIDs []string) (int64, error) {
	result, err := executor.NewUpdate().Model((*ToolExecutionRecord)(nil)).Set("execution_state = 'retry_requested'").Set("result_summary_json = NULL").Set("completed_at = NULL").Where("session_id = ? AND local_turn_id = ?", sessionID, localTurnID).Where("provider_call_id IN (?)", bun.In(providerCallIDs)).Where("execution_state IN (?)", bun.In([]string{"running", "interrupted"})).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ResponseDAO) ListRequestedToolRecoveries(ctx context.Context, executor bun.IDB, sessionID, localTurnID string, providerCallIDs []string) ([]ToolExecutionRecord, error) {
	var records []ToolExecutionRecord
	err := executor.NewSelect().Model(&records).
		Where("session_id = ? AND local_turn_id = ?", sessionID, localTurnID).
		Where("provider_call_id IN (?)", bun.In(providerCallIDs)).
		Where("execution_state = ?", "retry_requested").
		OrderExpr("id ASC").
		Scan(ctx)
	return records, err
}

func (d *ResponseDAO) AbandonTools(ctx context.Context, executor bun.IDB, sessionID, localTurnID string, summary []byte, completedAt string) (int64, error) {
	result, err := executor.NewUpdate().Model((*ToolExecutionRecord)(nil)).Set("execution_state = 'abandoned'").Set("result_summary_json = ?", summary).Set("completed_at = ?", completedAt).Where("session_id = ? AND local_turn_id = ?", sessionID, localTurnID).Where("execution_state IN (?)", bun.In([]string{"running", "interrupted"})).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ResponseDAO) UpsertRun(ctx context.Context, executor bun.IDB, record *ResponseRunRecord) error {
	_, err := executor.NewInsert().Model(record).On("CONFLICT(session_id, local_run_id) DO UPDATE SET local_turn_id = excluded.local_turn_id, message_id = excluded.message_id, response_id = excluded.response_id, provider = excluded.provider, api = excluded.api, state = excluded.state, polling_url = excluded.polling_url, last_event_sequence = excluded.last_event_sequence, cancel_requested = excluded.cancel_requested, updated_at = excluded.updated_at").Exec(ctx)
	return err
}

func (d *ResponseDAO) GetRun(ctx context.Context, sessionID, localRunID string) (*ResponseRunRecord, error) {
	record := new(ResponseRunRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ? AND local_run_id = ?", sessionID, localRunID).Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) ListRunsForSession(ctx context.Context, sessionID string, limit int) ([]ResponseRunRecord, error) {
	var records []ResponseRunRecord
	err := d.db.NewSelect().Model(&records).Where("session_id = ?", sessionID).OrderExpr("created_at ASC").Limit(limit).Scan(ctx)
	return records, err
}

func (d *ResponseDAO) GetSessionState(ctx context.Context, sessionID string) (*ResponseSessionStateRecord, error) {
	record := new(ResponseSessionStateRecord)
	err := d.db.NewSelect().Model(record).Where("session_id = ?", sessionID).Limit(1).Scan(ctx)
	return record, err
}

func (d *ResponseDAO) InsertSessionState(ctx context.Context, executor bun.IDB, record *ResponseSessionStateRecord) (int64, error) {
	result, err := executor.NewInsert().Model(record).On("CONFLICT(session_id) DO NOTHING").Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (d *ResponseDAO) UpdateSessionStateCAS(ctx context.Context, executor bun.IDB, record *ResponseSessionStateRecord, expectedVersion int64) (int64, error) {
	result, err := executor.NewUpdate().Model((*ResponseSessionStateRecord)(nil)).Set("state_mode = ?", record.StateMode).Set("previous_response_id = ?", record.PreviousResponseID).Set("conversation_id = ?", record.ConversationID).Set("provider = ?", record.Provider).Set("api = ?", record.API).Set("model = ?", record.Model).Set("version = version + 1").Set("updated_at = ?", record.UpdatedAt).Where("session_id = ? AND version = ?", record.SessionID, expectedVersion).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func IsNoRowsResponse(err error) bool { return err == sql.ErrNoRows }
