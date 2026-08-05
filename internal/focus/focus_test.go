package focus

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
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

// fakeOsascript puts a scripted osascript (and ps) on PATH so the
// orchestration runs without touching real terminals.
func fakeOsascript(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/osascript", []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Session needs ps for the tty lookup; forward to the real one.
	real, _ := exec.LookPath("ps")
	if err := os.WriteFile(dir+"/ps", []byte("#!/bin/sh\nexec "+real+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestRunScriptErrorMapping(t *testing.T) {
	fakeOsascript(t, `exit 0`)
	if err := runScript("anything"); err != nil {
		t.Fatalf("success path: %v", err)
	}

	fakeOsascript(t, `echo "execution error: Not authorized to send Apple events to iTerm. (-1743)" >&2; exit 1`)
	if err := runScript("anything"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("want ErrNotAuthorized, got %v", err)
	}

	fakeOsascript(t, `echo "some real applescript failure" >&2; exit 1`)
	err := runScript("anything")
	if err == nil || !strings.Contains(err.Error(), "some real applescript failure") {
		t.Fatalf("stderr not surfaced: %v", err)
	}
}

func TestSpawnPicksHostTerminal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if err := SpawnInNewWindow("x"); err == nil {
			t.Fatal("non-darwin must refuse")
		}
		return
	}
	dir := fakeOsascript(t, `cat > /dev/null; echo "$2" >> `+t.TempDir()+`/calls; exit 0`)
	_ = dir

	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if err := SpawnInNewWindow("echo hi"); err != nil {
		t.Fatalf("iterm spawn: %v", err)
	}
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if err := SpawnInNewWindow("echo hi"); err != nil {
		t.Fatalf("terminal spawn: %v", err)
	}
}

func TestSessionNotAuthorizedShortCircuits(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if err := Session(os.Getpid()); err == nil {
			t.Fatal("non-darwin must refuse")
		}
		return
	}
	fakeOsascript(t, `echo "Not authorized to send Apple events (-1743)" >&2; exit 1`)
	err := Session(os.Getpid())
	if err == nil {
		t.Skip("test process has no tty and no gui ancestor path hit")
	}
	if !errors.Is(err, ErrNotAuthorized) && !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGuiAncestorTerminates(t *testing.T) {
	// Must terminate and not panic regardless of platform; own process
	// always has an ancestry chain.
	_ = guiAncestor(os.Getpid())
	if got := guiAncestor(-1); got != 0 {
		t.Fatalf("invalid pid should yield 0, got %d", got)
	}
}

func TestHostIsITermEnv(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if !hostIsITerm() {
		t.Fatal("TERM_PROGRAM=iTerm.app must report iTerm")
	}
}
