// Package claudedirs is the claude-dirs and codex-dirs commands: listing,
// adding and removing the agent homes (accounts) session-protect protects.
package claudedirs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rexovas/session-protect/internal/hook"
	"github.com/rexovas/session-protect/internal/sessionfile"
	"github.com/rexovas/session-protect/internal/targets"
)

// Run is `claude-dirs` (agent claude) or `codex-dirs` (agent codex).
func Run(agent string, args []string, stdout io.Writer, stderr io.Writer) int {
	command := agent + "-dirs"
	if len(args) == 0 || args[0] == "list" {
		return list(agent, stdout)
	}
	switch args[0] {
	case "add":
		return add(agent, args[1:], stdout, stderr)
	case "remove", "rm":
		if len(args) != 2 {
			fmt.Fprintf(stderr, "usage: session-protect %s remove LABEL|PATH\n", command)
			return 2
		}
		home, err := targets.UnregisterDir(agent, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "%s remove: %v\n", command, err)
			return 1
		}
		fmt.Fprintf(stdout, "No longer protecting %s (%s).\n", home.Label, tilde(home.Dir))
		fmt.Fprintln(stdout, "Its files, hooks and existing backups were left alone.")
		return 0
	case "which":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		return which(dir, stdout, stderr)
	case "help", "-h", "--help":
		usage(agent, stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown %s command: %s\n\n", command, args[0])
		usage(agent, stderr)
		return 2
	}
}

func usage(agent string, out io.Writer) {
	command := agent + "-dirs"
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintf(out, "  session-protect %s                       list protected %s dirs\n", command, agent)
	if agent == targets.AgentClaude {
		fmt.Fprintf(out, "  session-protect %s add PATH [--label L] [--no-hooks]\n", command)
	} else {
		fmt.Fprintf(out, "  session-protect %s add PATH [--label L]\n", command)
	}
	fmt.Fprintf(out, "  session-protect %s remove LABEL|PATH\n", command)
	fmt.Fprintf(out, "  session-protect %s which [DIR]            the account that owns a folder\n", command)
	fmt.Fprintln(out)
	if agent == targets.AgentClaude {
		fmt.Fprintln(out, "Claude keeps its login, settings and sessions in its config dir:")
		fmt.Fprintln(out, "~/.claude, or whatever CLAUDE_CONFIG_DIR points at. A dir whose settings")
		fmt.Fprintln(out, "carry the session-protect hooks registers itself on its first session;")
		fmt.Fprintln(out, "add registers one now and installs the hooks.")
	} else {
		fmt.Fprintln(out, "Codex keeps its login, settings and sessions in its home: ~/.codex, or")
		fmt.Fprintln(out, "whatever CODEX_HOME points at. add registers one; the directory must exist.")
	}
	fmt.Fprintln(out, "Each extra dir is backed up, browsed and resumed as its own account. The")
	fmt.Fprintln(out, "label (derived from the dir name unless given) names the account and the")
	fmt.Fprintln(out, "dir's backup folder, and is fixed. A claude dir and a codex home with the")
	fmt.Fprintln(out, "same label are the same account.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Folders are owned by the account whose sessions they hold, learned, never")
	fmt.Fprintln(out, "set: a folder with no sessions of its own inherits from the nearest owned")
	fmt.Fprintln(out, "parent whose subtree holds no other account's work. Unowned folders are")
	fmt.Fprintln(out, "default's.")
}

func add(agent string, args []string, stdout io.Writer, stderr io.Writer) int {
	command := agent + "-dirs"
	var path, label string
	hooks := agent == targets.AgentClaude
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--label":
			i++
			if i == len(args) {
				fmt.Fprintln(stderr, "--label requires a value")
				return 2
			}
			label = args[i]
		case "--no-hooks":
			hooks = false
		default:
			if path != "" || strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(stderr, "unexpected argument: %s\n", args[i])
				return 2
			}
			path = args[i]
		}
	}
	if path == "" {
		fmt.Fprintf(stderr, "usage: session-protect %s add PATH [--label L]\n", command)
		return 2
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "%s add: %s is not a directory\n", command, path)
		return 1
	}
	home, changed, err := targets.RegisterDir(agent, path, label)
	if err != nil {
		fmt.Fprintf(stderr, "%s add: %v\n", command, err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "Protecting %s as %q (backups under %s).\n", tilde(home.Dir), home.Label, home.TargetName())
	} else {
		fmt.Fprintf(stdout, "%s is already protected as %q.\n", tilde(home.Dir), home.Label)
	}
	if hooks {
		return hook.InstallInto(home.Dir, stdout, stderr)
	}
	if agent == targets.AgentCodex {
		fmt.Fprintf(stdout, "Log it in with: CODEX_HOME=%s codex login\n", tilde(home.Dir))
	}
	return 0
}

func list(agent string, stdout io.Writer) int {
	homes := targets.HomesOf(agent)
	width := len("LABEL")
	for _, home := range homes {
		width = max(width, len(home.Label))
	}
	claude := agent == targets.AgentClaude
	if claude {
		fmt.Fprintf(stdout, "%-*s  %-8s  %-6s  %s\n", width, "LABEL", "SESSIONS", "HOOKS", "DIR")
	} else {
		fmt.Fprintf(stdout, "%-*s  %-8s  %s\n", width, "LABEL", "SESSIONS", "DIR")
	}
	for _, home := range homes {
		dir := tilde(home.Dir)
		if _, err := os.Stat(home.Dir); err != nil {
			dir += "  (missing; backups kept)"
		}
		if claude {
			hooks := "yes"
			if !hook.InstalledIn(home.Dir) {
				hooks = "no"
			}
			fmt.Fprintf(stdout, "%-*s  %8d  %-6s  %s\n", width, home.Label, countClaude(home.Dir), hooks, dir)
		} else {
			fmt.Fprintf(stdout, "%-*s  %8d  %s\n", width, home.Label, countCodex(home), dir)
		}
	}
	if active := targets.ActiveDir(agent); active != "" {
		if _, known := targets.HomeForDirOf(agent, active); !known {
			fmt.Fprintf(stdout, "\n%s=%s is not protected yet: session-protect %s-dirs add %s\n",
				targets.EnvVarFor(agent), tilde(active), agent, tilde(active))
		}
	}
	return 0
}

func countClaude(dir string) int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(dir, "projects"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() && entry.Name() == "subagents" {
			return filepath.SkipDir
		}
		if !entry.IsDir() && sessionfile.IsTranscript(entry.Name()) {
			n++
		}
		return nil
	})
	return n
}

// countCodex counts a codex home's top-level sessions (not subagents).
func countCodex(home targets.Home) int {
	ids := map[string]bool{}
	for _, entry := range targets.CodexRollouts(home) {
		if !entry.Subagent {
			ids[entry.ID] = true // plain + compressed copies count once
		}
	}
	return len(ids)
}

func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

func which(dir string, stdout io.Writer, stderr io.Writer) int {
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "which: %v\n", err)
		return 1
	}
	owner := targets.FolderOwner(abs)
	switch {
	case len(owner.Conflict) > 0:
		fmt.Fprintf(stdout, "conflict: %s all have sessions in %s\n", strings.Join(owner.Conflict, ", "), tilde(owner.ClaimedAt))
		return 1
	case owner.Inherited(abs):
		fmt.Fprintf(stdout, "%s  (inherited from %s)\n", owner.Owner.Label, tilde(owner.ClaimedAt))
	case owner.Owned():
		fmt.Fprintf(stdout, "%s  (has its sessions)\n", owner.Owner.Label)
	default:
		line := owner.Owner.Label + "  (unowned)"
		if owner.BroadClaim != "" {
			line += fmt.Sprintf("; %s has %s sessions but also other accounts' work, so it passes nothing down", tilde(owner.BroadClaim), owner.BroadHome)
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}
