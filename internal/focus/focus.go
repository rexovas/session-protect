// Package focus brings the terminal window hosting a live session to the
// foreground. On macOS one mechanism covers every terminal: AppleScript.
// iTerm2 and Terminal.app are scriptable down to the exact window/tab
// owning a tty, and anything else gets raised app-level through System
// Events. The first use prompts for macOS Automation permission, once
// per hosting app.
package focus

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrNotAuthorized means macOS has a stored Automation denial for the
// hosting app; the system never re-prompts, so the user must flip the
// toggle in Privacy & Security → Automation.
var ErrNotAuthorized = errors.New("macOS automation permission denied")

// osName is injectable so platform gating is testable everywhere.
var osName = runtime.GOOS

// execCommand is the single subprocess seam: everything this package asks
// of the system goes through it, and tests replace it with an in-process
// mock. The default runs the real binary with a timeout.
var execCommand = defaultExec

func defaultExec(timeout time.Duration, name string, args ...string) (stdout string, stderr string, err error) {
	cmd := exec.Command(name, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return outBuf.String(), errBuf.String(), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return outBuf.String(), errBuf.String(), fmt.Errorf("%s timed out", name)
	}
}

// Session raises the window of the terminal hosting pid.
func Session(pid int) error {
	if osName != "darwin" {
		return fmt.Errorf("jump-to-session is macOS-only for now")
	}
	if tty := ttyOf(pid); tty != "" {
		err := runScript(iterm2Script(tty))
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNotAuthorized) {
			return err
		}
		err = runScript(terminalScript(tty))
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNotAuthorized) {
			return err
		}
	}
	// Unscriptable terminal: raise the hosting application itself.
	if app := guiAncestor(pid); app > 0 {
		return runScript(frontmostScript(app))
	}
	return fmt.Errorf("could not locate a terminal window for pid %d", pid)
}

// ttyOf returns the controlling terminal device, e.g. /dev/ttys032.
func ttyOf(pid int) string {
	out, _, err := execCommand(3*time.Second, "ps", "-o", "tty=", "-p", strconv.Itoa(pid))
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(out)
	if tty == "" || tty == "??" || tty == "?" {
		return ""
	}
	return "/dev/" + tty
}

// guiAncestor walks parents until the process launched by launchd (pid 1),
// which is the GUI application on macOS.
func guiAncestor(pid int) int {
	for range [12]int{} {
		out, _, err := execCommand(3*time.Second, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid))
		if err != nil {
			return 0
		}
		parent, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil || parent <= 0 {
			return 0
		}
		if parent == 1 {
			return pid
		}
		pid = parent
	}
	return 0
}

// iterm2Script selects the iTerm2 session owning tty, erroring when absent
// so the caller can try the next terminal.
func iterm2Script(tty string) string {
	return `tell application "iTerm2"
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if tty of s is "` + tty + `" then
					select w
					select t
					select s
					activate
					return
				end if
			end repeat
		end repeat
	end repeat
end tell
error "tty not found"`
}

// terminalScript selects the Terminal.app tab owning tty.
func terminalScript(tty string) string {
	return `tell application "Terminal"
	repeat with w in windows
		repeat with t in tabs of w
			if tty of t is "` + tty + `" then
				set selected of t to true
				set index of w to 1
				activate
				return
			end if
		end repeat
	end repeat
end tell
error "tty not found"`
}

// frontmostScript raises an application by its unix pid.
func frontmostScript(pid int) string {
	return `tell application "System Events"
	set frontmost of (first process whose unix id is ` + strconv.Itoa(pid) + `) to true
end tell`
}

func runScript(script string) error {
	// Long timeout: the first run can sit behind the macOS Automation
	// permission dialog.
	_, stderr, err := execCommand(15*time.Second, "osascript", "-e", script)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(stderr)
	if strings.Contains(detail, "-1743") || strings.Contains(detail, "Not authorized") {
		return ErrNotAuthorized
	}
	if detail != "" {
		return fmt.Errorf("%s", detail)
	}
	return err
}

// SpawnInNewWindow opens a new window in the terminal app hosting this
// process and runs command there. iTerm2 when that is the host, else
// Terminal.app — always present on macOS, so sp inside any other
// terminal still gets a working spawn.
func SpawnInNewWindow(command string) error {
	if osName != "darwin" {
		return fmt.Errorf("resume-in-new-window is macOS-only for now")
	}
	if hostIsITerm() {
		return runScript(iterm2SpawnScript(command))
	}
	return runScript(terminalSpawnScript(command))
}

// hostIsITerm reports whether this process runs inside iTerm2.
func hostIsITerm() bool {
	if strings.Contains(os.Getenv("TERM_PROGRAM"), "iTerm") {
		return true
	}
	app := guiAncestor(os.Getpid())
	if app <= 0 {
		return false
	}
	out, _, err := execCommand(3*time.Second, "ps", "-o", "comm=", "-p", strconv.Itoa(app))
	return err == nil && strings.Contains(out, "iTerm")
}

func iterm2SpawnScript(command string) string {
	return `tell application "iTerm2"
	set w to (create window with default profile)
	tell current session of w
		write text "` + appleScriptQuote(command) + `"
	end tell
	activate
end tell`
}

func terminalSpawnScript(command string) string {
	return `tell application "Terminal"
	do script "` + appleScriptQuote(command) + `"
	activate
end tell`
}

// appleScriptQuote escapes a string for embedding in an AppleScript
// double-quoted literal.
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// ShellQuote single-quotes a value for the shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// OpenAutomationSettings opens the Privacy & Security → Automation pane,
// where a cached denial can be flipped.
func OpenAutomationSettings() {
	_, _, _ = execCommand(3*time.Second, "open",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_Automation")
}
