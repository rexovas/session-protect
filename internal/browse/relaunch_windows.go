//go:build windows

package browse

// Windows cannot exec-replace a process; the user restarts manually.
func relaunch(string) error { return nil }
