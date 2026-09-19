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
	id, cwd, sub := CodexSessionMeta(path)
	if id != "abc-123" || cwd != "/w/project" || sub {
		t.Fatalf("meta = %q %q sub=%v", id, cwd, sub)
	}
	if id, cwd, _ := CodexSessionMeta(filepath.Join(t.TempDir(), "missing.jsonl")); id != "" || cwd != "" {
		t.Fatal("missing file must yield empties")
	}
}

func TestCodexSessionMetaDetectsSubagent(t *testing.T) {
	dir := t.TempDir()
	// Top-level session: source is a plain string.
	top := filepath.Join(dir, "top.jsonl")
	if err := os.WriteFile(top, []byte(`{"type":"session_meta","payload":{"id":"t1","cwd":"/w","source":"cli"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, sub := CodexSessionMeta(top); sub {
		t.Fatal("cli session flagged as subagent")
	}
	// Subagent (guardian): source is an object with a subagent key.
	sa := filepath.Join(dir, "sa.jsonl")
	if err := os.WriteFile(sa, []byte(`{"type":"session_meta","payload":{"id":"s1","cwd":"/w","source":{"subagent":{"other":"guardian"}}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, sub := CodexSessionMeta(sa); !sub {
		t.Fatal("guardian subagent not detected")
	}
}
