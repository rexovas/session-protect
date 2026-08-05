package focus

import (
	"os"
	"strings"
	"testing"
)

func TestFocusSequenceShape(t *testing.T) {
	if !strings.Contains(focusSequence, "1337;StealFocus") {
		t.Fatal("iTerm2 StealFocus escape missing")
	}
	if !strings.Contains(focusSequence, "\x1b[5t") || !strings.Contains(focusSequence, "\x1b[1t") {
		t.Fatal("XTWINOPS raise/deiconify missing")
	}
	for _, b := range []byte(focusSequence) {
		if b >= 0x20 && b != 0x1b && (b < 0x20 || b > 0x7e) {
			t.Fatal("sequence contains non-printable garbage")
		}
	}
}

func TestTtyOfNoTerminal(t *testing.T) {
	// pid 1 never has a controlling terminal on any platform.
	if tty := ttyOf(1); tty != "" {
		t.Fatalf("ttyOf(1) = %q, want empty", tty)
	}
}

func TestSessionWritesToOwnTty(t *testing.T) {
	tty := ttyOf(os.Getpid())
	if tty == "" {
		t.Skip("test process has no controlling terminal")
	}
	if err := Session(os.Getpid()); err != nil {
		t.Fatalf("writing focus escapes to own tty failed: %v", err)
	}
}
