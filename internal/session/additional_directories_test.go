package session

import "testing"

func TestAdditionalDirectoriesReplayPreservesLeaf(t *testing.T) {
	m := New(t.TempDir(), t.TempDir())
	if err := m.InitWithID("session-directories"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendModelChange("provider", "model"); err != nil {
		t.Fatal(err)
	}
	if got := m.GetLeafID(); got == nil {
		t.Fatal("model append did not set leaf")
	}
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := m.GetLeafID(); got == nil {
		t.Fatal("reload lost model leaf")
	}
	if _, err := m.AppendAdditionalDirectories([]string{"/tmp/extra"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	entry, ok := m.GetLatestAdditionalDirectories()
	if !ok || len(entry.Directories) != 1 || entry.Directories[0] != "/tmp/extra" {
		t.Fatalf("entry = %#v, %v", entry, ok)
	}
}
