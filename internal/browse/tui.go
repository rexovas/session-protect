package browse

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rexovas/session-protect/internal/config"
	"github.com/rexovas/session-protect/internal/version"
)

var (
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7D79F6"})
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#999999", Dark: "#666666"})
	styleCursor   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#FFFFFF"}).Background(lipgloss.AdaptiveColor{Light: "#E8E8FF", Dark: "#333355"})
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0A8754", Dark: "#3DDC97"})
	styleStale    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#F5C518"})
	styleUnbacked = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF6B6B"})
	styleRecover  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8E44AD", Dark: "#C39BD3"})
	styleFooter   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#999999"})
)

type model struct {
	cfg      config.Config
	projects []*Project

	start string   // where the shell launched us
	root  string   // directory currently viewed
	trail []string // roots we descended through, for going back up

	folders []Folder
	here    *Project // project whose sessions live exactly at root

	// One pane is visible at a time; tab switches folders/sessions and
	// ctrl+a opens the recursive all-sessions pane. Each pane keeps its
	// own cursor so switching does not lose the user's place.
	showSessions bool
	showAll      bool
	allSessions  []Session
	detail       *Session
	detailData   Detail
	fCursor      int
	fOffset      int
	sCursor      int
	sOffset      int
	aCursor      int
	aOffset      int

	width  int
	height int
}

type rescanMsg []*Project

func newModel(cfg config.Config) model {
	projects := Scan(cfg)
	start, err := os.Getwd()
	if err != nil {
		start = string(os.PathSeparator)
	}
	m := model{cfg: cfg, projects: projects, start: start, width: 100, height: 32}
	m.root = NearestRoot(projects, start)
	m.rebuild()
	return m
}

func (m *model) rebuild() {
	m.folders = ChildrenOf(m.projects, m.root, m.start)
	m.here = ProjectAt(m.projects, m.root)
	if m.here != nil {
		LoadCustomNames(m.here)
	}
	if m.showAll {
		m.allSessions = AllUnder(m.projects, m.root)
	}
	// Show the pane that has content when the other is empty.
	if len(m.folders) == 0 && m.sessionCount() > 0 {
		m.showSessions = true
	}
	if m.sessionCount() == 0 && len(m.folders) > 0 {
		m.showSessions = false
	}
	m.setCursor(m.currentCursor())
}

func (m model) sessionCount() int {
	if m.here == nil {
		return 0
	}
	return len(m.here.Sessions)
}

func (m model) itemCount() int {
	switch {
	case m.showAll:
		return len(m.allSessions)
	case m.showSessions:
		return m.sessionCount()
	default:
		return len(m.folders)
	}
}

func (m model) currentCursor() int {
	switch {
	case m.showAll:
		return m.aCursor
	case m.showSessions:
		return m.sCursor
	default:
		return m.fCursor
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case rescanMsg:
		m.projects = msg
		m.rebuild()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.detail != nil {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "i", "esc", "q", "backspace", "h", "left", "enter":
			m.detail = nil
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "i":
		if session := m.selectedSession(); session != nil {
			m.detail = session
			m.detailData = LoadDetail(*session)
		}
	case "r":
		cfg := m.cfg
		return m, func() tea.Msg { return rescanMsg(Scan(cfg)) }
	case "tab":
		if m.showAll {
			m.showAll = false
			m.showSessions = len(m.folders) == 0 && m.sessionCount() > 0
		} else if len(m.folders) > 0 && m.sessionCount() > 0 {
			m.showSessions = !m.showSessions
		}
		m.setCursor(m.currentCursor())
	case "ctrl+a":
		m.showAll = !m.showAll
		if m.showAll {
			m.allSessions = AllUnder(m.projects, m.root)
			m.aCursor, m.aOffset = 0, 0
		}
		m.setCursor(m.currentCursor())
	case "up", "k":
		m.setCursor(m.currentCursor() - 1)
	case "down", "j":
		m.setCursor(m.currentCursor() + 1)
	case "pgup":
		m.setCursor(m.currentCursor() - m.pageSize())
	case "pgdown":
		m.setCursor(m.currentCursor() + m.pageSize())
	case "g":
		m.setCursor(0)
	case "G":
		m.setCursor(1 << 30)
	case "enter", "l", "right":
		if !m.showSessions && !m.showAll && m.fCursor < len(m.folders) {
			m.trail = append(m.trail, m.root)
			m.root = m.folders[m.fCursor].Path
			m.resetPanes()
			m.rebuild()
		}
	case "esc", "backspace", "h", "left":
		// Leave the current pane first, but never land on an empty pane:
		// at a leaf folder (sessions only) back means up.
		if m.showAll {
			m.showAll = false
			m.showSessions = len(m.folders) == 0 && m.sessionCount() > 0
			m.setCursor(m.currentCursor())
			return m, nil
		}
		if m.showSessions && len(m.folders) > 0 {
			m.showSessions = false
			m.setCursor(m.currentCursor())
			return m, nil
		}
		m.goUp()
	}
	return m, nil
}

func (m model) selectedSession() *Session {
	switch {
	case m.showAll:
		if m.aCursor < len(m.allSessions) {
			return &m.allSessions[m.aCursor]
		}
	case m.showSessions:
		if m.here != nil && m.sCursor < len(m.here.Sessions) {
			return &m.here.Sessions[m.sCursor]
		}
	}
	return nil
}

func (m *model) resetPanes() {
	m.fCursor, m.fOffset, m.sCursor, m.sOffset, m.aCursor, m.aOffset = 0, 0, 0, 0, 0, 0
	m.showSessions = false
	m.showAll = false
}

func (m *model) goUp() {
	target := ""
	if len(m.trail) > 0 {
		target = m.trail[len(m.trail)-1]
		m.trail = m.trail[:len(m.trail)-1]
	} else if parent := parentDir(m.root); parent != m.root {
		target = parent
	}
	if target == "" {
		return
	}
	m.root = target
	m.resetPanes()
	m.rebuild()
}

func parentDir(path string) string {
	if !strings.HasPrefix(path, string(os.PathSeparator)) {
		return path // pseudo root; only the trail can leave it
	}
	parent := path
	if idx := strings.LastIndex(path, string(os.PathSeparator)); idx > 0 {
		parent = path[:idx]
	} else if idx == 0 && path != string(os.PathSeparator) {
		parent = string(os.PathSeparator)
	}
	return parent
}

func (m *model) setCursor(position int) {
	if position < 0 {
		position = 0
	}
	if position > m.itemCount()-1 {
		position = max(0, m.itemCount()-1)
	}
	switch {
	case m.showAll:
		m.aCursor = position
		m.aOffset = clampOffset(m.aOffset, position, m.pageSize())
	case m.showSessions:
		m.sCursor = position
		m.sOffset = clampOffset(m.sOffset, position, m.pageSize())
	default:
		m.fCursor = position
		m.fOffset = clampOffset(m.fOffset, position, m.pageSize())
	}
}

func (m model) pageSize() int { return max(4, m.height-7) }

// shortCommit is the installed build's revision, shown faintly for
// dogfooding traceability.
func shortCommit() string {
	commit := version.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if commit == "unknown" {
		return ""
	}
	return commit
}

// nameWidth is the flexible folder-name column: everything the fixed
// columns don't use.
func (m model) nameWidth() int { return max(24, m.width-46) }

// titleWidth is the flexible session-title column; the all pane reserves
// room for the IN column.
func (m model) titleWidth() int {
	width := m.width - 41
	if m.showAll {
		width -= 31
	}
	return max(24, width)
}

func clampOffset(offset int, cursor int, page int) int {
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+page {
		return cursor - page + 1
	}
	return offset
}

func (m model) View() string {
	if m.detail != nil {
		return m.detailView()
	}
	var b strings.Builder

	title := styleHeader.Render("Session Explorer")
	if commit := shortCommit(); commit != "" {
		title += styleDim.Render("  " + commit)
	}
	b.WriteString(title + "\n")

	total := 0
	for _, folder := range m.folders {
		total += folder.Sessions
	}
	total += m.sessionCount()
	name := m.root
	if idx := strings.LastIndex(m.root, string(os.PathSeparator)); idx >= 0 && len(m.root) > idx+1 {
		name = m.root[idx+1:]
	}
	beneath := fmt.Sprintf("  total nested sessions %d ", total)
	countStyle := styleDim
	if m.showAll {
		countStyle = styleCursor // the all pane is exactly this set
	}
	left := styleHeader.Render("▸ "+truncate(name, 40)) + countStyle.Render(beneath)
	right := m.tabBar()
	if pad := m.width - lipgloss.Width(left) - lipgloss.Width(right); pad > 0 {
		left += strings.Repeat(" ", pad)
	}
	b.WriteString(left + right + "\n")
	b.WriteString(styleFooter.Render(strings.Repeat("─", max(m.width, 10))) + "\n")

	cursor := m.currentCursor()
	switch {
	case m.showAll:
		b.WriteString(styleDim.Render(fmt.Sprintf("  %-11s  %-*s %-7s %8s  %-8s %s",
			"STATE", m.titleWidth(), "TITLE", "AGENT", "SIZE", "MODIFIED", "IN")) + "\n")
		end := min(len(m.allSessions), m.aOffset+m.pageSize())
		for i := m.aOffset; i < end; i++ {
			b.WriteString(m.allSessionRow(m.allSessions[i], i == cursor) + "\n")
		}
	case m.showSessions:
		b.WriteString(styleDim.Render(fmt.Sprintf("  %-11s  %-*s %-7s %8s  %s",
			"STATE", m.titleWidth(), "TITLE", "AGENT", "SIZE", "MODIFIED")) + "\n")
		end := min(m.sessionCount(), m.sOffset+m.pageSize())
		for i := m.sOffset; i < end; i++ {
			b.WriteString(m.sessionRow(m.here.Sessions[i], i == cursor) + "\n")
		}
	default:
		b.WriteString(styleDim.Render(fmt.Sprintf("   %-*s  %8s  %8s  %-9s%s",
			m.nameWidth(), "FOLDER", "SESSIONS", "SIZE", "LAST USED", " BACKUP")) + "\n")
		end := min(len(m.folders), m.fOffset+m.pageSize())
		for i := m.fOffset; i < end; i++ {
			b.WriteString(m.folderRow(m.folders[i], i == cursor) + "\n")
		}
	}
	if m.itemCount() == 0 {
		b.WriteString(styleDim.Render("  nothing here") + "\n")
	}

	help := "↑/↓ move · enter open · ← up · tab sessions · ctrl+a all · q quit"
	if m.showSessions || m.showAll {
		help = "↑/↓ move · i info · ← folders · tab folders · ctrl+a all · q quit"
	}
	return m.pinBottom(b.String(), help)
}

// pinBottom pads the content so the footer block hugs the window bottom:
// path (left) with the activity legend (right), a separator, then key help
// on the very last line.
func (m model) pinBottom(content string, help string) string {
	legend := styleOK.Render("●") + styleDim.Render(" <1h  ") +
		styleStale.Render("●") + styleDim.Render(" today  ") + styleDim.Render("○ older  ") +
		styleOK.Render("▶") + styleDim.Render(" open now ")
	pathLine := styleDim.Render(" " + truncate(m.root, m.width-lipgloss.Width(legend)-3))
	if pad := m.width - lipgloss.Width(pathLine) - lipgloss.Width(legend); pad > 0 {
		pathLine += strings.Repeat(" ", pad)
	}
	footer := []string{
		pathLine + legend,
		styleFooter.Render(strings.Repeat("─", max(m.width, 10))),
		styleFooter.Render(" " + help),
	}
	used := strings.Count(content, "\n")
	if pad := m.height - used - len(footer); pad > 0 {
		content += strings.Repeat("\n", pad)
	}
	return content + strings.Join(footer, "\n")
}

// tabBar renders the pane switcher; the active pane is highlighted. The all
// pane has no segment — the "sessions beneath" count highlights instead.
func (m model) tabBar() string {
	segment := func(label string, active bool) string {
		if active {
			return styleCursor.Render(label)
		}
		return styleDim.Render(label)
	}
	return segment(fmt.Sprintf(" Folders %d ", len(m.folders)), !m.showSessions && !m.showAll) +
		segment(fmt.Sprintf(" Sessions %d ", m.sessionCount()), m.showSessions && !m.showAll)
}

// allSessionRow is a session row whose trailing column shows where the
// session lives relative to the current root.
func (m model) allSessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	live := " "
	if session.LiveStatus != "" {
		live = styleOK.Render("▶")
	}
	row := fmt.Sprintf("%s%s  %s %-7s %8s  %-8s %s",
		live, style.Render(state), sessionTitle(session, m.titleWidth()), session.Target,
		formatBytes(session.Size), ago(session.Modified), styleDim.Render(truncate(m.relOfRoot(session), 30)))
	if active {
		return styleCursor.Render(row)
	}
	return row
}

// detailView is the full-stat inspector for one session (i to open/close).
func (m model) detailView() string {
	session := *m.detail
	data := m.detailData
	width := max(min(m.width-2, 110), 40)
	inner := width - 4 // box border + padding

	var b strings.Builder

	name := session.CustomName
	if name == "" {
		name = truncate(session.Title, 60)
	}
	if name == "" {
		name = session.ID
	}
	b.WriteString(styleHeader.Render("▸ "+name) + "\n\n")

	state, style := sessionState(session.State)
	liveNote := ""
	if session.LiveStatus != "" {
		liveNote = styleOK.Render("  ▶ open now (" + session.LiveStatus + ")")
	}
	kv := func(key string, value string) {
		b.WriteString(styleDim.Render(fmt.Sprintf(" %-10s", key)) + value + "\n")
	}
	kv("state", strings.TrimSpace(style.Render(state))+liveNote)
	kv("agent", session.Target)
	kv("session", session.ID)
	project := session.ProjectPath
	if project == "" {
		project = m.root
	}
	kv("project", tildePath(project))
	stamp := formatBytes(session.Size)
	if !data.Created.IsZero() {
		stamp += styleDim.Render("  ·  started ") + ago(data.Created)
	}
	stamp += styleDim.Render("  ·  modified ") + ago(session.Modified)
	if session.BackupModified.IsZero() {
		stamp += styleDim.Render("  ·  backup ") + styleUnbacked.Render("never")
	} else {
		stamp += styleDim.Render("  ·  backup ") + ago(session.BackupModified)
	}
	kv("size", stamp)
	kvPath := func(key string, path string) {
		if path == "" {
			return
		}
		lines := chunk(tildePath(path), m.width-13)
		kv(key, lines[0])
		for _, line := range lines[1:] {
			b.WriteString(strings.Repeat(" ", 11) + line + "\n")
		}
	}
	kvPath("source", session.SourcePath)
	kvPath("backup", session.BackupPath)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "#BBBBBB", Dark: "#555555"}).
		Padding(0, 1).
		Width(width - 2)

	first := data.FirstPrompt
	if first == "" {
		first = session.Title
	}
	if first != "" {
		b.WriteString("\n" + styleDim.Render(" SESSION START") + "\n")
		b.WriteString(box.Render(strings.Join(wrapText(first, inner, 5), "\n")) + "\n")
	}

	if data.LastPrompt != "" || data.LastResponse != "" {
		responseLines := max(4, m.height-24)
		var tail []string
		if data.LastPrompt != "" {
			prompt := wrapText(data.LastPrompt, inner-2, 4)
			tail = append(tail, styleDim.Render("❯ ")+prompt[0])
			for _, line := range prompt[1:] {
				tail = append(tail, "  "+line)
			}
		}
		if data.LastResponse != "" {
			if len(tail) > 0 {
				tail = append(tail, "")
			}
			tail = append(tail, wrapText(data.LastResponse, inner, responseLines)...)
		}
		b.WriteString("\n" + styleDim.Render(" SESSION TAIL") + "\n")
		b.WriteString(box.Render(strings.Join(tail, "\n")) + "\n")
	}

	return m.pinBottom(b.String(), "i/esc close · ctrl+c quit")
}

// tildePath abbreviates the home directory for display.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home); ok {
		return "~" + rest
	}
	return path
}

// chunk hard-wraps an unbroken string (like a path) into width-sized pieces.
func chunk(s string, width int) []string {
	if width < 10 {
		width = 10
	}
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	return append(out, s)
}

// wrapText breaks text into at most maxLines lines of the given width,
// ellipsizing the remainder.
func wrapText(text string, width int, maxLines int) []string {
	if width < 10 {
		width = 10
	}
	words := strings.Fields(text)
	var lines []string
	current := ""
	for _, word := range words {
		if current != "" && len(current)+1+len(word) > width {
			lines = append(lines, current)
			if len(lines) == maxLines {
				lines[maxLines-1] = truncate(lines[maxLines-1]+" …", width)
				return lines
			}
			current = word
			continue
		}
		if current == "" {
			current = word
		} else {
			current += " " + word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (m model) relOfRoot(session Session) string {
	if rel, ok := strings.CutPrefix(session.ProjectPath, m.root+string(os.PathSeparator)); ok {
		return rel
	}
	return "."
}

func (m model) folderRow(folder Folder, active bool) string {
	glyph, glyphStyle := activityGlyph(folder.Latest)
	name := truncate(folder.Name, m.nameWidth()-1) + "/"
	if folder.Pseudo {
		name = truncate(folder.Name, m.nameWidth())
	}

	health := ""
	if folder.Open > 0 {
		health += styleOK.Render(fmt.Sprintf(" ▶%d", folder.Open))
	}
	if folder.Stale > 0 {
		health += styleStale.Render(fmt.Sprintf(" ~%d", folder.Stale))
	}
	if folder.Unbacked > 0 {
		health += styleUnbacked.Render(fmt.Sprintf(" !%d", folder.Unbacked))
	}
	if folder.RecoverOnly > 0 {
		health += styleRecover.Render(fmt.Sprintf(" ✝%d", folder.RecoverOnly))
	}
	if health == "" {
		health = styleOK.Render(" ok")
	}

	row := fmt.Sprintf(" %s %-*s  %8d  %8s  %-9s%s",
		glyphStyle.Render(glyph), m.nameWidth(), name, folder.Sessions, formatBytes(folder.SizeBytes), ago(folder.Latest), health)
	if active {
		return styleCursor.Render(row)
	}
	return row
}

func (m model) sessionRow(session Session, active bool) string {
	state, style := sessionState(session.State)
	live := " "
	if session.LiveStatus != "" {
		live = styleOK.Render("▶")
	}
	row := fmt.Sprintf("%s%s  %s %-7s %8s  %s",
		live, style.Render(state), sessionTitle(session, m.titleWidth()), session.Target,
		formatBytes(session.Size), ago(session.Modified))
	if active {
		return styleCursor.Render(row)
	}
	return row
}

// sessionTitle renders the display title in a fixed-width cell: custom names
// bright, first-prompt fallback dim.
func sessionTitle(session Session, width int) string {
	switch {
	case session.CustomName != "":
		return fmt.Sprintf("%-*s", width, truncate(session.CustomName, width))
	case session.Title != "":
		return styleDim.Render(fmt.Sprintf("%-*s", width, truncate(session.Title, width)))
	default:
		return styleDim.Render(fmt.Sprintf("%-*s", width, "(not set)"))
	}
}

func sessionState(state string) (string, lipgloss.Style) {
	switch state {
	case "OK":
		return "● ok       ", styleOK
	case "STALE_BACKUP":
		return "~ stale    ", styleStale
	case "MISSING_BACKUP":
		return "! unbacked ", styleUnbacked
	default:
		return "✝ recover  ", styleRecover
	}
}

func activityGlyph(t time.Time) (string, lipgloss.Style) {
	switch since := time.Since(t); {
	case since < time.Hour:
		return "●", styleOK
	case since < 24*time.Hour:
		return "●", styleStale
	default:
		return "○", styleDim
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ago renders a compact relative age; the columns' meaning makes the "ago"
// implicit, and it never falls back to absolute dates.
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	since := time.Since(t)
	switch {
	case since < time.Minute:
		return "now"
	case since < time.Hour:
		return fmt.Sprintf("%dm", int(since.Minutes()))
	case since < 24*time.Hour:
		return fmt.Sprintf("%dh", int(since.Hours()))
	case since < 60*24*time.Hour:
		return fmt.Sprintf("%dd", int(since.Hours()/24))
	case since < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(since.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy", int(since.Hours()/24/365))
	}
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}
