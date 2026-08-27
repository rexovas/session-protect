package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
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
	if _, err := os.Stat(filepath.Join(cfg.BackupRoot, ".session-meta.json")); err != nil {
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

func TestScanSurfacesLostSessions(t *testing.T) {
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

	project := filepath.Join(home, "work", "app")
	// A live session plus history entries: one for the live session, two
	// for a session that no longer exists anywhere.
	writeFile(t, filepath.Join(home, ".claude", "projects", "-slug", "alive.jsonl"), `{"cwd":"`+project+`"}`)
	writeFile(t, filepath.Join(home, ".claude", "history.jsonl"),
		`{"display":"still here","sessionId":"alive","project":"`+project+`","timestamp":1000}
{"display":"first lost prompt","sessionId":"ghost","project":"`+project+`","timestamp":2000}
{"display":"second lost prompt","sessionId":"ghost","project":"`+project+`","timestamp":3000}
`)

	projects := Scan(cfg)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %+v", projects)
	}
	if projects[0].Lost != 1 {
		t.Fatalf("expected 1 lost session, got %+v", projects[0])
	}
	var lost *Session
	for i := range projects[0].Sessions {
		if projects[0].Sessions[i].State == "LOST" {
			lost = &projects[0].Sessions[i]
		}
	}
	if lost == nil || lost.ID != "ghost" || lost.Prompts != 2 || lost.Title != "first lost prompt" {
		t.Fatalf("lost session wrong: %+v", lost)
	}
	// The live session must not be duplicated as lost.
	if len(projects[0].Sessions) != 2 {
		t.Fatalf("expected 2 sessions total, got %d", len(projects[0].Sessions))
	}
}

func TestProjectPathPrefersSlugConsistentCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	// A session that started in ~/work before cd'ing into the real
	// project: early lines carry the foreign cwd, later lines the real
	// one. The file lives under the real project's slug.
	project := filepath.Join(home, "legal", "tello-case")
	slug := targets.ClaudeSlug(project)
	dir := filepath.Join(home, ".claude", "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, "work")
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, `{"type":"user","cwd":%q,"sessionId":"s1","message":{"role":"user","content":"early"}}`+"\n", foreign)
	}
	fmt.Fprintf(&b, `{"type":"user","cwd":%q,"sessionId":"s1","message":{"role":"user","content":"late"}}`+"\n", project)
	if err := os.WriteFile(filepath.Join(dir, "11111111-0000-0000-0000-000000000001.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{BackupRoot: filepath.Join(home, "root"), Topology: "combined"}
	var found *Project
	for _, p := range Scan(cfg) {
		if p.Slug == slug {
			found = p
		}
	}
	if found == nil {
		t.Fatal("project not scanned")
	}
	if found.Path != project {
		t.Fatalf("path = %q, want the slug-consistent cwd %q", found.Path, project)
	}
}

func TestProjectPathDecodesSlugForManualCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	// A manually copied transcript: it lives under the slug of a real
	// directory (dashes and a dot in the name — both slug to '-'), but
	// every cwd inside is foreign; it was never resumed here.
	project := filepath.Join(home, "legal", "tello-case.v2")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	slug := targets.ClaudeSlug(project)
	dir := filepath.Join(home, ".claude", "projects", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(home, "elsewhere")
	line := fmt.Sprintf(`{"type":"user","cwd":%q,"sessionId":"s1","message":{"role":"user","content":"copied"}}`+"\n", foreign)
	if err := os.WriteFile(filepath.Join(dir, "22222222-0000-0000-0000-000000000002.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{BackupRoot: filepath.Join(home, "root"), Topology: "combined"}
	var found *Project
	for _, p := range Scan(cfg) {
		if p.Slug == slug {
			found = p
		}
	}
	if found == nil {
		t.Fatal("project not scanned")
	}
	if found.Path != project {
		t.Fatalf("path = %q, want the decoded slug path %q", found.Path, project)
	}
}

func TestTitlesSkipSlashCommands(t *testing.T) {
	if !isSlashCommand("/model") || !isSlashCommand("/model sonnet") || !isSlashCommand("/compact") {
		t.Fatal("commands not detected")
	}
	if isSlashCommand("/Users/x/notes.md") || isSlashCommand("plain text") || isSlashCommand("/ starts odd") {
		t.Fatal("non-commands misdetected")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	history := `{"display":"/model","sessionId":"s1","project":"/p","timestamp":1}
{"display":"fix the login flow","sessionId":"s1","project":"/p","timestamp":2}
{"display":"/model","sessionId":"s2","project":"/p","timestamp":3}
`
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	titles := historyTitles()
	if titles["s1"] != "fix the login flow" {
		t.Fatalf("s1 title = %q, want the substantive prompt", titles["s1"])
	}
	// A session that only ever ran a command keeps it — better than blank.
	if titles["s2"] != "/model" {
		t.Fatalf("s2 title = %q", titles["s2"])
	}
}

func TestScanSweepsAssistScratchSlugs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	leak := filepath.Join(home, ".claude", "projects", "-private-var-folders-xx-T-sp-assist-1234")
	if err := os.MkdirAll(leak, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leak, "aaaa0000-0000-4000-8000-000000000000.jsonl"),
		[]byte(`{"type":"user","cwd":"/tmp/sp-assist-1234","sessionId":"aaaa0000-0000-4000-8000-000000000000","message":{"role":"user","content":"helper"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BackupRoot: filepath.Join(home, "root"), Topology: "combined"}
	for _, p := range Scan(cfg) {
		if strings.Contains(p.Slug, "sp-assist") || strings.Contains(p.Path, "sp-assist") {
			t.Fatalf("assist scratch surfaced as project: %+v", p)
		}
	}
	if _, err := os.Stat(leak); !os.IsNotExist(err) {
		t.Fatal("leaked scratch slug not swept")
	}
}
