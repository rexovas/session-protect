// Package launch starts the claude agent on the account (claude home) that
// owns the current folder, for the shell function `shell-init` defines.
package launch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/rexovas/session-protect/internal/binpath"
	"github.com/rexovas/session-protect/internal/targets"
)

// accountFlag picks the home explicitly; it is ours and never reaches the
// agent. A first session started with it is what teaches a new folder its
// owner.
const accountFlag = "--account"

// Run resolves the home and replaces this process with the agent. args are
// the agent's own arguments (after "--").
func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	label, rest, err := takeAccount(args)
	if err != nil {
		fmt.Fprintf(stderr, "session-protect: %v\n", err)
		return 2
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	env, note, err := Environment(os.Environ(), dir, label)
	if err != nil {
		fmt.Fprintf(stderr, "session-protect: %v\n", err)
		return 2
	}
	if note != "" {
		fmt.Fprintln(stderr, "session-protect: "+note)
	}
	agent, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(stderr, "session-protect: claude is not on PATH")
		return 127
	}
	return run(agent, append([]string{"claude"}, rest...), env, stderr)
}

// Environment is the agent's environment for a launch in dir: an explicit
// --account wins, then an explicit CLAUDE_CONFIG_DIR, then the folder's
// owner. The default home runs with CLAUDE_CONFIG_DIR removed: the agent
// keys its stored login on whether the variable is set at all, so setting
// it to ~/.claude would ask for a different login.
func Environment(environ []string, dir string, label string) ([]string, string, error) {
	var home targets.ClaudeHome
	note := ""
	switch {
	case label != "":
		chosen, ok := targets.HomeByLabel(label)
		if !ok {
			return nil, "", fmt.Errorf("no claude dir labelled %q (see `session-protect claude-dirs`)", label)
		}
		home = chosen
	case lookup(environ, "CLAUDE_CONFIG_DIR") != "":
		return environ, "", nil
	default:
		owner := targets.FolderOwner(dir)
		if len(owner.Conflict) > 0 {
			return nil, "", fmt.Errorf("this folder has sessions in several claude dirs (%s); pick one with %s LABEL",
				strings.Join(owner.Conflict, ", "), accountFlag)
		}
		if owner.BroadClaim != "" {
			note = fmt.Sprintf("%s has %s sessions but also other accounts' work, so it isn't treated as %s's; using default (%s LABEL to choose)",
				owner.BroadClaim, owner.BroadHome, owner.BroadHome, accountFlag)
		}
		home = owner.Owner
	}
	out := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			out = append(out, entry)
		}
	}
	if !home.IsDefault() {
		out = append(out, home.Env())
	}
	return out, note, nil
}

// takeAccount removes our --account flag from the agent's arguments.
func takeAccount(args []string) (string, []string, error) {
	var rest []string
	label := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return label, append(rest, args[i:]...), nil
		case arg == accountFlag:
			if i+1 == len(args) {
				return "", nil, fmt.Errorf("%s requires a label", accountFlag)
			}
			label = args[i+1]
			i++
		case strings.HasPrefix(arg, accountFlag+"="):
			label = strings.TrimPrefix(arg, accountFlag+"=")
		default:
			rest = append(rest, arg)
		}
	}
	return label, rest, nil
}

func lookup(environ []string, key string) string {
	for _, entry := range environ {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

// ShellInit prints the shell function that routes `claude` through us. It
// falls back to the plain agent when this binary is gone, which is never
// worse than having no function at all.
func ShellInit(args []string, stdout io.Writer, stderr io.Writer) int {
	shell := "zsh"
	if len(args) > 0 {
		shell = args[0]
	}
	if shell != "zsh" && shell != "bash" {
		fmt.Fprintf(stderr, "shell-init supports zsh and bash, not %q\n", shell)
		return 2
	}
	binary, err := binpath.Current()
	if err != nil {
		fmt.Fprintf(stderr, "shell-init: %v\n", err)
		return 1
	}
	quoted := "'" + strings.ReplaceAll(binary, "'", `'\''`) + "'"
	fmt.Fprintf(stdout, `# session-protect: run claude on the account that owns this folder
# (claude --account LABEL picks one; "command claude" skips this).
claude() {
  if [ -x %[1]s ]; then
    %[1]s launch -- "$@"
  else
    command claude "$@"
  fi
}
`, quoted)
	return 0
}
