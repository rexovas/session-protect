package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrBusy means another backup or sync currently holds the lock.
var ErrBusy = errors.New("another session-protect operation is in progress")

// staleAfter guards against locks left behind by killed processes.
const staleAfter = 15 * time.Minute

// Acquire takes an exclusive lock under root and returns a release function.
func Acquire(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, ".session-protect.lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(file, "%d\n", os.Getpid())
			file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue // lock vanished between attempts; retry
		}
		if time.Since(info.ModTime()) < staleAfter {
			return nil, ErrBusy
		}
		_ = os.Remove(path)
	}
	return nil, ErrBusy
}
