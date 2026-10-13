package agentruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSessionRejectsUnavailableWorkDir(t *testing.T) {
	sessionDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := CreateSession(CreateSessionOptions{WorkDir: missing, SessionDir: sessionDir}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("CreateSession missing work dir error = %v, want unavailable directory error", err)
	}

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSession(CreateSessionOptions{WorkDir: file, SessionDir: sessionDir}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("CreateSession regular file error = %v, want not-a-directory error", err)
	}
}

func TestOpenSessionForWorkDirRejectsDeletedDirectory(t *testing.T) {
	sessionDir := t.TempDir()
	workDir := t.TempDir()
	mgr, err := CreateSession(CreateSessionOptions{WorkDir: workDir, SessionDir: sessionDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workDir); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionForWorkDir(workDir, sessionDir, mgr.GetHeader().ID); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("OpenSessionForWorkDir deleted directory error = %v, want unavailable directory error", err)
	}
}
