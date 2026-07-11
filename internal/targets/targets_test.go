package targets

import "testing"

func TestDetectAllIncludesKnownTargets(t *testing.T) {
	got := DetectAll()
	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(got))
	}
	if got[0].Name != "claude" {
		t.Fatalf("expected first target claude, got %q", got[0].Name)
	}
	if got[1].Name != "codex" {
		t.Fatalf("expected second target codex, got %q", got[1].Name)
	}
}
