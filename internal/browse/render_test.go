package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBrowseOnceRendersFrame drives the full pipeline — scan, model
// build, one rendered frame — against a fabricated home, exercising the
// row/header/footer rendering that unit tests cannot reach in isolation.
func TestBrowseOnceRendersFrame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	projectPath := filepath.Join(home, "work", "app")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// macOS temp dirs are symlinked (/var → /private/var); the session's
	// cwd must match what os.Getwd reports inside the dir.
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		projectPath = resolved
	}
	slug := ""
	for _, c := range projectPath {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			slug += string(c)
		} else {
			slug += "-"
		}
	}
	session := filepath.Join(home, ".claude", "projects", slug, "aaaa1111-2222-3333-4444-555566667777.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"user","cwd":"` + projectPath + `","sessionId":"aaaa1111-2222-3333-4444-555566667777","message":{"role":"user","content":"hello world"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":2}}}
`
	if err := os.WriteFile(session, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("backup_root = \""+filepath.Join(home, "root")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	cwd, _ := os.Getwd()
	if err := os.Chdir(projectPath); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	var out, errOut strings.Builder
	if code := Run([]string{"--once"}, &out, &errOut); code != 0 {
		t.Fatalf("browse --once exit %d: %s", code, errOut.String())
	}
	frame := out.String()
	for _, want := range []string{"Session Explorer", "STATE", "! unbacked", "(not set)", "m menu"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}

	// Bad argument path.
	if code := Run([]string{"--wat"}, &out, &errOut); code != 2 {
		t.Fatal("unknown argument should exit 2")
	}
}
