package session

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/commondb"
	"github.com/startvibecoding/mothx/internal/platform"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/util"
)

const CurrentVersion = 3

var ErrSessionModified = errors.New("session was modified by another process")

// ErrSessionIDExists means a new session attempted to reuse an existing ID.
// A duplicate must be rejected: updating the sessions row would merge the new
// header with the old entries and create a forked conversation.
var ErrSessionIDExists = errors.New("session ID already exists")

// Manager manages a single session's state and persistence.
type Manager struct {
	mu         sync.RWMutex
	file       string // path to the session's .db handle file
	header     *Header
	entries    []interface{} // all entry types
	leafID     *string
	cwd        string
	sessionDir string
	subAgent   bool
}

type replayState struct {
	messages []provider.Message
	entryIDs []string
}

// SequencedMessage is a persisted conversation message with its entries.seq cursor.
type SequencedMessage struct {
	Seq     int64
	EntryID string
	Message provider.Message
}

// SequencedSessionRunEvent is a run lifecycle event with its table cursor.
type SequencedSessionRunEvent struct {
	Seq   int64
	Event SessionRunEvent
}

// SequencedSessionCapabilityEvent is a capability event with its table cursor.
type SequencedSessionCapabilityEvent struct {
	Seq   int64
	Event SessionCapabilityEvent
}

// encodePath encodes a directory path for use in a session directory name.
// Uses base64 URL encoding to avoid collisions from different characters mapping
// to the same replacement (e.g. "/" and ":" both mapped to "-").
func encodePath(p string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p))
}

// New creates a new session manager for a new session.
func New(cwd, sessionDir string) *Manager {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	return &Manager{
		cwd:        cwd,
		sessionDir: sessionDir,
	}
}

// NewSubAgent creates a session manager whose records are stored separately
// from user-continuable sessions.
func NewSubAgent(cwd, sessionDir string) *Manager {
	m := New(cwd, sessionDir)
	m.subAgent = true
	return m
}

func (m *Manager) sessionTable() string {
	if m.subAgent {
		return "sub_session"
	}
	return "sessions"
}

func (m *Manager) entriesTable() string {
	if m.subAgent {
		return "sub_entries"
	}
	return "entries"
}

// Open opens an existing session file.
func Open(path string) (*Manager, error) {
	m := &Manager{file: path}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload refreshes a manager from the shared SQLite session database.
// Serve entry points may retain a Manager while another UI writes the same
// session; reload after acquiring the session runtime lock so the next append
// uses the current leaf instead of an old optimistic-lock parent.
func (m *Manager) Reload() error {
	if m == nil {
		return fmt.Errorf("session manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.file == "" {
		return fmt.Errorf("session manager is not initialized")
	}
	oldHeader, oldEntries, oldLeaf, oldCwd := m.header, m.entries, m.leafID, m.cwd
	m.header = nil
	m.entries = nil
	m.leafID = nil
	if err := m.load(); err != nil {
		m.header, m.entries, m.leafID, m.cwd = oldHeader, oldEntries, oldLeaf, oldCwd
		return err
	}
	return nil
}

// ContinueRecent continues the most recent session for a directory, or creates new.
func ContinueRecent(cwd, sessionDir string) (*Manager, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	sessions, err := ListForDir(cwd, sessionDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) > 0 {
		// Most recent
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].ModTime.After(sessions[j].ModTime)
		})
		return Open(sessions[0].Path)
	}

	m := New(cwd, sessionDir)
	if err := m.Init(); err != nil {
		return nil, err
	}
	return m, nil
}

// OpenByPathOrID opens a session using either an explicit file path or a
// session ID for the supplied working directory.
func OpenByPathOrID(cwd, sessionDir, value string) (*Manager, error) {
	if value == "" {
		return nil, fmt.Errorf("session value is empty")
	}
	if strings.HasSuffix(value, ".db") || strings.ContainsRune(value, os.PathSeparator) {
		return Open(value)
	}
	return OpenByID(cwd, sessionDir, value)
}

// SessionInfo contains metadata about a session file.
type SessionInfo struct {
	Path            string
	ModTime         time.Time
	Name            string
	Cwd             string
	ChannelType     string
	ChannelID       string
	ParentSession   string
	ForkBoundarySeq int64
	SeedLength      int64
	ForkKind        string
}

// sessionDirForCwd returns the encoded session directory path for a working directory.
func sessionDirForCwd(cwd, sessionDir string) string {
	encoded := encodePath(cwd)
	return filepath.Join(sessionDir, "--"+encoded+"--")
}

func canonicalDBPath(path string) (string, error) {
	return commondb.CanonicalPath(path)
}

func sqliteDSN(path string) string {
	return commondb.DSNForOS(path, false)
}

func sqliteDSNForOS(path string, windows bool) string {
	return commondb.DSNForOS(path, windows)
}

func cachedDB(path string) (*sql.DB, error) {
	return commondb.Open(path, EnsureCurrentSchema)
}

// OpenStandaloneDB opens a configured, caller-owned SQLite connection.
func OpenStandaloneDB(path string) (*sql.DB, error) {
	return commondb.OpenStandalone(path, EnsureCurrentSchema)
}

// CloseDatabases checkpoints and closes all process-owned session connections.
func CloseDatabases() error {
	return commondb.CloseAll()
}

func openExistingSessionDB(sessionDir string) (*sql.DB, bool, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	dbPath := filepath.Join(sessionDir, "sessions.db")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	db, err := cachedDB(dbPath)
	return db, err == nil, err
}

// OpenRootDB opens the shared sessions.db for a session root directory.
func OpenRootDB(sessionDir string) (*sql.DB, error) {
	return cachedDB(rootDBPath(sessionDir))
}

func rootDBPath(sessionDir string) string {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	return filepath.Join(sessionDir, "sessions.db")
}

func parseSessionTimestamp(timestampStr string) time.Time {
	ts, _ := time.Parse(time.RFC3339Nano, timestampStr)
	if ts.IsZero() {
		ts, _ = time.Parse(time.RFC3339, timestampStr)
	}
	return ts
}

func virtualSessionFile(sessionDir, id string, ts time.Time) string {
	return filepath.Join(sessionDir, fmt.Sprintf("%s_%s.db", ts.Format("20060102-150405"), id))
}

// ListForDir lists session files for a given working directory.
func ListForDir(cwd, sessionDir string) ([]SessionInfo, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}

	rows, err := db.Query("SELECT id, cwd, timestamp, channel_type, channel_id, parent_session, fork_boundary_seq, seed_length, fork_kind FROM sessions WHERE cwd = ? ORDER BY timestamp DESC", cwd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var id, rowCwd, timestampStr, channelType, channelID string
		var parentSession, forkKind sql.NullString
		var forkBoundarySeq, seedLength sql.NullInt64
		if err := rows.Scan(&id, &rowCwd, &timestampStr, &channelType, &channelID, &parentSession, &forkBoundarySeq, &seedLength, &forkKind); err != nil {
			continue
		}
		ts := parseSessionTimestamp(timestampStr)

		// Create a virtual file path in the sessionDir directory
		virtualFile := virtualSessionFile(sessionDir, id, ts)

		sessions = append(sessions, SessionInfo{
			Path: virtualFile, ModTime: ts, Cwd: rowCwd, ChannelType: channelType, ChannelID: channelID,
			ParentSession: parentSession.String, ForkBoundarySeq: forkBoundarySeq.Int64, SeedLength: seedLength.Int64, ForkKind: forkKind.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sessions, nil
}

// ListAll lists session files across all working directories.
func ListAll(sessionDir string, opts ...ListOption) ([]SessionInfo, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	var opt listOptions
	for _, fn := range opts {
		fn(&opt)
	}

	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}

	query := "SELECT id, cwd, timestamp, channel_type, channel_id, parent_session, fork_boundary_seq, seed_length, fork_kind FROM sessions"
	where, args := sessionListFilter(opt)
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY timestamp DESC"
	if opt.limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opt.limit)
		if opt.offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", opt.offset)
		}
	} else if opt.offset > 0 {
		// OFFSET without LIMIT is invalid SQL; use a large limit.
		query += fmt.Sprintf(" LIMIT 999999 OFFSET %d", opt.offset)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var id, cwd, timestampStr, channelType, channelID, forkKind string
		var parentSession sql.NullString
		var forkBoundarySeq, seedLength int64
		if err := rows.Scan(&id, &cwd, &timestampStr, &channelType, &channelID, &parentSession, &forkBoundarySeq, &seedLength, &forkKind); err != nil {
			provider.DebugLogf("session list scan row: %v", err)
			continue
		}
		ts := parseSessionTimestamp(timestampStr)
		sessions = append(sessions, SessionInfo{
			Path: virtualSessionFile(sessionDir, id, ts), ModTime: ts, Cwd: cwd, ChannelType: channelType, ChannelID: channelID,
			ParentSession: parentSession.String, ForkBoundarySeq: forkBoundarySeq, SeedLength: seedLength, ForkKind: forkKind,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return sessions, nil
}

// CountWithMessages returns the number of sessions that contain at least one
// persisted conversation message. Empty sessions can be created transiently
// during startup or request setup and are not user-visible history.
func CountWithMessages(sessionDir string, opts ...ListOption) (int, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	var opt listOptions
	opt.messagesOnly = true
	for _, fn := range opts {
		fn(&opt)
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return 0, err
	}
	where, args := sessionListFilter(opt)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions WHERE "+where, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func CountAll(sessionDir string) (int, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return 0, err
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

type listOptions struct {
	limit        int
	offset       int
	messagesOnly bool
	search       string
}

type ListOption func(*listOptions)

func WithLimit(limit int) ListOption {
	return func(o *listOptions) {
		o.limit = limit
	}
}

func WithOffset(offset int) ListOption {
	return func(o *listOptions) {
		o.offset = offset
	}
}

// WithMessagesOnly limits session listings to sessions containing at least one
// persisted conversation message. This avoids loading transient empty sessions
// during history pagination.
func WithMessagesOnly() ListOption {
	return func(o *listOptions) {
		o.messagesOnly = true
	}
}

// WithSearch filters sessions by ID, work directory, channel metadata, or
// persisted message/session-info content. It is intended for session listings.
func WithSearch(search string) ListOption {
	return func(o *listOptions) {
		o.search = strings.TrimSpace(search)
	}
}

func sessionListFilter(opt listOptions) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 6)
	if opt.messagesOnly {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM entries e WHERE e.session_id = sessions.id AND e.type = 'message')")
	}
	if opt.search != "" {
		pattern := "%" + opt.search + "%"
		clauses = append(clauses, `(id LIKE ? COLLATE NOCASE OR cwd LIKE ? COLLATE NOCASE OR channel_type LIKE ? COLLATE NOCASE OR channel_id LIKE ? COLLATE NOCASE OR EXISTS (SELECT 1 FROM entries e WHERE e.session_id = sessions.id AND e.data LIKE ? COLLATE NOCASE))`)
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	return strings.Join(clauses, " AND "), args
}

// InitWithBinding initializes a new session with a channel binding.
func (m *Manager) InitWithBinding(channelType, channelID string) error {
	if err := validateBinding(channelType, channelID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initWithBindingLocked("", channelType, channelID)
}

// InitWithIDAndBinding initializes a session with a specific ID and channel binding.
func (m *Manager) InitWithIDAndBinding(id, channelType, channelID string) error {
	if err := validateBinding(channelType, channelID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initWithBindingLocked(id, channelType, channelID)
}

func validateBinding(channelType, channelID string) error {
	if channelType == "" {
		channelType = "local"
	}
	if channelType == "local" && channelID != "" {
		return fmt.Errorf("local session cannot have channel ID")
	}
	if channelType != "local" && channelType != "wechat" && channelType != "feishu" {
		return fmt.Errorf("unsupported channel type %q", channelType)
	}
	if channelType != "local" && channelID == "" {
		return fmt.Errorf("channel ID is required for %s session", channelType)
	}
	return nil
}

// Init initializes a new session with an auto-generated session ID.
// Must be called before appending entries.
func (m *Manager) Init() error {
	return m.InitWithID("")
}

// InitWithID initializes a new session using the provided session ID.
// If id is empty, a new random ID is generated.
func (m *Manager) InitWithID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.initWithIDLocked(id)
}

func (m *Manager) initWithIDLocked(id string) error {
	return m.initWithBindingLocked(id, "local", "")
}

func (m *Manager) initWithBindingLocked(id, channelType, channelID string) error {
	explicitID := id != ""
	for attempt := 0; attempt < 8; attempt++ {
		now := time.Now()
		candidate := id
		if candidate == "" {
			candidate = GenerateID()
		}
		m.header = &Header{
			Type:        EntrySession,
			Version:     CurrentVersion,
			ID:          candidate,
			Timestamp:   now,
			Cwd:         m.cwd,
			ChannelType: channelType,
			ChannelID:   channelID,
		}
		m.entries = nil
		m.leafID = nil

		m.file = filepath.Join(m.sessionDir, fmt.Sprintf("%s_%s.db", now.Format("20060102-150405"), candidate))
		handlePath := ""

		// Write session ID to handle file only for per-channel user session directories.
		if strings.Contains(m.sessionDir, "channels") {
			dir := sessionDirForCwd(m.cwd, m.sessionDir)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return fmt.Errorf("create session dir: %w", err)
			}
			m.file = filepath.Join(dir, fmt.Sprintf("%s_%s.db", now.Format("20060102-150405"), candidate))
			handlePath = m.file
		}

		err := m.writeEntry(m.header)
		if err == nil {
			if handlePath != "" {
				if err := os.WriteFile(handlePath, []byte(candidate), 0600); err != nil {
					return fmt.Errorf("write session handle file: %w", err)
				}
			}
			return nil
		}
		if explicitID || !errors.Is(err, ErrSessionIDExists) {
			return err
		}
	}
	return fmt.Errorf("generate unique session ID: %w", ErrSessionIDExists)
}

func (m *Manager) ensureInitializedLocked() error {
	if m.file != "" {
		return nil
	}
	return m.initWithIDLocked("")
}

// OpenByID opens the session for cwd whose session ID matches sessionID.
// Supports prefix matching — if sessionID matches multiple sessions, an error is returned.
func OpenByID(cwd, sessionDir, sessionID string) (*Manager, error) {
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}

	dbPath := filepath.Join(sessionDir, "sessions.db")

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("session %s not found for cwd %s", sessionID, cwd)
	}

	db, err := cachedDB(dbPath)
	if err != nil {
		return nil, err
	}

	// Query by exact match first
	var exactID string
	err = db.QueryRow("SELECT id FROM sessions WHERE id = ? AND cwd = ?", sessionID, cwd).Scan(&exactID)
	if err == nil {
		return openSessionFromDB(exactID, sessionDir)
	}

	// Prefix match
	rows, err := db.Query("SELECT id FROM sessions WHERE cwd = ? AND id LIKE ?", cwd, sessionID+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			provider.DebugLogf("session OpenByID scan match: %v", err)
			continue
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("session %s not found for cwd %s", sessionID, cwd)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("session ID %s is ambiguous for cwd %s", sessionID, cwd)
	}

	return openSessionFromDB(matches[0], sessionDir)
}

// OpenByIDExact opens a session by exact session ID regardless of cwd.
func OpenByIDExact(sessionDir, sessionID string) (*Manager, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is empty")
	}
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	dbPath := filepath.Join(sessionDir, "sessions.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	return openSessionFromDB(sessionID, sessionDir)
}

// LatestAdditionalDirectoriesByID reads the replayed directory binding for a
// session without exposing SQLite details to protocol adapters.
func LatestAdditionalDirectoriesByID(sessionDir, sessionID string) ([]string, error) {
	m, err := OpenByIDExact(sessionDir, sessionID)
	if err != nil {
		return nil, err
	}
	entry, ok := m.GetLatestAdditionalDirectories()
	if !ok {
		return []string{}, nil
	}
	return append([]string(nil), entry.Directories...), nil
}

// LatestModelChangeByID reads the replayed provider/model binding for a
// session without exposing SQLite details to protocol adapters.
func LatestModelChangeByID(sessionDir, sessionID string) (ModelChangeEntry, bool, error) {
	m, err := OpenByIDExact(sessionDir, sessionID)
	if err != nil {
		return ModelChangeEntry{}, false, err
	}
	entry, ok := m.GetLatestModelChange()
	return entry, ok, nil
}

// findHandleForID finds the .db handle file that contains the given session ID.
func findHandleForID(dir, sessionID string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		// Skip sessions.db itself
		if e.Name() == "sessions.db" || strings.HasPrefix(e.Name(), "sessions.db-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == sessionID {
			return path
		}
		// Also check filename pattern: timestamp_id.db
		base := strings.TrimSuffix(e.Name(), ".db")
		if idx := strings.Index(base, "_"); idx >= 0 {
			if strings.HasPrefix(base[idx+1:], sessionID) {
				return path
			}
		}
	}
	return ""
}

// openSessionFromDB reconstructs a Manager directly from the SQLite database
// when no handle file is available.
func openSessionFromDB(sessionID, dir string) (*Manager, error) {
	m := &Manager{
		sessionDir: dir,
	}

	dbPath := filepath.Join(dir, "sessions.db")
	db, err := cachedDB(dbPath)
	if err != nil {
		return nil, err
	}
	var timestampStr string
	if err := db.QueryRow("SELECT timestamp FROM sessions WHERE id = ?", sessionID).Scan(&timestampStr); err != nil && err != sql.ErrNoRows {
		provider.DebugLogf("session open %q read timestamp: %v", sessionID, err)
	}

	if timestampStr != "" {
		ts, _ := time.Parse(time.RFC3339Nano, timestampStr)
		if ts.IsZero() {
			ts, _ = time.Parse(time.RFC3339, timestampStr)
		}
		if !ts.IsZero() {
			m.file = filepath.Join(dir, fmt.Sprintf("%s_%s.db", ts.Format("20060102-150405"), sessionID))
		}
	}

	if m.file == "" {
		m.file = filepath.Join(dir, sessionID+".db")
	}

	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func sessionFileID(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".db")
	if idx := strings.Index(base, "_"); idx >= 0 {
		return base[idx+1:]
	}
	if base == "" || base == "active" || base == "sessions" {
		return ""
	}
	if len(base) >= 8 {
		return base
	}
	return ""
}

// AppendMessage adds a message entry.
func (m *Manager) AppendMessage(msg provider.Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}

	id := GenerateID()
	entry := MessageEntry{
		EntryBase: EntryBase{
			Type:      EntryMessage,
			ID:        id,
			ParentID:  m.leafID,
			Timestamp: time.Now(),
		},
		Message: msg,
	}

	if err := m.writeEntry(entry); err != nil {
		return "", err
	}

	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// AppendModelChange records a model change.
func (m *Manager) AppendModelChange(providerName, modelID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}

	id := GenerateID()
	entry := ModelChangeEntry{
		EntryBase: EntryBase{
			Type:      EntryModelChange,
			ID:        id,
			ParentID:  m.leafID,
			Timestamp: time.Now(),
		},
		Provider: providerName,
		ModelID:  modelID,
	}

	if err := m.writeEntry(entry); err != nil {
		return "", err
	}

	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// AppendModeChange records a session execution mode change.
func (m *Manager) AppendModeChange(mode string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}

	id := GenerateID()
	entry := ModeChangeEntry{
		EntryBase: EntryBase{
			Type:      EntryModeChange,
			ID:        id,
			ParentID:  m.leafID,
			Timestamp: time.Now(),
		},
		Mode: mode,
	}
	if err := m.writeEntry(entry); err != nil {
		return "", err
	}
	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// AppendThinkingLevelChange records a thinking level change.
func (m *Manager) AppendThinkingLevelChange(level string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}

	id := GenerateID()
	entry := ThinkingLevelChangeEntry{
		EntryBase: EntryBase{
			Type:      EntryThinkingChange,
			ID:        id,
			ParentID:  m.leafID,
			Timestamp: time.Now(),
		},
		ThinkingLevel: level,
	}

	if err := m.writeEntry(entry); err != nil {
		return "", err
	}

	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// AppendAdditionalDirectories records a complete replacement of the
// session's additional directory roots.
func (m *Manager) AppendAdditionalDirectories(directories []string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}
	id := GenerateID()
	entry := AdditionalDirectoriesEntry{EntryBase: EntryBase{Type: EntryAdditionalDirectories, ID: id, ParentID: m.leafID, Timestamp: time.Now()}, Directories: append([]string(nil), directories...)}
	if err := m.writeEntry(entry); err != nil {
		return "", err
	}
	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// AppendCompaction records a context compaction.
func (m *Manager) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}

	summaryVersion := 1
	previousCompactionID := ""
	if previous, ok := latestCompactionLocked(m.entries); ok {
		previousCompactionID = previous.ID
		if previous.SummaryVersion > 0 {
			summaryVersion = previous.SummaryVersion + 1
		} else {
			summaryVersion = 2
		}
	}

	id := GenerateID()
	entry := CompactionEntry{
		EntryBase: EntryBase{
			Type:      EntryCompaction,
			ID:        id,
			ParentID:  m.leafID,
			Timestamp: time.Now(),
		},
		Summary:              summary,
		FirstKeptEntry:       firstKeptEntryID,
		TokensBefore:         tokensBefore,
		SummaryVersion:       summaryVersion,
		PreviousCompactionID: previousCompactionID,
		LastSummarizedEntry:  lastSummarizedEntryIDLocked(m.entries, firstKeptEntryID),
	}

	if err := m.writeEntry(entry); err != nil {
		return "", err
	}

	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

func latestCompactionLocked(entries []interface{}) (CompactionEntry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entry, ok := entries[i].(CompactionEntry); ok {
			return entry, true
		}
	}
	return CompactionEntry{}, false
}

func lastSummarizedEntryIDLocked(entries []interface{}, firstKeptEntryID string) string {
	state := buildReplayState(entries)
	if firstKeptEntryID == "" {
		for i := len(state.entryIDs) - 1; i >= 0; i-- {
			if state.entryIDs[i] != "" {
				return state.entryIDs[i]
			}
		}
		return ""
	}
	for i, id := range state.entryIDs {
		if id != firstKeptEntryID {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if state.entryIDs[j] != "" {
				return state.entryIDs[j]
			}
		}
		return ""
	}
	return ""
}

// AppendSessionInfo records a session display name. It is retained for
// compatibility; new callers should use AppendSessionTitle with a source.
func (m *Manager) AppendSessionInfo(name string) (string, error) {
	return m.AppendSessionTitle(name, "manual")
}

// AppendSessionTitle records a session display name and its origin.
func (m *Manager) AppendSessionTitle(name, source string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("session title is required")
	}
	if source != "manual" && source != "auto" {
		return "", fmt.Errorf("invalid session title source")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return "", err
	}
	id := GenerateID()
	entry := SessionInfoEntry{EntryBase: EntryBase{Type: EntrySessionInfo, ID: id, ParentID: m.leafID, Timestamp: time.Now()}, Name: name, Source: source}
	if err := m.writeEntry(entry); err != nil {
		return "", err
	}
	m.entries = append(m.entries, entry)
	m.leafID = &id
	return id, nil
}

// ReplayState is the reconstructed conversation state after applying compactions.
type ReplayState struct {
	Messages []provider.Message
	EntryIDs []string
}

// GetMessages extracts all messages from the current branch.
func (m *Manager) GetMessages() []provider.Message {
	state := m.GetReplayState()
	return state.Messages
}

// GetReplayState returns the current branch after applying compaction entries.
func (m *Manager) GetReplayState() ReplayState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := buildReplayState(m.entries)
	return ReplayState{
		Messages: state.messages,
		EntryIDs: state.entryIDs,
	}
}

// GetLeafID returns the current leaf entry ID.
func (m *Manager) GetLeafID() *string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.leafID
}

// GetLatestCompaction returns the newest compaction entry in the current session.
func (m *Manager) GetLatestCompaction() (CompactionEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return latestCompactionLocked(m.entries)
}

// GetLatestModelChange returns the newest model binding in the session.
func (m *Manager) GetLatestModelChange() (ModelChangeEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.entries) - 1; i >= 0; i-- {
		if entry, ok := m.entries[i].(ModelChangeEntry); ok {
			return entry, true
		}
	}
	return ModelChangeEntry{}, false
}

// GetLatestModeChange returns the newest session mode in the session.
func (m *Manager) GetLatestModeChange() (ModeChangeEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.entries) - 1; i >= 0; i-- {
		if entry, ok := m.entries[i].(ModeChangeEntry); ok {
			return entry, true
		}
	}
	return ModeChangeEntry{}, false
}

// GetLatestThinkingLevelChange returns the newest thinking level in the session.
func (m *Manager) GetLatestThinkingLevelChange() (ThinkingLevelChangeEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.entries) - 1; i >= 0; i-- {
		if entry, ok := m.entries[i].(ThinkingLevelChangeEntry); ok {
			return entry, true
		}
	}
	return ThinkingLevelChangeEntry{}, false
}

// GetLatestAdditionalDirectories returns the latest complete directory-root
// binding persisted in this session.
func (m *Manager) GetLatestAdditionalDirectories() (AdditionalDirectoriesEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.entries) - 1; i >= 0; i-- {
		if entry, ok := m.entries[i].(AdditionalDirectoriesEntry); ok {
			entry.Directories = append([]string(nil), entry.Directories...)
			return entry, true
		}
	}
	return AdditionalDirectoriesEntry{}, false
}

// GetFile returns the session file path.
func (m *Manager) GetFile() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.file
}

// GetSessionDir returns the root directory containing this manager's shared
// sessions database. Runtime extensions use it for auxiliary session tables.
func (m *Manager) GetSessionDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.sessionDir == "" && m.file != "" {
		return filepath.Dir(resolveDBPath(m.file))
	}
	return m.sessionDir
}

// GetHeader returns the session header.
func (m *Manager) GetHeader() *Header {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.header
}

// StartConversationTurn opens the durable boundary used by Session fork
// resolution. It is intentionally optional on session.Store so transient and
// in-memory agents do not need a SQLite turn index.
func (m *Manager) StartConversationTurn(turnID, intentID, runID string) error {
	if m == nil {
		return fmt.Errorf("session manager is nil")
	}
	m.mu.Lock()
	if err := m.ensureInitializedLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	sessionDir, sessionID, file := m.sessionDir, m.header.ID, m.file
	m.mu.Unlock()
	if sessionDir == "" {
		sessionDir = filepath.Dir(resolveDBPath(file))
	}
	if err := StartConversationTurn(sessionDir, ConversationTurn{ID: turnID, SessionID: sessionID, IntentID: intentID, RunID: runID}); err != nil {
		return err
	}
	if err := m.Reload(); err != nil {
		if cleanupErr := EndConversationTurn(sessionDir, sessionID, turnID, "failed", "turn_reload", time.Now()); cleanupErr != nil {
			return fmt.Errorf("reload session after starting conversation turn: %w (turn cleanup: %v)", err, cleanupErr)
		}
		return err
	}
	return nil
}

// EndConversationTurn closes the durable boundary used by Session fork
// resolution. It is safe for callers to report failed, cancelled and
// incomplete outcomes; all are terminal turn states.
func (m *Manager) EndConversationTurn(turnID, status, stopReason string) error {
	if m == nil || m.header == nil {
		return fmt.Errorf("session manager is not initialized")
	}
	m.mu.RLock()
	sessionDir, sessionID, file := m.sessionDir, m.header.ID, m.file
	m.mu.RUnlock()
	if sessionDir == "" {
		sessionDir = filepath.Dir(resolveDBPath(file))
	}
	if err := EndConversationTurn(sessionDir, sessionID, turnID, status, stopReason, time.Now()); err != nil {
		return err
	}
	return m.Reload()
}

func buildReplayState(entries []interface{}) replayState {
	state := replayState{}
	for _, entry := range entries {
		switch e := entry.(type) {
		case MessageEntry:
			state.messages = append(state.messages, cloneMessage(e.Message))
			state.entryIDs = append(state.entryIDs, e.ID)
		case CompactionEntry:
			applyCompactionEntry(&state, e)
		}
	}
	return state
}

type sequencedReplayState struct {
	messages []SequencedMessage
	entryIDs []string
}

func applySequencedCompactionEntry(state *sequencedReplayState, entry CompactionEntry, seq int64) {
	if state == nil {
		return
	}

	summary := provider.NewSystemInjectedUserMessage(entry.Summary)
	if entry.FirstKeptEntry == "" {
		state.messages = []SequencedMessage{{
			Seq:     seq,
			EntryID: entry.ID,
			Message: summary,
		}}
		state.entryIDs = []string{entry.ID}
		return
	}

	firstKept := -1
	for i, id := range state.entryIDs {
		if id == entry.FirstKeptEntry {
			firstKept = i
			break
		}
	}
	if firstKept < 0 {
		return
	}
	if firstKept > len(state.messages) || firstKept > len(state.entryIDs) {
		return
	}

	nextMessages := make([]SequencedMessage, 0, 1+len(state.messages[firstKept:]))
	nextMessages = append(nextMessages, SequencedMessage{
		Seq:     seq,
		EntryID: entry.ID,
		Message: summary,
	})
	for _, msg := range state.messages[firstKept:] {
		cloned := msg
		cloned.Message = cloneMessage(msg.Message)
		cloned.Message.Usage = nil
		nextMessages = append(nextMessages, cloned)
	}

	nextEntryIDs := make([]string, 0, 1+len(state.entryIDs[firstKept:]))
	nextEntryIDs = append(nextEntryIDs, entry.ID)
	nextEntryIDs = append(nextEntryIDs, append([]string(nil), state.entryIDs[firstKept:]...)...)

	state.messages = nextMessages
	state.entryIDs = nextEntryIDs
}

func applyCompactionEntry(state *replayState, entry CompactionEntry) {
	if state == nil {
		return
	}

	summary := provider.NewSystemInjectedUserMessage(entry.Summary)
	if entry.FirstKeptEntry == "" {
		state.messages = []provider.Message{summary}
		state.entryIDs = []string{""}
		return
	}

	firstKept := -1
	for i, id := range state.entryIDs {
		if id == entry.FirstKeptEntry {
			firstKept = i
			break
		}
	}
	if firstKept < 0 {
		return
	}
	// Guard against message/entryID slices that may be out of sync to avoid
	// slicing out of bounds below.
	if firstKept > len(state.messages) || firstKept > len(state.entryIDs) {
		return
	}

	nextMessages := make([]provider.Message, 0, 1+len(state.messages[firstKept:]))
	nextMessages = append(nextMessages, summary)
	for _, msg := range state.messages[firstKept:] {
		cloned := cloneMessage(msg)
		cloned.Usage = nil
		nextMessages = append(nextMessages, cloned)
	}

	nextEntryIDs := make([]string, 0, 1+len(state.entryIDs[firstKept:]))
	nextEntryIDs = append(nextEntryIDs, "")
	nextEntryIDs = append(nextEntryIDs, append([]string(nil), state.entryIDs[firstKept:]...)...)

	state.messages = nextMessages
	state.entryIDs = nextEntryIDs
}

func cloneMessage(msg provider.Message) provider.Message {
	cloned := msg
	if len(msg.Contents) > 0 {
		cloned.Contents = make([]provider.ContentBlock, len(msg.Contents))
		for i, block := range msg.Contents {
			cloned.Contents[i] = cloneContentBlock(block)
		}
	}
	if msg.Usage != nil {
		usage := *msg.Usage
		cloned.Usage = &usage
	}
	return cloned
}

func cloneContentBlock(block provider.ContentBlock) provider.ContentBlock {
	cloned := block
	if block.Image != nil {
		image := *block.Image
		cloned.Image = &image
	}
	if block.ToolCall != nil {
		toolCall := *block.ToolCall
		toolCall.Arguments = append([]byte(nil), block.ToolCall.Arguments...)
		cloned.ToolCall = &toolCall
	}
	if block.CacheControl != nil {
		cacheControl := *block.CacheControl
		cloned.CacheControl = &cacheControl
	}
	return cloned
}

// resolveDBPath determines the path to the shared sessions.db for a given session file.
func resolveDBPath(sessionFilePath string) string {
	clean := filepath.Clean(sessionFilePath)
	dir := filepath.Dir(clean)

	// If inside standard session dir --<encoded>--, use the shared DB in the parent session root.
	if strings.Contains(filepath.Base(dir), "--") {
		return filepath.Join(filepath.Dir(dir), "sessions.db")
	}

	// If inside messaging channel per-user sessions dir, use the DB beside active/archive handles.
	if strings.Contains(clean, string(filepath.Separator)+"channels"+string(filepath.Separator)) {
		return filepath.Join(dir, "sessions.db")
	}

	// If dir is "." or empty, or does not exist, use default home fallback if possible
	if dir == "." || dir == "" {
		return filepath.Join(platform.SessionDir(), "sessions.db")
	}

	return filepath.Join(dir, "sessions.db")
}

func (m *Manager) withDB(fn func(*sql.DB) error) error {
	dbPath := resolveDBPath(m.file)
	db, err := cachedDB(dbPath)
	if err != nil {
		return err
	}
	return fn(db)
}

func getEntryMetadata(entry interface{}) (id string, typeStr string, parentID *string, timestamp time.Time) {
	switch e := entry.(type) {
	case *Header:
		return e.ID, string(e.Type), nil, e.Timestamp
	case Header:
		return e.ID, string(e.Type), nil, e.Timestamp
	case *MessageEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case MessageEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *ModelChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case ModelChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *ModeChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case ModeChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *ThinkingLevelChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case ThinkingLevelChangeEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *AdditionalDirectoriesEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case AdditionalDirectoriesEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *CompactionEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case CompactionEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *SessionInfoEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case SessionInfoEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *TurnStartEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case TurnStartEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *TurnEndEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case TurnEndEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *BranchSummaryEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case BranchSummaryEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case *LabelEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	case LabelEntry:
		return e.ID, string(e.Type), e.ParentID, e.Timestamp
	default:
		return "", "", nil, time.Now()
	}
}

// load reads a session from the SQLite database using the handle file's session ID.
func (m *Manager) load() error {
	var sessionID string
	idBytes, err := os.ReadFile(m.file)
	if err == nil {
		sessionID = strings.TrimSpace(string(idBytes))
	} else if os.IsNotExist(err) {
		sessionID = sessionFileID(m.file)
	} else {
		return fmt.Errorf("read session handle file: %w", err)
	}

	if sessionID == "" {
		return fmt.Errorf("could not determine session ID from %s", m.file)
	}

	return m.withDB(func(db *sql.DB) error {
		// Load session metadata
		var cwd, timestamp, parentSession, channelType, channelID, forkKind sql.NullString
		var forkBoundarySeq, seedLength int64
		var version int
		err := db.QueryRow("SELECT cwd, timestamp, parent_session, version, channel_type, channel_id, fork_boundary_seq, seed_length, fork_kind FROM "+m.sessionTable()+" WHERE id = ?", sessionID).
			Scan(&cwd, &timestamp, &parentSession, &version, &channelType, &channelID, &forkBoundarySeq, &seedLength, &forkKind)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("session %q not registered in DB", sessionID)
			}
			return err
		}

		ts, _ := time.Parse(time.RFC3339Nano, timestamp.String)
		m.header = &Header{
			Type:            EntrySession,
			Version:         version,
			ID:              sessionID,
			Timestamp:       ts,
			Cwd:             cwd.String,
			ParentSession:   parentSession.String,
			ChannelType:     channelType.String,
			ChannelID:       channelID.String,
			ForkBoundarySeq: forkBoundarySeq,
			SeedLength:      seedLength,
			ForkKind:        forkKind.String,
		}
		m.cwd = cwd.String

		rows, err := db.Query("SELECT type, data FROM "+m.entriesTable()+" WHERE session_id = ? ORDER BY seq ASC", sessionID)
		if err != nil {
			return err
		}
		defer rows.Close()

		var corruptRows int
		for rows.Next() {
			var typeStr string
			var dataStr string
			if err := rows.Scan(&typeStr, &dataStr); err != nil {
				corruptRows++
				continue
			}

			line := []byte(dataStr)
			switch EntryType(typeStr) {
			case EntrySession:
				// Already loaded from sessions table

			case EntryMessage:
				var e MessageEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryModelChange:
				var e ModelChangeEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryModeChange:
				var e ModeChangeEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryThinkingChange:
				var e ThinkingLevelChangeEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryAdditionalDirectories:
				var e AdditionalDirectoriesEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				e.Directories = append([]string(nil), e.Directories...)
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryCompaction:
				var e CompactionEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntrySessionInfo:
				var e SessionInfoEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryTurnStart:
				var e TurnStartEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryTurnEnd:
				var e TurnEndEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID

			case EntryBranchSummary:
				var e BranchSummaryEntry
				if err := json.Unmarshal(line, &e); err != nil {
					corruptRows++
					continue
				}
				m.entries = append(m.entries, e)
				m.leafID = &e.ID
			}
		}

		if corruptRows > 0 {
			log.Printf("[session] warning: skipped %d corrupt row(s) in %s", corruptRows, m.file)
		}
		return rows.Err()
	})
}

var sessionChildTables = []string{
	"session_channel_tool_generations",
	"session_channel_tools",
	"response_items",
	"tool_execution_records",
	"response_runs",
	"response_session_state",
	"response_turns",
	"session_run_events",
	"session_run_recoveries",
	"session_runs",
	"session_capability_events",
	"session_capabilities",
	"session_esm_objectives",
	"conversation_turns",
	"session_runtime_leases",
	"entries",
}

// deleteSessionDataTx removes every root-session child row before deleting the
// session itself. Keep this list in one place so deletion and integrity tests
// cannot silently drift apart.
func deleteSessionDataTx(tx *sql.Tx, sessionID string) error {
	if tx == nil {
		return fmt.Errorf("delete session transaction is nil")
	}
	if _, err := tx.Exec("DELETE FROM session_fork_requests WHERE source_session_id = ? OR child_session_id = ?", sessionID, sessionID); err != nil {
		return fmt.Errorf("delete session %s from session_fork_requests: %w", sessionID, err)
	}
	for _, table := range sessionChildTables {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE session_id = ?", sessionID); err != nil {
			return fmt.Errorf("delete session %s from %s: %w", sessionID, table, err)
		}
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE id = ?", sessionID); err != nil {
		return fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	return nil
}

// DeleteSession deletes a session file if it is under sessionDir.
func DeleteSession(path string, sessionDir string) error {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve session path: %w", err)
	}
	cleanSessionDir, err := filepath.Abs(filepath.Clean(sessionDir))
	if err != nil {
		return fmt.Errorf("resolve session dir: %w", err)
	}
	rel, err := filepath.Rel(cleanSessionDir, cleanPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("session path %s is outside session directory %s", path, sessionDir)
	}
	if filepath.Ext(cleanPath) != ".db" {
		return fmt.Errorf("session path %s is not a .db file", path)
	}
	base := filepath.Base(cleanPath)
	if base == "sessions.db" || strings.HasPrefix(base, "sessions.db-") {
		return fmt.Errorf("refusing to delete shared SQLite database %s as a session handle", path)
	}

	// Read session ID and delete from SQLite DB
	var sessionID string
	idBytes, err := os.ReadFile(cleanPath)
	if err == nil {
		sessionID = strings.TrimSpace(string(idBytes))
	} else if os.IsNotExist(err) {
		sessionID = sessionFileID(cleanPath)
	}

	if sessionID != "" {
		dbPath := resolveDBPath(cleanPath)
		db, err := cachedDB(dbPath)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin deleting session %s: %w", sessionID, err)
		}
		if err := deleteSessionDataTx(tx, sessionID); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit deleting session %s: %w", sessionID, err)
		}
	}

	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}

// SessionDetail contains detailed metadata about a session for display.
type SessionDetail struct {
	SessionInfo
	ID           string
	MessageCount int
	Preview      string // first user message (truncated)
}

// SessionCapabilities stores persisted per-session runtime capability state.
type SessionCapabilities struct {
	SessionID    string
	Mode         string
	DisplayMode  string
	DelegateMode bool
	MultiAgent   bool
	Workflows    bool
	WebSearch    bool
	Browser      bool
	A2AMaster    bool
	UpdatedAt    time.Time
}

// SessionRunEvent records one lifecycle event for a single chat/run execution.
type SessionRunEvent struct {
	ID        string
	SessionID string
	RunID     string
	EventType string
	Source    string
	Status    string
	Model     string
	Mode      string
	Timestamp time.Time
	Data      json.RawMessage
}

// SessionCapabilityEvent records one capability state transition.
type SessionCapabilityEvent struct {
	ID         string
	SessionID  string
	RunID      string
	EventType  string
	Source     string
	Actor      string
	Capability string
	OldValue   string
	NewValue   string
	Timestamp  time.Time
	Data       json.RawMessage
}

// LoadSessionCapabilities loads persisted capabilities for a session.
func LoadSessionCapabilities(sessionDir, sessionID string) (*SessionCapabilities, bool, error) {
	if sessionID == "" {
		return nil, false, nil
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, false, err
	}

	var caps SessionCapabilities
	var delegateMode, multiAgent, workflows, webSearch, browser, a2aMaster int
	var updatedAt string
	err = db.QueryRow(`SELECT session_id, mode, display_mode, delegate_mode, multi_agent, workflows, web_search, browser, a2a_master, updated_at
		FROM session_capabilities WHERE session_id = ?`, sessionID).Scan(
		&caps.SessionID,
		&caps.Mode,
		&caps.DisplayMode,
		&delegateMode,
		&multiAgent,
		&workflows,
		&webSearch,
		&browser,
		&a2aMaster,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	caps.DelegateMode = delegateMode != 0
	caps.MultiAgent = multiAgent != 0
	caps.Workflows = workflows != 0
	caps.WebSearch = webSearch != 0
	caps.Browser = browser != 0
	caps.A2AMaster = a2aMaster != 0
	caps.UpdatedAt = parseSessionTimestamp(updatedAt)
	return &caps, true, nil
}

// SaveSessionCapabilities persists per-session runtime capability state.
func SaveSessionCapabilities(sessionDir string, caps SessionCapabilities) error {
	if caps.SessionID == "" {
		return fmt.Errorf("session capability session ID is empty")
	}
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	m := &Manager{file: filepath.Join(sessionDir, caps.SessionID+".db"), sessionDir: sessionDir}
	updatedAt := caps.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	return m.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := validateRuntimeLeaseTx(tx, sessionDir, caps.SessionID); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO session_capabilities
			(session_id, mode, display_mode, delegate_mode, multi_agent, workflows, web_search, browser, a2a_master, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
				mode = excluded.mode,
				display_mode = excluded.display_mode,
				delegate_mode = excluded.delegate_mode,
				multi_agent = excluded.multi_agent,
				workflows = excluded.workflows,
				web_search = excluded.web_search,
				browser = excluded.browser,
				a2a_master = excluded.a2a_master,
				updated_at = excluded.updated_at`,
			caps.SessionID,
			caps.Mode,
			caps.DisplayMode,
			boolToInt(caps.DelegateMode),
			boolToInt(caps.MultiAgent),
			boolToInt(caps.Workflows),
			boolToInt(caps.WebSearch),
			boolToInt(caps.Browser),
			boolToInt(caps.A2AMaster),
			updatedAt.Format(time.RFC3339Nano),
		)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// SaveSessionRunEvent appends a run lifecycle event to the independent run event table.
func SaveSessionRunEvent(sessionDir string, ev SessionRunEvent) (string, error) {
	if ev.SessionID == "" {
		return "", fmt.Errorf("session run event session ID is empty")
	}
	if ev.RunID == "" {
		return "", fmt.Errorf("session run event run ID is empty")
	}
	if ev.EventType == "" {
		return "", fmt.Errorf("session run event type is empty")
	}
	if ev.ID == "" {
		ev.ID = GenerateID()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	m := &Manager{file: filepath.Join(sessionDir, ev.SessionID+".db"), sessionDir: sessionDir}
	data := normalizeEventData(ev.Data)
	return ev.ID, m.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := validateRuntimeLeaseTx(tx, sessionDir, ev.SessionID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO session_run_events
			(id, session_id, run_id, event_type, source, status, model, mode, timestamp, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID,
			ev.SessionID,
			ev.RunID,
			ev.EventType,
			ev.Source,
			ev.Status,
			ev.Model,
			ev.Mode,
			ev.Timestamp.Format(time.RFC3339Nano),
			data,
		); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ListSessionRunEvents returns run events for a session, ordered by insertion.
func ListSessionRunEvents(sessionDir, sessionID string) ([]SessionRunEvent, error) {
	return ListSessionRunEventsContext(context.Background(), sessionDir, sessionID)
}

// ListSessionRunEventsContext is the cancellable event replay query used by
// bounded recovery and reconnect paths.
func ListSessionRunEventsContext(ctx context.Context, sessionDir, sessionID string) ([]SessionRunEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" {
		return nil, nil
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, run_id, event_type, source, status, model, mode, timestamp, data
		FROM session_run_events WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SessionRunEvent
	for rows.Next() {
		var ev SessionRunEvent
		var timestamp string
		var data string
		if err := rows.Scan(
			&ev.ID,
			&ev.SessionID,
			&ev.RunID,
			&ev.EventType,
			&ev.Source,
			&ev.Status,
			&ev.Model,
			&ev.Mode,
			&timestamp,
			&data,
		); err != nil {
			return nil, err
		}
		ev.Timestamp = parseSessionTimestamp(timestamp)
		ev.Data = json.RawMessage(data)
		events = append(events, ev)
	}
	return events, rows.Err()
}

// LatestSessionRunEventSeq returns the durable replay cursor for one Run.
// Callers use it to reconcile a disconnected adapter before requesting only
// the missing portion of the session event stream.
func LatestSessionRunEventSeq(sessionDir, runID string) (int64, error) {
	if runID == "" {
		return 0, nil
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return 0, err
	}
	var seq int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM session_run_events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// SaveSessionCapabilityEvent appends a capability transition event to the independent event table.
func SaveSessionCapabilityEvent(sessionDir string, ev SessionCapabilityEvent) (string, error) {
	if ev.SessionID == "" {
		return "", fmt.Errorf("session capability event session ID is empty")
	}
	if ev.EventType == "" {
		return "", fmt.Errorf("session capability event type is empty")
	}
	if ev.Capability == "" {
		return "", fmt.Errorf("session capability event capability is empty")
	}
	if ev.ID == "" {
		ev.ID = GenerateID()
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if sessionDir == "" {
		sessionDir = platform.SessionDir()
	}
	m := &Manager{file: filepath.Join(sessionDir, ev.SessionID+".db"), sessionDir: sessionDir}
	data := normalizeEventData(ev.Data)
	return ev.ID, m.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := validateRuntimeLeaseTx(tx, sessionDir, ev.SessionID); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO session_capability_events
			(id, session_id, run_id, event_type, source, actor, capability, old_value, new_value, timestamp, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.ID,
			ev.SessionID,
			ev.RunID,
			ev.EventType,
			ev.Source,
			ev.Actor,
			ev.Capability,
			ev.OldValue,
			ev.NewValue,
			ev.Timestamp.Format(time.RFC3339Nano),
			data,
		)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ListSessionCapabilityEvents returns capability events for a session, ordered by insertion.
func ListSessionCapabilityEvents(sessionDir, sessionID string) ([]SessionCapabilityEvent, error) {
	if sessionID == "" {
		return nil, nil
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, run_id, event_type, source, actor, capability, old_value, new_value, timestamp, data
		FROM session_capability_events WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SessionCapabilityEvent
	for rows.Next() {
		var ev SessionCapabilityEvent
		var timestamp string
		var data string
		if err := rows.Scan(
			&ev.ID,
			&ev.SessionID,
			&ev.RunID,
			&ev.EventType,
			&ev.Source,
			&ev.Actor,
			&ev.Capability,
			&ev.OldValue,
			&ev.NewValue,
			&timestamp,
			&data,
		); err != nil {
			return nil, err
		}
		ev.Timestamp = parseSessionTimestamp(timestamp)
		ev.Data = json.RawMessage(data)
		events = append(events, ev)
	}
	return events, rows.Err()
}

// ListSessionMessagesWithSeq returns the visible replay messages for a session,
// preserving each message row's entries.seq cursor.
func ListSessionMessagesWithSeq(sessionDir, sessionID string) ([]SequencedMessage, error) {
	if sessionID == "" {
		return nil, nil
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.Query(`SELECT seq, type, data FROM entries
		WHERE session_id = ? AND type IN (?, ?)
		ORDER BY seq ASC`, sessionID, string(EntryMessage), string(EntryCompaction))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	state := sequencedReplayState{}
	for rows.Next() {
		var seq int64
		var typeStr string
		var data string
		if err := rows.Scan(&seq, &typeStr, &data); err != nil {
			return nil, err
		}
		switch EntryType(typeStr) {
		case EntryMessage:
			var e MessageEntry
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				continue
			}
			state.messages = append(state.messages, SequencedMessage{
				Seq:     seq,
				EntryID: e.ID,
				Message: cloneMessage(e.Message),
			})
			state.entryIDs = append(state.entryIDs, e.ID)
		case EntryCompaction:
			var e CompactionEntry
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				continue
			}
			applySequencedCompactionEntry(&state, e, seq)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return state.messages, nil
}

// ListSessionMessagesAfter returns persisted message rows after entries.seq.
func ListSessionMessagesAfter(sessionDir, sessionID string, afterSeq int64, limit int) ([]SequencedMessage, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.Query(`SELECT seq, data FROM entries
		WHERE session_id = ? AND type = ? AND seq > ?
		ORDER BY seq ASC LIMIT ?`, sessionID, string(EntryMessage), afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []SequencedMessage
	for rows.Next() {
		var seq int64
		var data string
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, err
		}
		var e MessageEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue
		}
		messages = append(messages, SequencedMessage{
			Seq:     seq,
			EntryID: e.ID,
			Message: cloneMessage(e.Message),
		})
	}
	return messages, rows.Err()
}

// ListSessionMessagesLatest returns the latest N message entries (highest seq first).
func ListSessionMessagesLatest(sessionDir, sessionID string, limit int) ([]SequencedMessage, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.Query(`SELECT seq, data FROM entries
		WHERE session_id = ? AND type = ?
		ORDER BY seq DESC LIMIT ?`, sessionID, string(EntryMessage), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []SequencedMessage
	for rows.Next() {
		var seq int64
		var data string
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, err
		}
		var e MessageEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue
		}
		messages = append(messages, SequencedMessage{
			Seq:     seq,
			EntryID: e.ID,
			Message: cloneMessage(e.Message),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending order.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// ListSessionMessagesBefore returns messages with seq < beforeSeq, newest first limited to `limit`.
func ListSessionMessagesBefore(sessionDir, sessionID string, beforeSeq int64, limit int) ([]SequencedMessage, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.Query(`SELECT seq, data FROM entries
		WHERE session_id = ? AND type = ? AND seq < ?
		ORDER BY seq DESC LIMIT ?`, sessionID, string(EntryMessage), beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []SequencedMessage
	for rows.Next() {
		var seq int64
		var data string
		if err := rows.Scan(&seq, &data); err != nil {
			return nil, err
		}
		var e MessageEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			continue
		}
		messages = append(messages, SequencedMessage{
			Seq:     seq,
			EntryID: e.ID,
			Message: cloneMessage(e.Message),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending order.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// ListSessionRunEventsWithSeq returns run events with their session_run_events.seq cursor.
func ListSessionRunEventsWithSeq(sessionDir, sessionID string) ([]SequencedSessionRunEvent, error) {
	return ListSessionRunEventsAfter(sessionDir, sessionID, 0, 0)
}

// ListSessionRunEventsAfter returns run events after session_run_events.seq.
func ListSessionRunEventsAfter(sessionDir, sessionID string, afterSeq int64, limit int) ([]SequencedSessionRunEvent, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit > 500 {
		limit = 500
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	query := `SELECT seq, id, session_id, run_id, event_type, source, status, model, mode, timestamp, data
		FROM session_run_events WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{sessionID, afterSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SequencedSessionRunEvent
	for rows.Next() {
		var item SequencedSessionRunEvent
		var timestamp string
		var data string
		if err := rows.Scan(
			&item.Seq,
			&item.Event.ID,
			&item.Event.SessionID,
			&item.Event.RunID,
			&item.Event.EventType,
			&item.Event.Source,
			&item.Event.Status,
			&item.Event.Model,
			&item.Event.Mode,
			&timestamp,
			&data,
		); err != nil {
			return nil, err
		}
		item.Event.Timestamp = parseSessionTimestamp(timestamp)
		item.Event.Data = json.RawMessage(data)
		events = append(events, item)
	}
	return events, rows.Err()
}

// ListSessionCapabilityEventsWithSeq returns capability events with their seq cursor.
func ListSessionCapabilityEventsWithSeq(sessionDir, sessionID string) ([]SequencedSessionCapabilityEvent, error) {
	return ListSessionCapabilityEventsAfter(sessionDir, sessionID, 0, 0)
}

// ListSessionCapabilityEventsAfter returns capability events after session_capability_events.seq.
func ListSessionCapabilityEventsAfter(sessionDir, sessionID string, afterSeq int64, limit int) ([]SequencedSessionCapabilityEvent, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit > 500 {
		limit = 500
	}
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	query := `SELECT seq, id, session_id, run_id, event_type, source, actor, capability, old_value, new_value, timestamp, data
		FROM session_capability_events WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{sessionID, afterSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SequencedSessionCapabilityEvent
	for rows.Next() {
		var item SequencedSessionCapabilityEvent
		var timestamp string
		var data string
		if err := rows.Scan(
			&item.Seq,
			&item.Event.ID,
			&item.Event.SessionID,
			&item.Event.RunID,
			&item.Event.EventType,
			&item.Event.Source,
			&item.Event.Actor,
			&item.Event.Capability,
			&item.Event.OldValue,
			&item.Event.NewValue,
			&timestamp,
			&data,
		); err != nil {
			return nil, err
		}
		item.Event.Timestamp = parseSessionTimestamp(timestamp)
		item.Event.Data = json.RawMessage(data)
		events = append(events, item)
	}
	return events, rows.Err()
}

func normalizeEventData(data json.RawMessage) string {
	if len(data) == 0 {
		return "{}"
	}
	if !json.Valid(data) {
		return "{}"
	}
	return string(data)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ListForDirDetailed lists sessions with details (ID, message count, preview).
func ListForDirDetailed(cwd, sessionDir string) ([]SessionDetail, error) {
	sessions, err := ListForDir(cwd, sessionDir)
	if err != nil {
		return nil, err
	}
	return buildSessionDetails(sessions)
}

// ListAllDetailed lists sessions with details across all working directories.
func ListAllDetailed(sessionDir string, opts ...ListOption) ([]SessionDetail, error) {
	sessions, err := ListAll(sessionDir, opts...)
	if err != nil {
		return nil, err
	}
	return buildSessionDetails(sessions)
}

func buildSessionDetails(sessions []SessionInfo) ([]SessionDetail, error) {
	if len(sessions) == 0 {
		return nil, nil
	}

	// Open the shared DB from the first session's path.
	dbPath := resolveDBPath(sessions[0].Path)
	db, err := cachedDB(dbPath)
	if err != nil {
		return nil, err
	}

	// Build session ID list.
	ids := make([]string, len(sessions))
	idPos := make(map[string]int, len(sessions))
	for i, s := range sessions {
		id := sessionFileID(s.Path)
		ids[i] = id
		idPos[id] = i
	}

	details := make([]SessionDetail, len(sessions))
	for i, s := range sessions {
		details[i] = SessionDetail{SessionInfo: s, ID: sessionFileID(s.Path)}
	}

	// Build IN clause placeholders.
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Query 1: get message count per session.
	countQuery := fmt.Sprintf(`SELECT e.session_id, COUNT(*) AS message_count
	FROM entries e
	WHERE e.session_id IN (%s) AND e.type = 'message'
	GROUP BY e.session_id`, inClause)
	countRows, err := db.Query(countQuery, args...)
	if err != nil {
		return nil, err
	}
	defer countRows.Close()

	msgCounts := make(map[string]int)
	for countRows.Next() {
		var sessionID string
		var count int
		if err := countRows.Scan(&sessionID, &count); err != nil {
			continue
		}
		msgCounts[sessionID] = count
	}
	if err := countRows.Err(); err != nil {
		return nil, err
	}
	countRows.Close()

	// Query 2: get first message data per session (no correlated subquery — use join).
	firstQuery := fmt.Sprintf(`SELECT e.session_id, e.data
	FROM entries e
	INNER JOIN (
		SELECT session_id, MIN(seq) AS min_seq
		FROM entries
		WHERE type = 'message' AND session_id IN (%s)
		GROUP BY session_id
	) first ON e.session_id = first.session_id AND e.seq = first.min_seq`, inClause)
	firstRows, err := db.Query(firstQuery, args...)
	if err != nil {
		return nil, err
	}
	defer firstRows.Close()

	firstMsgData := make(map[string]string)
	for firstRows.Next() {
		var sessionID, data string
		if err := firstRows.Scan(&sessionID, &data); err != nil {
			continue
		}
		firstMsgData[sessionID] = data
	}
	if err := firstRows.Err(); err != nil {
		return nil, err
	}
	firstRows.Close()

	// Populate details from counts and first message data.
	for sessionID, count := range msgCounts {
		if idx, ok := idPos[sessionID]; ok {
			details[idx].MessageCount = count
			if data := firstMsgData[sessionID]; data != "" {
				var entry MessageEntry
				if err := json.Unmarshal([]byte(data), &entry); err == nil && entry.Message.Role == "user" {
					text := entry.Message.Content
					if text == "" {
						for _, b := range entry.Message.Contents {
							if b.Type == "text" && b.Text != "" {
								text = b.Text
								break
							}
						}
					}
					if text != "" {
						details[idx].Preview = util.TruncateWithSuffix(text, 60, "...")
					}
				}
			}
		}
	}

	// Another query: get session info entries (name).
	infoQuery := fmt.Sprintf(`SELECT e.session_id, e.data
	FROM entries e
	WHERE e.session_id IN (%s) AND e.type = 'session_info'
	ORDER BY e.seq DESC`, inClause)
	infoRows, err := db.Query(infoQuery, args...)
	if err != nil {
		return nil, err
	}
	defer infoRows.Close()

	seenInfo := make(map[string]struct{}, len(idPos))
	for infoRows.Next() {
		var sessionID, data string
		if err := infoRows.Scan(&sessionID, &data); err != nil {
			continue
		}
		// Rows are ordered newest first. Keep only the first entry for each
		// session so a later, older session_info record cannot overwrite a
		// freshly renamed title.
		if _, seen := seenInfo[sessionID]; seen {
			continue
		}
		idx, ok := idPos[sessionID]
		if !ok {
			continue
		}
		var entry SessionInfoEntry
		if err := json.Unmarshal([]byte(data), &entry); err == nil {
			details[idx].Name = entry.Name
			seenInfo[sessionID] = struct{}{}
		}
	}
	if err := infoRows.Err(); err != nil {
		return nil, err
	}
	infoRows.Close()

	// Sort by modification time (newest first).
	sort.Slice(details, func(i, j int) bool {
		return details[i].ModTime.After(details[j].ModTime)
	})

	return details, nil
}

// RecordUsage records a single LLM request's token usage and timing.
func (m *Manager) RecordUsage(provider, protocol, model string, inputTokens, outputTokens, totalTokens, durationMs int) error {
	m.mu.RLock()
	sessionID := ""
	if m.header != nil {
		sessionID = m.header.ID
	}
	m.mu.RUnlock()

	now := time.Now().Format(time.RFC3339Nano)

	return m.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := validateRuntimeLeaseTx(tx, m.GetSessionDir(), sessionID); err != nil {
			return err
		}
		_, err = tx.Exec(
			"INSERT INTO request_stats (timestamp, session_id, provider, protocol, model, input_tokens, output_tokens, total_tokens, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			now, sessionID, provider, protocol, model, inputTokens, outputTokens, totalTokens, durationMs,
		)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

// RecordUsageFromProviderUsage records usage from a provider.Usage struct.
func (m *Manager) RecordUsageFromProviderUsage(provider, protocol, model string, usage *provider.Usage, durationMs int) error {
	if usage == nil {
		return nil
	}
	return m.RecordUsage(provider, protocol, model, usage.TotalInputTokens(), usage.Output, usage.TotalInputTokens()+usage.Output, durationMs)
}

func (m *Manager) writeEntry(entry interface{}) error {
	// Verify handle file or its database is writable to honor file permission settings
	dbPath := resolveDBPath(m.file)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	if _, err := os.Stat(dbPath); err == nil {
		f, err := os.OpenFile(dbPath, os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open session file: %w", err)
		}
		f.Close()
	}

	id, typeStr, parentID, ts := getEntryMetadata(entry)
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	var sessionID string
	if m.header != nil {
		sessionID = m.header.ID
	} else {
		idBytes, err := os.ReadFile(m.file)
		if err == nil {
			sessionID = strings.TrimSpace(string(idBytes))
		} else if os.IsNotExist(err) {
			sessionID = sessionFileID(m.file)
		}
	}
	if sessionID == "" {
		return fmt.Errorf("no session ID found for writeEntry")
	}

	return m.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin writing session entry: %w", err)
		}
		defer tx.Rollback()
		if err := validateRuntimeLeaseTx(tx, filepath.Dir(dbPath), sessionID); err != nil {
			return err
		}

		if typeStr != string(EntrySession) {
			var currentLeaf sql.NullString
			err := tx.QueryRow(
				"SELECT id FROM "+m.entriesTable()+" WHERE session_id = ? AND type != ? ORDER BY seq DESC LIMIT 1",
				sessionID, string(EntrySession),
			).Scan(&currentLeaf)
			if err != nil && err != sql.ErrNoRows {
				return fmt.Errorf("read current session leaf: %w", err)
			}
			expectedLeaf := ""
			if parentID != nil {
				expectedLeaf = *parentID
			}
			if currentLeaf.String != expectedLeaf {
				return fmt.Errorf("%w: expected leaf %q, current leaf %q; reopen the session before writing", ErrSessionModified, expectedLeaf, currentLeaf.String)
			}
		}

		// Register session if header is being written
		if typeStr == string(EntrySession) && m.header != nil {
			var parentSess interface{}
			if m.header.ParentSession != "" {
				parentSess = m.header.ParentSession
			}
			_, err = tx.Exec(
				"INSERT INTO "+m.sessionTable()+" (id, cwd, timestamp, parent_session, version, channel_type, channel_id, fork_boundary_seq, seed_length, fork_kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				sessionID, m.cwd, m.header.Timestamp.Format(time.RFC3339Nano), parentSess, m.header.Version, m.header.ChannelType, m.header.ChannelID, m.header.ForkBoundarySeq, m.header.SeedLength, m.header.ForkKind,
			)
			if err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed: "+strings.ToLower(m.sessionTable())+".id") {
					return fmt.Errorf("%w: %s", ErrSessionIDExists, err)
				}
				return fmt.Errorf("register session: %w", err)
			}
		}

		var parentIDVal interface{}
		if parentID != nil {
			parentIDVal = *parentID
		}
		_, err = tx.Exec(
			"INSERT INTO "+m.entriesTable()+" (session_id, id, type, parent_id, timestamp, data) VALUES (?, ?, ?, ?, ?, ?)",
			sessionID, id, typeStr, parentIDVal, ts.Format(time.RFC3339Nano), string(data),
		)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit session entry: %w", err)
		}
		return nil
	})
}
