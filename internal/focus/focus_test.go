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

func TestQuotingAndSpawnScripts(t *testing.T) {
	if got := ShellQuote("/a/b c's"); got != `'/a/b c'\''s'` {
		t.Fatalf("ShellQuote = %s", got)
	}
	if got := appleScriptQuote(`say "hi" \ there`); got != `say \"hi\" \\ there` {
		t.Fatalf("appleScriptQuote = %s", got)
	}
	cmd := `cd '/tmp/x' && claude --resume abc`
	if s := iterm2SpawnScript(cmd); !strings.Contains(s, "create window") || !strings.Contains(s, "claude --resume abc") {
		t.Fatal("iterm2 spawn script malformed")
	}
	if s := terminalSpawnScript(cmd); !strings.Contains(s, "do script") || !strings.Contains(s, "claude --resume abc") {
		t.Fatal("terminal spawn script malformed")
	}
}
