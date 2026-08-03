package browse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

func writeTranscript(t *testing.T, path string) {
	t.Helper()
	lines := `{"type":"user","message":{"role":"user","content":"set up MEGATRON as the conduit peer"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Configuring megatron now. Megatron will join the mesh."}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"echo MEGATRON hidden in tool input"}}]}}
{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary mentioning MEGATRON should not count"}}
`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestContentSearchCountsAndRanks(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Config{BackupRoot: filepath.Join(tmp, "backup")}

	heavy := filepath.Join(tmp, "heavy.jsonl")
	writeTranscript(t, heavy)
	light := filepath.Join(tmp, "light.jsonl")
	if err := os.WriteFile(light, []byte(`{"type":"user","message":{"role":"user","content":"megatron once"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions := []Session{
		{ID: "light", SourcePath: light, Modified: time.Now()},
		{ID: "heavy", SourcePath: heavy, Modified: time.Now()},
		{ID: "lost", State: "LOST"},
	}

	hits := ContentSearch(cfg, sessions, "MeGaTrOn")
	if len(hits) != 2 {
		t.Fatalf("expected 2 sessions with hits, got %+v", hits)
	}
	// Conversation text only: 1 user + 2 assistant mentions; the tool_use
	// input and the compaction summary must not count.
	if hits[0].Session.ID != "heavy" || hits[0].Count != 3 {
		t.Fatalf("heavy session should rank first with 3 hits, got %s/%d", hits[0].Session.ID, hits[0].Count)
	}
	if hits[1].Session.ID != "light" || hits[1].Count != 1 {
		t.Fatalf("light session should have 1 hit, got %s/%d", hits[1].Session.ID, hits[1].Count)
	}
	if hits[0].Snippet == "" {
		t.Fatal("expected a snippet for the top hit")
	}
}

func TestTextCacheRefreshesOnMtimeOnly(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Config{BackupRoot: filepath.Join(tmp, "backup")}
	src := filepath.Join(tmp, "s.jsonl")
	writeTranscript(t, src)
	sessions := []Session{{ID: "s", SourcePath: src}}

	refreshTextCache(cfg, sessions)
	extract := filepath.Join(cfg.BackupRoot, textCacheDir, "s.txt")
	first, err := os.Stat(extract)
	if err != nil {
		t.Fatalf("extract missing: %v", err)
	}

	// Unchanged mtime: refresh must not rewrite the extract.
	time.Sleep(10 * time.Millisecond)
	refreshTextCache(cfg, sessions)
	second, _ := os.Stat(extract)
	if !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("extract rewritten although the source did not change")
	}

	// A source touch re-extracts.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	refreshTextCache(cfg, sessions)
	third, _ := os.Stat(extract)
	if third.ModTime().Equal(first.ModTime()) {
		t.Fatal("extract not refreshed after the source changed")
	}
}
