package schedule

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rexovas/session-protect/internal/binpath"
	"github.com/rexovas/session-protect/internal/config"
)

const label = "com.session-protect.backup"

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(stderr, "scheduling is currently implemented for macOS only")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "schedule failed: %v\n", err)
		return 1
	}

	switch args[0] {
	case "install":
		return install(cfg, stdout, stderr)
	case "reload":
		return reload(stdout, stderr)
	case "status":
		return status(stdout)
	case "uninstall":
		return uninstall(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown schedule command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect schedule install")
	fmt.Fprintln(out, "  session-protect schedule reload")
	fmt.Fprintln(out, "  session-protect schedule status")
	fmt.Fprintln(out, "  session-protect schedule uninstall")
}

func install(cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	binary, err := binpath.Current()
	if err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	hour, minute, err := cfg.Schedule.Clock()
	if err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "session-protect", "backup.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	path := plistPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(Plist(binary, hour, minute, logPath)), 0o600); err != nil {
		fmt.Fprintf(stderr, "schedule install failed: %v\n", err)
		return 1
	}

	if err := load(path); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintf(stdout, "Installed daily backup schedule\n")
	fmt.Fprintf(stdout, "  agent   %s\n", path)
	fmt.Fprintf(stdout, "  runs    daily at %02d:%02d\n", hour, minute)
	fmt.Fprintf(stdout, "  binary  %s\n", binary)
	fmt.Fprintf(stdout, "  log     %s\n", logPath)
	return 0
}

func reload(stdout io.Writer, stderr io.Writer) int {
	installed, err := Reload()
	if err != nil {
		fmt.Fprintf(stderr, "schedule reload failed: %v\n", err)
		return 1
	}
	if !installed {
		fmt.Fprintln(stdout, "schedule: not installed; nothing to reload")
		return 0
	}
	fmt.Fprintln(stdout, "Reloaded daily backup schedule")
	return 0
}

// Reload re-registers an installed backup agent with launchd. launchd
// records the identity of the binary a LaunchAgent runs when the agent is
// loaded. Once that binary is replaced (an update, a reinstall), macOS
// refuses to start the job: every run fails with EX_CONFIG before the binary
// executes, so nothing reaches the backup log. Reloading registers the new
// binary. The agent runs at load, so a reload also starts a backup.
//
// It reports whether an agent was installed; without one it does nothing.
func Reload() (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	path := plistPath(home)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := repairBrewBinary(path); err != nil {
		return true, err
	}
	return true, load(path)
}

// repairBrewBinary rewrites an agent written before installs pointed at
// Homebrew's stable link: one whose binary sits in a versioned Homebrew
// directory is switched to the link. It reports whether it rewrote the file;
// launchd keeps running the old definition until the agent is reloaded.
func repairBrewBinary(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	binary := plistBinary(string(data))
	if binary == "" {
		return false, nil
	}
	stable := binpath.Stable(binary)
	if stable == binary {
		return false, nil
	}
	fixed := strings.Replace(string(data), "<string>"+binary+"</string>", "<string>"+stable+"</string>", 1)
	return true, os.WriteFile(path, []byte(fixed), 0o600)
}

var programPattern = regexp.MustCompile(`<key>ProgramArguments</key>\s*<array>\s*<string>([^<]+)</string>`)

// plistBinary is the program an agent runs, or "" when it can't be read.
func plistBinary(plist string) string {
	match := programPattern.FindStringSubmatch(plist)
	if match == nil {
		return ""
	}
	return match[1]
}

// load (re)registers the agent at path. bootout is asynchronous, so a
// bootstrap right after it can fail while the old registration is still
// going away; retry briefly.
func load(path string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, path).Run() // reload cleanly if present
	var err error
	var out []byte
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		out, err = exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput()
		if err == nil {
			recordLoaded(path)
			return nil
		}
	}
	return fmt.Errorf("launchctl bootstrap failed: %v: %s", err, strings.TrimSpace(string(out)))
}

// ReloadIfStale reloads the agent when its binary changed since the agent
// was last loaded by this tool: an upgrade that went around
// `session-protect update`, like `brew upgrade`, which cannot reload it from
// Homebrew's sandbox. It is cheap when nothing changed (a file read and a
// stat), so frequent commands can call it. An agent loaded before this check
// existed has no record and is reloaded once, and so is one whose agent file
// had to be repointed at Homebrew's stable link, or one launchd refuses to
// start (see below). It reports whether it reloaded.
func ReloadIfStale() (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	path := plistPath(home)
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	repaired, err := repairBrewBinary(path)
	if err != nil {
		return false, err
	}
	if repaired {
		return Reload()
	}
	current := identity(path)
	if current == "" {
		return false, nil // binary missing: reloading cannot help
	}
	stamp := stampPath(home)
	recorded, err := os.ReadFile(stamp)
	if err != nil || string(recorded) != current {
		return Reload()
	}
	// Same binary, but launchd refuses to start it. Seen after a macOS
	// update re-evaluated an agent loaded under the previous release, with
	// the binary itself unchanged; a reload clears it. At most once per
	// refusalRetry, so an agent that keeps failing does not reload on every
	// command.
	if agent := Inspect(); agent.HasExit && agent.LastExit == exConfig {
		if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) > refusalRetry {
			return Reload()
		}
	}
	return false, nil
}

// exConfig is the status launchd records when it refuses to start an
// agent's program (sysexits EX_CONFIG). session-protect never exits with it.
const exConfig = 78

const refusalRetry = time.Hour

// identity describes the binary an agent runs, as it is on disk now: its
// resolved path, size and modification time. Empty when unreadable.
func identity(plist string) string {
	data, err := os.ReadFile(plist)
	if err != nil {
		return ""
	}
	binary := plistBinary(string(data))
	if binary == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s\n%d\n%d\n", resolved, info.Size(), info.ModTime().UnixNano())
}

// recordLoaded notes which binary the agent was loaded with, for
// ReloadIfStale. Best effort: without the record the next check reloads.
func recordLoaded(plist string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	id := identity(plist)
	if id == "" {
		return
	}
	stamp := stampPath(home)
	if err := os.MkdirAll(filepath.Dir(stamp), 0o700); err == nil {
		_ = os.WriteFile(stamp, []byte(id), 0o600)
	}
}

func stampPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "SessionProtect", "schedule-binary")
}

// Agent is the backup agent as launchd sees it.
type Agent struct {
	Installed bool // the agent file exists
	Loaded    bool // launchd has it registered
	LastExit  int  // exit status of the last run, when HasExit
	HasExit   bool
}

var lastExitPattern = regexp.MustCompile(`(?m)^\s*last exit code = (-?\d+)`)

// Inspect reports the backup agent's state. Off macOS it reports nothing
// installed.
func Inspect() Agent {
	var agent Agent
	if runtime.GOOS != "darwin" {
		return agent
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return agent
	}
	if _, err := os.Stat(plistPath(home)); err != nil {
		return agent
	}
	agent.Installed = true
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Output()
	if err != nil {
		return agent
	}
	agent.Loaded = true
	agent.LastExit, agent.HasExit = parseLastExit(string(out))
	return agent
}

func parseLastExit(printed string) (int, bool) {
	match := lastExitPattern.FindStringSubmatch(printed)
	if match == nil {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return code, true
}

func status(stdout io.Writer) int {
	home, _ := os.UserHomeDir()
	path := plistPath(home)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintln(stdout, "schedule: not installed")
		return 1
	}
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if err := exec.Command("launchctl", "print", domain).Run(); err != nil {
		fmt.Fprintf(stdout, "schedule: agent file present but not loaded (%s)\n", path)
		return 1
	}
	fmt.Fprintf(stdout, "schedule: installed and loaded (%s)\n", path)
	return 0
}

func uninstall(stdout io.Writer, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	path := plistPath(home)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "schedule uninstall failed: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Removed backup schedule")
	return 0
}

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// Plist renders the LaunchAgent definition.
func Plist(binary string, hour int, minute int, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>backup</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>%d</integer>
		<key>Minute</key>
		<integer>%d</integer>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, binary, hour, minute, logPath, logPath)
}
