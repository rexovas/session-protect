// Package guard detects a session being reopened while it is already live in
// another process, using the agent's session registry. The registry format is
// not a documented API, so parsing is defensive and failures are silent — the
// guard must never break session startup.
package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rexovas/session-protect/internal/targets"
)

// Info is one live agent process from the session registry.
type Info struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Run implements the SessionStart hook: read the hook payload, look for
// another live process holding the same session, and if found emit a warning
// the agent shows to the user, plus start a detached safety backup so the
// pre-divergence state is committed.
func Run(stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	var input struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(stdin).Decode(&input)
	if input.SessionID == "" {
		return 0
	}

	conflicts := Conflicts(RegistryDir(), input.SessionID, os.Getppid())
	if len(conflicts) == 0 {
		return 0
	}

	conflict := conflicts[0]
	message := fmt.Sprintf(
		"⚠ session-protect: this session is ALREADY OPEN in another process (pid %d, status %s, last active %s). "+
			"Working in both places will interleave and can diverge the transcript (claude-code#69364). "+
			"A safety backup commit was started; consider closing one side.",
		conflict.PID, conflict.Status, sinceMillis(conflict.UpdatedAt))
	_ = json.NewEncoder(stdout).Encode(map[string]string{"systemMessage": message})

	if binary, err := os.Executable(); err == nil {
		command := exec.Command(binary, "backup")
		command.Stdout, command.Stderr = nil, nil
		_ = command.Start() // detached; never block session startup
	}
	return 0
}

// RegistryDir is where the agent records one JSON file per running process.
func RegistryDir() string {
	return filepath.Join(targets.DetectClaude().Source, "sessions")
}

// Conflicts returns live registry entries holding sessionID, excluding the
// process that invoked us (its parent is the agent that just started).
func Conflicts(dir string, sessionID string, excludePID int) []Info {
	var conflicts []Info
	for _, info := range Live(dir) {
		if info.SessionID == sessionID && info.PID != excludePID {
			conflicts = append(conflicts, info)
		}
	}
	return conflicts
}

// Live returns registry entries whose process is still running, keyed by
// session id. Dead processes leave stale files behind; a signal-0 probe
// filters them.
func Live(dir string) map[string]Info {
	live := map[string]Info{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return live
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var info Info
		if json.Unmarshal(data, &info) != nil || info.PID <= 0 || info.SessionID == "" {
			continue
		}
		if !pidAlive(info.PID) {
			continue
		}
		if existing, ok := live[info.SessionID]; !ok || info.UpdatedAt > existing.UpdatedAt {
			live[info.SessionID] = info
		}
	}
	return live
}

func pidAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func sinceMillis(ms int64) string {
	if ms <= 0 {
		return "unknown"
	}
	since := time.Since(time.UnixMilli(ms))
	switch {
	case since < time.Minute:
		return "just now"
	case since < time.Hour:
		return fmt.Sprintf("%dm ago", int(since.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(since.Hours()))
	}
}
