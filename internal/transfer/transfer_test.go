package transfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard/codextest"
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

// codexRollout writes a rollout in a codex home's date tree.
func codexRollout(t *testing.T, home, date, id, cwd, source string) string {
	t.Helper()
	if source == "" {
		source = `"cli"`
	}
	path := filepath.Join(home, "sessions", date, "rollout-2026-11-01T10-00-00-"+id+".jsonl")
	write(t, path, `{"type":"session_meta","payload":{"id":"`+id+`","cwd":"`+cwd+`","source":`+source+`}}`+"\n")
	return path
}

// An account's codex sessions move with its claude ones: the rollouts in
// scope, the subagents they spawned (wherever those worked), and copies
// of their history and thread names; other folders' rollouts stay.
func TestTransferMovesCodexSessions(t *testing.T) {
	e := setup(t)
	defCodex := filepath.Join(e.home, ".codex")
	acmeCodex := filepath.Join(e.home, "acme-codex")
	if err := os.MkdirAll(acmeCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterDir(targets.AgentCodex, acmeCodex, "acme"); err != nil {
		t.Fatal(err)
	}
	session(t, e.def, e.project, "s1")
	top := codexRollout(t, defCodex, "2026/11/01", "c1", e.project, "")
	child := codexRollout(t, defCodex, "2026/11/02", "c2", filepath.Join(e.home, "elsewhere"),
		`{"subagent":{"thread_spawn":{"parent_thread_id":"c1","depth":1}}}`)
	grandchild := codexRollout(t, defCodex, "2026/11/02", "c3", filepath.Join(e.home, "elsewhere"),
		`{"subagent":{"thread_spawn":{"parent_thread_id":"c2","depth":2}}}`)
	other := codexRollout(t, defCodex, "2026/11/01", "c9", filepath.Join(e.home, "other"), "")
	write(t, filepath.Join(defCodex, "history.jsonl"), `{"session_id":"c1","ts":1,"text":"hi"}`+"\n"+`{"session_id":"c9","ts":2,"text":"no"}`+"\n")
	write(t, filepath.Join(defCodex, "session_index.jsonl"), `{"id":"c1","thread_name":"named","updated_at":"2026-11-01T00:00:00Z"}`+"\n")

	plan, err := Build(e.cfg, Options{To: "acme", Project: e.project, Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sessions) != 1 || len(plan.Codex) != 3 {
		t.Fatalf("claude %d, codex %d", len(plan.Sessions), len(plan.Codex))
	}
	if len(plan.Folders) != 1 || plan.Folders[0].Sessions != 1 || plan.Folders[0].Codex != 1 || plan.Folders[0].After != "acme" {
		t.Fatalf("folders = %+v", plan.Folders)
	}
	if err := Apply(e.cfg, plan); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{top, child, grandchild} {
		rel, _ := filepath.Rel(filepath.Join(defCodex, "sessions"), path)
		if exists(path) || !exists(filepath.Join(acmeCodex, "sessions", rel)) {
			t.Errorf("not moved: %s", rel)
		}
	}
	if !exists(other) {
		t.Error("another folder's rollout moved")
	}
	names, _ := os.ReadFile(filepath.Join(acmeCodex, "session_index.jsonl"))
	history, _ := os.ReadFile(filepath.Join(acmeCodex, "history.jsonl"))
	if !strings.Contains(string(names), "named") || !strings.Contains(string(history), `"c1"`) || strings.Contains(string(history), `"c9"`) {
		t.Errorf("names=%s history=%s", names, history)
	}

	// The account has no codex home for codex-only work elsewhere: refused.
	codexRollout(t, defCodex, "2026/11/03", "c5", filepath.Join(e.home, "work", "g"), "")
	if _, err := Build(e.cfg, Options{To: "globex", Project: filepath.Join(e.home, "work", "g")}); err == nil || !strings.Contains(err.Error(), "no codex home") {
		t.Fatalf("missing codex home: %v", err)
	}
	if _, err := Build(e.cfg, Options{To: "globex", Project: filepath.Join(e.home, "work", "g"), Agent: "claude"}); err == nil {
		t.Fatal("--agent claude with nothing of claude's should be refused")
	}
}

// A conversation codex's background server has open can't move, even
// with no codex process in a terminal.
func TestTransferRefusesCodexOpenInServer(t *testing.T) {
	e := setup(t)
	defCodex := filepath.Join(e.home, ".codex")
	acmeCodex := filepath.Join(e.home, "acme-codex")
	if err := os.MkdirAll(acmeCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterDir(targets.AgentCodex, acmeCodex, "acme"); err != nil {
		t.Fatal(err)
	}
	codexRollout(t, defCodex, "2026/11/01", "c1", e.project, "")
	codextest.Server(t, defCodex, []string{"c1"})
	if _, err := Build(e.cfg, Options{To: "acme", Project: e.project, Agent: "codex"}); err == nil || !strings.Contains(err.Error(), "open in codex") {
		t.Fatalf("open conversation not refused: %v", err)
	}
}

// Lost sessions (history only) in scope have their prompt history copied
// to the account; a second run copies nothing twice.
func TestTransferCopiesLostSessionHistory(t *testing.T) {
	e := setup(t)
	session(t, e.acme, e.project, "live-elsewhere") // acme owns the folder already
	write(t, filepath.Join(e.def, "history.jsonl"),
		`{"display":"one","sessionId":"ghost","project":"`+e.project+`","timestamp":1}`+"\n"+
			`{"display":"two","sessionId":"ghost","project":"`+e.project+`/sub","timestamp":2}`+"\n"+
			`{"display":"moved","sessionId":"live-elsewhere","project":"`+e.project+`","timestamp":3}`+"\n"+
			`{"display":"mine","sessionId":"mine","project":"`+e.home+`/other","timestamp":4}`+"\n")
	plan, err := Build(e.cfg, Options{To: "acme", Project: e.project, Recursive: true, Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Lost, ",") != "ghost" || len(plan.Sessions) != 0 {
		t.Fatalf("lost = %v, sessions = %d", plan.Lost, len(plan.Sessions))
	}
	// The preview lists the lost session under its (last recorded) folder.
	if len(plan.Folders) != 1 || plan.Folders[0].Path != e.project+"/sub" || plan.Folders[0].Lost != 1 {
		t.Fatalf("folders = %+v", plan.Folders)
	}
	var preview strings.Builder
	PrintPlan(&preview, plan, true)
	if !strings.Contains(preview.String(), "LOST") || !strings.Contains(preview.String(), "/sub") {
		t.Errorf("preview = %s", preview.String())
	}
	for run := 0; run < 2; run++ {
		if err := Apply(e.cfg, plan); err != nil {
			t.Fatal(err)
		}
	}
	if again, err := Build(e.cfg, Options{To: "acme", Project: e.project, Recursive: true, Agent: "claude"}); err == nil {
		t.Errorf("an already-copied lost session was planned again: %+v", again.Lost)
	}
	history, _ := os.ReadFile(filepath.Join(e.acme, "history.jsonl"))
	if n := strings.Count(string(history), `"ghost"`); n != 2 || strings.Contains(string(history), `"mine"`) {
		t.Errorf("acme history (%d ghost lines) = %s", n, history)
	}
	if source, _ := os.ReadFile(filepath.Join(e.def, "history.jsonl")); strings.Count(string(source), "\n") != 4 {
		t.Error("the source history was modified")
	}
}

// Codex indexes a moved rollout without its name, so a transfer sets each
// moved thread's latest name in the destination.
func TestTransferRestoresCodexThreadNames(t *testing.T) {
	e := setup(t)
	defCodex := filepath.Join(e.home, ".codex")
	acmeCodex := filepath.Join(e.home, "acme-codex")
	if err := os.MkdirAll(acmeCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterDir(targets.AgentCodex, acmeCodex, "acme"); err != nil {
		t.Fatal(err)
	}
	codexRollout(t, defCodex, "2026/11/01", "c1", e.project, "")
	write(t, filepath.Join(defCodex, "session_index.jsonl"),
		`{"id":"c1","thread_name":"first","updated_at":"2026-09-17T18:00:00Z"}`+"\n"+
			`{"id":"c1","thread_name":"LATEST-NAME","updated_at":"2026-09-17T22:00:00Z"}`+"\n")
	var got map[string]string
	setCodexNames = func(home string, names map[string]string) []string {
		if home != acmeCodex {
			t.Errorf("names set in %s", home)
		}
		got = names
		return nil
	}
	t.Cleanup(func() { setCodexNames = func(string, map[string]string) []string { return nil } })
	plan, err := Build(e.cfg, Options{To: "acme", Project: e.project, Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(e.cfg, plan); err != nil {
		t.Fatal(err)
	}
	if got["c1"] != "LATEST-NAME" || len(got) != 1 {
		t.Fatalf("names restored = %v", got)
	}
}
