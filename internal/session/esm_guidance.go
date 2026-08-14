package session

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ESMGuidance struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"sessionId"`
	ObjectiveVersion string     `json:"objectiveVersion,omitempty"`
	Guidance         string     `json:"guidance"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	ConsumedAt       *time.Time `json:"consumedAt,omitempty"`
}

func SaveESMGuidance(sessionDir string, g ESMGuidance) error {
	g.Guidance = strings.TrimSpace(g.Guidance)
	if g.ID == "" || g.SessionID == "" || g.Guidance == "" {
		return fmt.Errorf("ESM guidance ID, session ID, and guidance are required")
	}
	if g.Status == "" {
		g.Status = "pending"
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO session_esm_guidance (id, session_id, objective_version, guidance, status, created_at, consumed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, g.ID, g.SessionID, g.ObjectiveVersion, g.Guidance, g.Status, g.CreatedAt.Format(time.RFC3339Nano), nullableTime(g.ConsumedAt))
	return err
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}

func ListESMGuidance(sessionDir, sessionID, status string, limit int) ([]ESMGuidance, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, session_id, objective_version, guidance, status, created_at, consumed_at FROM session_esm_guidance WHERE session_id = ?`
	args := []any{sessionID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at ASC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ESMGuidance
	for rows.Next() {
		var g ESMGuidance
		var created string
		var consumed sql.NullString
		if err := rows.Scan(&g.ID, &g.SessionID, &g.ObjectiveVersion, &g.Guidance, &g.Status, &created, &consumed); err != nil {
			return nil, err
		}
		g.CreatedAt = parseSessionTimestamp(created)
		if consumed.Valid {
			t := parseSessionTimestamp(consumed.String)
			g.ConsumedAt = &t
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func ConsumeESMGuidance(sessionDir, sessionID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, err := tx.Exec(`UPDATE session_esm_guidance SET status='consumed', consumed_at=? WHERE id=? AND session_id=? AND status='pending'`, now, id, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
