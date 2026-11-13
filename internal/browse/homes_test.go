package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/backup"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// homesEnv is a temp HOME with the default claude dir plus one extra
// config dir (registered as "client"), and a config pointing at a temp
// backup root.
func homesEnv(t *testing.T) (config.Config, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "test")
	}
	for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "test@example.invalid")
	}
	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n[encryption]\nmode = \"none\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(home, "elsewhere", "client-claude")
	if err := os.MkdirAll(client, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterClaudeDir(client, ""); err != nil {
		t.Fatal(err)
	}
	return cfg, home, client
}

func sessionByID(projects []*Project, id string) *Session {
	for _, project := range projects {
		for i := range project.Sessions {
			if project.Sessions[i].ID == id {
				return &project.Sessions[i]
			}
		}
	}
	return nil
}

// Sessions from every claude home share the folder tree, tagged with
// their home; each home backs up to its own target and restores back
// into itself; lost sessions remember the home whose history held them.
func TestScanAcrossClaudeHomes(t *testing.T) {
	cfg, home, client := homesEnv(t)
	project := filepath.Join(home, "work", "app")
	slug := targets.ClaudeSlug(project)
	writeFile(t, filepath.Join(home, ".claude", "projects", slug, "mine.jsonl"), `{"cwd":"`+project+`","type":"user"}`)
	writeFile(t, filepath.Join(client, "projects", slug, "theirs.jsonl"), `{"cwd":"`+project+`","type":"user"}`)
	writeFile(t, filepath.Join(client, "history.jsonl"),
		`{"display":"gone prompt","sessionId":"ghost","project":"`+project+`","timestamp":2000}`+"\n")

	projects := Scan(cfg)
	if len(projects) != 1 || len(projects[0].Sessions) != 3 {
		t.Fatalf("expected one project with 3 sessions, got %+v", projects)
	}
	for id, want := range map[string]string{"mine": "default", "theirs": "client", "ghost": "client"} {
		if s := sessionByID(projects, id); s == nil || homeOf(*s) != want {
			t.Errorf("%s home = %+v, want %s", id, s, want)
		}
	}

	if _, err := backup.Execute(cfg, backup.Options{}); err != nil {
		t.Fatal(err)
	}
	repo, prefix := cfg.RepoFor("claude-client")
	if _, err := os.Stat(filepath.Join(repo, prefix, "projects", slug, "theirs.jsonl")); err != nil {
		t.Fatalf("client session not backed up under its own target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupRoot, "all", "claude", "projects", slug, "theirs.jsonl")); err == nil {
		t.Fatal("client session leaked into the default home's backup")
	}

	if err := os.Remove(filepath.Join(client, "projects", slug, "theirs.jsonl")); err != nil {
		t.Fatal(err)
	}
	projects = Scan(cfg)
	theirs := sessionByID(projects, "theirs")
	if theirs == nil || theirs.State != "MISSING_SOURCE" || theirs.Home != "client" {
		t.Fatalf("deleted client session not recoverable in its home: %+v", theirs)
	}
	dest, overwriting, err := restoreDest(cfg, *theirs)
	if err != nil || overwriting || dest != filepath.Join(client, "projects", slug, "theirs.jsonl") {
		t.Fatalf("restore lands at %s (%v, %v), want the client home", dest, overwriting, err)
	}
}

// Resume pins the agent to the session's home once there are several:
// the default with CLAUDE_CONFIG_DIR removed (setting it, even to
// ~/.claude, makes the agent look for a different login), others with it set.
func TestResumeCommandPinsHome(t *testing.T) {
	_, _, client := homesEnv(t)
	mine := resumeCommand(Session{Target: "claude", Home: "default", ID: "a1"}, "/p")
	if mine != "cd '/p' && env -u CLAUDE_CONFIG_DIR claude --resume a1" {
		t.Errorf("default resume = %s", mine)
	}
	theirs := resumeCommand(Session{Target: "claude", Home: "client", ID: "b2"}, "/p")
	if theirs != "cd '/p' && env CLAUDE_CONFIG_DIR='"+client+"' claude --resume b2" {
		t.Errorf("client resume = %s", theirs)
	}
	if codex := resumeCommand(Session{Target: "codex", ID: "c3"}, "/p"); codex != "cd '/p' && codex resume c3" {
		t.Errorf("codex resume = %s", codex)
	}
	if _, err := targets.UnregisterClaudeDir("client"); err != nil {
		t.Fatal(err)
	}
	if single := resumeCommand(Session{Target: "claude", ID: "a1"}, "/p"); single != "cd '/p' && claude --resume a1" {
		t.Errorf("single-home resume changed: %s", single)
	}
}

// The ACCOUNT column and filter appear only with several homes; the
// filter narrows sessions by home and its selection saves as the default
// the next explorer starts with.
func TestAccountFilterAndSavedDefault(t *testing.T) {
	cfg, _, _ := homesEnv(t)
	sessions := []Session{
		{ID: "m1", Target: "claude", Home: "default", Title: "mine"},
		{ID: "t1", Target: "claude", Home: "client", Title: "theirs"},
		{ID: "x1", Target: "codex", Title: "codex one"},
	}
	m := model{cfg: cfg, width: 140, height: 40, root: "/p", homes: targets.ClaudeHomes(),
		projects: []*Project{{Path: "/p", Sessions: sessions}}}
	m.rebuild()
	if !strings.Contains(m.View(), "ACCOUNT") {
		t.Fatal("no ACCOUNT column with two homes")
	}

	m.buildFilterItems()
	var homeItems []string
	for _, item := range m.filterItems {
		if item.kind == "home" {
			homeItems = append(homeItems, item.key)
		}
	}
	if strings.Join(homeItems, ",") != "default,client" {
		t.Fatalf("ACCOUNT items = %v", homeItems)
	}
	m.fHomes = map[string]bool{"client": true}
	if got := m.filterFacets(sessions); len(got) != 1 || got[0].ID != "t1" {
		t.Fatalf("account filter kept %+v", got)
	}

	m.showFilter = true
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	m = next.(model)
	if !strings.Contains(m.notice, "default saved: client") {
		t.Fatalf("notice = %q", m.notice)
	}
	fresh := model{cfg: cfg, homes: targets.ClaudeHomes()}
	fresh.applyDefaultAccounts()
	if !fresh.fHomes["client"] || len(fresh.fHomes) != 1 {
		t.Fatalf("saved default not applied: %v", fresh.fHomes)
	}

	// One home: no column, no section, and a stale saved default is ignored.
	if _, err := targets.UnregisterClaudeDir("client"); err != nil {
		t.Fatal(err)
	}
	single := model{cfg: cfg, width: 140, height: 40, root: "/p", homes: targets.ClaudeHomes(),
		projects: []*Project{{Path: "/p", Sessions: sessions}}}
	single.applyDefaultAccounts()
	single.rebuild()
	single.buildFilterItems()
	if single.fHomes != nil || strings.Contains(single.View(), "ACCOUNT") {
		t.Fatal("account UI shown with a single home")
	}
	for _, item := range single.filterItems {
		if item.kind == "home" {
			t.Fatal("ACCOUNT section shown with a single home")
		}
	}
}

// Folder rows show the learned owner and ⇄N for sessions on another
// account inside it; A offers the owner first.
func TestOwnershipBadgeAndTransferChooser(t *testing.T) {
	cfg, _, _ := homesEnv(t)
	sessions := []Session{
		{ID: "c1", Target: "claude", Home: "client", SourcePath: "/x/c1.jsonl"},
		{ID: "d1", Target: "claude", Home: "default", SourcePath: "/x/d1.jsonl"},
		{ID: "d2", Target: "claude", Home: "default", SourcePath: "/x/d2.jsonl"},
	}
	m := model{cfg: cfg, width: 140, height: 40, root: "/w", homes: targets.ClaudeHomes(),
		projects: []*Project{{Path: "/w/app", Sessions: sessions}, {Path: "/w/mine", Sessions: []Session{
			{ID: "d3", Target: "claude", Home: "default", SourcePath: "/x/d3.jsonl"}}}}}
	m.rebuild()
	badge := m.ownerBadge(Folder{Path: "/w/app"}, true)
	if !strings.Contains(badge, "@client") || !strings.Contains(badge, "⇄2") {
		t.Errorf("owned mixed folder badge = %q", badge)
	}
	if badge := m.ownerBadge(Folder{Path: "/w/mine"}, true); badge != "" {
		t.Errorf("unowned folder badge = %q", badge)
	}
	for i, folder := range m.folders {
		if folder.Path == "/w/app" {
			m.fCursor = i
		}
	}
	m = m.pressTransfer()
	if m.xfer == nil || len(m.xfer.choices) == 0 || m.xfer.choices[0] != "client" || !m.xferOwnerFirst() {
		t.Fatalf("transfer chooser = %+v", m.xfer)
	}
}
