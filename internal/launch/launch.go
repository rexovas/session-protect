// Package launch starts an agent (claude or codex) on the account that
// owns the current folder, for the shell functions `shell-init` defines.
package launch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/rexovas/session-protect/internal/binpath"
	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/targets"
)

// accountFlag picks the home explicitly; it is ours and never reaches the
// agent. A first session started with it is what teaches a new folder its
// owner.
const accountFlag = "--account"

// Run resolves the home and replaces this process with the agent. args are
// an optional agent name (claude by default) and then, after "--", the
// agent's own arguments.
func Run(args []string, stdout io.Writer, stderr io.Writer) int {
	agent := targets.AgentClaude
	if len(args) > 0 && (args[0] == targets.AgentClaude || args[0] == targets.AgentCodex) {
		agent, args = args[0], args[1:]
	}
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
	require := false
	if cfg, err := config.Load(); err == nil {
		require = cfg.Accounts.RequireClientHome
	}
	env, note, err := Environment(agent, os.Environ(), dir, label, require)
	if err != nil {
		fmt.Fprintf(stderr, "session-protect: %v\n", err)
		return 2
	}
	if note != "" {
		fmt.Fprintln(stderr, "session-protect: "+note)
	}
	binary, err := exec.LookPath(agent)
	if err != nil {
		fmt.Fprintf(stderr, "session-protect: %s is not on PATH\n", agent)
		return 127
	}
	return run(binary, append([]string{agent}, rest...), env, stderr)
}

// Environment is the agent's environment for a launch in dir: an explicit
// --account wins, then an explicit home variable, then the folder's owner.
// The default home runs with its variable removed (or, for codex, set to a
// CODEX_HOME the user keeps for everything): claude keys its stored login
// on whether CLAUDE_CONFIG_DIR is set at all, so setting it to ~/.claude
// would ask for a different login.
//
// In a client's folder, when the client has no home for this agent, it
// runs on the default home with a note, or is refused when require is set.
func Environment(agent string, environ []string, dir string, label string, require bool) ([]string, string, error) {
	variable := targets.EnvVarFor(agent)
	note := ""
	account := label
	switch {
	case label != "":
	case lookup(environ, variable) != "" && !isDefaultDir(agent, lookup(environ, variable)):
		return environ, "", nil
	default:
		owner := targets.FolderOwner(dir)
		if len(owner.Conflict) > 0 {
			return nil, "", fmt.Errorf("this folder has sessions in several accounts (%s); pick one with %s LABEL",
				strings.Join(owner.Conflict, ", "), accountFlag)
		}
		if owner.BroadClaim != "" {
			note = fmt.Sprintf("%s has %s sessions but also other accounts' work, so it isn't treated as %s's; using default (%s LABEL to choose)",
				owner.BroadClaim, owner.BroadHome, owner.BroadHome, accountFlag)
		}
		account = owner.Owner.Label
	}
	home, ok := targets.HomeByLabelOf(agent, account)
	if !ok {
		if label != "" {
			return nil, "", fmt.Errorf("the %s account has no %s dir (see `session-protect %s-dirs`)", label, agent, agent)
		}
		hint := fmt.Sprintf("add one with `session-protect %s-dirs add DIR --label %s`", agent, account)
		if require {
			return nil, "", fmt.Errorf("this folder belongs to the %s account, which has no %s dir yet; %s, or run %s %s default",
				account, agent, hint, agent, accountFlag)
		}
		home = targets.HomesOf(agent)[0]
		note = fmt.Sprintf("%s has no %s dir; this session runs on your default %s (%s). To keep it separate, %s.",
			account, agent, agent, home.Dir, hint)
	}
	out := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, variable+"=") {
			out = append(out, entry)
		}
	}
	if value, unset := home.Pin(); !unset {
		out = append(out, variable+"="+value)
	}
	return out, note, nil
}

// isDefaultDir reports whether value is the agent's default home, which
// counts as no choice at all (a CODEX_HOME kept for everything).
func isDefaultDir(agent string, value string) bool {
	if agent != targets.AgentCodex {
		return false
	}
	home, ok := targets.HomeForDirOf(agent, value)
	return ok && home.IsDefault()
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
	fmt.Fprintf(stdout, `# session-protect: run claude and codex on the account that owns this
# folder (--account LABEL picks one; "command claude" / "command codex" skip this).
claude() {
  if [ -x %[1]s ]; then
    %[1]s launch claude -- "$@"
  else
    command claude "$@"
  fi
}
codex() {
  if [ -x %[1]s ]; then
    %[1]s launch codex -- "$@"
  else
    command codex "$@"
  fi
}
`, quoted)
	return 0
}
