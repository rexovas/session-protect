package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/schedule"
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
