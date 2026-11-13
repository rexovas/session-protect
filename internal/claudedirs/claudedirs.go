// Package claudedirs is the claude-dirs command: listing, adding and
// removing the claude config dirs (homes) session-protect protects.
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

func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "list" {
		return list(stdout)
	}
	switch args[0] {
	case "add":
		return add(args[1:], stdout, stderr)
	case "remove", "rm":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: session-protect claude-dirs remove LABEL|PATH")
			return 2
		}
		home, err := targets.UnregisterClaudeDir(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "claude-dirs remove: %v\n", err)
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
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown claude-dirs command: %s\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  session-protect claude-dirs                       list protected claude config dirs")
	fmt.Fprintln(out, "  session-protect claude-dirs add PATH [--label L] [--no-hooks]")
	fmt.Fprintln(out, "  session-protect claude-dirs remove LABEL|PATH")
	fmt.Fprintln(out, "  session-protect claude-dirs which [DIR]            the account that owns a folder")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Claude keeps its login, settings and sessions in its config dir:")
	fmt.Fprintln(out, "~/.claude, or whatever CLAUDE_CONFIG_DIR points at. Each extra dir is")
	fmt.Fprintln(out, "backed up, browsed and resumed as its own account. A dir whose settings")
	fmt.Fprintln(out, "carry the session-protect hooks registers itself on its first session;")
	fmt.Fprintln(out, "add registers one now and installs the hooks. The label (derived from")
	fmt.Fprintln(out, "the dir name unless given) names the dir's backup folder and is fixed.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Folders are owned by the dir their sessions live in, learned, never set:")
	fmt.Fprintln(out, "a folder with no sessions of its own inherits from the nearest owned parent")
	fmt.Fprintln(out, "whose subtree holds no other account's work. Unowned folders are default's.")
}

func add(args []string, stdout io.Writer, stderr io.Writer) int {
	var path, label string
	hooks := true
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
		fmt.Fprintln(stderr, "usage: session-protect claude-dirs add PATH [--label L] [--no-hooks]")
		return 2
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "claude-dirs add: %s is not a directory\n", path)
		return 1
	}
	home, changed, err := targets.RegisterClaudeDir(path, label)
	if err != nil {
		fmt.Fprintf(stderr, "claude-dirs add: %v\n", err)
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
	return 0
}

func list(stdout io.Writer) int {
	homes := targets.ClaudeHomes()
	width := len("LABEL")
	for _, home := range homes {
		width = max(width, len(home.Label))
	}
	fmt.Fprintf(stdout, "%-*s  %-8s  %-6s  %s\n", width, "LABEL", "SESSIONS", "HOOKS", "DIR")
	for _, home := range homes {
		hooks := "yes"
		if !hook.InstalledIn(home.Dir) {
			hooks = "no"
		}
		dir := tilde(home.Dir)
		if _, err := os.Stat(home.Dir); err != nil {
			dir += "  (missing; backups kept)"
		}
		fmt.Fprintf(stdout, "%-*s  %8d  %-6s  %s\n", width, home.Label, countSessions(home.Dir), hooks, dir)
	}
	if active := targets.ActiveClaudeDir(); active != "" {
		if _, known := targets.HomeForDir(active); !known {
			fmt.Fprintf(stdout, "\nCLAUDE_CONFIG_DIR=%s is not protected yet: session-protect claude-dirs add %s\n", tilde(active), tilde(active))
		}
	}
	return 0
}

func countSessions(dir string) int {
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

func tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

func which(dir string, stdout io.Writer, stderr io.Writer) int {
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(stderr, "claude-dirs which: %v\n", err)
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
