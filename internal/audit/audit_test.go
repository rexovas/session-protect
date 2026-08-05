package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	if entries := Read(root); entries != nil {
		t.Fatalf("missing log should read empty, got %v", entries)
	}

	first := Entry{Time: time.Now().Truncate(time.Second), Action: "restore", Target: "claude", SessionID: "a", From: "/x", To: "/y"}
	Append(root, []Entry{first})
	Append(root, []Entry{{Time: first.Time, Action: "transplant", Target: "codex", SessionID: "b", Overwrote: true, SafetyCopy: "/s"}})

	entries := Read(root)
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Action != "restore" || entries[0].SessionID != "a" || entries[0].To != "/y" {
		t.Fatalf("first entry mangled: %+v", entries[0])
	}
	if entries[1].Action != "transplant" || !entries[1].Overwrote || entries[1].SafetyCopy != "/s" {
		t.Fatalf("second entry mangled: %+v", entries[1])
	}
}

func TestReadSkipsDamagedLines(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "audit.log")
	content := `{"time":"2026-08-01T00:00:00Z","action":"restore","session_id":"good"}
this line is corrupt garbage
{"no_action_field":true}
{"time":"2026-08-02T00:00:00Z","action":"restore","session_id":"also-good"}
`
	if err := os.WriteFile(log, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := Read(root)
	if len(entries) != 2 || entries[0].SessionID != "good" || entries[1].SessionID != "also-good" {
		t.Fatalf("damaged-line handling wrong: %+v", entries)
	}
}
