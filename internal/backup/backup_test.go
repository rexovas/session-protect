package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func setupEnv(t *testing.T) (home string, cfg config.Config) {
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

	writeFile(t, filepath.Join(home, ".claude", "projects", "-p-app", "s1.jsonl"), "{}")
	writeFile(t, filepath.Join(home, ".claude", "history.jsonl"), "{}")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(home, ".claude", "auth.json"), "secret")
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "x.jsonl"), "{}")
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), "secret")

	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n[encryption]\nmode = \"none\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return home, cfg
}

func TestDryRunWritesNothing(t *testing.T) {
	home, cfg := setupEnv(t)
	results, err := Execute(cfg, "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(results))
	}
	for _, result := range results {
		if result.Files == 0 {
			t.Errorf("%s: expected planned files", result.Target)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "root")); !os.IsNotExist(err) {
		t.Fatal("dry run created the backup root")
	}
}

func TestBackupCommitsAndIsIdempotent(t *testing.T) {
	home, cfg := setupEnv(t)

	results, err := Execute(cfg, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "root", "all")
	for _, result := range results {
		if !result.Committed || result.Commit == "" {
			t.Fatalf("%s: expected commit, got %+v", result.Target, result)
		}
		if result.Repo != repo {
			t.Fatalf("%s: repo = %q", result.Target, result.Repo)
		}
	}

	for _, want := range []string{
		"claude/projects/-p-app/s1.jsonl",
		"claude/history.jsonl",
		"codex/sessions/2026/x.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing backed-up file %s: %v", want, err)
		}
	}
	for _, banned := range []string{"claude/auth.json", "codex/auth.json"} {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(banned))); !os.IsNotExist(err) {
			t.Errorf("credential file %s must not be backed up", banned)
		}
	}

	// No changes: no new commit.
	results, err = Execute(cfg, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Committed {
			t.Fatalf("%s: expected no commit on unchanged sources", result.Target)
		}
	}

	// A deleted source session is removed from the working tree (but stays in
	// git history).
	if err := os.Remove(filepath.Join(home, ".claude", "projects", "-p-app", "s1.jsonl")); err != nil {
		t.Fatal(err)
	}
	results, err = Execute(cfg, "claude", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Removed != 1 || !results[0].Committed {
		t.Fatalf("expected 1 removal committed, got %+v", results)
	}
}

func TestEncryptionFailsClosed(t *testing.T) {
	home, cfg := setupEnv(t)
	cfg.Encryption.Mode = "git-crypt"

	_, err := Execute(cfg, "claude", false, false)
	if err == nil || !strings.Contains(err.Error(), "git-crypt") {
		t.Fatalf("expected git-crypt fail-closed error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "root")); !os.IsNotExist(statErr) {
		t.Fatal("failed encryption check must not create the backup root")
	}

	results, err := Execute(cfg, "claude", false, true)
	if err != nil {
		t.Fatalf("--allow-unencrypted should proceed: %v", err)
	}
	if !results[0].Committed {
		t.Fatalf("expected commit with --allow-unencrypted, got %+v", results)
	}
}
