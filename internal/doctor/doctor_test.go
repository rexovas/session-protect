package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChecksAndRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("backup_root = \""+filepath.Join(home, "root")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	checks := Checks()
	if len(checks) == 0 {
		t.Fatal("no checks")
	}
	sawGit := false
	for _, check := range checks {
		if strings.Contains(check.Name, "git") {
			sawGit = true
		}
	}
	if !sawGit {
		t.Fatal("git check missing")
	}

	var out strings.Builder
	// Exit code depends on optional binaries (git-crypt); the report
	// itself must always render.
	Run(&out)
	if !strings.Contains(out.String(), "git") {
		t.Fatalf("doctor output missing checks: %s", out.String())
	}
}
