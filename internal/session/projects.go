package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SessionMetadata struct {
	ProjectID string `json:"projectId,omitempty"`
	Pinned    bool   `json:"pinned"`
}

func parseProjectTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func ListProjects(sessionDir string) ([]Project, error) {
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return []Project{}, err
	}
	rows, err := db.Query("SELECT id, name, created_at, updated_at FROM projects ORDER BY updated_at DESC, name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var p Project
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Name, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt, p.UpdatedAt = parseProjectTime(created), parseProjectTime(updated)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func CreateProject(sessionDir, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return Project{}, err
	}
	now := time.Now().UTC()
	id := GenerateID()
	_, err = db.Exec("INSERT INTO projects(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)", id, name, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}, nil
}

func RenameProject(sessionDir, id, name string) (Project, error) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if id == "" || name == "" {
		return Project{}, fmt.Errorf("project ID and name are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return Project{}, err
	}
	now := time.Now().UTC()
	result, err := db.Exec("UPDATE projects SET name = ?, updated_at = ? WHERE id = ?", name, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return Project{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Project{}, fmt.Errorf("project not found")
	}
	return Project{ID: id, Name: name, UpdatedAt: now}, nil
}

func DeleteProject(sessionDir, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("project ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM projects WHERE id = ?", id)
	return err
}

func SetSessionMetadata(sessionDir, sessionID string, metadata SessionMetadata) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	metadata.ProjectID = strings.TrimSpace(metadata.ProjectID)
	if metadata.ProjectID != "" {
		var exists int
		if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", metadata.ProjectID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("project not found")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO session_metadata(session_id, project_id, pinned, updated_at)
		VALUES(?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET project_id=NULLIF(excluded.project_id, ''), pinned=excluded.pinned, updated_at=excluded.updated_at`,
		sessionID, metadata.ProjectID, boolToInt(metadata.Pinned), now)
	return err
}

func LatestSessionTitle(sessionDir, sessionID string) (string, string, error) {
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return "", "", err
	}
	var data string
	err = db.QueryRow("SELECT data FROM entries WHERE session_id = ? AND type = 'session_info' ORDER BY seq DESC LIMIT 1", sessionID).Scan(&data)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var entry SessionInfoEntry
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return "", "", err
	}
	return entry.Name, entry.Source, nil
}

func GetSessionMetadata(sessionDir, sessionID string) (SessionMetadata, error) {
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return SessionMetadata{}, err
	}
	var metadata SessionMetadata
	var projectID sql.NullString
	var pinned int
	err = db.QueryRow("SELECT project_id, pinned FROM session_metadata WHERE session_id = ?", sessionID).Scan(&projectID, &pinned)
	if err == sql.ErrNoRows {
		return SessionMetadata{}, nil
	}
	if err != nil {
		return SessionMetadata{}, err
	}
	metadata.ProjectID, metadata.Pinned = projectID.String, pinned != 0
	return metadata, nil
}
