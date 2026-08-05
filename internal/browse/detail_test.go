package browse

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeClaudeSession(t *testing.T) string {
	t.Helper()
	lines := []string{
		`{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"role":"user","content":"first prompt here"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"reply one"}],"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"total 42"}]}}`,
		`{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":150000}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"machine summary, not a prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"harness line"}],"usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"final reply"}],"usage":{"input_tokens":30,"output_tokens":7}}}`,
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDetailClaude(t *testing.T) {
	detail := LoadDetail(Session{Target: "claude", SourcePath: writeClaudeSession(t)})

	if detail.FirstPrompt != "first prompt here" {
		t.Fatalf("FirstPrompt = %q", detail.FirstPrompt)
	}
	if detail.LastPrompt != "second prompt" {
		t.Fatalf("LastPrompt = %q (compact summary must not count)", detail.LastPrompt)
	}
	if detail.LastResponse != "final reply" {
		t.Fatalf("LastResponse = %q", detail.LastResponse)
	}
	if detail.Created.IsZero() || detail.Created.Format("2006-01-02") != "2026-08-01" {
		t.Fatalf("Created = %v", detail.Created)
	}
	if detail.Compactions != 1 || detail.LastCompact != "auto" || detail.LastCompactPre != 150000 {
		t.Fatalf("compaction data wrong: %d %q %d", detail.Compactions, detail.LastCompact, detail.LastCompactPre)
	}
	if detail.Tokens.Input != 140 || detail.Tokens.Output != 32 ||
		detail.Tokens.CacheRead != 1000 || detail.Tokens.CacheWrite != 50 {
		t.Fatalf("token totals wrong: %+v", detail.Tokens)
	}
	// Models with billed tokens only — the synthetic harness line is not
	// a model.
	if len(detail.Models) != 2 || detail.Models[0] != "claude-opus-4-8" || detail.Models[1] != "claude-sonnet-4-6" {
		t.Fatalf("models = %v", detail.Models)
	}
	if opus := detail.PerModel["claude-opus-4-8"]; opus.Input != 110 || opus.Output != 25 {
		t.Fatalf("per-model split wrong: %+v", opus)
	}

	roles := map[string]int{}
	for _, msg := range detail.Transcript {
		roles[msg.Role]++
	}
	if roles["user"] != 2 || roles["assistant"] != 3 || roles["tool"] != 1 ||
		roles["result"] != 1 || roles["compact"] != 1 || roles["summary"] != 1 {
		t.Fatalf("transcript roles = %v", roles)
	}
}

func TestLoadDetailKeepRingAndTotal(t *testing.T) {
	path := writeClaudeSession(t)
	detail := LoadDetailKeep(Session{Target: "claude", SourcePath: path}, 3)
	if len(detail.Transcript) != 3 {
		t.Fatalf("ring size = %d", len(detail.Transcript))
	}
	full := LoadDetail(Session{Target: "claude", SourcePath: path})
	if detail.TranscriptTotal != full.TranscriptTotal || detail.TranscriptTotal <= 3 {
		t.Fatalf("TranscriptTotal = %d (full %d)", detail.TranscriptTotal, full.TranscriptTotal)
	}
	// The ring keeps the tail: last message must be the final reply.
	if last := detail.Transcript[len(detail.Transcript)-1]; last.Text != "final reply" {
		t.Fatalf("ring tail = %q", last.Text)
	}
}

func TestPricing(t *testing.T) {
	if _, ok := priceFor("gpt-5.5"); ok {
		t.Fatal("non-anthropic model must have no price")
	}
	if price, ok := priceFor("claude-haiku-4-5-20251001"); !ok || price.Input != 1 {
		t.Fatalf("date-suffixed id must prefix-match: %+v %v", price, ok)
	}

	cost, ok := costUSD("claude-opus-4-8", TokenTotals{
		Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000,
	})
	if !ok {
		t.Fatal("opus must be priced")
	}
	// 5 + 25 + 5*0.1 + 5*1.25 = 36.75
	if math.Abs(cost-36.75) > 1e-9 {
		t.Fatalf("cost = %f, want 36.75", cost)
	}
	if _, ok := costUSD("unknown-model", TokenTotals{Input: 1}); ok {
		t.Fatal("unknown model must not report a cost")
	}
}

func TestContentHelpers(t *testing.T) {
	if got := contentText([]byte(`"plain  string\nvalue"`)); got != "plain string value" {
		t.Fatalf("contentText plain = %q", got)
	}
	if got := contentRaw([]byte(`"keep\nnewlines"`)); got != "keep\nnewlines" {
		t.Fatalf("contentRaw must preserve structure: %q", got)
	}
	blocks := []byte(`[{"type":"text","text":"a"},{"type":"tool_use","name":"Bash"},{"type":"text","text":"b"}]`)
	if got := contentText(blocks); got != "a b" {
		t.Fatalf("contentText blocks = %q", got)
	}
	uses := toolUses([]byte(`[{"type":"tool_use","name":"Bash","input":{"command":"echo hi"}}]`))
	if len(uses) != 1 || !strings.HasPrefix(uses[0], "Bash") || !strings.Contains(uses[0], "echo hi") {
		t.Fatalf("toolUses = %v", uses)
	}
	results := toolResults([]byte(`[{"type":"tool_result","content":"line one\nline two"}]`))
	if len(results) != 1 || strings.Contains(results[0], "line two") {
		t.Fatalf("toolResults should summarize to one line: %v", results)
	}
}

func TestSessionStateMapping(t *testing.T) {
	cases := map[string]string{
		"OK": "ok", "ACTIVE": "active", "OPEN": "open", "STALE_BACKUP": "stale",
		"MISSING_BACKUP": "unbacked", "RESTORED": "restored", "LOST": "lost",
		"MISSING_SOURCE": "recover",
	}
	for state, want := range cases {
		label, _ := sessionState(state)
		if !strings.Contains(label, want) {
			t.Fatalf("state %s → %q, want %q", state, label, want)
		}
	}
}

func TestTreeHelpers(t *testing.T) {
	now := time.Now()
	projects := []*Project{
		{Path: "/home/u/work/app", OK: 1, Sessions: []Session{{ID: "1", State: "OK", Modified: now}}},
		{Path: "/home/u/work/app/subdir", RecoverOnly: 1, Sessions: []Session{{ID: "2", State: "MISSING_SOURCE", Modified: now}}},
		{Path: "/home/u/other", OK: 1, Sessions: []Session{{ID: "3", State: "OK", Modified: now}}},
	}

	folders := ChildrenOf(projects, "/home/u/work", "/home/u/work")
	if len(folders) != 1 || folders[0].Name != "app" || folders[0].Sessions != 2 {
		t.Fatalf("ChildrenOf = %+v", folders)
	}
	if folders[0].RecoverOnly != 1 {
		t.Fatalf("health rollup missing recover count: %+v", folders[0])
	}

	if root := NearestRoot(projects, "/home/u/work/app/subdir/deeper"); root != "/home/u/work/app/subdir" {
		t.Fatalf("NearestRoot = %s", root)
	}

	all := AllUnder(projects, "/home/u/work")
	if len(all) != 2 {
		t.Fatalf("AllUnder = %d sessions", len(all))
	}
	for _, session := range all {
		if session.ProjectPath == "" {
			t.Fatal("AllUnder must set ProjectPath for display")
		}
	}
}

func TestHumanTokensAndTruncate(t *testing.T) {
	cases := map[int64]string{999: "999", 1_500: "1.5k", 2_500_000: "2.5M", 3_100_000_000: "3.1B"}
	for in, want := range cases {
		if got := humanTokens(in); got != want {
			t.Fatalf("humanTokens(%d) = %s, want %s", in, got, want)
		}
	}
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("truncate short = %q", got)
	}
	if got := truncate("hello world", 8); len([]rune(got)) > 8 {
		t.Fatalf("truncate long = %q", got)
	}
}
