package launch

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rexovas/session-protect/internal/targets"
)

func setup(t *testing.T) (home, acme, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	acme = filepath.Join(home, "acme-config")
	project = filepath.Join(home, "work", "client")
	slugDir := filepath.Join(acme, "projects", targets.ClaudeSlug(project))
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slugDir, "s.jsonl"), []byte(`{"cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targets.RegisterClaudeDir(acme, "acme"); err != nil {
		t.Fatal(err)
	}
	return home, acme, project
}

func configDir(env []string) (string, bool) {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok {
			return value, true
		}
	}
	return "", false
}

// The launch environment follows the folder's owner; the default runs with
// CLAUDE_CONFIG_DIR removed; --account and an explicit variable win.
func TestEnvironment(t *testing.T) {
	home, acme, project := setup(t)
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude")}

	env, _, err := Environment(base[:1], filepath.Join(project, "sub"), "")
	if dir, _ := configDir(env); err != nil || dir != acme {
		t.Errorf("owned folder: %v %v", env, err)
	}
	env, _, _ = Environment(base[:1], filepath.Join(home, "elsewhere"), "")
	if _, set := configDir(env); set {
		t.Errorf("default launch sets CLAUDE_CONFIG_DIR: %v", env)
	}
	env, _, _ = Environment(base, project, "")
	if dir, _ := configDir(env); dir != filepath.Join(home, ".claude") {
		t.Errorf("an explicit CLAUDE_CONFIG_DIR was overridden: %v", env)
	}
	env, _, _ = Environment(base, project, "default")
	if _, set := configDir(env); set {
		t.Errorf("--account default kept CLAUDE_CONFIG_DIR: %v", env)
	}
	if _, _, err := Environment(base, project, "nope"); err == nil {
		t.Error("an unknown --account was accepted")
	}
}

func TestTakeAccount(t *testing.T) {
	label, rest, err := takeAccount([]string{"--resume", "x", "--account", "acme", "-p", "--", "--account", "lit"})
	if err != nil || label != "acme" || strings.Join(rest, " ") != "--resume x -p -- --account lit" {
		t.Errorf("label=%q rest=%v err=%v", label, rest, err)
	}
	if label, _, _ := takeAccount([]string{"--account=acme"}); label != "acme" {
		t.Errorf("--account= form: %q", label)
	}
	if _, _, err := takeAccount([]string{"--account"}); err == nil {
		t.Error("a bare --account was accepted")
	}
}

func TestShellInit(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := ShellInit([]string{"zsh"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "claude() {") || !strings.Contains(out.String(), `launch -- "$@"`) || !strings.Contains(out.String(), `command claude "$@"`) {
		t.Errorf("shell-init = %s", out.String())
	}
	if code := ShellInit([]string{"fish"}, &out, &errOut); code == 0 {
		t.Error("an unsupported shell was accepted")
	}
}
