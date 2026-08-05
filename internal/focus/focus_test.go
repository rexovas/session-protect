package focus

import (
	"strings"
	"testing"
)

func TestScriptsEmbedTargets(t *testing.T) {
	if s := iterm2Script("/dev/ttys009"); !strings.Contains(s, `"/dev/ttys009"`) || !strings.Contains(s, "iTerm2") {
		t.Fatal("iterm2 script malformed")
	}
	if s := terminalScript("/dev/ttys009"); !strings.Contains(s, `"/dev/ttys009"`) || !strings.Contains(s, "Terminal") {
		t.Fatal("terminal script malformed")
	}
	if s := frontmostScript(42); !strings.Contains(s, "unix id is 42") {
		t.Fatal("frontmost script malformed")
	}
}

func TestTtyOfNoTerminal(t *testing.T) {
	// pid 1 never has a controlling terminal on any platform.
	if tty := ttyOf(1); tty != "" {
		t.Fatalf("ttyOf(1) = %q, want empty", tty)
	}
}
