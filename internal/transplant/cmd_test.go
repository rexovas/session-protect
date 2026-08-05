package transplant

import (
	"strings"
	"testing"
)

func TestRunFlagValidation(t *testing.T) {
	cfg, _, source, _ := setup(t)
	_ = cfg

	cases := []struct {
		args []string
		want int
	}{
		{[]string{"--session", "x"}, 1},                                 // no --to
		{[]string{"--to", "/tmp/t"}, 1},                                 // no scope
		{[]string{"--session", "x", "--project", "y", "--to", "/t"}, 1}, // both scopes
		{[]string{"--memory", "bogus", "--session", "x", "--to", "/t"}, 2},
		{[]string{"--unknown-flag"}, 2},
		{[]string{"help"}, 0},
	}
	for _, tc := range cases {
		var out, errOut strings.Builder
		if code := Run(tc.args, strings.NewReader(""), &out, &errOut); code != tc.want {
			t.Errorf("Run(%v) = %d, want %d (%s)", tc.args, code, tc.want, errOut.String())
		}
	}

	// Dry run prints the plan and writes nothing.
	var out, errOut strings.Builder
	if code := Run([]string{"--project", source, "--to", "/tmp/elsewhere-dry", "--dry-run"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("dry run exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), sessionID) {
		t.Fatalf("dry-run plan missing content: %s", out.String())
	}

	// Declining the confirmation aborts.
	out.Reset()
	if code := Run([]string{"--project", source, "--to", "/tmp/elsewhere-no"}, strings.NewReader("n\n"), &out, &errOut); code != 1 {
		t.Fatalf("declined confirm should exit 1, got %d", code)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Fatalf("no abort message: %s", out.String())
	}
}
