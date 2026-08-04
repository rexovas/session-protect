package transplant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
)

const sessionID = "11111111-2222-3333-4444-555555555555"

func setup(t *testing.T) (cfg config.Config, home string, source string, target string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	source = filepath.Join(home, "work", "old-app")
	target = filepath.Join(home, "work", "new-app")

	slug := claudeSlug(source)
	dir := filepath.Join(home, ".claude", "projects", slug)
	lines := []string{
		`{"type":"mode","sessionId":"` + sessionID + `","mode":"default"}`,
		`{"type":"user","sessionId":"` + sessionID + `","cwd":"` + source + `","message":{"role":"user","content":"hello"},"tokens":12345678901}`,
		`{"type":"assistant","sessionId":"` + sessionID + `","cwd":"` + source + `/sub","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`not json at all — must pass through verbatim`,
	}
	if err := os.MkdirAll(filepath.Join(dir, sessionID, "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID, "subagents", "agent-1.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", "MEMORY.md"), []byte("old-app memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg = config.Config{BackupRoot: filepath.Join(home, "backup"), Topology: "combined"}
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfg, home, source, target
}

func TestClaudeSlug(t *testing.T) {
	if got := claudeSlug("/Users/x/projects/re.xo_vas"); got != "-Users-x-projects-re-xo-vas" {
		t.Fatalf("slug = %s", got)
	}
}

func TestMoveRewritesAndRemoves(t *testing.T) {
	cfg, home, source, target := setup(t)
	plan, err := Build(cfg, Options{Project: source, To: target})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MemoryAction != "move" || !plan.Emptied {
		t.Fatalf("full project move should move memory, got %s emptied=%v", plan.MemoryAction, plan.Emptied)
	}
	if err := Apply(cfg, plan, Options{Project: source, To: target}); err != nil {
		t.Fatal(err)
	}

	dstFile := filepath.Join(home, ".claude", "projects", claudeSlug(target), sessionID+".jsonl")
	data, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("moved session missing: %v", err)
	}
	text := string(data)
	if strings.Contains(text, source) {
		t.Fatal("cwd fields still reference the source path")
	}
	if !strings.Contains(text, `"cwd":"`+target+`"`) || !strings.Contains(text, target+`/sub`) {
		t.Fatal("cwd fields not rewritten to target (incl. subpaths)")
	}
	if !strings.Contains(text, "12345678901") {
		t.Fatal("large number mangled by rewrite")
	}
	if !strings.Contains(text, "not json at all") {
		t.Fatal("non-JSON line did not pass through")
	}
	for _, gone := range []string{
		filepath.Join(home, ".claude", "projects", claudeSlug(source), sessionID+".jsonl"),
		filepath.Join(home, ".claude", "projects", claudeSlug(source), "memory"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("source not removed after move: %s", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", claudeSlug(target), "memory", "MEMORY.md")); err != nil {
		t.Fatalf("memory did not move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", claudeSlug(target), sessionID, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatalf("sidecar did not move: %v", err)
	}

	var sawSession, sawMemory bool
	for _, entry := range audit.Read(cfg.BackupRoot) {
		if entry.Action == "transplant" && entry.SessionID == sessionID {
			sawSession = true
		}
		if entry.Action == "transplant-memory-move" {
			sawMemory = true
		}
	}
	if !sawSession || !sawMemory {
		t.Fatal("audit entries missing for transplant")
	}
}

func TestCopyMintsNewIdentityAndKeepsSource(t *testing.T) {
	cfg, home, source, target := setup(t)
	opts := Options{SessionID: sessionID, To: target, Copy: true}
	plan, err := Build(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sessions) != 1 || plan.Sessions[0].NewID == "" || plan.Sessions[0].NewID == sessionID {
		t.Fatalf("copy must mint a new id, got %+v", plan.Sessions)
	}
	if plan.MemoryAction != "copy" {
		t.Fatalf("copy should copy memory, got %s", plan.MemoryAction)
	}
	if err := Apply(cfg, plan, opts); err != nil {
		t.Fatal(err)
	}

	// Source untouched.
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", claudeSlug(source), sessionID+".jsonl")); err != nil {
		t.Fatalf("copy removed the source: %v", err)
	}
	newID := plan.Sessions[0].NewID
	data, err := os.ReadFile(filepath.Join(home, ".claude", "projects", claudeSlug(target), newID+".jsonl"))
	if err != nil {
		t.Fatalf("copied session missing: %v", err)
	}
	if strings.Contains(string(data), sessionID) {
		t.Fatal("old session id survives inside the copy")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &event); err != nil {
		t.Fatalf("copy first line unparseable: %v", err)
	}
	if event["sessionId"] != newID {
		t.Fatalf("sessionId not rewritten: %v", event["sessionId"])
	}
}

func TestMemoryKeepBothNeverOverwrites(t *testing.T) {
	cfg, home, source, target := setup(t)
	targetMemory := filepath.Join(home, ".claude", "projects", claudeSlug(target), "memory")
	if err := os.MkdirAll(targetMemory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetMemory, "MEMORY.md"), []byte("target memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := Options{Project: source, To: target}
	plan, err := Build(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MemoryAction != "keep-both" {
		t.Fatalf("default on conflict must be keep-both, got %s", plan.MemoryAction)
	}
	if err := Apply(cfg, plan, opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(targetMemory, "MEMORY.md"))
	if err != nil || string(data) != "target memory\n" {
		t.Fatalf("target memory was touched: %q %v", data, err)
	}
	incoming, err := os.ReadFile(filepath.Join(plan.MemoryDst, "MEMORY.md"))
	if err != nil || string(incoming) != "old-app memory\n" {
		t.Fatalf("incoming memory not landed beside: %q %v", incoming, err)
	}
}

func TestRefusesSameProjectAndMissingSession(t *testing.T) {
	cfg, _, source, _ := setup(t)
	if _, err := Build(cfg, Options{Project: source, To: source}); err == nil {
		t.Fatal("same source and target must be refused")
	}
	if _, err := Build(cfg, Options{SessionID: "nope", To: "/tmp/elsewhere"}); err == nil {
		t.Fatal("unknown session must be refused")
	}
}
