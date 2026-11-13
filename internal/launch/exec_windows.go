//go:build windows

package launch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// run starts the agent as a child with our terminal and passes its exit
// status through (Windows has no exec).
func run(agent string, argv []string, env []string, stderr io.Writer) int {
	cmd := exec.Command(agent, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(stderr, "session-protect: start claude: %v\n", err)
		return 126
	}
	return 0
}
