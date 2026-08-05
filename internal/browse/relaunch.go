//go:build !windows

package browse

import (
	"os"
	"syscall"
)

// relaunch replaces this process with the updated binary, preserving
// arguments and environment.
func relaunch(path string) error {
	argv := append([]string{path}, os.Args[1:]...)
	return syscall.Exec(path, argv, os.Environ())
}
