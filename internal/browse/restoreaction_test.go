package browse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rexovas/session-protect/internal/config"
)

func restoreTestConfig(t *testing.T) (config.Config, string, string) {
	t.Helper()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "live")
	backupRoot := filepath.Join(tmp, "backup")
	cfg := config.Config{
		BackupRoot: backupRoot,
		Topology:   "combined",
		Targets: map[string]config.TargetConfig{
			"claude": {Source: source},
		},
	}
	return cfg, source, backupRoot
}

func TestRestoreDest(t *testing.T) {
	cfg, source, backupRoot := restoreTestConfig(t)
	session := Session{
		Target:     "claude",
		ID:         "abc",
		State:      "MISSING_SOURCE",
		BackupPath: filepath.Join(backupRoot, "all", "claude", "projects", "p", "abc.jsonl"),
	}

	dest, overwriting, err := restoreDest(cfg, session)
	if err != nil {
		t.Fatalf("restoreDest: %v", err)
	}
	if want := filepath.Join(source, "projects", "p", "abc.jsonl"); dest != want {
		t.Fatalf("dest = %s, want %s", dest, want)
	}
	if overwriting {
		t.Fatal("restore of a missing-source session must not report overwriting")
	}

	session.SourcePath = filepath.Join(source, "projects", "p", "abc.jsonl")
	dest, overwriting, err = restoreDest(cfg, session)
	if err != nil {
		t.Fatalf("restoreDest with live copy: %v", err)
	}
	if dest != session.SourcePath || !overwriting {
		t.Fatalf("live copy should restore over itself as an overwrite; got %s overwriting=%v", dest, overwriting)
	}

	session.SourcePath = ""
	session.BackupPath = filepath.Join(backupRoot, "elsewhere", "abc.jsonl")
	if _, _, err := restoreDest(cfg, session); err == nil {
		t.Fatal("path outside the target's backup tree must be rejected")
	}
}

func TestRestoreSessionRoundTrip(t *testing.T) {
	cfg, source, backupRoot := restoreTestConfig(t)
	backupFile := filepath.Join(backupRoot, "all", "claude", "projects", "p", "abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(backupFile), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"type":"user"}` + "\n")
	if err := os.WriteFile(backupFile, content, 0o600); err != nil {
		t.Fatal(err)
	}

	dest, err := RestoreSession(cfg, Session{
		Target:     "claude",
		ID:         "abc",
		State:      "MISSING_SOURCE",
		BackupPath: backupFile,
	})
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	restored, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(restored) != string(content) {
		t.Fatalf("restored content mismatch: %q", restored)
	}
	if want := filepath.Join(source, "projects", "p", "abc.jsonl"); dest != want {
		t.Fatalf("dest = %s, want %s", dest, want)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupRoot, "restore.log")); err != nil {
		t.Fatalf("restore should append an audit log: %v", err)
	}
}
