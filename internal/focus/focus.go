// Package focus brings the terminal window hosting a live session to the
// foreground — without AppleScript or any automation permission. We know
// the session's tty, and terminals interpret escape sequences arriving on
// it: iTerm2's StealFocus escape focuses the exact window/tab/session
// owning the tty, and the standard XTWINOPS raise covers xterm-compatible
// terminals on macOS and Linux alike. Writing to your own tty devices
// needs no special rights, and terminals that recognize neither sequence
// simply ignore the invisible bytes.
package focus

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// focusSequence is what we write to the session's tty:
// OSC 1337 StealFocus (iTerm2), then XTWINOPS de-iconify + raise.
const focusSequence = "\x1b]1337;StealFocus\x07\x1b[1t\x1b[5t"

// Session raises the terminal window hosting pid by writing focus escapes
// to its controlling tty.
func Session(pid int) error {
	tty := ttyOf(pid)
	if tty == "" {
		return fmt.Errorf("pid %d has no controlling terminal", pid)
	}
	file, err := os.OpenFile(tty, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", tty, err)
	}
	defer file.Close()
	if _, err := file.WriteString(focusSequence); err != nil {
		return fmt.Errorf("write to %s: %w", tty, err)
	}
	return nil
}

// ttyOf returns the controlling terminal device of a process, e.g.
// /dev/ttys032 on macOS or /dev/pts/3 on Linux.
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
