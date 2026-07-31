package browse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
)

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanMergesLiveAndBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@example.invalid")
	}

	projectPath := filepath.Join(home, "work", "app")
	writeFile(t, filepath.Join(home, ".claude", "projects", "-slug", "c1.jsonl"),
		`{"cwd":"`+projectPath+`","type":"user"}`)
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "x1.jsonl"),
		`{"type":"session_meta","payload":{"id":"x1","cwd":"`+projectPath+`"}}`)

	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n[encryption]\nmode = \"none\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	// Before any backup: everything is unbacked, claude+codex merge onto the
	// same project via cwd.
	projects := Scan(cfg)
	if len(projects) != 1 {
		t.Fatalf("expected 1 merged project, got %+v", projects)
	}
	if projects[0].Path != projectPath {
		t.Fatalf("project path = %q, want %q", projects[0].Path, projectPath)
	}
	if projects[0].Unbacked != 2 || projects[0].ClaudeCount != 1 || projects[0].CodexCount != 1 {
		t.Fatalf("unexpected counts: %+v", projects[0])
	}

	// After a backup everything is OK.
	if _, err := backup.Execute(cfg, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	projects = Scan(cfg)
	if projects[0].OK != 2 || projects[0].Unbacked != 0 {
		t.Fatalf("expected 2 OK after backup: %+v", projects[0])
	}

	// A deleted live session stays visible as recoverable.
	if err := os.Remove(filepath.Join(home, ".claude", "projects", "-slug", "c1.jsonl")); err != nil {
		t.Fatal(err)
	}
	projects = Scan(cfg)
	if projects[0].RecoverOnly != 1 {
		t.Fatalf("expected 1 recover-only session: %+v", projects[0])
	}
}

func TestLoadCustomNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeFile(t, path, `{"type":"user","cwd":"/p"}
{"type":"custom-title","customTitle":"first-name","sessionId":"s"}
{"type":"custom-title","customTitle":"final-name","sessionId":"s"}
`)
	project := &Project{Sessions: []Session{{Target: "claude", ID: "s", SourcePath: path}}}
	LoadCustomNames(project)
	if project.Sessions[0].CustomName != "final-name" {
		t.Fatalf("custom name = %q, want final-name (latest wins)", project.Sessions[0].CustomName)
	}
	if !project.NamesLoaded {
		t.Fatal("NamesLoaded not set")
	}
}

func TestApplyNamesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, ".claude", "projects", "-p", "s1.jsonl")
	writeFile(t, path, `{"type":"custom-title","customTitle":"named-one","sessionId":"s1"}`+"\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	projects := ScanNamed(cfg)
	if projects[0].Sessions[0].CustomName != "named-one" {
		t.Fatalf("name not applied: %+v", projects[0].Sessions[0])
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupRoot, ".session-names.json")); err != nil {
		t.Fatalf("cache not written: %v", err)
	}

	// A rename appends an event and bumps mtime; the cache must refresh.
	writeFile(t, path, `{"type":"custom-title","customTitle":"named-one","sessionId":"s1"}
{"type":"custom-title","customTitle":"renamed","sessionId":"s1"}
`)
	projects = ScanNamed(cfg)
	if projects[0].Sessions[0].CustomName != "renamed" {
		t.Fatalf("rename not picked up: %+v", projects[0].Sessions[0])
	}
}
