package version

import "testing"

func TestCurrentUsesBuildVersion(t *testing.T) {
	previous := Version
	Version = "0.3.1"
	t.Cleanup(func() { Version = previous })

	if got := Current(); got != "0.3.1" {
		t.Fatalf("Current() = %q, want 0.3.1", got)
	}
}

func TestCurrentIsNeverEmpty(t *testing.T) {
	previous := Version
	Version = ""
	t.Cleanup(func() { Version = previous })

	if got := Current(); got == "" {
		t.Fatal("Current() is empty")
	}
}
