package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/config"
)

// weeklyEnv points the agents' homes and the backup root at a temp dir and
// pins the clock to Wednesday 2026-10-21 (week of Mon 10/19).
func weeklyEnv(t *testing.T) (config.Config, string, time.Time) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	configPath := filepath.Join(home, "config.toml")
	writeFile(t, configPath, "backup_root = \""+filepath.Join(home, "root")+"\"\n")
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	nowFn = func() time.Time { return now }
	codex := liveCodexIDs
	liveCodexIDs = func() map[string]string { return nil }
	t.Cleanup(func() { nowFn, liveCodexIDs = time.Now, codex })
	return cfg, home, now
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.Local) }

func weekGroup(groups []Group, key string) *Group {
	for i := range groups {
		if groups[i].Week == key {
			return &groups[i]
		}
	}
	return nil
}

func members(g *Group) string {
	if g == nil {
		return "<none>"
	}
	return strings.Join(g.Sessions, ",")
}

// Weeks run Monday through Sunday and are named by that span.
func TestWeekBoundsAndName(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 12, 0, 0, 0, 0, time.Local), "10/12/26 - 10/18/26"},   // Monday 00:00
		{day(2026, 10, 14), "10/12/26 - 10/18/26"},                                 // Wednesday
		{time.Date(2026, 10, 18, 23, 59, 0, 0, time.Local), "10/12/26 - 10/18/26"}, // Sunday night
		{time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local), "10/19/26 - 10/25/26"},   // next Monday
		{day(2026, 12, 30), "12/28/26 - 01/03/27"},                                 // across a year
	} {
		if got := weekName(weekStart(tc.at)); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.at, got, tc.want)
		}
	}
}

// Every source of activity lands in the right week; weeks only grow; weeks
// past 30 days clear; other groups are untouched.
func TestUpdateWeeklySourcesRetentionAndGrowth(t *testing.T) {
	cfg, home, now := weeklyEnv(t)
	// d: a prompt in history during the week of 10/12 (backfill).
	writeFile(t, filepath.Join(home, ".claude", "history.jsonl"), fmt.Sprintf(
		`{"display":"hi","sessionId":"d","project":"/p","timestamp":%d}`+"\n", day(2026, 10, 13).UnixMilli()))
	// e: seen open by a turn-end sync this week.
	writeFile(t, observationsPath(cfg), `{"2026-10-19":["e","not-scanned"]}`)

	sessions := []Session{
		{ID: "a", SourcePath: "/a.jsonl", Modified: day(2026, 10, 20)}, // written this week
		{ID: "b", SourcePath: "/b.jsonl", Modified: day(2026, 8, 1),
			LiveStatus: "idle", LiveSince: day(2026, 10, 6)}, // open since the week of 10/05
		{ID: "c", SourcePath: "/c.jsonl", Modified: day(2026, 8, 20)},        // older than 30 days
		{ID: "z", BackupPath: "/z.jsonl", BackupModified: day(2026, 10, 20)}, // backup copy only
		{ID: "d"}, {ID: "e"},
	}
	groups := []Group{
		{Name: "my work", Sessions: []string{"a"}, Created: day(2026, 10, 1)},
		{Name: "08/03/26 - 08/09/26", Week: "2026-08-03", Sessions: []string{"old"}},
	}
	groups, changed := updateWeekly(cfg, groups, sessions)
	if !changed {
		t.Fatal("no change reported")
	}
	if got := members(weekGroup(groups, "2026-10-19")); got != "a,b,e" {
		t.Errorf("this week = %s, want a,b,e", got)
	}
	if got := members(weekGroup(groups, "2026-10-12")); got != "b,d" {
		t.Errorf("week of 10/12 = %s, want b,d", got)
	}
	if got := members(weekGroup(groups, "2026-10-05")); got != "b" {
		t.Errorf("week of 10/05 = %s, want b", got)
	}
	if weekGroup(groups, "2026-08-03") != nil {
		t.Error("a week older than 30 days was not cleared")
	}
	if g := weekGroup(groups, "2026-10-19"); g == nil || g.Name != "10/19/26 - 10/25/26" {
		t.Errorf("weekly group misnamed: %+v", g)
	}
	if i := groupByName(groups, "my work"); i < 0 || members(&groups[i]) != "a" {
		t.Error("a manual group was disturbed")
	}
	if groups[len(groups)-1].Week == "" || groups[0].Week != "" {
		t.Error("weekly groups not sorted after the others")
	}
	if saved := LoadGroups(cfg); weekGroup(saved, "2026-10-19") == nil {
		t.Error("weekly groups not persisted")
	}

	// Next refresh: a closed and nothing new. Membership never shrinks.
	sessions[0].Modified, sessions[1].LiveStatus = day(2026, 8, 1), ""
	groups, changed = updateWeekly(cfg, groups, sessions)
	if changed || members(weekGroup(groups, "2026-10-19")) != "a,b,e" {
		t.Errorf("membership changed on a quiet refresh: %v %s", changed, members(weekGroup(groups, "2026-10-19")))
	}
	_ = now
}

// A turn-end sync records open claude sessions for every week since they
// started, and open codex sessions for this week; stale weeks are dropped.
func TestRecordOpenSessions(t *testing.T) {
	cfg, home, _ := weeklyEnv(t)
	liveCodexIDs = func() map[string]string { return map[string]string{"cx": "1"} }
	writeFile(t, filepath.Join(home, ".claude", "sessions", "1.json"), fmt.Sprintf(
		`{"pid":%d,"sessionId":"cl","status":"idle","startedAt":%d}`, os.Getpid(), day(2026, 10, 14).UnixMilli()))
	writeFile(t, observationsPath(cfg), `{"2026-07-06":["ancient"]}`)

	RecordOpenSessions(cfg)
	seen := loadObservations(cfg)
	if !seen["2026-10-19"]["cl"] || !seen["2026-10-12"]["cl"] {
		t.Errorf("claude session not credited since it started: %v", seen)
	}
	if !seen["2026-10-19"]["cx"] || seen["2026-10-12"]["cx"] {
		t.Errorf("codex session not credited to this week only: %v", seen)
	}
	if seen["2026-07-06"] != nil {
		t.Error("an observation older than 30 days was kept")
	}
}

// The groups page shows weekly groups in their own folding section; they
// can be opened and copied but not deleted or chosen as add/merge targets.
func TestWeeklySectionOnGroupsPage(t *testing.T) {
	cfg, _, _ := weeklyEnv(t)
	m := model{cfg: cfg, width: 120, height: 40, showGroups: true, groups: []Group{
		{Name: "my work", Sessions: []string{"a"}},
		{Name: "10/19/26 - 10/25/26", Week: "2026-10-19", Sessions: []string{"a"}},
		{Name: "10/12/26 - 10/18/26", Week: "2026-10-12", Sessions: []string{"a"}},
	}}
	view := m.View()
	for _, want := range []string{"weekly sessions (2)", "this week (10/19/26 - 10/25/26)", "10/12/26 - 10/18/26"} {
		if !strings.Contains(view, want) {
			t.Fatalf("groups page missing %q", want)
		}
	}
	if names := strings.Join(manualGroupNames(m.groups), ","); names != "my work" {
		t.Errorf("add-to-group targets = %s", names)
	}
	if targets := strings.Join(m.mergeTargets("x"), ","); targets != "my work" {
		t.Errorf("merge targets = %s", targets)
	}

	// Rows: my work, header, this week, last week. Cursor onto last week.
	m.gCursor = 3
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = next.(model)
	if m.confirmDelGroup != nil || !strings.Contains(m.notice, "clear themselves") {
		t.Fatal("d on a weekly group must explain, not offer deletion")
	}
	if len(deleteGroup(cfg, m.groups, "10/12/26 - 10/18/26")) != 3 {
		t.Fatal("deleteGroup removed a weekly group")
	}

	// Fold the section from its header; the weeks disappear from the page.
	m.gCursor = 1
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.weeklyCollapsed || len(m.groupRows()) != 2 || strings.Contains(m.View(), "this week (") {
		t.Fatal("folding the weekly section did not hide the weeks")
	}
}
