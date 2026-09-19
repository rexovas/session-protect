package browse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rexovas/session-protect/internal/config"
)

// A group is a named, cross-project set of sessions — a view, never a
// move: no session files are relocated, only their ids are recorded in
// SessionProtect's own state. Snapshots of the currently-open sessions
// and hand-curated workspaces both live here, resumable together or one
// at a time.

// autoGroupName is the reserved, auto-maintained group that tracks the
// most recent set of open sessions, so a workspace survives a reboot.
const autoGroupName = "recently open"

// Group is a named set of session ids.
type Group struct {
	Name     string    `json:"name"`
	Sessions []string  `json:"sessions"`
	Archived []string  `json:"archived,omitempty"` // hidden-by-default members
	Created  time.Time `json:"created"`
	Auto     bool      `json:"auto,omitempty"`
}

func groupsPath(cfg config.Config) string {
	return filepath.Join(cfg.BackupRoot, "groups.json")
}

// LoadGroups reads the saved groups, auto group first, then manual
// groups by most-recent.
func LoadGroups(cfg config.Config) []Group {
	data, err := os.ReadFile(groupsPath(cfg))
	if err != nil {
		return nil
	}
	var groups []Group
	if json.Unmarshal(data, &groups) != nil {
		return nil
	}
	sortGroups(groups)
	return groups
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Auto != groups[j].Auto {
			return groups[i].Auto // auto group pinned first
		}
		return groups[i].Created.After(groups[j].Created)
	})
}

func saveGroups(cfg config.Config, groups []Group) {
	if err := os.MkdirAll(cfg.BackupRoot, 0o700); err != nil {
		return
	}
	if data, err := json.MarshalIndent(groups, "", "  "); err == nil {
		_ = os.WriteFile(groupsPath(cfg), data, 0o600)
	}
}

// groupByName returns the index of a group, or -1.
func groupByName(groups []Group, name string) int {
	for i := range groups {
		if groups[i].Name == name {
			return i
		}
	}
	return -1
}

// addSessionToGroup adds id to the named group (creating it if needed)
// and persists, returning the updated slice. Duplicate ids are ignored.
func addSessionToGroup(cfg config.Config, groups []Group, name, id string) ([]Group, bool) {
	idx := groupByName(groups, name)
	if idx < 0 {
		groups = append(groups, Group{Name: name, Sessions: []string{id}, Created: nowFn()})
	} else {
		for _, existing := range groups[idx].Sessions {
			if existing == id {
				return groups, false // already a member
			}
		}
		groups[idx].Sessions = append(groups[idx].Sessions, id)
	}
	sortGroups(groups)
	saveGroups(cfg, groups)
	return groups, true
}

// removeSessionFromGroup drops id from a group and persists.
func removeSessionFromGroup(cfg config.Config, groups []Group, name, id string) []Group {
	idx := groupByName(groups, name)
	if idx < 0 {
		return groups
	}
	kept := groups[idx].Sessions[:0]
	for _, existing := range groups[idx].Sessions {
		if existing != id {
			kept = append(kept, existing)
		}
	}
	groups[idx].Sessions = kept
	saveGroups(cfg, groups)
	return groups
}

// deleteGroup removes a whole group (the auto group cannot be deleted —
// it just refreshes) and persists.
func deleteGroup(cfg config.Config, groups []Group, name string) []Group {
	idx := groupByName(groups, name)
	if idx < 0 || groups[idx].Auto {
		return groups
	}
	groups = append(groups[:idx], groups[idx+1:]...)
	saveGroups(cfg, groups)
	return groups
}

// snapshotOpen writes the currently-open sessions into a group. With a
// name it creates a manual workspace snapshot; with an empty name it
// refreshes the reserved auto group. Returns the updated slice and the
// number of open sessions captured.
func snapshotOpen(cfg config.Config, groups []Group, sessions []Session, name string) ([]Group, int) {
	var open []string
	seen := map[string]bool{}
	for _, s := range sessions {
		if s.LiveStatus != "" && !seen[s.ID] {
			seen[s.ID] = true
			open = append(open, s.ID)
		}
	}
	auto := name == ""
	if auto {
		name = autoGroupName
	}
	idx := groupByName(groups, name)

	if auto {
		// The auto group grows with your work but never erodes as you
		// close sessions: only replace it when the current open set adds
		// a session it does not already hold (new work or a context
		// switch). A pure shrink — or an empty set after a reboot —
		// leaves the last real workspace intact, so it is still there to
		// restore when everything has closed.
		if len(open) == 0 {
			return groups, 0
		}
		if idx >= 0 {
			have := map[string]bool{}
			for _, id := range groups[idx].Sessions {
				have[id] = true
			}
			introduces := false
			for _, id := range open {
				if !have[id] {
					introduces = true
					break
				}
			}
			if !introduces {
				return groups, len(groups[idx].Sessions) // subset: keep the peak
			}
		}
	}

	g := Group{Name: name, Sessions: open, Created: nowFn(), Auto: auto}
	if idx < 0 {
		groups = append(groups, g)
	} else if !auto {
		g.Created = groups[idx].Created
		groups[idx] = g
	} else {
		groups[idx] = g
	}
	sortGroups(groups)
	saveGroups(cfg, groups)
	return groups, len(open)
}

// groupMembers resolves a group's session ids to live Session records
// from the scan, dropping ids that no longer exist. Order follows the
// group; each carries its current state.
func groupMembers(group Group, byID map[string]Session) []Session {
	var out []Session
	for _, id := range group.Sessions {
		if s, ok := byID[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// nowFn is seamed for deterministic tests.
var nowFn = time.Now

// addSessionsToGroup adds several ids to a group (creating it if needed)
// and persists, returning the count newly added.
func addSessionsToGroup(cfg config.Config, groups []Group, name string, ids []string) ([]Group, int) {
	added := 0
	for _, id := range ids {
		var ok bool
		groups, ok = addSessionToGroup(cfg, groups, name, id)
		if ok {
			added++
		}
	}
	return groups, added
}

// setArchived moves a member into or out of a group's archived (hidden)
// set and persists. Archiving never removes the session from the group.
func setArchived(cfg config.Config, groups []Group, name, id string, archived bool) []Group {
	idx := groupByName(groups, name)
	if idx < 0 {
		return groups
	}
	kept := groups[idx].Archived[:0]
	for _, existing := range groups[idx].Archived {
		if existing != id {
			kept = append(kept, existing)
		}
	}
	if archived {
		kept = append(kept, id)
	}
	groups[idx].Archived = kept
	saveGroups(cfg, groups)
	return groups
}

// isArchived reports whether id is archived in the group.
func isArchived(group Group, id string) bool {
	for _, a := range group.Archived {
		if a == id {
			return true
		}
	}
	return false
}

// visibleGroupIDs returns the group's member ids honoring the archived
// set: all members when showArchived, otherwise the un-archived ones.
func visibleGroupIDs(group Group, showArchived bool) map[string]bool {
	arch := map[string]bool{}
	for _, a := range group.Archived {
		arch[a] = true
	}
	ids := map[string]bool{}
	for _, id := range group.Sessions {
		if showArchived || !arch[id] {
			ids[id] = true
		}
	}
	return ids
}
