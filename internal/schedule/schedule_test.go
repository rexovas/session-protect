package schedule

import (
	"runtime"
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
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %s", want)
		}
	}
}

func TestPlistPath(t *testing.T) {
	got := plistPath("/Users/x")
	if !strings.HasSuffix(got, "Library/LaunchAgents/com.session-protect.backup.plist") {
		t.Fatalf("plistPath = %s", got)
	}
}

func TestRunArgHandling(t *testing.T) {
	var out, errOut strings.Builder
	if code := Run([]string{"help"}, &out, &errOut); code != 0 {
		t.Fatal("help should exit 0")
	}
	// Non-darwin platforms refuse before subcommand dispatch.
	want := 2
	if runtime.GOOS != "darwin" {
		want = 1
	}
	if code := Run([]string{"bogus"}, &out, &errOut); code != want {
		t.Fatalf("unknown subcommand exit = %d, want %d", code, want)
	}
}
