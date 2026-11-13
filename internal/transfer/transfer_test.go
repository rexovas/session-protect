package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

type env struct {
	cfg             config.Config
	home, def, acme string
	globex, project string
}

func setup(t *testing.T) env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := env{
		cfg:     config.Config{BackupRoot: filepath.Join(home, "backup"), Topology: "combined"},
		home:    home,
		def:     filepath.Join(home, ".claude"),
		acme:    filepath.Join(home, "acme-config"),
		globex:  filepath.Join(home, "globex-config"),
		project: filepath.Join(home, "work", "client"),
	}
	for _, dir := range []string{e.cfg.BackupRoot, e.acme, e.globex, filepath.Join(e.project, "team", "infra")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for label, dir := range map[string]string{"acme": e.acme, "globex": e.globex} {
		if _, _, err := targets.RegisterClaudeDir(dir, label); err != nil {
			t.Fatal(err)
		}
	}
	return e
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

func session(t *testing.T, home, project, id string) {
	t.Helper()
	write(t, filepath.Join(home, "projects", targets.ClaudeSlug(project), id+".jsonl"), `{"type":"user","cwd":"`+project+`"}`+"\n")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A recursive project transfer moves every session with its id-keyed
// extras, drained dirs' leftovers and memory, copies prompt history, and
// leaves the folders owned by the destination.
func TestTransferProjectRecursive(t *testing.T) {
	e := setup(t)
	team := filepath.Join(e.project, "team")
	infra := filepath.Join(team, "infra")
	session(t, e.def, e.project, "s1")
	session(t, e.def, e.project, "s2")
	session(t, e.def, team, "s3")
	write(t, filepath.Join(e.def, "projects", targets.ClaudeSlug(e.project), "s1", "subagents", "a.jsonl"), "{}\n")
	write(t, filepath.Join(e.def, "file-history", "s1", "v1"), "x")
	write(t, filepath.Join(e.def, "session-env", "s2", "env"), "x")
	write(t, filepath.Join(e.def, "projects", targets.ClaudeSlug(e.project), "memory", "MEMORY.md"), "client memory\n")
	write(t, filepath.Join(e.def, "projects", targets.ClaudeSlug(e.project), "stray-sidecar", "x"), "x")
	write(t, filepath.Join(e.def, "projects", targets.ClaudeSlug(infra), "memory", "MEMORY.md"), "infra memory\n") // memory only
	other := filepath.Join(e.home, "work", "client-tools")                                                         // a sibling: out of scope
	session(t, e.def, other, "s9")
	write(t, filepath.Join(e.def, "history.jsonl"),
		`{"display":"one","sessionId":"s1","project":"`+e.project+`","timestamp":1}`+"\n"+
			`{"display":"nine","sessionId":"s9","project":"`+other+`","timestamp":2}`+"\n")

	plan, err := Build(e.cfg, Options{To: "acme", Project: e.project, Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sessions) != 3 {
		t.Fatalf("sessions = %+v", plan.Sessions)
	}
	for _, folder := range plan.Folders {
		if folder.Before != "default" || folder.After != "acme" {
			t.Errorf("ownership preview %+v", folder)
		}
	}
	if err := Apply(e.cfg, plan); err != nil {
		t.Fatal(err)
	}

	slug := targets.ClaudeSlug(e.project)
	for _, path := range []string{
		filepath.Join(e.acme, "projects", slug, "s1.jsonl"),
		filepath.Join(e.acme, "projects", slug, "s1", "subagents", "a.jsonl"),
		filepath.Join(e.acme, "projects", targets.ClaudeSlug(team), "s3.jsonl"),
		filepath.Join(e.acme, "file-history", "s1", "v1"),
		filepath.Join(e.acme, "session-env", "s2", "env"),
		filepath.Join(e.acme, "projects", slug, "memory", "MEMORY.md"),
		filepath.Join(e.acme, "projects", slug, "stray-sidecar", "x"),
		filepath.Join(e.acme, "projects", targets.ClaudeSlug(infra), "memory", "MEMORY.md"),
	} {
		if !exists(path) {
			t.Errorf("missing after transfer: %s", path)
		}
	}
	for _, path := range []string{
		filepath.Join(e.def, "projects", slug, "s1.jsonl"),
		filepath.Join(e.def, "file-history", "s1"),
		filepath.Join(e.def, "projects", slug, "memory"),
	} {
		if exists(path) {
			t.Errorf("left behind: %s", path)
		}
	}
	for _, dir := range []string{slug, targets.ClaudeSlug(team), targets.ClaudeSlug(infra)} {
		if exists(filepath.Join(e.def, "projects", dir)) {
			t.Errorf("drained project dir left behind: %s", dir)
		}
	}
	if !exists(filepath.Join(e.def, "projects", targets.ClaudeSlug(other), "s9.jsonl")) {
		t.Error("a sibling folder's session moved")
	}
	history, _ := os.ReadFile(filepath.Join(e.acme, "history.jsonl"))
	if !strings.Contains(string(history), `"s1"`) || strings.Contains(string(history), `"s9"`) {
		t.Errorf("destination history = %s", history)
	}
	if source, _ := os.ReadFile(filepath.Join(e.def, "history.jsonl")); !strings.Contains(string(source), `"s1"`) {
		t.Error("the source history was modified")
	}
	if owner := targets.FolderOwner(filepath.Join(e.project, "new")); owner.Owner.Label != "acme" {
		t.Errorf("new subfolder after transfer = %+v", owner)
	}
}

// One session moves alone; its project's memory is copied, not moved,
// because the other sessions stay.
func TestTransferOneSessionCopiesMemory(t *testing.T) {
	e := setup(t)
	session(t, e.def, e.project, "s1")
	session(t, e.def, e.project, "s2")
	write(t, filepath.Join(e.def, "projects", targets.ClaudeSlug(e.project), "memory", "MEMORY.md"), "m\n")
	plan, err := Build(e.cfg, Options{To: "acme", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Memory) != 1 || !plan.Memory[0].Copy || plan.Folders[0].Left != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if err := Apply(e.cfg, plan); err != nil {
		t.Fatal(err)
	}
	slug := targets.ClaudeSlug(e.project)
	if !exists(filepath.Join(e.def, "projects", slug, "memory", "MEMORY.md")) || !exists(filepath.Join(e.acme, "projects", slug, "memory", "MEMORY.md")) {
		t.Error("memory not copied to both")
	}
	if !exists(filepath.Join(e.def, "projects", slug, "s2.jsonl")) || exists(filepath.Join(e.def, "projects", slug, "s1.jsonl")) || len(plan.Drained) != 0 {
		t.Error("wrong sessions moved")
	}
}

// A folder another account owns can't take this account's sessions, and
// an existing file at the destination is never overwritten.
func TestTransferRefusals(t *testing.T) {
	e := setup(t)
	session(t, e.def, e.project, "s1")
	session(t, e.globex, e.project, "g1") // globex owns the folder
	if _, err := Build(e.cfg, Options{To: "acme", Project: e.project}); err == nil || !strings.Contains(err.Error(), "globex") {
		t.Fatalf("transfer into a folder owned by a third account: %v", err)
	}
	if _, err := Build(e.cfg, Options{To: "globex", Project: e.project}); err != nil {
		t.Fatalf("transfer to the owner refused: %v", err)
	}
	session(t, e.globex, e.project, "s1") // same id already there
	if _, err := Build(e.cfg, Options{To: "globex", Project: e.project}); err == nil {
		t.Fatal("an existing destination file would be overwritten")
	}
	if _, err := Build(e.cfg, Options{To: "default", Project: e.project}); err == nil {
		t.Fatal("source and destination the same was accepted")
	}
	if _, err := Build(e.cfg, Options{To: "nope", Project: e.project}); err == nil {
		t.Fatal("an unknown destination was accepted")
	}
}
