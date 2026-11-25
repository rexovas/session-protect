package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/assist"
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
	if check, ok := semanticCheck(time.Now()); ok {
		checks = append(checks, check)
	}
	checks = append(checks, codexDaemonChecks()...)

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

// semanticCheck reports on the opt-in semantic index, when one was built:
// AI find can only use it while Ollama answers, and the daily backup keeps
// it current only then too.
func semanticCheck(now time.Time) (Check, bool) {
	cfg, err := config.Load()
	if err != nil {
		return Check{}, false
	}
	info, err := os.Stat(filepath.Join(cfg.BackupRoot, ".session-vec", "passages.bin"))
	if err != nil {
		if legacy, lerr := os.Stat(filepath.Join(cfg.BackupRoot, ".session-vec", "index.bin")); lerr == nil {
			return Check{Name: "semantic index", Status: "warn", Detail: "updated " + legacy.ModTime().Format("2006-01-02"),
				Warning: "earlier format (one vector per session); rebuild by passage: session-protect index"}, true
		}
		return Check{}, false // never built: semantic search is opt-in
	}
	check := Check{Name: "semantic index", Status: "ok", Detail: "updated " + info.ModTime().Format("2006-01-02")}
	switch {
	case !assist.EmbedderReachable(cfg.Assist):
		check.Status = "warn"
		check.Warning = "Ollama isn't running, so AI find falls back to keywords and the index isn't refreshed; run: brew services start ollama"
	case now.Sub(info.ModTime()) > 7*24*time.Hour:
		check.Status = "warn"
		check.Warning = "not refreshed in over a week; run: session-protect index"
	}
	return check, true
}

// codexInstalled is seamed for tests, like codexDaemonVersion.
var codexInstalled = func() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// codexDaemonVersion asks codex for a home's background server version;
// seamed so tests never run codex.
var codexDaemonVersion = func(home string) ([]byte, error) {
	cmd := exec.Command("codex", "app-server", "daemon", "version")
	cmd.Env = append(withoutEnv(os.Environ(), "CODEX_HOME"), "CODEX_HOME="+home)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
		return out, err
	case <-time.After(10 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("timed out")
	}
}

func withoutEnv(environ []string, key string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return out
}

// codexDaemonChecks warns about codex homes whose background server is
// older (or newer) than the codex CLI. Each home runs its own server, and
// when the CLI updates (e.g. by Homebrew) a server left behind breaks
// things in that home's sessions, such as network approvals. Homes that
// never started a server, or no codex at all, are skipped.
func codexDaemonChecks() []Check {
	if !codexInstalled() {
		return nil
	}
	var checks []Check
	for _, home := range targets.CodexHomes() {
		if !dirExists(home.Dir) {
			continue
		}
		out, err := codexDaemonVersion(home.Dir)
		if err != nil {
			continue // no server in this home yet
		}
		var version struct {
			Status           string `json:"status"`
			CLIVersion       string `json:"cliVersion"`
			AppServerVersion string `json:"appServerVersion"`
			ManagedVersion   string `json:"managedCodexVersion"`
		}
		if json.Unmarshal(out, &version) != nil || version.CLIVersion == "" {
			continue
		}
		server := version.AppServerVersion
		if server == "" {
			server = version.ManagedVersion
		}
		check := Check{Name: home.TargetName() + " server", Status: "ok", Detail: "codex " + server}
		if server != "" && server != version.CLIVersion {
			command := "codex app-server daemon update --from-cli -y"
			if !home.IsDefault() || os.Getenv("CODEX_HOME") != "" {
				command = "CODEX_HOME=" + home.Dir + " " + command
			}
			check.Status = "warn"
			check.Warning = fmt.Sprintf("server %s doesn't match the codex CLI %s, which can break network approvals in this home's sessions; run: %s (restarts it)",
				server, version.CLIVersion, command)
		}
		checks = append(checks, check)
	}
	return checks
}
