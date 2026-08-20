package rescue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

const lostID = "dead0000-1111-4222-8333-444455556666"

func setup(t *testing.T) (cfg config.Config, project string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	project = filepath.Join(home, "work", "app")

	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	var history strings.Builder
	for i, text := range []string{
		"set up the billing webhooks",
		"add retry with exponential backoff",
		"write tests for the signature check",
	} {
		fmt.Fprintf(&history, `{"display":%q,"sessionId":%q,"project":%q,"timestamp":%d}`+"\n",
			text, lostID, project, base.Add(time.Duration(i)*time.Hour).UnixMilli())
	}
	// Noise from other sessions must be ignored.
	fmt.Fprintf(&history, `{"display":"unrelated","sessionId":"other-id","project":%q,"timestamp":%d}`+"\n",
		project, base.UnixMilli())
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg = config.Config{BackupRoot: filepath.Join(home, "backup"), Topology: "combined"}
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfg, project
}

func TestLostPrompts(t *testing.T) {
	_, project := setup(t)
	prompts, gotProject, err := LostPrompts("claude", lostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 || gotProject != project {
		t.Fatalf("prompts=%d project=%q", len(prompts), gotProject)
	}
	if prompts[0].Text != "set up the billing webhooks" || !prompts[0].At.Before(prompts[2].At) {
		t.Fatalf("order/content wrong: %+v", prompts)
	}
	if _, _, err := LostPrompts("claude", "no-such-id"); err == nil {
		t.Fatal("unknown session must error")
	}
}

func TestExportArtifact(t *testing.T) {
	cfg, project := setup(t)
	path, err := Export(cfg, "claude", lostID, "billing webhooks work")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"billing webhooks work", lostID, project,
		"prompts: 3", "exponential backoff", "signature check",
		"Only the human side survives",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("artifact missing %q", want)
		}
	}
	entries := audit.Read(cfg.BackupRoot)
	if len(entries) != 1 || entries[0].Action != "export" || entries[0].SessionID != lostID {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestReconstructBuildsResumableSession(t *testing.T) {
	cfg, project := setup(t)
	newID, path, err := Reconstruct(cfg, lostID, "billing webhooks work")
	if err != nil {
		t.Fatal(err)
	}
	if newID == lostID || newID == "" {
		t.Fatalf("newID = %q", newID)
	}
	wantDir := filepath.Join(targets.DetectClaude().Source, "projects", targets.ClaudeSlug(project))
	if filepath.Dir(path) != wantDir {
		t.Fatalf("path = %s, want under %s", path, wantDir)
	}

	// Validate the resume-critical structure (proven live before ship):
	// alternating user/assistant, a parentUuid chain, consistent ids.
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []map[string]any
	dec := json.NewDecoder(file)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("invalid line: %v", err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6 (3 prompts × user+assistant)", len(lines))
	}
	var prevUUID any
	for i, line := range lines {
		if line["sessionId"] != newID {
			t.Fatalf("line %d sessionId = %v", i, line["sessionId"])
		}
		if line["cwd"] != project {
			t.Fatalf("line %d cwd = %v", i, line["cwd"])
		}
		wantType := "user"
		if i%2 == 1 {
			wantType = "assistant"
		}
		if line["type"] != wantType {
			t.Fatalf("line %d type = %v", i, line["type"])
		}
		if i == 0 {
			if line["parentUuid"] != nil {
				t.Fatal("first line must have null parentUuid")
			}
		} else if line["parentUuid"] != prevUUID {
			t.Fatalf("line %d parent chain broken", i)
		}
		prevUUID = line["uuid"]
	}
	last := lines[5]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(last, "reconstructed by session-protect") {
		t.Fatalf("final placeholder missing marker: %q", last)
	}

	var sawReconstruct bool
	for _, entry := range audit.Read(cfg.BackupRoot) {
		if entry.Action == "reconstruct" && entry.SessionID == newID && entry.From == lostID {
			sawReconstruct = true
		}
	}
	if !sawReconstruct {
		t.Fatal("reconstruct audit entry missing")
	}

	// A second reconstruction must mint a different identity, never clash.
	secondID, _, err := Reconstruct(cfg, lostID, "")
	if err != nil || secondID == newID {
		t.Fatalf("second reconstruct: %q %v", secondID, err)
	}
}
