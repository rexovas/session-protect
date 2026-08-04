package browse

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCodexDetailAndExtract(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "rollout.jsonl")
	lines := `{"timestamp":"2026-07-09T07:30:37.837Z","type":"session_meta","payload":{"id":"x","cwd":"/w"}}
{"timestamp":"2026-07-09T07:31:26.190Z","type":"turn_context","payload":{"model":"gpt-5.5"}}
{"timestamp":"2026-07-09T07:31:26.199Z","type":"event_msg","payload":{"type":"user_message","message":"fix the MEGATRON build"}}
{"timestamp":"2026-07-09T07:31:30.070Z","type":"event_msg","payload":{"type":"agent_message","message":"On it - megatron first."}}
{"timestamp":"2026-07-09T07:32:44.306Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"make build\"}"}}
{"timestamp":"2026-07-09T07:32:45.000Z","type":"response_item","payload":{"type":"function_call_output","output":"exit 0\nmore"}}
{"timestamp":"2026-07-09T20:00:32.195Z","type":"compacted","payload":{"message":""}}
{"timestamp":"2026-07-09T20:01:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":50}}}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	d := LoadDetailKeep(Session{Target: "codex", SourcePath: path}, 300)
	if d.FirstPrompt != "fix the MEGATRON build" || d.LastResponse != "On it - megatron first." {
		t.Fatalf("prompts wrong: %q / %q", d.FirstPrompt, d.LastResponse)
	}
	if len(d.Models) != 1 || d.Models[0] != "gpt-5.5" {
		t.Fatalf("model missing: %v", d.Models)
	}
	if d.Compactions != 1 || d.Messages != 2 {
		t.Fatalf("counts wrong: compactions=%d messages=%d", d.Compactions, d.Messages)
	}
	if d.Tokens.Input != 400 || d.Tokens.CacheRead != 600 || d.Tokens.Output != 50 {
		t.Fatalf("token split wrong: %+v", d.Tokens)
	}
	roles := map[string]int{}
	for _, msg := range d.Transcript {
		roles[msg.Role]++
	}
	if roles["tool"] != 1 || roles["result"] != 1 || roles["compact"] != 1 {
		t.Fatalf("transcript roles wrong: %v", roles)
	}

	if _, model := scanFileMeta(path); model != "gpt-5.5" {
		t.Fatalf("scanFileMeta model = %q", model)
	}

	text := extractText(path)
	if !strings.Contains(text, "MEGATRON build") || !strings.Contains(text, "megatron first") {
		t.Fatalf("extract missing conversation: %q", text)
	}
	if strings.Contains(text, "make build") {
		t.Fatal("tool call leaked into extract")
	}
}

func TestParseCodexProcs(t *testing.T) {
	ps := ` 10862 codex resume
 74108 /usr/local/bin/codex resume 019f4aad-282c-7d53-8056-6b8b39a0f760
 90000 codexish something
 91000 vim codex-notes.txt
`
	ids, pids := parseCodexProcs(ps)
	if !ids["019f4aad-282c-7d53-8056-6b8b39a0f760"] || len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	if len(pids) != 1 || pids[0] != "10862" {
		t.Fatalf("pids = %v", pids)
	}
}
