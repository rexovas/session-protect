package schedule

import (
	"strings"
	"testing"
)

func TestPlist(t *testing.T) {
	plist := Plist("/usr/local/bin/session-protect", 12, 30, "/tmp/backup.log")
	for _, want := range []string{
		"<string>com.session-protect.backup</string>",
		"<string>/usr/local/bin/session-protect</string>",
		"<string>backup</string>",
		"<integer>12</integer>",
		"<integer>30</integer>",
		"<string>/tmp/backup.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %s", want)
		}
	}
}
