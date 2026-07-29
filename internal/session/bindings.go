package session

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Binding describes a current external channel binding.
type Binding struct {
	SessionID   string `json:"sessionId"`
	ChannelType string `json:"channelType"`
	ChannelID   string `json:"channelId"`
}

// ChannelToolConfig describes one persisted tool selection for a channel session.
type ChannelToolConfig struct {
	ToolName string `json:"toolName"`
	Enabled  bool   `json:"enabled"`
}

func ListChannelTools(sessionDir, sessionID string) ([]ChannelToolConfig, error) {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT tool_name, enabled FROM session_channel_tools WHERE session_id = ? ORDER BY tool_name`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ChannelToolConfig
	for rows.Next() {
		var item ChannelToolConfig
		var enabled int
		if err := rows.Scan(&item.ToolName, &enabled); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

func SetChannelTools(sessionDir, sessionID string, tools []ChannelToolConfig) error {
	if sessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM session_channel_tools WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for _, item := range tools {
		name := strings.TrimSpace(item.ToolName)
		if name == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO session_channel_tools(session_id, tool_name, enabled) VALUES (?, ?, ?)`, sessionID, name, boolInt(item.Enabled)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ListBindings(sessionDir string) ([]Binding, error) {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, channel_type, channel_id FROM sessions WHERE channel_type IN ('wechat', 'feishu') AND channel_id <> '' ORDER BY channel_type, channel_id`)
	if err != nil {
		return nil, fmt.Errorf("list session bindings: %w", err)
	}
	defer rows.Close()
	var result []Binding
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.SessionID, &b.ChannelType, &b.ChannelID); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func FindBinding(sessionDir, channelType, channelID string) (*Binding, error) {
	if err := validateBinding(channelType, channelID); err != nil {
		return nil, err
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var b Binding
	err = db.QueryRow(`SELECT id, channel_type, channel_id FROM sessions WHERE channel_type = ? AND channel_id = ?`, channelType, channelID).
		Scan(&b.SessionID, &b.ChannelType, &b.ChannelID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find session binding: %w", err)
	}
	return &b, nil
}

// BindSession atomically binds an existing local session to one channel identity.
func BindSession(sessionDir, sessionID, channelType, channelID string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	if err := validateBinding(channelType, channelID); err != nil {
		return err
	}
	if channelType == "local" {
		return fmt.Errorf("use UnbindSession to make a session local")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin bind session: %w", err)
	}
	defer tx.Rollback()
	var currentType, currentID string
	if err := tx.QueryRow(`SELECT channel_type, channel_id FROM sessions WHERE id = ?`, sessionID).Scan(&currentType, &currentID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("session %q not found", sessionID)
		}
		return fmt.Errorf("read session binding: %w", err)
	}
	if currentType != "local" || currentID != "" {
		return fmt.Errorf("session %q is already bound to %s/%s", sessionID, currentType, currentID)
	}
	var other string
	err = tx.QueryRow(`SELECT id FROM sessions WHERE channel_type = ? AND channel_id = ? AND id <> ?`, channelType, channelID, sessionID).Scan(&other)
	if err != sql.ErrNoRows {
		if err == nil {
			return fmt.Errorf("identity is already bound to session %q", other)
		}
		return fmt.Errorf("check existing binding: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET channel_type = ?, channel_id = ? WHERE id = ?`, channelType, channelID, sessionID); err != nil {
		return fmt.Errorf("bind session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bind session: %w", err)
	}
	return nil
}

// UnbindSession makes a channel-bound session local while retaining its history.
func UnbindSession(sessionDir, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	result, err := db.Exec(`UPDATE sessions SET channel_type = 'local', channel_id = '' WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("unbind session: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

// TransferBinding atomically moves a channel identity from one session to another.
func TransferBinding(sessionDir, channelType, channelID, fromSessionID, toSessionID string) error {
	if channelType != "wechat" && channelType != "feishu" {
		return fmt.Errorf("unsupported channel type %q", channelType)
	}
	if channelID == "" || fromSessionID == "" || toSessionID == "" {
		return fmt.Errorf("channel ID and session IDs are required")
	}
	if fromSessionID == toSessionID {
		return fmt.Errorf("source and target sessions must differ")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transfer binding: %w", err)
	}
	defer tx.Rollback()
	var currentType, currentID string
	if err := tx.QueryRow(`SELECT channel_type, channel_id FROM sessions WHERE id = ?`, fromSessionID).Scan(&currentType, &currentID); err != nil {
		return fmt.Errorf("read source binding: %w", err)
	}
	if currentType != channelType || currentID != channelID {
		return fmt.Errorf("source session is not bound to %s/%s", channelType, channelID)
	}
	var targetType, targetID string
	if err := tx.QueryRow(`SELECT channel_type, channel_id FROM sessions WHERE id = ?`, toSessionID).Scan(&targetType, &targetID); err != nil {
		return fmt.Errorf("read target session: %w", err)
	}
	if targetType != "local" || targetID != "" {
		return fmt.Errorf("target session is already bound")
	}
	if _, err := tx.Exec(`UPDATE sessions SET channel_type = 'local', channel_id = '' WHERE id = ?`, fromSessionID); err != nil {
		return fmt.Errorf("clear source binding: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET channel_type = ?, channel_id = ? WHERE id = ?`, channelType, channelID, toSessionID); err != nil {
		return fmt.Errorf("set target binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transfer binding: %w", err)
	}
	return nil
}

// RotateBoundSession atomically creates a new bound session and clears the old one.
func RotateBoundSession(workDir, sessionDir, channelType, channelID, oldSessionID string) (*Manager, error) {
	if err := validateBinding(channelType, channelID); err != nil {
		return nil, err
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var currentType, currentID string
	if err := tx.QueryRow(`SELECT channel_type, channel_id FROM sessions WHERE id = ?`, oldSessionID).Scan(&currentType, &currentID); err != nil {
		return nil, err
	}
	if currentType != channelType || currentID != channelID {
		return nil, fmt.Errorf("session is no longer bound to %s/%s", channelType, channelID)
	}
	id := GenerateID()
	if _, err := tx.Exec(`INSERT INTO sessions(id, cwd, timestamp, parent_session, version, channel_type, channel_id) VALUES (?, ?, ?, '', ?, ?, ?)`, id, workDir, time.Now().UTC().Format(time.RFC3339Nano), CurrentVersion, channelType, channelID); err != nil {
		return nil, fmt.Errorf("create rotated session: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET channel_type = 'local', channel_id = '' WHERE id = ?`, oldSessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return OpenByIDExact(sessionDir, id)
}

func CreateBound(workDir, sessionDir, channelType, channelID string) (*Manager, error) {
	if err := validateBinding(channelType, channelID); err != nil {
		return nil, err
	}
	m := New(workDir, sessionDir)
	if err := m.InitWithBinding(channelType, channelID); err != nil {
		return nil, err
	}
	return m, nil
}

// SetSessionBinding updates a Manager's in-memory header after a binding change.
func (m *Manager) SetSessionBinding(channelType, channelID string) error {
	if err := validateBinding(channelType, channelID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.header == nil {
		return fmt.Errorf("session is not initialized")
	}
	m.header.ChannelType, m.header.ChannelID = channelType, channelID
	return nil
}
