package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := Defaults()
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if cfg.Topology != "combined" {
		t.Fatalf("default topology = %q", cfg.Topology)
	}
	if cfg.Encryption.Mode != "git-crypt" {
		t.Fatalf("default encryption = %q", cfg.Encryption.Mode)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.BackupRoot != Defaults().BackupRoot {
		t.Fatalf("expected default backup root, got %q", cfg.BackupRoot)
	}
}

func TestLoadOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `
backup_root = "~/backups"
topology = "per-target"

[encryption]
mode = "none"

[targets.codex]
enabled = false

[targets.claude]
include = ["projects/"]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if cfg.BackupRoot != filepath.Join(home, "backups") {
		t.Fatalf("backup root not expanded: %q", cfg.BackupRoot)
	}
	if cfg.Topology != "per-target" {
		t.Fatalf("topology = %q", cfg.Topology)
	}
	if cfg.Encryption.Mode != "none" {
		t.Fatalf("encryption = %q", cfg.Encryption.Mode)
	}

	resolved := cfg.ResolveTargets()
	if len(resolved) != 1 || resolved[0].Name != "claude" {
		t.Fatalf("expected only claude after disabling codex, got %+v", resolved)
	}
	if len(resolved[0].Include) != 1 || resolved[0].Include[0] != "projects/" {
		t.Fatalf("claude include override not applied: %+v", resolved[0].Include)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("topology = \"weird\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", path)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid topology")
	}
}

func TestRepoFor(t *testing.T) {
	cfg := Defaults()
	cfg.BackupRoot = "/data"

	repo, prefix := cfg.RepoFor("claude")
	if repo != filepath.Join("/data", "all") || prefix != "claude" {
		t.Fatalf("combined: repo=%q prefix=%q", repo, prefix)
	}

	cfg.Topology = "per-target"
	repo, prefix = cfg.RepoFor("claude")
	if repo != filepath.Join("/data", "claude") || prefix != "" {
		t.Fatalf("per-target: repo=%q prefix=%q", repo, prefix)
	}
}
