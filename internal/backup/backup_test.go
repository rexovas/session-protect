package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/encryption"
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
	results, err := Execute(cfg, Options{DryRun: true})
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

	results, err := Execute(cfg, Options{})
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
	results, err = Execute(cfg, Options{})
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
	results, err = Execute(cfg, Options{Target: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Removed != 1 || !results[0].Committed {
		t.Fatalf("expected 1 removal committed, got %+v", results)
	}
}

func TestEncryptionFailsClosedOnPlainRepo(t *testing.T) {
	_, cfg := setupEnv(t)

	// Create a plain repo first, then demand encryption for it.
	if _, err := Execute(cfg, Options{Target: "claude"}); err != nil {
		t.Fatal(err)
	}
	cfg.Encryption.Mode = "git-crypt"

	_, err := Execute(cfg, Options{Target: "claude"})
	if err == nil || !strings.Contains(err.Error(), "git-crypt") {
		t.Fatalf("expected git-crypt fail-closed error, got %v", err)
	}

	results, err := Execute(cfg, Options{Target: "claude", AllowUnencrypted: true})
	if err != nil {
		t.Fatalf("--allow-unencrypted should proceed: %v", err)
	}
	if results[0].Skipped != "" {
		t.Fatalf("expected claude to run, got %+v", results)
	}
}

func TestGitCryptSetupOnFreshRepo(t *testing.T) {
	if !encryption.Installed() {
		t.Skip("git-crypt not installed")
	}
	home, cfg := setupEnv(t)
	cfg.Encryption.Mode = "git-crypt"

	results, err := Execute(cfg, Options{Target: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "root", "all")
	if results[0].KeyExported != cfg.Encryption.KeyPath {
		t.Fatalf("expected key export path %q, got %+v", cfg.Encryption.KeyPath, results[0])
	}
	info, err := os.Stat(cfg.Encryption.KeyPath)
	if err != nil {
		t.Fatalf("recovery key missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitattributes")); err != nil {
		t.Fatalf(".gitattributes missing: %v", err)
	}

	// Committed blobs must be ciphertext.
	out, err := exec.Command("git", "-C", repo, "show", "HEAD:claude/history.jsonl").Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if !strings.HasPrefix(string(out), "\x00GITCRYPT") {
		t.Fatalf("committed blob is not git-crypt encrypted: %q", out[:min(len(out), 16)])
	}

	// A second run must not re-export or fail on the now-unlocked repo.
	results, err = Execute(cfg, Options{Target: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].KeyExported != "" {
		t.Fatalf("unexpected re-export: %+v", results[0])
	}
}

func TestSyncOnlyDoesNotCommit(t *testing.T) {
	home, cfg := setupEnv(t)

	results, err := Execute(cfg, Options{SyncOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "root", "all")
	for _, result := range results {
		if result.Committed {
			t.Fatalf("%s: sync must not commit", result.Target)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "claude", "history.jsonl")); err != nil {
		t.Fatalf("sync did not mirror files: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Run(); err == nil {
		t.Fatal("sync created a commit")
	}
}
