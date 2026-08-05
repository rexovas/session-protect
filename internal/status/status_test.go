package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	session := filepath.Join(home, ".claude", "projects", "-p", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("backup_root = \""+filepath.Join(home, "root")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	return home
}

func TestBuildAndPrint(t *testing.T) {
	setupEnv(t)
	built := Build()
	var claude *TargetStatus
	for i := range built.Targets {
		if built.Targets[i].Name == "claude" {
			claude = &built.Targets[i]
		}
	}
	if claude == nil || !claude.Detected || claude.SessionCount != 1 {
		t.Fatalf("claude target status wrong: %+v", claude)
	}

	var out strings.Builder
	if code := Print(&out, true); code != 0 {
		t.Fatal("json print failed")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out.String()), &parsed); err != nil {
		t.Fatalf("output is not valid json: %v", err)
	}
	out.Reset()
	if code := Print(&out, false); code != 0 || !strings.Contains(out.String(), "claude") {
		t.Fatalf("text print wrong: %s", out.String())
	}
}
