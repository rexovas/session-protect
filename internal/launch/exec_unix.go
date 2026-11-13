//go:build !windows

package launch

import (
	"fmt"
	"io"
	"syscall"
)

// run replaces this process with the agent so it owns the terminal, its
// signals and its exit status directly.
func run(agent string, argv []string, env []string, stderr io.Writer) int {
	err := syscall.Exec(agent, argv, env)
	fmt.Fprintf(stderr, "session-protect: start claude: %v\n", err)
	return 126
}
