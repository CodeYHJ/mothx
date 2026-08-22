package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxResponseArchiveJSONBytes = 128 * 1024

// ResponseTurn is the durable lineage and lifecycle summary for one Responses
// API turn. It intentionally contains summaries, not a second transcript.
type ResponseTurn struct {
	ID                 int64
	SessionID          string
	LocalTurnID        string
	MessageID          *int64
	RequestID          string
	ResponseID         string
	PreviousResponseID string
	ConversationID     string
	Provider           string
	API                string
	Model              string
	StateMode          string
	Status             string
	IncompleteReason   string
	RequestSummary     json.RawMessage
	ResponseSummary    json.RawMessage
	CreatedAt          time.Time
	CompletedAt        *time.Time
}

// ResponseItemArchive stores one sanitized normalized item. Raw provider
// request/response bodies must not be passed here.
type ResponseItemArchive struct {
	ID            int64
	SessionID     string
	LocalTurnID   string
	ResponseID    string
	ItemID        string
	OutputIndex   int
	ItemType      string
	ItemStatus    string
	ItemKey       string
	SanitizedJSON json.RawMessage
	CreatedAt     time.Time
}

// ToolExecutionRecord is the cross-protocol idempotency record for a tool
// invocation. ExecutionKey is local and remains the deduplication authority.
type ToolExecutionRecord struct {
	ID               int64
	SessionID        string
	LocalTurnID      string
	ExecutionKey     string
	Provider         string
	API              string
	ResponseID       string
	ProviderCallID   string
	ToolKind         string
	ToolName         string
	ArgsHash         string
	ExecutionState   string
	ResultSummary    json.RawMessage
	ProviderMetadata json.RawMessage
	SideEffecting    bool
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

// ResponseRun is the durable state for a Responses background run.
type ResponseRun struct {
	ID                int64     `json:"id"`
	SessionID         string    `json:"sessionId"`
	LocalRunID        string    `json:"localRunId"`
	LocalTurnID       string    `json:"localTurnId,omitempty"`
	MessageID         *int64    `json:"messageId,omitempty"`
	ResponseID        string    `json:"responseId,omitempty"`
	Provider          string    `json:"provider"`
	API               string    `json:"api"`
	State             string    `json:"state"`
	PollingURL        string    `json:"pollingUrl,omitempty"`
	LastEventSequence *int64    `json:"lastEventSequence,omitempty"`
	CancelRequested   bool      `json:"cancelRequested,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ResponseSessionState is the compare-and-swap protected remote lineage for a
// single local session. Provider config supplies defaults; this record keeps
// concurrent sessions and concurrent turns from sharing mutable remote state.
type ResponseSessionState struct {
	SessionID          string
	StateMode          string
	PreviousResponseID string
	ConversationID     string
	Provider           string
	API                string
	Model              string
	Version            int64
	UpdatedAt          time.Time
}

// ResponseReplayTurn groups the native output items belonging to one local
// Responses turn. It lets callers place those items at the corresponding
// assistant position while rebuilding a complete local conversation.
type ResponseReplayTurn struct {
	LocalTurnID string
	Items       []json.RawMessage
}

func SaveResponseTurn(sessionDir string, turn ResponseTurn) error {
	if err := validateResponseTurn(turn); err != nil {
		return err
	}
	requestSummary, err := archiveJSON(turn.RequestSummary)
	if err != nil {
		return fmt.Errorf("request summary: %w", err)
	}
	responseSummary, err := archiveJSON(turn.ResponseSummary)
	if err != nil {
		return fmt.Errorf("response summary: %w", err)
	}
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = time.Now()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var completed any
	if turn.CompletedAt != nil {
		completed = turn.CompletedAt.Format(time.RFC3339Nano)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, turn.SessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO response_turns
		(session_id, local_turn_id, message_id, request_id, response_id, previous_response_id,
		 conversation_id, provider, api, model, state_mode, status, incomplete_reason,
		 request_summary_json, response_summary_json, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, local_turn_id) DO UPDATE SET
			message_id = excluded.message_id,
			request_id = excluded.request_id,
			response_id = excluded.response_id,
			previous_response_id = excluded.previous_response_id,
			conversation_id = excluded.conversation_id,
			provider = excluded.provider,
			api = excluded.api,
			model = excluded.model,
			state_mode = excluded.state_mode,
			status = excluded.status,
			incomplete_reason = excluded.incomplete_reason,
			request_summary_json = excluded.request_summary_json,
			response_summary_json = excluded.response_summary_json,
			completed_at = excluded.completed_at`,
		turn.SessionID, turn.LocalTurnID, nullableInt64(turn.MessageID), nullableString(turn.RequestID),
		nullableString(turn.ResponseID), nullableString(turn.PreviousResponseID),
		nullableString(turn.ConversationID), turn.Provider, turn.API, turn.Model, turn.StateMode,
		turn.Status, nullableString(turn.IncompleteReason), requestSummary, responseSummary,
		turn.CreatedAt.Format(time.RFC3339Nano), completed)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validateResponseTurn(turn ResponseTurn) error {
	switch {
	case turn.SessionID == "":
		return fmt.Errorf("response turn session ID is required")
	case turn.LocalTurnID == "":
		return fmt.Errorf("response turn local turn ID is required")
	case turn.Provider == "":
		return fmt.Errorf("response turn provider is required")
	case turn.API == "":
		return fmt.Errorf("response turn API is required")
	case turn.Model == "":
		return fmt.Errorf("response turn model is required")
	case turn.StateMode == "":
		return fmt.Errorf("response turn state mode is required")
	case turn.Status == "":
		return fmt.Errorf("response turn status is required")
	default:
		return nil
	}
}

func GetResponseTurn(sessionDir, sessionID, localTurnID string) (*ResponseTurn, error) {
	if sessionID == "" || localTurnID == "" {
		return nil, fmt.Errorf("session ID and local turn ID are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var turn ResponseTurn
	var messageID sql.NullInt64
	var requestID, responseID, previousResponseID, conversationID, incompleteReason sql.NullString
	var requestSummary, responseSummary []byte
	var createdAt string
	var completedAt sql.NullString
	err = db.QueryRow(`SELECT id, session_id, local_turn_id, message_id, request_id, response_id,
		previous_response_id, conversation_id, provider, api, model, state_mode, status,
		incomplete_reason, request_summary_json, response_summary_json, created_at, completed_at
		FROM response_turns WHERE session_id = ? AND local_turn_id = ?`, sessionID, localTurnID).
		Scan(&turn.ID, &turn.SessionID, &turn.LocalTurnID, &messageID, &requestID, &responseID,
			&previousResponseID, &conversationID, &turn.Provider, &turn.API, &turn.Model,
			&turn.StateMode, &turn.Status, &incompleteReason, &requestSummary, &responseSummary,
			&createdAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	turn.MessageID = nullableInt64Value(messageID)
	turn.RequestID = requestID.String
	turn.ResponseID = responseID.String
	turn.PreviousResponseID = previousResponseID.String
	turn.ConversationID = conversationID.String
	turn.IncompleteReason = incompleteReason.String
	turn.RequestSummary = cloneArchiveJSON(requestSummary)
	turn.ResponseSummary = cloneArchiveJSON(responseSummary)
	turn.CreatedAt = parseSessionTimestamp(createdAt)
	if completedAt.Valid && completedAt.String != "" {
		value := parseSessionTimestamp(completedAt.String)
		turn.CompletedAt = &value
	}
	return &turn, nil
}

func SaveResponseItem(sessionDir string, item ResponseItemArchive) error {
	if item.SessionID == "" || item.LocalTurnID == "" || item.ItemType == "" {
		return fmt.Errorf("session ID, local turn ID and item type are required")
	}
	sanitized, err := archiveJSON(item.SanitizedJSON)
	if err != nil {
		return fmt.Errorf("sanitized item: %w", err)
	}
	if len(sanitized) == 0 {
		return fmt.Errorf("sanitized item is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.ItemKey == "" {
		if item.ItemID != "" {
			item.ItemKey = fmt.Sprintf("%s:%d", item.ItemID, item.OutputIndex)
		} else {
			item.ItemKey = fmt.Sprintf("output:%d", item.OutputIndex)
		}
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, item.SessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO response_items
		(session_id, local_turn_id, response_id, item_id, output_index, item_type, item_status, item_key, sanitized_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, local_turn_id, item_key) DO UPDATE SET
			response_id = excluded.response_id,
			item_id = excluded.item_id,
			output_index = excluded.output_index,
			item_type = excluded.item_type,
			item_status = excluded.item_status,
			sanitized_json = excluded.sanitized_json,
			updated_at = excluded.updated_at`,
		item.SessionID, item.LocalTurnID, nullableString(item.ResponseID), nullableString(item.ItemID),
		item.OutputIndex, item.ItemType, nullableString(item.ItemStatus), item.ItemKey, sanitized,
		item.CreatedAt.Format(time.RFC3339Nano), item.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func ListResponseItems(sessionDir, sessionID, localTurnID string) ([]ResponseItemArchive, error) {
	if sessionID == "" || localTurnID == "" {
		return nil, fmt.Errorf("session ID and local turn ID are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, local_turn_id, response_id, item_id, output_index,
		item_type, item_status, item_key, sanitized_json, created_at
		FROM response_items WHERE session_id = ? AND local_turn_id = ? ORDER BY id ASC`, sessionID, localTurnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ResponseItemArchive
	for rows.Next() {
		var item ResponseItemArchive
		var responseID, itemID, itemStatus sql.NullString
		var data []byte
		var createdAt string
		if err := rows.Scan(&item.ID, &item.SessionID, &item.LocalTurnID, &responseID, &itemID,
			&item.OutputIndex, &item.ItemType, &itemStatus, &item.ItemKey, &data, &createdAt); err != nil {
			return nil, err
		}
		item.ResponseID = responseID.String
		item.ItemID = itemID.String
		item.ItemStatus = itemStatus.String
		item.SanitizedJSON = cloneArchiveJSON(data)
		item.CreatedAt = parseSessionTimestamp(createdAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

// ListResponseReplayItems returns the ordered, sanitized native items from
// completed Responses turns. Callers can pass this sequence to a provider's
// native replay path instead of reconstructing prior assistant output from
// plain transcript text.
func ListResponseReplayItems(sessionDir, sessionID string, limit int) ([]json.RawMessage, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT ri.sanitized_json
		FROM response_items ri
		JOIN response_turns rt ON rt.session_id = ri.session_id AND rt.local_turn_id = ri.local_turn_id
		WHERE ri.session_id = ? AND rt.status IN ('completed', 'incomplete')
		ORDER BY rt.created_at ASC, ri.output_index ASC, ri.id ASC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("stored response replay item is invalid JSON")
		}
		items = append(items, cloneArchiveJSON(raw))
	}
	return items, rows.Err()
}

// ListResponseReplayTurns returns completed native output grouped by local
// turn, ordered by their original completion order.
func ListResponseReplayTurns(sessionDir, sessionID string, limit int) ([]ResponseReplayTurn, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT rt.local_turn_id, ri.sanitized_json
		FROM response_turns rt
		JOIN response_items ri ON ri.session_id = rt.session_id AND ri.local_turn_id = rt.local_turn_id
		WHERE rt.session_id = ? AND rt.status IN ('completed', 'incomplete')
		ORDER BY rt.created_at ASC, ri.output_index ASC, ri.id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTurn := make(map[string]int)
	seenCalls := make(map[string]map[string]struct{})
	var turns []ResponseReplayTurn
	for rows.Next() {
		var turnID string
		var raw []byte
		if err := rows.Scan(&turnID, &raw); err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("stored response replay item is invalid JSON")
		}
		// Older archives may contain both the streamed output_item and a
		// response.completed snapshot for one function call. Gate replay by
		// provider call identity so those historical duplicates are not sent
		// back to the provider as two calls.
		var identity struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(raw, &identity) == nil &&
			(identity.Type == "function_call" || identity.Type == "custom_tool_call") {
			callID := identity.CallID
			if callID == "" {
				callID = identity.ID
			}
			if callID != "" {
				if seenCalls[turnID] == nil {
					seenCalls[turnID] = make(map[string]struct{})
				}
				if _, duplicate := seenCalls[turnID][identity.Type+"\x00"+callID]; duplicate {
					continue
				}
				seenCalls[turnID][identity.Type+"\x00"+callID] = struct{}{}
			}
		}
		index, ok := byTurn[turnID]
		if !ok {
			if len(turns) >= limit {
				break
			}
			index = len(turns)
			byTurn[turnID] = index
			turns = append(turns, ResponseReplayTurn{LocalTurnID: turnID})
		}
		turns[index].Items = append(turns[index].Items, cloneArchiveJSON(raw))
	}
	return turns, rows.Err()
}

// ClaimToolExecutionRecord atomically claims an execution key. A false
// created result means another request already owns the key and its record
// must be consulted before executing a side effect.
func ClaimToolExecutionRecord(sessionDir string, record ToolExecutionRecord) (*ToolExecutionRecord, bool, error) {
	if record.SessionID == "" || record.ExecutionKey == "" || record.ToolName == "" || record.ArgsHash == "" {
		return nil, false, fmt.Errorf("session ID, execution key, tool name and args hash are required")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	resultSummary, err := archiveJSON(record.ResultSummary)
	if err != nil {
		return nil, false, fmt.Errorf("result summary: %w", err)
	}
	providerMetadata, err := archiveJSON(record.ProviderMetadata)
	if err != nil {
		return nil, false, fmt.Errorf("provider metadata: %w", err)
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err := validateRuntimeLeaseTx(tx, sessionDir, record.SessionID); err != nil {
		return nil, false, err
	}
	insert, err := tx.Exec(`INSERT INTO tool_execution_records
		(session_id, local_turn_id, execution_key, provider, api, response_id, provider_call_id,
		 tool_kind, tool_name, args_hash, execution_state, result_summary_json,
		 provider_metadata_json, side_effecting, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(execution_key) DO NOTHING`,
		record.SessionID, record.LocalTurnID, record.ExecutionKey, record.Provider, record.API,
		nullableString(record.ResponseID), nullableString(record.ProviderCallID), record.ToolKind,
		record.ToolName, record.ArgsHash, record.ExecutionState, resultSummary, providerMetadata,
		boolToInt(record.SideEffecting), record.CreatedAt.Format(time.RFC3339Nano), nil)
	if err != nil {
		return nil, false, err
	}
	created, err := insert.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	var claimed ToolExecutionRecord
	var responseID, providerCallID sql.NullString
	var resultData, metadataData []byte
	var createdAt string
	var completedAt sql.NullString
	err = tx.QueryRow(`SELECT id, session_id, local_turn_id, execution_key, provider, api,
		response_id, provider_call_id, tool_kind, tool_name, args_hash, execution_state,
		result_summary_json, provider_metadata_json, side_effecting, created_at, completed_at
		FROM tool_execution_records WHERE execution_key = ?`, record.ExecutionKey).
		Scan(&claimed.ID, &claimed.SessionID, &claimed.LocalTurnID, &claimed.ExecutionKey,
			&claimed.Provider, &claimed.API, &responseID, &providerCallID, &claimed.ToolKind,
			&claimed.ToolName, &claimed.ArgsHash, &claimed.ExecutionState, &resultData,
			&metadataData, &claimed.SideEffecting, &createdAt, &completedAt)
	if err != nil {
		return nil, false, err
	}
	claimed.ResponseID = responseID.String
	claimed.ProviderCallID = providerCallID.String
	claimed.ResultSummary = cloneArchiveJSON(resultData)
	claimed.ProviderMetadata = cloneArchiveJSON(metadataData)
	claimed.CreatedAt = parseSessionTimestamp(createdAt)
	if completedAt.Valid && completedAt.String != "" {
		value := parseSessionTimestamp(completedAt.String)
		claimed.CompletedAt = &value
	}
	if claimed.SessionID != record.SessionID || claimed.LocalTurnID != record.LocalTurnID ||
		claimed.Provider != record.Provider || claimed.API != record.API ||
		claimed.ToolName != record.ToolName || claimed.ArgsHash != record.ArgsHash ||
		(record.ProviderCallID != "" && claimed.ProviderCallID != record.ProviderCallID) {
		return nil, false, fmt.Errorf("execution key collision for %q", record.ExecutionKey)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &claimed, created > 0, nil
}

func UpdateToolExecutionRecord(sessionDir string, record ToolExecutionRecord) error {
	if record.ExecutionKey == "" || record.ExecutionState == "" {
		return fmt.Errorf("execution key and execution state are required")
	}
	resultSummary, err := archiveJSON(record.ResultSummary)
	if err != nil {
		return fmt.Errorf("result summary: %w", err)
	}
	providerMetadata, err := archiveJSON(record.ProviderMetadata)
	if err != nil {
		return fmt.Errorf("provider metadata: %w", err)
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var completed any
	if record.CompletedAt != nil {
		completed = record.CompletedAt.Format(time.RFC3339Nano)
	}
	// Only an actively owned execution may publish a result. This prevents a
	// stale process from overwriting an explicit abandon or a newer recovery.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, record.SessionID); err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE tool_execution_records SET execution_state = ?,
		result_summary_json = ?, provider_metadata_json = ?, completed_at = ?
		WHERE execution_key = ? AND execution_state IN ('running', 'retry_requested')`,
		record.ExecutionState, resultSummary, providerMetadata, completed, record.ExecutionKey)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("tool execution %q is no longer writable", record.ExecutionKey)
	}
	return tx.Commit()
}

// ReclaimInterruptedToolExecution atomically reopens a tool record after a
// process interruption. Read-only running/interrupted records are eligible
// automatically; side-effecting records require the explicit retry_requested
// state set by the confirmation API.
func ReclaimInterruptedToolExecution(sessionDir, executionKey string) (bool, error) {
	if sessionDir == "" || executionKey == "" {
		return false, fmt.Errorf("session directory and execution key are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM tool_execution_records WHERE execution_key = ?`, executionKey).Scan(&sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return false, err
	}
	var state string
	var sideEffecting bool
	var metadata []byte
	if err := tx.QueryRow(`SELECT execution_state, side_effecting, provider_metadata_json FROM tool_execution_records WHERE execution_key = ?`, executionKey).Scan(&state, &sideEffecting, &metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !((!sideEffecting && (state == "running" || state == "interrupted")) || state == "retry_requested") {
		return false, nil
	}
	reason := "automatic_read_only"
	if state == "retry_requested" {
		reason = "user_confirmed"
	}
	meta := map[string]any{}
	if len(metadata) > 0 && json.Unmarshal(metadata, &meta) != nil {
		meta = map[string]any{}
	}
	meta["recoveryReason"] = reason
	meta["recoveryAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	metadataJSON, err := archiveJSON(mustJSON(meta))
	if err != nil {
		return false, fmt.Errorf("recovery metadata: %w", err)
	}
	result, err := tx.Exec(`UPDATE tool_execution_records
		SET execution_state = 'running', result_summary_json = NULL, provider_metadata_json = ?, completed_at = NULL
		WHERE execution_key = ? AND execution_state = ?`, metadataJSON, executionKey, state)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// RequestToolExecutionRecovery marks selected interrupted tool calls for an
// explicit user-confirmed retry. It never changes completed records and does
// not itself execute any tool.
func RequestToolExecutionRecovery(sessionDir, sessionID, localTurnID string, providerCallIDs []string) (int64, error) {
	if sessionDir == "" || sessionID == "" || localTurnID == "" || len(providerCallIDs) == 0 {
		return 0, fmt.Errorf("session, local turn and provider call IDs are required")
	}
	placeholders := make([]string, len(providerCallIDs))
	args := make([]any, 0, len(providerCallIDs)+3)
	args = append(args, sessionID, localTurnID)
	for i, id := range providerCallIDs {
		if strings.TrimSpace(id) == "" {
			return 0, fmt.Errorf("provider call IDs must not be empty")
		}
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, "running", "interrupted")
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return 0, err
	}
	query := `UPDATE tool_execution_records SET execution_state = 'retry_requested', result_summary_json = NULL, completed_at = NULL
		WHERE session_id = ? AND local_turn_id = ? AND provider_call_id IN (` + strings.Join(placeholders, ",") + `)
		AND execution_state IN (?, ?)`
	result, err := tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// AbandonInterruptedToolExecutionRecords marks uncertain executions as
// explicitly abandoned. It never retries a tool or invents a tool output;
// callers use it only after they have established that no runtime owns the
// session lock. This makes a subsequent user-submitted run a new operation
// instead of silently replaying a potentially side-effecting call.
func AbandonInterruptedToolExecutionRecords(sessionDir, sessionID, localTurnID string) (int64, error) {
	if sessionID == "" || localTurnID == "" {
		return 0, fmt.Errorf("session ID and local turn ID are required")
	}
	details, err := archiveJSON(json.RawMessage(`{"content":"Tool execution explicitly abandoned after interruption; it was not retried.","isError":true,"reason":"manual_abandon"}`))
	if err != nil {
		return 0, fmt.Errorf("build abandonment summary: %w", err)
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return 0, err
	}
	now := time.Now().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE tool_execution_records
		SET execution_state = 'abandoned', result_summary_json = ?, completed_at = ?
		WHERE session_id = ? AND local_turn_id = ? AND execution_state IN ('running', 'interrupted')`,
		details, now, sessionID, localTurnID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func SaveResponseRun(sessionDir string, run ResponseRun) error {
	if run.SessionID == "" || run.LocalRunID == "" || run.Provider == "" || run.API == "" || run.State == "" {
		return fmt.Errorf("session ID, local run ID, provider, API and state are required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, run.SessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO response_runs
		(session_id, local_run_id, local_turn_id, message_id, response_id, provider, api, state, polling_url,
		 last_event_sequence, cancel_requested, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, local_run_id) DO UPDATE SET
			local_turn_id = excluded.local_turn_id,
			message_id = excluded.message_id,
			response_id = excluded.response_id,
			provider = excluded.provider,
			api = excluded.api,
			state = excluded.state,
			polling_url = excluded.polling_url,
			last_event_sequence = excluded.last_event_sequence,
			cancel_requested = excluded.cancel_requested,
			updated_at = excluded.updated_at`,
		run.SessionID, run.LocalRunID, run.LocalTurnID, nullableInt64(run.MessageID), nullableString(run.ResponseID), run.Provider, run.API,
		run.State, nullableString(run.PollingURL), nullableInt64(run.LastEventSequence),
		boolToInt(run.CancelRequested), run.CreatedAt.Format(time.RFC3339Nano),
		run.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func GetResponseRun(sessionDir, sessionID, localRunID string) (*ResponseRun, error) {
	if sessionID == "" || localRunID == "" {
		return nil, fmt.Errorf("session ID and local run ID are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var run ResponseRun
	var responseID, pollingURL sql.NullString
	var messageID sql.NullInt64
	var sequence sql.NullInt64
	var createdAt, updatedAt string
	err = db.QueryRow(`SELECT id, session_id, local_run_id, local_turn_id, message_id, response_id, provider, api, state,
		polling_url, last_event_sequence, cancel_requested, created_at, updated_at
		FROM response_runs WHERE session_id = ? AND local_run_id = ?`, sessionID, localRunID).
		Scan(&run.ID, &run.SessionID, &run.LocalRunID, &run.LocalTurnID, &messageID, &responseID, &run.Provider, &run.API,
			&run.State, &pollingURL, &sequence, &run.CancelRequested, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.ResponseID = responseID.String
	run.MessageID = nullableInt64Value(messageID)
	run.PollingURL = pollingURL.String
	run.LastEventSequence = nullableInt64Value(sequence)
	run.CreatedAt = parseSessionTimestamp(createdAt)
	run.UpdatedAt = parseSessionTimestamp(updatedAt)
	return &run, nil
}

func ListResponseRuns(sessionDir, sessionID string, limit int) ([]ResponseRun, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, local_run_id, local_turn_id, message_id, response_id, provider, api, state,
		polling_url, last_event_sequence, cancel_requested, created_at, updated_at
		FROM response_runs WHERE session_id = ? ORDER BY created_at ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ResponseRun
	for rows.Next() {
		var run ResponseRun
		var responseID, pollingURL sql.NullString
		var messageID sql.NullInt64
		var sequence sql.NullInt64
		var createdAt, updatedAt string
		if err := rows.Scan(&run.ID, &run.SessionID, &run.LocalRunID, &run.LocalTurnID, &messageID, &responseID, &run.Provider,
			&run.API, &run.State, &pollingURL, &sequence, &run.CancelRequested,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		run.ResponseID = responseID.String
		run.MessageID = nullableInt64Value(messageID)
		run.PollingURL = pollingURL.String
		run.LastEventSequence = nullableInt64Value(sequence)
		run.CreatedAt = parseSessionTimestamp(createdAt)
		run.UpdatedAt = parseSessionTimestamp(updatedAt)
		result = append(result, run)
	}
	return result, rows.Err()
}

// GetResponseSessionState returns the durable remote lineage for a local
// session. A missing record means the caller must use its configured default,
// normally replay mode.
func GetResponseSessionState(sessionDir, sessionID string) (*ResponseSessionState, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var state ResponseSessionState
	var previousResponseID, conversationID sql.NullString
	var updatedAt string
	err = db.QueryRow(`SELECT session_id, state_mode, previous_response_id, conversation_id,
		provider, api, model, version, updated_at
		FROM response_session_state WHERE session_id = ?`, sessionID).
		Scan(&state.SessionID, &state.StateMode, &previousResponseID, &conversationID,
			&state.Provider, &state.API, &state.Model, &state.Version, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.PreviousResponseID = previousResponseID.String
	state.ConversationID = conversationID.String
	state.UpdatedAt = parseSessionTimestamp(updatedAt)
	return &state, nil
}

// CompareAndSwapResponseSessionState advances a session lineage only when the
// caller observed expectedVersion. It prevents two concurrent turns from
// silently branching a previous_response_id chain.
func CompareAndSwapResponseSessionState(sessionDir string, state ResponseSessionState, expectedVersion int64) (bool, error) {
	if state.SessionID == "" || state.StateMode == "" {
		return false, fmt.Errorf("session ID and state mode are required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, state.SessionID); err != nil {
		return false, err
	}
	if expectedVersion == 0 {
		result, err := tx.Exec(`INSERT INTO response_session_state
			(session_id, state_mode, previous_response_id, conversation_id, provider, api, model, version, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(session_id) DO NOTHING`,
			state.SessionID, state.StateMode, nullableString(state.PreviousResponseID), nullableString(state.ConversationID),
			state.Provider, state.API, state.Model, state.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return false, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return changed == 1, nil
	}
	result, err := tx.Exec(`UPDATE response_session_state SET
		state_mode = ?, previous_response_id = ?, conversation_id = ?, provider = ?, api = ?, model = ?,
		version = version + 1, updated_at = ?
		WHERE session_id = ? AND version = ?`,
		state.StateMode, nullableString(state.PreviousResponseID), nullableString(state.ConversationID),
		state.Provider, state.API, state.Model, state.UpdatedAt.Format(time.RFC3339Nano),
		state.SessionID, expectedVersion)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64Value(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func archiveJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	sanitized := redactArchiveValue(value)
	encoded, err := json.Marshal(sanitized)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxResponseArchiveJSONBytes {
		return nil, fmt.Errorf("JSON exceeds %d bytes", maxResponseArchiveJSONBytes)
	}
	return encoded, nil
}

func redactArchiveValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, nested := range typed {
			lower := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
			if strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "password") || strings.Contains(lower, "cookie") {
				if isArchiveUsageCounter(lower, nested) {
					result[key] = nested
					continue
				}
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactArchiveValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, nested := range typed {
			result[i] = redactArchiveValue(nested)
		}
		return result
	default:
		return value
	}
}

// isArchiveUsageCounter preserves well-known numeric usage counters without
// weakening redaction for credentials such as access_token or id_token.
func isArchiveUsageCounter(key string, value any) bool {
	if _, ok := value.(json.Number); !ok {
		return false
	}
	key = strings.ReplaceAll(key, "_", "")
	switch key {
	case "inputtokens", "outputtokens", "totaltokens", "cachedtokens", "reasoningtokens",
		"prompttokens", "completiontokens", "cachereadtokens", "cachewritetokens":
		return true
	default:
		return false
	}
}

func cloneArchiveJSON(raw []byte) json.RawMessage {
	if raw == nil {
		return nil
	}
	result := make([]byte, len(raw))
	copy(result, raw)
	return json.RawMessage(result)
}
