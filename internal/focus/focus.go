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

// Session raises the window of the terminal hosting pid.
func Session(pid int) error {
	if runtime.GOOS != "darwin" {
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
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	tty := strings.TrimSpace(string(out))
	if tty == "" || tty == "??" || tty == "?" {
		return ""
	}
	return "/dev/" + tty
}

// guiAncestor walks parents until the process launched by launchd (pid 1),
// which is the GUI application on macOS.
func guiAncestor(pid int) int {
	for range [12]int{} {
		out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0
		}
		parent, err := strconv.Atoi(strings.TrimSpace(string(out)))
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
	cmd := exec.Command("osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		detail := strings.TrimSpace(stderr.String())
		if strings.Contains(detail, "-1743") || strings.Contains(detail, "Not authorized") {
			return ErrNotAuthorized
		}
		if detail != "" {
			return fmt.Errorf("%s", detail)
		}
		return err
	case <-time.After(15 * time.Second):
		// Long timeout: the first run can sit behind the macOS
		// Automation permission dialog.
		_ = cmd.Process.Kill()
		return fmt.Errorf("timed out talking to the terminal")
	}
}

// OpenAutomationSettings opens the Privacy & Security → Automation pane,
// where a cached denial can be flipped.
func OpenAutomationSettings() {
	_ = exec.Command("open",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_Automation").Start()
}
