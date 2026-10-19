package browse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/guard"
)

// Weekly groups record which sessions were open or active during each
// Monday–Sunday week, one group per week, named by its date range. The
// current week keeps growing; once it ends it simply stops changing.
// Weeks older than weeklyRetention clear themselves — copy one into a
// normal group to keep it.
//
// A session counts toward a week when, during that week, it:
//   - got a prompt (both agents' prompt histories carry timestamps, so
//     this also backfills weeks when nothing of ours was running);
//   - had its transcript written (an agent working without a new prompt);
//   - was seen open — by the explorer on each refresh, or by the sync that
//     runs at the end of every agent turn. A claude session still open
//     when observed counts toward every week since it started.

const weeklyRetention = 30 * 24 * time.Hour

const weekKeyLayout = "2006-01-02"

// weekStart is local midnight on the Monday that begins t's week.
func weekStart(t time.Time) time.Time {
	t = t.Local()
	offset := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return time.Date(t.Year(), t.Month(), t.Day()-offset, 0, 0, 0, 0, time.Local)
}

func weekKey(t time.Time) string { return weekStart(t).Format(weekKeyLayout) }

// weekName labels a week by its Monday and Sunday: "10/12/26 - 10/18/26".
func weekName(start time.Time) string {
	return start.Format("01/02/06") + " - " + start.AddDate(0, 0, 6).Format("01/02/06")
}

func parseWeekKey(key string) (time.Time, bool) {
	start, err := time.ParseInLocation(weekKeyLayout, key, time.Local)
	return start, err == nil
}

// weekKept reports whether the week starting at start overlaps the
// retention window ending now.
func weekKept(start, now time.Time) bool {
	return start.AddDate(0, 0, 7).After(now.Add(-weeklyRetention))
}

// weekSet maps week key to the session ids active in it.
type weekSet map[string]map[string]bool

func (w weekSet) add(key, id string) {
	if w[key] == nil {
		w[key] = map[string]bool{}
	}
	w[key][id] = true
}

// mark credits id to the week containing t, if that week is retained.
func (w weekSet) mark(t time.Time, id string, now time.Time) {
	if t.IsZero() || t.After(now) {
		return
	}
	if start := weekStart(t); weekKept(start, now) {
		w.add(start.Format(weekKeyLayout), id)
	}
}

// markSpan credits id to every retained week from since through now.
func (w weekSet) markSpan(since time.Time, id string, now time.Time) {
	if since.IsZero() || since.After(now) {
		since = now
	}
	if floor := now.Add(-weeklyRetention); since.Before(floor) {
		since = floor
	}
	for start := weekStart(since); !start.After(now); start = start.AddDate(0, 0, 7) {
		w.mark(start, id, now)
	}
}

// --- open-session observations ---------------------------------------------

func observationsPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, "weekly-seen.json")
}

func loadObservations(cfg config.Config) weekSet {
	seen := weekSet{}
	if data, err := os.ReadFile(observationsPath(cfg)); err == nil {
		var stored map[string][]string
		if json.Unmarshal(data, &stored) == nil {
			for key, ids := range stored {
				for _, id := range ids {
					seen.add(key, id)
				}
			}
		}
	}
	return seen
}

func saveObservations(cfg config.Config, seen weekSet) {
	stored := map[string][]string{}
	for key, ids := range seen {
		for id := range ids {
			stored[key] = append(stored[key], id)
		}
		sort.Strings(stored[key])
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil || os.MkdirAll(cfg.BackupRoot, 0o700) != nil {
		return
	}
	// Turn-end syncs from several agents can race; write-then-rename keeps
	// the file whole, and a lost update is re-observed on the next turn.
	tmp := observationsPath(cfg) + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, observationsPath(cfg))
	}
}

// liveCodexIDs is seamed so tests never shell out to ps.
var liveCodexIDs = func() map[string]string {
	ids, _ := guard.LiveCodex()
	return ids
}

// RecordOpenSessions notes which sessions are open right now, for the
// weekly groups. It runs after every sync (the agent's turn-end hook) and
// backup, so open sessions are observed all day whether or not the
// explorer is running. It reads only the agents' process registries — no
// transcript scan — so it adds next to nothing to a sync.
func RecordOpenSessions(cfg config.Config) {
	now := nowFn()
	seen := loadObservations(cfg)
	for id, info := range guard.Live(guard.RegistryDir()) {
		var since time.Time
		if info.StartedAt > 0 {
			since = time.UnixMilli(info.StartedAt)
		}
		seen.markSpan(since, id, now)
	}
	for id := range liveCodexIDs() {
		seen.mark(now, id, now)
	}
	for key := range seen {
		if start, ok := parseWeekKey(key); !ok || !weekKept(start, now) {
			delete(seen, key)
		}
	}
	saveObservations(cfg, seen)
}

// --- weekly groups ---------------------------------------------------------

// weeklyActivity gathers, per retained week, the scanned sessions that were
// open or active in it.
func weeklyActivity(cfg config.Config, sessions []Session, now time.Time) weekSet {
	known := map[string]bool{}
	for _, s := range sessions {
		known[s.ID] = true
	}
	active := weekSet{}
	for _, s := range sessions {
		// The live transcript's mtime is its last write. Backup copies are
		// ignored: a backup run touches them all at once.
		if s.SourcePath != "" {
			active.mark(s.Modified, s.ID, now)
		}
		if s.LiveStatus != "" {
			active.markSpan(s.LiveSince, s.ID, now)
		}
	}
	for _, history := range []map[string]lostInfo{claudeHistorySessions(), codexHistorySessions()} {
		for id, info := range history {
			if !known[id] {
				continue
			}
			for key := range info.Weeks {
				if start, ok := parseWeekKey(key); ok {
					active.mark(start, id, now)
				}
			}
		}
	}
	for key, ids := range loadObservations(cfg) {
		start, ok := parseWeekKey(key)
		if !ok || !weekKept(start, now) {
			continue
		}
		for id := range ids {
			if known[id] {
				active.add(key, id)
			}
		}
	}
	return active
}

// updateWeekly folds the latest activity into the weekly groups — creating
// a week's group when its first session appears, only ever adding members —
// and clears weeks past retention. It persists only when something changed.
func updateWeekly(cfg config.Config, groups []Group, sessions []Session) ([]Group, bool) {
	now := nowFn()
	changed := false
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if g.Week != "" {
			if start, ok := parseWeekKey(g.Week); !ok || !weekKept(start, now) {
				changed = true
				continue
			}
		}
		out = append(out, g)
	}
	active := weeklyActivity(cfg, sessions, now)
	keys := make([]string, 0, len(active))
	for key := range active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		idx := -1
		for i := range out {
			if out[i].Week == key {
				idx = i
				break
			}
		}
		if idx < 0 {
			start, _ := parseWeekKey(key)
			out = append(out, Group{Name: weekName(start), Week: key, Created: start})
			idx = len(out) - 1
			changed = true
		}
		have := map[string]bool{}
		for _, id := range out[idx].Sessions {
			have[id] = true
		}
		ids := make([]string, 0, len(active[key]))
		for id := range active[key] {
			if !have[id] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			out[idx].Sessions = append(out[idx].Sessions, ids...)
			changed = true
		}
	}
	if changed {
		sortGroups(out)
		saveGroups(cfg, out)
	}
	return out, changed
}

// RecordWeekly is the headless form of the weekly update, run after a
// backup so the groups stay current (and old weeks clear) even when the
// explorer has not been opened.
func RecordWeekly(cfg config.Config) {
	projects := Scan(cfg)
	updateWeekly(cfg, LoadGroups(cfg), allProjectSessions(projects))
}
