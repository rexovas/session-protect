package browse

import (
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RecountProject recomputes a project's aggregate counters from its
// current Sessions — used when building a filtered (group-scoped) view.
func RecountProject(p *Project) {
	p.Latest = time.Time{}
	p.SizeBytes = 0
	p.OK, p.Stale, p.Unbacked, p.RecoverOnly, p.Lost = 0, 0, 0, 0, 0
	p.Open, p.Active, p.ClaudeCount, p.CodexCount = 0, 0, 0, 0
	for _, s := range p.Sessions {
		if latest := newest(s); latest.After(p.Latest) {
			p.Latest = latest
		}
		p.SizeBytes += s.Size
		switch s.State {
		case "OK", "ACTIVE", "OPEN", "RESTORED", "REBUILT":
			p.OK++
			if s.State == "ACTIVE" {
				p.Active++
			}
		case "STALE_BACKUP":
			p.Stale++
		case "MISSING_BACKUP":
			p.Unbacked++
		case "MISSING_SOURCE":
			p.RecoverOnly++
		case "LOST":
			p.Lost++
		}
		if s.LiveStatus != "" {
			p.Open++
		}
		if s.Target == "claude" {
			p.ClaudeCount++
		} else {
			p.CodexCount++
		}
	}
}

// scopeProjects returns copies of the projects restricted to the given
// session ids (empty projects dropped, counters recomputed), so the
// normal browser can render a group as a scoped folder/session tree.
func scopeProjects(full []*Project, ids map[string]bool) []*Project {
	var out []*Project
	for _, p := range full {
		var kept []Session
		for _, s := range p.Sessions {
			if ids[s.ID] {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			continue
		}
		cp := *p
		cp.Sessions = kept
		RecountProject(&cp)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Latest.After(out[j].Latest) })
	return out
}

// scopeRootFor is the deepest directory containing every scoped project,
// so the group's tree is compact yet shows all members.
func scopeRootFor(projects []*Project) string {
	var abs []string
	for _, p := range projects {
		if filepath.IsAbs(p.Path) {
			abs = append(abs, p.Path)
		}
	}
	if len(abs) == 0 {
		return string(filepath.Separator)
	}
	common := abs[0]
	sep := string(filepath.Separator)
	for _, path := range abs[1:] {
		for !strings.HasPrefix(path+sep, common+sep) {
			parent := filepath.Dir(common)
			if parent == common {
				return sep
			}
			common = parent
		}
	}
	return common
}

// enterGroupScope swaps the browser onto a group's sessions so the normal
// folder/session panes, tab, and / filter operate on that group — the
// group view is the main browser, scoped.
func (m model) enterGroupScope(group Group) model {
	m.groupShowArchived = false
	ids := visibleGroupIDs(group, m.groupShowArchived)
	if m.fullProjects == nil {
		m.fullProjects = m.projects
	}
	m.groupOpen = &group
	m.projects = scopeProjects(m.fullProjects, ids)
	m.scopeRoot = scopeRootFor(m.projects)
	m.root = m.scopeRoot
	m.trail = nil
	m.showGroups = false
	m.query = ""
	m.resetPanes()
	// A group is a cross-folder set, so open straight into the all-nested
	// session view rather than the folder tree — the sessions are the point.
	m.showAll = true
	m.rebuild()
	return m
}

// rescopeGroup re-applies the active group scope to a fresh full-project
// set (after a rescan), keeping the browse position.
func (m model) rescopeGroup(full []*Project) model {
	ids := visibleGroupIDs(*m.groupOpen, m.groupShowArchived)
	m.fullProjects = full
	m.projects = scopeProjects(full, ids)
	m.rebuild()
	return m
}

// rescopeGroupTo updates the active group (e.g. after removing a member)
// and re-applies the scope against the saved full project set.
func (m model) rescopeGroupTo(group Group) model {
	g := group
	m.groupOpen = &g
	full := m.fullProjects
	if full == nil {
		full = m.projects
	}
	return m.rescopeGroup(full)
}

// exitGroupScope restores the full browser and returns to the group list.
func (m model) exitGroupScope() model {
	if m.fullProjects != nil {
		m.projects = m.fullProjects
		m.fullProjects = nil
	}
	m.groupOpen = nil
	m.scopeRoot = ""
	m.root = NearestRoot(m.projects, m.start)
	m.trail = nil
	m.query = ""
	m.resetPanes()
	m.showGroups = true
	m.rebuild()
	return m
}
