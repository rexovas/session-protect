package restore

import (
	"os"
	"path/filepath"
	"testing"

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

// setupEnv creates a fake home with one claude project session and a codex
// session for the project, backs both up, then returns everything needed to
// exercise restores against that backup.
func setupEnv(t *testing.T) (home string, projectPath string, cfg config.Config) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@example.invalid")
	}

	projectPath = filepath.Join(home, "work", "app")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}

	slug := ""
	for _, c := range projectPath {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			slug += string(c)
		} else {
			slug += "-"
		}
	}
	writeFile(t, filepath.Join(home, ".claude", "projects", slug, "sess-1.jsonl"), "{\"claude\":1}")
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "sess-2.jsonl"),
		`{"type":"session_meta","payload":{"id":"sess-2","cwd":"`+projectPath+`"}}`)

	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n[encryption]\nmode = \"none\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Execute(cfg, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	return home, projectPath, cfg
}

func TestRestoreMissingSessions(t *testing.T) {
	home, projectPath, cfg := setupEnv(t)

	claudeLive := filepath.Join(home, ".claude", "projects")
	var claudePath string
	_ = filepath.WalkDir(claudeLive, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && filepath.Base(path) == "sess-1.jsonl" {
			claudePath = path
		}
		return nil
	})
	codexPath := filepath.Join(home, ".codex", "sessions", "2026", "sess-2.jsonl")
	for _, path := range []string{claudePath, codexPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}

	items, err := Plan(Options{Project: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 missing sessions, got %+v", items)
	}

	restored, err := Apply(cfg, items)
	if err != nil {
		t.Fatal(err)
	}
	if restored != 2 {
		t.Fatalf("restored = %d", restored)
	}
	for _, path := range []string{claudePath, codexPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("restored file missing: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupRoot, "restore.log")); err != nil {
		t.Errorf("restore log missing: %v", err)
	}
}

func TestRestoreRefusesLiveSessionsWithoutOverwrite(t *testing.T) {
	_, projectPath, cfg := setupEnv(t)

	// Nothing is missing, so the default plan is empty.
	items, err := Plan(Options{Project: projectPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty plan for fully-live project, got %+v", items)
	}

	// Targeting a live session explicitly must fail without --overwrite.
	if _, err := Plan(Options{Project: projectPath, SessionID: "sess-2"}); err == nil {
		t.Fatal("expected error for live session without --overwrite")
	}

	// With --overwrite it proceeds and makes a safety copy.
	items, err = Plan(Options{Project: projectPath, SessionID: "sess-2", Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Overwriting {
		t.Fatalf("expected 1 overwriting item, got %+v", items)
	}
	if _, err := Apply(cfg, items); err != nil {
		t.Fatal(err)
	}
	if items[0].SafetyCopy == "" {
		t.Fatal("expected a safety copy path")
	}
	if _, err := os.Stat(items[0].SafetyCopy); err != nil {
		t.Fatalf("safety copy missing: %v", err)
	}
}
