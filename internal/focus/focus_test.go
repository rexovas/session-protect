package focus

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNonDarwinRefuses(t *testing.T) {
	osName = "linux"
	t.Cleanup(func() { osName = runtime.GOOS })
	if err := Session(1); err == nil {
		t.Fatal("non-darwin Session must refuse")
	}
	if err := SpawnInNewWindow("x"); err == nil {
		t.Fatal("non-darwin spawn must refuse")
	}
}

// mockExec routes every subprocess request to an in-process handler and
// restores the real runner when the test ends.
func mockExec(t *testing.T, handler func(name string, args []string) (string, string, error)) {
	t.Helper()
	osName = "darwin"
	t.Cleanup(func() { osName = runtime.GOOS })
	execCommand = func(_ time.Duration, name string, args ...string) (string, string, error) {
		return handler(name, args)
	}
	t.Cleanup(func() { execCommand = defaultExec })
}

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

func TestTtyOf(t *testing.T) {
	mockExec(t, func(name string, args []string) (string, string, error) {
		return " ttys032 \n", "", nil
	})
	if got := ttyOf(123); got != "/dev/ttys032" {
		t.Fatalf("ttyOf = %q", got)
	}
	mockExec(t, func(name string, args []string) (string, string, error) {
		return " ?? \n", "", nil
	})
	if got := ttyOf(123); got != "" {
		t.Fatalf("no-tty must be empty, got %q", got)
	}
}

func TestRunScriptErrorMapping(t *testing.T) {
	mockExec(t, func(name string, args []string) (string, string, error) {
		return "", "", nil
	})
	if err := runScript("x"); err != nil {
		t.Fatalf("success path: %v", err)
	}

	mockExec(t, func(name string, args []string) (string, string, error) {
		return "", "execution error: Not authorized to send Apple events to iTerm. (-1743)", errors.New("exit status 1")
	})
	if err := runScript("x"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("want ErrNotAuthorized, got %v", err)
	}

	mockExec(t, func(name string, args []string) (string, string, error) {
		return "", "24:31: some real applescript failure", errors.New("exit status 1")
	})
	if err := runScript("x"); err == nil || !strings.Contains(err.Error(), "applescript failure") {
		t.Fatalf("stderr not surfaced: %v", err)
	}
}

func TestSessionFallsThroughTerminals(t *testing.T) {
	// iTerm2 rejects the tty, Terminal.app accepts: Session must succeed
	// on the second script without reaching the app-raise fallback.
	var scripts []string
	mockExec(t, func(name string, args []string) (string, string, error) {
		if name == "ps" {
			return "ttys009", "", nil
		}
		scripts = append(scripts, args[1])
		if strings.Contains(args[1], "iTerm2") {
			return "", "tty not found", errors.New("exit status 1")
		}
		return "", "", nil
	})
	if err := Session(4242); err != nil {
		t.Fatalf("Session = %v", err)
	}
	if len(scripts) != 2 || !strings.Contains(scripts[1], "Terminal") {
		t.Fatalf("fallback order wrong: %d scripts", len(scripts))
	}
}

func TestSessionNotAuthorizedShortCircuits(t *testing.T) {
	calls := 0
	mockExec(t, func(name string, args []string) (string, string, error) {
		if name == "ps" {
			return "ttys009", "", nil
		}
		calls++
		return "", "Not authorized to send Apple events (-1743)", errors.New("exit status 1")
	})
	if err := Session(4242); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("want ErrNotAuthorized, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("denial must short-circuit, ran %d scripts", calls)
	}
}

func TestSessionAppRaiseFallback(t *testing.T) {
	// No tty at all: Session walks ancestry and raises the GUI app.
	mockExec(t, func(name string, args []string) (string, string, error) {
		if name == "ps" && args[1] == "tty=" {
			return "??", "", nil
		}
		if name == "ps" && args[1] == "ppid=" {
			if args[3] == "4242" {
				return "77", "", nil
			}
			return "1", "", nil // 77's parent is launchd → 77 is the app
		}
		if name == "osascript" && strings.Contains(args[1], "unix id is 77") {
			return "", "", nil
		}
		return "", "", fmt.Errorf("unexpected call: %s %v", name, args)
	})
	if err := Session(4242); err != nil {
		t.Fatalf("app-raise fallback failed: %v", err)
	}
}

func TestSpawnPicksHostTerminal(t *testing.T) {
	var scripts []string
	mockExec(t, func(name string, args []string) (string, string, error) {
		if name == "ps" {
			return "1", "", nil
		}
		scripts = append(scripts, args[1])
		return "", "", nil
	})
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if err := SpawnInNewWindow("echo hi"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if err := SpawnInNewWindow("echo hi"); err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 2 || !strings.Contains(scripts[0], "iTerm2") || !strings.Contains(scripts[1], "Terminal") {
		t.Fatalf("host selection wrong: %d scripts", len(scripts))
	}
}

func TestGuiAncestorNeverLoops(t *testing.T) {
	mockExec(t, func(name string, args []string) (string, string, error) {
		return args[3], "", nil // every process is its own parent: a cycle
	})
	if got := guiAncestor(9); got != 0 {
		t.Fatalf("cycle must terminate at 0, got %d", got)
	}
}
