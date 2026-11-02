// Package retention bounds a backup repo's history by rewriting it to keep
// only selected snapshots. The kept snapshots keep their exact trees, dates
// and messages; everything older than the oldest kept one is folded into it.
package retention

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Policy says which snapshots to keep: "all", "latest", or "policy" with the
// Daily/Weekly/Monthly counts.
type Policy struct {
	Keep    string
	Daily   int
	Weekly  int
	Monthly int
}

// Snapshot is one commit on the branch's first-parent line.
type Snapshot struct {
	Hash    string
	Parent  string // first parent, "" for the root
	Time    time.Time
	Subject string
	Keep    bool
}

// Plan is what Apply would do.
type Plan struct {
	Snapshots []Snapshot // oldest first
	Kept      int
	Dropped   int
}

// ErrMoved means the branch changed while retention ran; nothing was
// rewritten.
var ErrMoved = errors.New("backup branch moved during retention; nothing was rewritten")

// Select marks which snapshots the policy keeps. snapshots are oldest first.
// The newest snapshot is always kept. A kept "backup-deletions" snapshot
// also keeps its parent, the commit holding the deleted files' final state,
// so a deletion inside the window stays recoverable.
func Select(snapshots []Snapshot, policy Policy) {
	if len(snapshots) == 0 {
		return
	}
	switch policy.Keep {
	case "all", "":
		for i := range snapshots {
			snapshots[i].Keep = true
		}
		return
	case "latest":
		snapshots[len(snapshots)-1].Keep = true
		return
	}

	type bucket struct {
		key   func(time.Time) string
		limit int
		seen  map[string]bool
	}
	buckets := []*bucket{
		{key: func(t time.Time) string { return t.Format("2006-01-02") }, limit: policy.Daily},
		{key: func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-W%02d", y, w) }, limit: policy.Weekly},
		{key: func(t time.Time) string { return t.Format("2006-01") }, limit: policy.Monthly},
	}
	for _, b := range buckets {
		b.seen = map[string]bool{}
	}
	// Newest first: the first snapshot seen in a period is that period's newest.
	for i := len(snapshots) - 1; i >= 0; i-- {
		at := snapshots[i].Time.Local()
		for _, b := range buckets {
			k := b.key(at)
			if b.seen[k] || len(b.seen) >= b.limit {
				continue
			}
			b.seen[k] = true
			snapshots[i].Keep = true
		}
	}
	snapshots[len(snapshots)-1].Keep = true

	byHash := map[string]int{}
	for i, s := range snapshots {
		byHash[s.Hash] = i
	}
	for _, s := range snapshots {
		if s.Keep && strings.Contains(s.Subject, "backup-deletions") {
			if p, ok := byHash[s.Parent]; ok {
				snapshots[p].Keep = true
			}
		}
	}
}

// Build reads the branch HEAD points at and selects snapshots under policy.
func Build(repo string, policy Policy) (Plan, error) {
	out, err := git(repo, nil, "log", "--first-parent", "--reverse", "--format=%H%x1f%P%x1f%ct%x1f%s", "HEAD")
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 4)
		if len(f) != 4 {
			return Plan{}, fmt.Errorf("unexpected log line %q", line)
		}
		secs, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return Plan{}, err
		}
		parent := ""
		if parents := strings.Fields(f[1]); len(parents) > 0 {
			parent = parents[0]
		}
		plan.Snapshots = append(plan.Snapshots, Snapshot{Hash: f[0], Parent: parent, Time: time.Unix(secs, 0), Subject: f[3]})
	}
	Select(plan.Snapshots, policy)
	for _, s := range plan.Snapshots {
		if s.Keep {
			plan.Kept++
		} else {
			plan.Dropped++
		}
	}
	return plan, nil
}

// Apply rewrites the branch to the plan's kept snapshots and reclaims the
// space of the dropped ones. It does nothing when nothing is dropped.
//
// Safety: kept snapshots are recreated from their own trees, with their
// author, committer, dates and messages; the new tip's tree must equal the
// old tip's; and the branch is moved only if it still points at the old tip
// (a compare-and-swap), so a concurrent commit aborts the rewrite unharmed.
// The oldest kept snapshot becomes the root and notes what was folded in.
func Apply(repo string, plan Plan) error {
	if plan.Dropped == 0 || len(plan.Snapshots) == 0 {
		return nil
	}
	branch, err := git(repo, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return fmt.Errorf("retention needs a branch checked out: %w", err)
	}
	branch = strings.TrimSpace(branch)
	oldTip := plan.Snapshots[len(plan.Snapshots)-1].Hash

	newParent := ""
	first := true
	for i, s := range plan.Snapshots {
		if !s.Keep {
			continue
		}
		meta, err := git(repo, nil, "log", "-1", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%B", s.Hash)
		if err != nil {
			return err
		}
		m := strings.SplitN(meta, "\x00", 7)
		if len(m) != 7 {
			return fmt.Errorf("unexpected commit metadata for %s", s.Hash)
		}
		message := strings.TrimRight(m[6], "\n")
		if first && i > 0 {
			message += fmt.Sprintf("\n\nRetention: %d earlier snapshot(s) were folded into this one on %s.",
				countDroppedBefore(plan.Snapshots, i), time.Now().Format("2006-01-02"))
		}
		args := []string{"commit-tree", s.Hash + "^{tree}", "-m", message}
		if newParent != "" {
			args = append(args, "-p", newParent)
		}
		env := []string{
			"GIT_AUTHOR_NAME=" + m[0], "GIT_AUTHOR_EMAIL=" + m[1], "GIT_AUTHOR_DATE=" + m[2],
			"GIT_COMMITTER_NAME=" + m[3], "GIT_COMMITTER_EMAIL=" + m[4], "GIT_COMMITTER_DATE=" + m[5],
		}
		out, err := git(repo, env, args...)
		if err != nil {
			return err
		}
		newParent = strings.TrimSpace(out)
		first = false
	}

	oldTree, err := git(repo, nil, "rev-parse", oldTip+"^{tree}")
	if err != nil {
		return err
	}
	newTree, err := git(repo, nil, "rev-parse", newParent+"^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(oldTree) != strings.TrimSpace(newTree) {
		return fmt.Errorf("retention: rewritten tip %s does not match %s; nothing was rewritten", newParent, oldTip)
	}
	if _, err := git(repo, nil, "update-ref", "-m", "session-protect retention", branch, newParent, oldTip); err != nil {
		return ErrMoved
	}

	// Reclaim the dropped snapshots' space. Other refs (tags, stashes) would
	// keep them alive; the backup repo has none of its own.
	if _, err := git(repo, nil, "reflog", "expire", "--expire=now", "--all"); err != nil {
		return err
	}
	_, err = git(repo, nil, "gc", "--prune=now", "--quiet")
	return err
}

func countDroppedBefore(snapshots []Snapshot, index int) int {
	n := 0
	for _, s := range snapshots[:index] {
		if !s.Keep {
			n++
		}
	}
	return n
}

func git(repo string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}
