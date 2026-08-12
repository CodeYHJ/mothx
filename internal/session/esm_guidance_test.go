package session

import "testing"

func TestESMGuidanceLifecycle(t *testing.T) {
	dir := t.TempDir()
	if _, err := ListESMGuidance(dir, "missing", "pending", 10); err != nil {
		t.Fatal(err)
	}
	g := ESMGuidance{ID: "g-1", SessionID: "guidance-session", Guidance: "run the focused tests"}
	if err := SaveESMGuidance(dir, g); err != nil {
		t.Fatal(err)
	}
	items, err := ListESMGuidance(dir, g.SessionID, "pending", 10)
	if err != nil || len(items) != 1 || items[0].Guidance != g.Guidance {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if err := ConsumeESMGuidance(dir, g.SessionID, []string{g.ID}); err != nil {
		t.Fatal(err)
	}
	items, err = ListESMGuidance(dir, g.SessionID, "consumed", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("consumed=%#v err=%v", items, err)
	}
}
