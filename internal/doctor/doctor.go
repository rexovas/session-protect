package doctor

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/hook"
	"github.com/rexovas/session-protect/internal/schedule"
	"github.com/rexovas/session-protect/internal/targets"
)

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Warning string `json:"warning,omitempty"`
}

func Run(out io.Writer) int {
	checks := Checks()
	width := 16
	for _, check := range checks {
		width = max(width, len(check.Name))
	}
	for _, check := range checks {
		line := fmt.Sprintf("%-6s  %-*s", strings.ToUpper(check.Status), width, check.Name)
		if check.Detail != "" {
			line += "  " + check.Detail
		}
		if check.Warning != "" {
			line += "  " + check.Warning
		}
		fmt.Fprintln(out, line)
	}

	for _, check := range checks {
		if check.Status == "fail" {
			return 1
		}
	}
	return 0
}

func Checks() []Check {
	var checks []Check
	checks = append(checks, commandCheck("git", "git --version"))
	checks = append(checks, commandCheck("git-crypt", "git-crypt --version"))

	for _, target := range targets.DetectAll() {
		status := "ok"
		detail := target.Source
		warning := ""
		if !target.Detected {
			status = "warn"
			warning = "not found; target would be skipped unless configured"
		}
		checks = append(checks, Check{
			Name:    target.Name + " source",
			Status:  status,
			Detail:  detail,
			Warning: warning,
		})
	}
	checks = append(checks, claudeDirChecks()...)

	if runtime.GOOS == "darwin" {
		checks = append(checks, scheduleCheck(schedule.Inspect(), lastBackup(), time.Now()))
	}

	return checks
}

// staleAfter is how old the newest backup may get before doctor warns: a
// day plus slack for a machine that was asleep at the scheduled time.
const staleAfter = 26 * time.Hour

const reloadHint = "run: session-protect schedule reload"

// scheduleCheck reports whether the daily backup is actually running. An
// agent can look installed while launchd refuses every run, for example
// after its binary was replaced; the failed exit and the age of the newest
// backup are what show it.
func scheduleCheck(agent schedule.Agent, last time.Time, now time.Time) Check {
	check := Check{Name: "schedule", Status: "ok"}
	switch {
	case !agent.Installed:
		check.Status = "warn"
		check.Warning = "no daily backup scheduled (session-protect schedule install)"
		return check
	case !agent.Loaded:
		check.Status = "warn"
		check.Warning = "agent installed but not loaded; " + reloadHint
		return check
	}
	if !last.IsZero() {
		check.Detail = "last backup " + last.Format("2006-01-02 15:04")
	}
	var problems []string
	if agent.HasExit && agent.LastExit != 0 {
		problems = append(problems, fmt.Sprintf("last scheduled run failed (exit %d)", agent.LastExit))
	}
	if !last.IsZero() && now.Sub(last) > staleAfter {
		problems = append(problems, "no backup since "+last.Format("2006-01-02"))
	}
	if len(problems) > 0 {
		check.Status = "warn"
		check.Warning = strings.Join(problems, "; ") + "; " + reloadHint
	}
	return check
}

// lastBackup is the time of the newest commit across the backup repos, or
// zero when there is none.
func lastBackup() time.Time {
	cfg, err := config.Load()
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	names := []string{"all"}
	for _, target := range targets.DetectAll() {
		names = append(names, target.Name)
	}
	for _, name := range names {
		out, err := exec.Command("git", "-C", filepath.Join(cfg.BackupRoot, name), "log", "-1", "--format=%cI").Output()
		if err != nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(out))); err == nil && at.After(newest) {
			newest = at
		}
	}
	return newest
}

func commandCheck(name string, versionCommand string) Check {
	path, err := exec.LookPath(name)
	if err != nil {
		return Check{Name: name, Status: "fail", Warning: "not found in PATH"}
	}

	parts := strings.Fields(versionCommand)
	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.Output()
	detail := path
	if err == nil {
		detail += "  " + strings.TrimSpace(string(output))
	}

	return Check{Name: name, Status: "ok", Detail: detail}
}

// claudeDirChecks covers extra claude config dirs: each needs our hooks
// for its turn-end syncs, and a CLAUDE_CONFIG_DIR in use but unregistered
// is not protected at all.
func claudeDirChecks() []Check {
	var checks []Check
	for _, home := range targets.ClaudeHomes() {
		if home.IsDefault() || !dirExists(home.Dir) {
			continue
		}
		if !hook.InstalledIn(home.Dir) {
			checks = append(checks, Check{
				Name:    home.TargetName() + " hooks",
				Status:  "warn",
				Detail:  home.Dir,
				Warning: "no session-protect hooks; run: session-protect claude-dirs add " + home.Dir,
			})
		}
	}
	if active := targets.ActiveClaudeDir(); dirExists(active) {
		if _, known := targets.HomeForDir(active); !known {
			checks = append(checks, Check{
				Name:    "CLAUDE_CONFIG_DIR",
				Status:  "warn",
				Detail:  active,
				Warning: "not protected; run: session-protect claude-dirs add " + active,
			})
		}
	}
	return checks
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
