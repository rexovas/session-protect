package targets

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestClaudeSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/x/projects/my-app": "-Users-x-projects-my-app",
		"/Users/x/app.v2/":         "-Users-x-app-v2", // cleaned before encoding
		"/a/b c/d_e":               "-a-b-c-d-e",
	}
	for in, want := range cases {
		if got := ClaudeSlug(in); got != want {
			t.Errorf("ClaudeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexSessionMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := `{"type":"session_meta","payload":{"id":"abc-123","cwd":"/w/project"}}
{"type":"event_msg","payload":{"type":"user_message","message":"hi"}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	id, cwd := CodexSessionMeta(path)
	if id != "abc-123" || cwd != "/w/project" {
		t.Fatalf("meta = %q %q", id, cwd)
	}
	if id, cwd := CodexSessionMeta(filepath.Join(t.TempDir(), "missing.jsonl")); id != "" || cwd != "" {
		t.Fatal("missing file must yield empties")
	}
}
