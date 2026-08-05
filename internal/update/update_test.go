package update

import (
	"strings"
	"testing"
)

func TestCheckAndFlagValidation(t *testing.T) {
	var out, errOut strings.Builder
	if code := Run([]string{"--check"}, &out, &errOut); code != 0 {
		t.Fatalf("--check exit %d", code)
	}
	// Dev builds are not configured for self-update.
	if !strings.Contains(out.String(), "Channel") || !strings.Contains(out.String(), "not configured") {
		t.Fatalf("check output wrong: %s", out.String())
	}
	if code := Run([]string{"--bogus"}, &out, &errOut); code != 2 {
		t.Fatal("unknown flag should exit 2")
	}
}
