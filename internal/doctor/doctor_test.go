package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/schedule"
	"github.com/rexovas/session-protect/internal/targets"
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

func TestScheduleCheck(t *testing.T) {
	now := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	recent := now.Add(-3 * time.Hour)
	for _, tc := range []struct {
		name   string
		agent  schedule.Agent
		last   time.Time
		status string
		warn   string
	}{
		{"not installed", schedule.Agent{}, recent, "warn", "no daily backup scheduled"},
		{"not loaded", schedule.Agent{Installed: true}, recent, "warn", "not loaded"},
		{"healthy", schedule.Agent{Installed: true, Loaded: true, HasExit: true}, recent, "ok", ""},
		{"never ran", schedule.Agent{Installed: true, Loaded: true}, recent, "ok", ""},
		{"refused by launchd", schedule.Agent{Installed: true, Loaded: true, HasExit: true, LastExit: 78}, now.Add(-43 * 24 * time.Hour), "warn", "last scheduled run failed (exit 78); no backup since 2026-09-18"},
		{"stale only", schedule.Agent{Installed: true, Loaded: true, HasExit: true}, now.Add(-30 * time.Hour), "warn", "no backup since 2026-10-30"},
	} {
		got := scheduleCheck(tc.agent, tc.last, now)
		if got.Status != tc.status || !strings.Contains(got.Warning, tc.warn) {
			t.Errorf("%s: got %s %q; want %s containing %q", tc.name, got.Status, got.Warning, tc.status, tc.warn)
		}
	}
}

// A codex home whose background server doesn't match the CLI is flagged
// with the command to fix it; a home with no server is skipped.
func TestCodexDaemonChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SESSION_PROTECT_CONFIG", filepath.Join(home, "cfg", "config.toml"))
	t.Setenv("CODEX_HOME", "")
	acme := filepath.Join(home, "acme-codex")
	for _, dir := range []string{filepath.Join(home, ".codex"), acme} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := targets.RegisterDir(targets.AgentCodex, acme, "acme"); err != nil {
		t.Fatal(err)
	}
	installed, version := codexInstalled, codexDaemonVersion
	t.Cleanup(func() { codexInstalled, codexDaemonVersion = installed, version })
	codexInstalled = func() bool { return true }
	codexDaemonVersion = func(dir string) ([]byte, error) {
		if dir == acme {
			return []byte(`{"status":"running","cliVersion":"0.172.0","appServerVersion":"0.171.0"}`), nil
		}
		return nil, errors.New("no server")
	}
	checks := codexDaemonChecks()
	if len(checks) != 1 || checks[0].Status != "warn" || !strings.Contains(checks[0].Warning, "CODEX_HOME="+acme+" codex app-server daemon update --from-cli -y") {
		t.Fatalf("checks = %+v", checks)
	}
}
