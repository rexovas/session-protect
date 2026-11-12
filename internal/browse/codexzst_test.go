package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/rexovas/session-protect/internal/config"
)

func writeZst(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, _ := zstd.NewWriter(file)
	_, _ = encoder.Write([]byte(content))
	_ = encoder.Close()
	_ = file.Close()
}

// Newer codex compresses older rollouts to .jsonl.zst. They must still be
// listed under their project, with name, model, detail and searchable text
// — not mistaken for lost sessions because only the history mentions them.
func TestCompressedCodexRolloutIsASession(t *testing.T) {
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
	id := "019bf1d3-61e1-7ec0-8aaf-97cc71746abc"
	writeZst(t, filepath.Join(home, ".codex", "sessions", "2026", "01", "24", "rollout-2026-01-24T16-05-20-"+id+".jsonl.zst"),
		`{"type":"session_meta","payload":{"id":"`+id+`","cwd":"`+project+`","source":"cli"}}
{"type":"turn_context","payload":{"model":"gpt-6-astra"}}
{"type":"event_msg","payload":{"type":"user_message","message":"compressed hello"}}
`)
	writeFile(t, filepath.Join(home, ".codex", "history.jsonl"),
		`{"session_id":"`+id+`","ts":1769288720,"text":"compressed hello"}`+"\n")
	writeFile(t, filepath.Join(home, ".codex", "session_index.jsonl"),
		`{"id":"`+id+`","thread_name":"ZST-THREAD","updated_at":"2026-01-24T21:10:00Z"}`+"\n")

	projects := ScanNamed(cfg)
	var found *Session
	for _, p := range projects {
		for i := range p.Sessions {
			s := &p.Sessions[i]
			if s.ID != id {
				continue
			}
			if p.Path != project {
				t.Fatalf("compressed session filed under %q (state %s), want its project", p.Path, s.State)
			}
			found = s
		}
	}
	if found == nil {
		t.Fatal("compressed codex rollout not listed")
	}
	if found.State == "LOST" {
		t.Fatal("compressed rollout reported as lost")
	}
	if found.CustomName != "ZST-THREAD" || found.LastModel != "gpt-6-astra" {
		t.Errorf("name/model = %q/%q", found.CustomName, found.LastModel)
	}
	if detail := LoadDetail(*found); detail.Messages == 0 && len(detail.Transcript) == 0 {
		t.Error("detail view read nothing from the compressed transcript")
	}
	if text := extractText(found.SourcePath); !strings.Contains(text, "compressed hello") {
		t.Errorf("content search text = %q", text)
	}
}

// When codex leaves one rollout both plain and compressed, the listing
// shows it once, from whichever copy holds more transcript.
func TestDuplicateRolloutPrefersFullerCopy(t *testing.T) {
	dir := t.TempDir()
	meta := `{"type":"session_meta","payload":{"id":"dup","cwd":"/p","source":"cli"}}` + "\n"
	long := meta + strings.Repeat(`{"type":"event_msg","payload":{"type":"user_message","message":"x"}}`+"\n", 200)
	for _, tc := range []struct {
		name       string
		plain, zst string
		wantZst    bool
	}{
		{"compressed is fuller", meta, long, true},
		{"plain is fuller", long, meta, false},
	} {
		sub := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
		writeFile(t, filepath.Join(sub, "rollout-dup.jsonl"), tc.plain)
		writeZst(t, filepath.Join(sub, "rollout-dup.jsonl.zst"), tc.zst)
		sessions := merge("codex", listCodex(sub), nil)
		if len(sessions) != 1 {
			t.Fatalf("%s: %d sessions, want 1", tc.name, len(sessions))
		}
		if got := strings.HasSuffix(sessions[0].SourcePath, ".zst"); got != tc.wantZst {
			t.Errorf("%s: picked %s", tc.name, filepath.Base(sessions[0].SourcePath))
		}
	}
}

// Codex has written messages in two shapes; both read as conversation,
// and nothing else does.
func TestCodexEventMessageBothShapes(t *testing.T) {
	for payload, want := range map[string][2]string{
		`{"type":"user_message","message":"old prompt"}`:                                                                             {"user", "old prompt"},
		`{"type":"agent_message","message":"old reply"}`:                                                                             {"assistant", "old reply"},
		`{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"new prompt"}]}}`:                    {"user", "new prompt"},
		`{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"new reply"}]}}`:                    {"assistant", "new reply"},
		`{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"a"},{"type":"Text","text":"b"}]}}`: {"assistant", "a\nb"},
		`{"type":"item_completed","item":{"type":"Reasoning","summary_text":[]}}`:                                                    {"", ""},
		`{"type":"item_completed","item":{"type":"CommandExecution","command":["ls"]}}`:                                              {"", ""},
		`{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"image","url":"x"}]}}`:                             {"", ""},
		`{"type":"token_count","info":{}}`:                                                                                           {"", ""},
	} {
		role, text := codexEventMessage([]byte(payload))
		if role != want[0] || text != want[1] {
			t.Errorf("%s -> (%q, %q), want %v", payload, role, text, want)
		}
	}
}

// A current-format codex session (item_completed messages), compressed,
// is fully readable: transcript, prompts, and searchable text.
func TestNewFormatCodexSessionIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-2026-09-17T14-19-15-01a0.jsonl.zst")
	writeZst(t, path, `{"type":"session_meta","payload":{"id":"01a0","cwd":"/p","source":"cli"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>noise</environment_context>"}]}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"work on tag registration"}]}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning","summary_text":[]}}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"registered the NFC tags"}],"phase":"final"}}}
`)
	session := Session{Target: "codex", ID: "01a0", SourcePath: path}
	detail := LoadDetail(session)
	if detail.Messages != 2 || detail.FirstPrompt != "work on tag registration" || detail.LastResponse != "registered the NFC tags" {
		t.Fatalf("detail = messages %d, first %q, last %q", detail.Messages, detail.FirstPrompt, detail.LastResponse)
	}
	text := extractText(path)
	if !strings.Contains(text, "tag registration") || !strings.Contains(text, "NFC tags") || strings.Contains(text, "environment_context") {
		t.Fatalf("search text = %q", text)
	}
}
