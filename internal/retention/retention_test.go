package retention

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func day(n int) time.Time {
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local).AddDate(0, 0, n)
}

func kept(snapshots []Snapshot) []string {
	var out []string
	for _, s := range snapshots {
		if s.Keep {
			out = append(out, s.Hash)
		}
	}
	return out
}

func TestSelectAllAndLatest(t *testing.T) {
	snaps := []Snapshot{{Hash: "a", Time: day(0)}, {Hash: "b", Time: day(1)}, {Hash: "c", Time: day(2)}}
	Select(snaps, Policy{Keep: "all"})
	if got := kept(snaps); len(got) != 3 {
		t.Fatalf("all kept %v", got)
	}
	snaps = []Snapshot{{Hash: "a", Time: day(0)}, {Hash: "b", Time: day(1)}, {Hash: "c", Time: day(2)}}
	Select(snaps, Policy{Keep: "latest"})
	if got := strings.Join(kept(snaps), ","); got != "c" {
		t.Fatalf("latest kept %s", got)
	}
}

func TestSelectPolicy(t *testing.T) {
	// 120 daily snapshots, two on the last day.
	var snaps []Snapshot
	for i := 0; i < 120; i++ {
		snaps = append(snaps, Snapshot{Hash: "d" + time.Duration(i).String(), Time: day(i)})
	}
	snaps = append(snaps, Snapshot{Hash: "last", Time: day(119).Add(time.Hour)})
	Select(snaps, Policy{Keep: "policy", Daily: 7, Weekly: 4, Monthly: 3})

	got := map[string]bool{}
	for _, h := range kept(snaps) {
		got[h] = true
	}
	if !got["last"] {
		t.Fatal("newest snapshot must be kept")
	}
	if got[snaps[119].Hash] {
		t.Fatal("only the newest snapshot of a day is kept")
	}
	// 7 days (6 earlier days + last) plus older week and month picks;
	// overlapping periods share snapshots, so the total stays small.
	if n := len(got); n < 7 || n > 7+4+3 {
		t.Fatalf("kept %d snapshots, want between 7 and 14", n)
	}
	if got[snaps[0].Hash] {
		t.Fatal("a snapshot 4 months back is outside a 3-month window")
	}
}

func TestSelectKeepsDeletionParent(t *testing.T) {
	snaps := []Snapshot{
		{Hash: "old", Time: day(0)},
		{Hash: "final", Parent: "old", Time: day(5), Subject: "session-protect: all backup x"},
		{Hash: "del", Parent: "final", Time: day(5).Add(time.Second), Subject: "session-protect: all backup-deletions x"},
	}
	Select(snaps, Policy{Keep: "policy", Daily: 1})
	if got := strings.Join(kept(snaps), ","); got != "final,del" {
		t.Fatalf("kept %s; a kept deletions commit must keep its parent", got)
	}
}

// repo creates a git repo with one commit per day; each commit rewrites
// state.txt and adds a per-day file, and returns the repo path.
func repo(t *testing.T, days int) string {
	t.Helper()
	dir := t.TempDir()
	run := func(env []string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), append([]string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, env...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(nil, "init", "-q", "-b", "main")
	run(nil, "config", "user.name", "t")
	run(nil, "config", "user.email", "t@example.com")
	for i := 0; i < days; i++ {
		if err := os.WriteFile(filepath.Join(dir, "state.txt"), []byte(strings.Repeat("x", i+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		date := day(i).Format(time.RFC3339)
		run(nil, "add", "-A")
		run([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", "session-protect: all backup "+date)
	}
	return dir
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestApplyRewritesAndReclaims(t *testing.T) {
	dir := repo(t, 30)
	oldTip := gitOut(t, dir, "rev-parse", "HEAD")
	oldTree := gitOut(t, dir, "rev-parse", "HEAD^{tree}")
	oldFirst := gitOut(t, dir, "rev-list", "--max-parents=0", "HEAD")

	plan, err := Build(dir, Policy{Keep: "policy", Daily: 3})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kept != 3 || plan.Dropped != 27 {
		t.Fatalf("plan kept %d dropped %d, want 3 and 27", plan.Kept, plan.Dropped)
	}
	if err := Apply(dir, plan); err != nil {
		t.Fatal(err)
	}

	if got := gitOut(t, dir, "rev-list", "--count", "HEAD"); got != "3" {
		t.Fatalf("history has %s commits, want 3", got)
	}
	if got := gitOut(t, dir, "rev-parse", "HEAD^{tree}"); got != oldTree {
		t.Fatal("the latest state changed")
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%cI", "HEAD"); !strings.HasPrefix(got, day(29).Format("2006-01-02")) {
		t.Fatalf("tip date %s not preserved", got)
	}
	if !strings.Contains(gitOut(t, dir, "log", "--format=%B", "--reverse", "HEAD"), "27 earlier snapshot(s) were folded") {
		t.Fatal("new root does not note the folded snapshots")
	}
	for _, gone := range []string{oldTip, oldFirst} {
		if exec.Command("git", "-C", dir, "cat-file", "-e", gone).Run() == nil {
			t.Fatalf("dropped commit %s still present after gc", gone)
		}
	}
	if status := gitOut(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("working tree disturbed: %q", status)
	}
}

func TestApplyRefusesWhenBranchMoved(t *testing.T) {
	dir := repo(t, 5)
	plan, err := Build(dir, Policy{Keep: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	// A backup lands between planning and applying.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, "add", "-A")
	exec.Command("git", "-C", dir, "commit", "-q", "-m", "concurrent").Run()
	before := gitOut(t, dir, "rev-list", "--count", "HEAD")

	if err := Apply(dir, plan); !errors.Is(err, ErrMoved) {
		t.Fatalf("want ErrMoved, got %v", err)
	}
	if after := gitOut(t, dir, "rev-list", "--count", "HEAD"); after != before {
		t.Fatalf("history changed from %s to %s commits", before, after)
	}
}

func TestApplyNothingToDrop(t *testing.T) {
	dir := repo(t, 3)
	tip := gitOut(t, dir, "rev-parse", "HEAD")
	plan, err := Build(dir, Policy{Keep: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, plan); err != nil {
		t.Fatal(err)
	}
	if gitOut(t, dir, "rev-parse", "HEAD") != tip {
		t.Fatal("keep=all must not rewrite")
	}
}
