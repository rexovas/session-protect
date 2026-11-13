package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/targets"
)

func writeEntry(t *testing.T, dir string, name string, info Info) {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConflicts(t *testing.T) {
	dir := t.TempDir()
	alive := os.Getpid() // guaranteed-alive stand-in for another agent process
	writeEntry(t, dir, "a.json", Info{PID: alive, SessionID: "s1", Status: "idle"})
	writeEntry(t, dir, "b.json", Info{PID: 1 << 30, SessionID: "s1", Status: "idle"}) // dead
	writeEntry(t, dir, "c.json", Info{PID: alive, SessionID: "s2", Status: "idle"})

	conflicts := Conflicts(dir, "s1", 0)
	if len(conflicts) != 1 || conflicts[0].PID != alive {
		t.Fatalf("expected 1 live conflict for s1, got %+v", conflicts)
	}

	// The just-started process itself must be excluded.
	if got := Conflicts(dir, "s1", alive); len(got) != 0 {
		t.Fatalf("expected no conflicts when excluding own pid, got %+v", got)
	}
	if got := Conflicts(dir, "s3", 0); len(got) != 0 {
		t.Fatalf("expected no conflicts for unknown session, got %+v", got)
	}
}

func TestRunEmitsWarningOnConflict(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEntry(t, registry, "other.json", Info{PID: os.Getpid(), SessionID: "sess-x", Status: "working"})
	_ = dir

	var out, errOut bytes.Buffer
	stdin := strings.NewReader(fmt.Sprintf(`{"session_id":%q}`, "sess-x"))
	if code := Run(stdin, &out, &errOut); code != 0 {
		t.Fatalf("guard must exit 0, got %d (%s)", code, errOut.String())
	}
	var payload struct {
		SystemMessage string `json:"systemMessage"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("expected JSON output, got %q", out.String())
	}
	if !strings.Contains(payload.SystemMessage, "ALREADY OPEN") {
		t.Fatalf("unexpected message: %q", payload.SystemMessage)
	}

	// No conflict: silent success.
	out.Reset()
	if code := Run(strings.NewReader(`{"session_id":"other-session"}`), &out, &errOut); code != 0 || out.Len() != 0 {
		t.Fatalf("expected silent success, got code=%d out=%q", code, out.String())
	}
}

// The reopen guard sees sessions open in any claude home, and registers
// the home of the session that ran it.
func TestRunCoversEveryClaudeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	client := filepath.Join(home, "client-config")
	registry := filepath.Join(client, "sessions")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEntry(t, registry, "other.json", Info{PID: os.Getpid(), SessionID: "sess-c", Status: "idle"})
	t.Setenv("CLAUDE_CONFIG_DIR", client)

	var out, errOut bytes.Buffer
	if code := Run(strings.NewReader(`{"session_id":"sess-c"}`), &out, &errOut); code != 0 {
		t.Fatalf("guard exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "ALREADY OPEN") {
		t.Fatalf("conflict in an extra home not reported: %q", out.String())
	}
	if h, ok := targets.HomeForDir(client); !ok || LiveAll()["sess-c"].Home != h.Label {
		t.Fatalf("home not registered or entry untagged: %+v %v", h, ok)
	}
}

// A session started on another account than its folder's owner is told so.
func TestAccountMismatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	acme := filepath.Join(home, "acme-config")
	project := filepath.Join(home, "work", "client")
	slugDir := filepath.Join(acme, "projects", targets.ClaudeSlug(project))
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slugDir, "s.jsonl"), []byte(`{"cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterClaudeDir(acme, "acme"); err != nil {
		t.Fatal(err)
	}
	if msg := AccountMismatch(project, filepath.Join(home, ".claude")); !strings.Contains(msg, "belongs to the acme account") {
		t.Errorf("default session in an acme folder: %q", msg)
	}
	if msg := AccountMismatch(filepath.Join(project, "sub"), acme); msg != "" {
		t.Errorf("right account flagged: %q", msg)
	}
	if msg := AccountMismatch(filepath.Join(home, "other"), filepath.Join(home, ".claude")); msg != "" {
		t.Errorf("unowned folder flagged: %q", msg)
	}
}
