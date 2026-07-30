package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
