package browse

import (
	"testing"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

func openSessions(ids ...string) []Session {
	var out []Session
	for _, id := range ids {
		out = append(out, Session{ID: id, LiveStatus: "open"})
	}
	return out
}

func autoIDs(groups []Group) []string {
	if i := groupByName(groups, autoGroupName); i >= 0 {
		return groups[i].Sessions
	}
	return nil
}

func TestAutoGroupGrowsNeverErodes(t *testing.T) {
	cfg := config.Config{BackupRoot: t.TempDir()}
	nowFn = func() time.Time { return time.Unix(0, 0) }
	defer func() { nowFn = time.Now }()

	var g []Group
	g, _ = snapshotOpen(cfg, g, openSessions("a", "b", "c"), "")
	if len(autoIDs(g)) != 3 {
		t.Fatalf("initial: %v", autoIDs(g))
	}
	// Wind down: each smaller set is a subset — the peak must survive.
	g, _ = snapshotOpen(cfg, g, openSessions("a", "b"), "")
	g, _ = snapshotOpen(cfg, g, openSessions("a"), "")
	g, _ = snapshotOpen(cfg, g, openSessions(), "") // all closed / reboot
	if got := autoIDs(g); len(got) != 3 {
		t.Fatalf("eroded to %v, want the 3-session peak", got)
	}
	// A new session appears → the group updates.
	g, _ = snapshotOpen(cfg, g, openSessions("a", "b", "c", "d"), "")
	if len(autoIDs(g)) != 4 {
		t.Fatalf("growth not captured: %v", autoIDs(g))
	}
	// A disjoint context replaces it.
	g, _ = snapshotOpen(cfg, g, openSessions("x", "y"), "")
	if got := autoIDs(g); len(got) != 2 || got[0] != "x" {
		t.Fatalf("context switch not captured: %v", got)
	}
}
