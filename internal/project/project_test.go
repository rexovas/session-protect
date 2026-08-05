package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
)

func TestClaudeProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/example/projects/my-app": "-Users-example-projects-my-app",
		"/Users/example/app.v2":          "-Users-example-app-v2",
		"/Users/example/my_app":          "-Users-example-my-app",
		"/a/b c/d":                       "-a-b-c-d",
	}
	for path, want := range cases {
		if got := claudeProjectSlug(path); got != want {
			t.Errorf("claudeProjectSlug(%q) = %q, want %q", path, got, want)
		}
	}
}

func write(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (home string, projectPath string) {
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
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	projectPath = filepath.Join(home, "work", "app")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	slug := claudeProjectSlug(projectPath)
	write(t, filepath.Join(home, ".claude", "projects", slug, "sess-1.jsonl"), `{"claude":1}`)
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "sess-2.jsonl"),
		`{"type":"session_meta","payload":{"id":"sess-2","cwd":"`+projectPath+`"}}`)

	configPath := filepath.Join(home, "config.toml")
	write(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n[encryption]\nmode = \"none\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	return home, projectPath
}

func TestBuildStates(t *testing.T) {
	home, projectPath := setup(t)

	// Before any backup: everything is missing from backup.
	status, err := Build(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Targets) != 2 {
		t.Fatalf("targets = %d", len(status.Targets))
	}
	for _, target := range status.Targets {
		if target.SourceCount != 1 || target.MissingBackup != 1 || target.OKCount != 0 {
			t.Fatalf("%s pre-backup status wrong: %+v", target.Name, target)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Execute(cfg, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	status, err = Build(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range status.Targets {
		if target.OKCount != 1 || target.MissingBackup != 0 {
			t.Fatalf("%s post-backup status wrong: %+v", target.Name, target)
		}
	}

	// Deleting the live claude session flips it to missing-source
	// (recoverable), never touching the backup copy.
	slug := claudeProjectSlug(projectPath)
	if err := os.Remove(filepath.Join(home, ".claude", "projects", slug, "sess-1.jsonl")); err != nil {
		t.Fatal(err)
	}
	status, err = Build(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range status.Targets {
		if target.Name == "claude" && (target.MissingSource != 1 || target.BackupCount != 1) {
			t.Fatalf("claude delete status wrong: %+v", target)
		}
	}
}

func TestRunJSON(t *testing.T) {
	_, projectPath := setup(t)
	var out, errOut strings.Builder
	if code := Run([]string{"status", projectPath, "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"project_path"`) || !strings.Contains(out.String(), `"missing_backup": 1`) {
		t.Fatalf("json output unexpected: %s", out.String())
	}
}
