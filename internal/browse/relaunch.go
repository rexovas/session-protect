//go:build !windows

package browse

import (
	"os"
	"syscall"
)

// relaunch replaces this process with argv, preserving the environment.
func relaunch(argv []string) error {
	return syscall.Exec(argv[0], argv, os.Environ())
}
