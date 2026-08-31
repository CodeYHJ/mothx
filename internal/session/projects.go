package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/startvibecoding/mothx/internal/dao"
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
	records, err := dao.NewProjectDAO(db.Bun()).List(context.Background())
	if err != nil {
		return nil, err
	}
	projects := []Project{}
	for _, record := range records {
		p := Project{ID: record.ID, Name: record.Name,
			CreatedAt: parseProjectTime(record.CreatedAt), UpdatedAt: parseProjectTime(record.UpdatedAt)}
		projects = append(projects, p)
	}
	return projects, nil
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
	err = dao.NewProjectDAO(db.Bun()).Insert(context.Background(), &dao.ProjectRecord{ID: id, Name: name, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)})
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
	changed, err := dao.NewProjectDAO(db.Bun()).UpdateName(context.Background(), id, name, now.Format(time.RFC3339Nano))
	if err != nil {
		return Project{}, err
	}
	if changed == 0 {
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
	return dao.NewProjectDAO(db.Bun()).Delete(context.Background(), id)
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
		exists, err := dao.NewProjectDAO(db.Bun()).Exists(context.Background(), metadata.ProjectID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("project not found")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var projectID *string
	if metadata.ProjectID != "" {
		projectID = &metadata.ProjectID
	}
	return dao.NewProjectDAO(db.Bun()).UpsertMetadata(context.Background(), &dao.SessionMetadataRecord{
		SessionID: sessionID, ProjectID: projectID, Pinned: boolToInt(metadata.Pinned), UpdatedAt: now,
	})
}

func LatestSessionTitle(sessionDir, sessionID string) (string, string, error) {
	db, ok, err := openExistingSessionDB(sessionDir)
	if err != nil || !ok {
		return "", "", err
	}
	data, err := dao.NewProjectDAO(db.Bun()).LatestSessionInfoData(context.Background(), sessionID)
	if err == dao.ErrNoRows {
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
	record, err := dao.NewProjectDAO(db.Bun()).Metadata(context.Background(), sessionID)
	if err != nil {
		return SessionMetadata{}, err
	}
	if record == nil {
		return SessionMetadata{}, nil
	}
	var metadata SessionMetadata
	if record.ProjectID != nil {
		metadata.ProjectID = *record.ProjectID
	}
	metadata.Pinned = record.Pinned != 0
	return metadata, nil
}
