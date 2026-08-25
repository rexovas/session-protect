package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/assist"
	"github.com/rexovas/session-protect/internal/audit"
	"github.com/rexovas/session-protect/internal/config"
)

// buildEnv fabricates a home with two nested claude projects and one lost
// session, then returns a model rooted at the work dir.
func buildEnv(t *testing.T) model {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	work := filepath.Join(home, "work")
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		work = filepath.Join(resolved, "work")
	}
	appPath := filepath.Join(work, "app")
	subPath := filepath.Join(work, "app", "sub")

	write := func(project string, id string, prompt string) {
		slug := ""
		for _, c := range project {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				slug += string(c)
			} else {
				slug += "-"
			}
		}
		path := filepath.Join(home, ".claude", "projects", slug, id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		content := `{"type":"user","cwd":"` + project + `","sessionId":"` + id + `","message":{"role":"user","content":"` + prompt + `"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"reply"}],"usage":{"input_tokens":5,"output_tokens":2}}}
`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(appPath, "aaaa1111-0000-0000-0000-000000000001", "searchable alpha prompt")
	write(subPath, "bbbb2222-0000-0000-0000-000000000002", "beta prompt")

	// History carries titles for the live sessions plus one permanently
	// lost session that exists nowhere else.
	now := time.Now().UnixMilli()
	history := fmt.Sprintf(`{"display":"searchable alpha prompt","sessionId":"aaaa1111-0000-0000-0000-000000000001","project":%q,"timestamp":%d}
{"display":"beta prompt","sessionId":"bbbb2222-0000-0000-0000-000000000002","project":%q,"timestamp":%d}
{"display":"the lost one","sessionId":"cccc3333-0000-0000-0000-000000000003","project":%q,"timestamp":%d}`,
		appPath, now, subPath, now, appPath, now)
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, []byte("backup_root = \""+filepath.Join(home, "root")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_PROTECT_CONFIG", configPath)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := model{cfg: cfg, projects: Scan(cfg), start: work, width: 120, height: 40}
	m.root = NearestRoot(m.projects, work)
	m.rebuild()
	return m
}

// deliver runs a command and feeds its message(s) back to the model,
// unwrapping batches (the AI commands ship with a spinner tick).
func deliver(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			m = deliver(t, m, sub)
		}
	case spinMsg:
		// animation only; nothing to assert through
	default:
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func press(t *testing.T, m model, keys ...any) model {
	t.Helper()
	for _, key := range keys {
		var msg tea.KeyMsg
		switch k := key.(type) {
		case tea.KeyType:
			msg = tea.KeyMsg{Type: k}
		case string:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		default:
			t.Fatalf("bad key %v", key)
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func TestNavigationDescendAndBack(t *testing.T) {
	m := buildEnv(t)
	if len(m.folders) != 1 || m.folders[0].Name != "app" {
		t.Fatalf("folders = %+v", m.folders)
	}
	root := m.root

	m = press(t, m, tea.KeyEnter) // descend into app
	if m.root == root || filepath.Base(m.root) != "app" {
		t.Fatalf("enter did not descend: %s", m.root)
	}
	m = press(t, m, tea.KeyEsc)
	if m.root != root {
		t.Fatalf("esc did not return: %s vs %s", m.root, root)
	}
}

func TestExpandAllToggle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyCtrlE)
	depths := map[int]bool{}
	for _, folder := range m.folders {
		depths[folder.Depth] = true
	}
	if !depths[1] {
		t.Fatalf("ctrl+e did not expand: %+v", m.folders)
	}
	m = press(t, m, tea.KeyCtrlE)
	for _, folder := range m.folders {
		if folder.Depth > 0 {
			t.Fatal("second ctrl+e did not collapse")
		}
	}
}

func TestFilterStaysInPane(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter) // into app: folder+session panes
	m = press(t, m, tea.KeyTab)   // sessions pane
	if !m.showSessions {
		t.Fatal("tab did not switch panes")
	}
	m = press(t, m, "/", "z", "z", "z")
	if !m.searching || m.query != "zzz" {
		t.Fatalf("search state: %v %q", m.searching, m.query)
	}
	if !m.showSessions {
		t.Fatal("empty result must not flip panes")
	}
	if view := m.View(); !strings.Contains(view, "no matches for /zzz") {
		t.Fatal("no-matches line missing")
	}
	m = press(t, m, tea.KeyEsc)
	if m.query != "" || m.searching {
		t.Fatal("esc did not clear the query")
	}
}

func TestFilterFindsSessionByTitle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions pane
	m = press(t, m, "/", "a", "l", "p", "h", "a")
	if len(m.visible) != 1 || !strings.Contains(m.visible[0].Title, "alpha") {
		t.Fatalf("filter missed the session: %+v", m.visible)
	}
	m = press(t, m, tea.KeyEnter) // keep filter
	if m.searching || m.query != "alpha" {
		t.Fatal("enter should keep the filter")
	}
}

func TestLostToggleAndSearchOverride(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab)
	base := len(m.visible)
	m = press(t, m, "x")
	if len(m.visible) != base+1 {
		t.Fatalf("x did not reveal the lost session: %d → %d", base, len(m.visible))
	}
	lostShown := false
	for _, session := range m.visible {
		if session.State == "LOST" {
			lostShown = true
		}
	}
	if !lostShown {
		t.Fatal("revealed session should be lost")
	}
	m = press(t, m, "x")
	if len(m.visible) != base {
		t.Fatal("x did not hide again")
	}
	// An active query overrides the toggle.
	m = press(t, m, "/", "l", "o", "s", "t")
	found := false
	for _, session := range m.visible {
		if session.State == "LOST" {
			found = true
		}
	}
	if !found {
		t.Fatal("search must find lost sessions with the toggle off")
	}
}

func TestMenuTabsAndKeysReference(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, "m")
	if !m.showMenu || m.menuTab != 0 {
		t.Fatalf("menu state: %v %d", m.showMenu, m.menuTab)
	}
	if view := m.View(); !strings.Contains(view, "PROTECTION") {
		t.Fatal("stats tab missing protection summary")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "no recorded actions yet") {
		t.Fatal("activity tab empty-state missing")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "NAVIGATE") || !strings.Contains(view, "transplant") {
		t.Fatal("keys tab reference missing")
	}
	m = press(t, m, tea.KeyEsc)
	if m.showMenu {
		t.Fatal("esc did not close the menu")
	}
	// s deep-links into stats.
	m = press(t, m, "s")
	if !m.showMenu || m.menuTab != 0 {
		t.Fatal("s should open the stats section")
	}
}

func TestDetailTabsLifecycle(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions
	m = press(t, m, "i")
	if m.detail == nil {
		t.Fatal("i did not open the inspector")
	}
	if view := m.View(); !strings.Contains(view, "initial prompt") || !strings.Contains(view, "searchable alpha prompt") {
		t.Fatalf("overview missing prompt box")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "CACHE READ") {
		t.Fatal("usage tab missing")
	}
	m = press(t, m, tea.KeyTab)
	if view := m.View(); !strings.Contains(view, "session start") {
		t.Fatal("transcript should show the start marker for a short session")
	}
	m = press(t, m, tea.KeyEsc)
	if m.detail != nil {
		t.Fatal("esc did not close the inspector")
	}
}

func TestHitsPaneLifecycle(t *testing.T) {
	m := buildEnv(t)
	m.hitsQuery = "alpha"
	next, _ := m.Update(hitsMsg([]Hit{
		{Session: m.projects[0].Sessions[0], Count: 7, Snippet: "searchable alpha prompt"},
	}))
	m = next.(model)
	if !m.showHits || len(m.hits) != 1 {
		t.Fatal("hitsMsg did not open the pane")
	}
	if view := m.View(); !strings.Contains(view, "HITS") || !strings.Contains(view, "7") {
		t.Fatal("hits view missing count")
	}
	m = press(t, m, tea.KeyEnter) // open the session from the hit
	if m.detail == nil {
		t.Fatal("enter on a hit should open detail")
	}
	m = press(t, m, tea.KeyEsc, tea.KeyEsc)
	if m.showHits {
		t.Fatal("esc did not leave the hits pane")
	}
}

func TestAskPageLifecycle(t *testing.T) {
	m := buildEnv(t)
	// No models available: notice, no page.
	assistModels = func(config.Assist) []assist.ModelOption { return nil }
	m = press(t, m, tea.KeyCtrlG)
	if m.showAsk || m.notice == "" {
		t.Fatal("missing backend should notice, not open")
	}

	assistModels = func(config.Assist) []assist.ModelOption {
		return []assist.ModelOption{
			{Backend: "claude", Model: "sonnet"},
			{Backend: "claude", Model: "opus"},
			{Backend: "ollama", Model: "qwen3:8b"},
		}
	}
	defer func() { assistModels = assist.AvailableModels }()
	m.notice = ""
	m = press(t, m, tea.KeyCtrlG)
	if !m.showAsk || m.askModel != 0 {
		t.Fatalf("ask page state: %v %d", m.showAsk, m.askModel)
	}
	if view := m.View(); !strings.Contains(view, "sonnet · claude") || !strings.Contains(view, "3 available") {
		t.Fatal("model selector missing")
	}
	// ←/→ cycle the model, typing still works.
	m = press(t, m, tea.KeyRight)
	if m.askModel != 1 {
		t.Fatalf("askModel = %d", m.askModel)
	}
	if view := m.View(); !strings.Contains(view, "opus · claude") {
		t.Fatal("selector did not advance")
	}
	m = press(t, m, tea.KeyLeft, tea.KeyLeft)
	if m.askModel != 2 {
		t.Fatalf("wraparound failed: %d", m.askModel)
	}
	m = press(t, m, "h", "i", tea.KeySpace, "x")
	if m.askInput != "hi x" {
		t.Fatalf("askInput = %q", m.askInput)
	}

	// Submit routes through the chosen option.
	var chosen []string
	assistRank = func(_ config.Assist, opt assist.ModelOption, _ string, _ []assist.Candidate) ([]assist.Match, error) {
		chosen = append(chosen, opt.Label())
		return nil, nil
	}
	defer func() { assistRank = assist.RankWith }()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter did not fire")
	}
	m = deliver(t, m, cmd)
	if len(chosen) != 1 || chosen[0] != "qwen3:8b · ollama" {
		t.Fatalf("chosen = %v", chosen)
	}
	m = press(t, m, tea.KeyCtrlG, tea.KeyEsc)
	if m.showAsk {
		t.Fatal("esc did not close the ask page")
	}
}

func TestTransplantPageOpenToggleClose(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, "t") // folder scope on app
	if !m.showTransplant || !strings.HasPrefix(m.tpSource, "project ") {
		t.Fatalf("transplant state: %v %q", m.showTransplant, m.tpSource)
	}
	if m.tpCopy {
		t.Fatal("default mode must be move")
	}
	m = press(t, m, tea.KeyTab)
	if !m.tpCopy {
		t.Fatal("tab did not toggle to copy")
	}
	if view := m.View(); !strings.Contains(view, "copy") || !strings.Contains(view, "target directory") {
		t.Fatal("transplant view missing elements")
	}
	m = press(t, m, tea.KeyEsc)
	if m.showTransplant {
		t.Fatal("esc did not close the transplant page")
	}
}

func TestResizeRebuildsLayout(t *testing.T) {
	m := buildEnv(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(model)
	if m.width != 80 || m.height != 24 {
		t.Fatal("resize not applied")
	}
	if view := m.View(); !strings.Contains(view, "Session Explorer") {
		t.Fatal("view broken after resize")
	}
}

func TestResumeDialogFlow(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions pane

	var spawned []string
	spawnWindow = func(command string) error {
		spawned = append(spawned, command)
		return nil
	}
	defer func() { spawnWindow = nil }()

	// o on a closed session arms the dialog, Cancel pre-selected.
	m = press(t, m, "o")
	if m.confirmResume == nil || m.confirmSel != 2 {
		t.Fatalf("dialog state: %v sel=%d", m.confirmResume != nil, m.confirmSel)
	}
	view := m.View()
	for _, want := range []string{"Open session?", "New window", "This terminal", "Cancel", "runs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog missing %q", want)
		}
	}

	// Reflexive enter on the default selection must NOT spawn.
	m = press(t, m, tea.KeyEnter)
	if m.confirmResume != nil || len(spawned) != 0 {
		t.Fatalf("enter on Cancel spawned: %v", spawned)
	}

	// Arrow back to New window (2→0 with two lefts), enter spawns.
	m = press(t, m, "o", tea.KeyLeft, tea.KeyLeft, tea.KeyEnter)
	if len(spawned) != 1 || !strings.Contains(spawned[0], "claude --resume aaaa1111") ||
		!strings.Contains(spawned[0], "cd '") {
		t.Fatalf("spawned = %v", spawned)
	}
	if m.notice == "" || m.noticeErr {
		t.Fatalf("no success notice: %q err=%v", m.notice, m.noticeErr)
	}

	// esc cancels, y confirms directly.
	m = press(t, m, "o", tea.KeyEsc)
	if m.confirmResume != nil {
		t.Fatal("esc did not cancel")
	}
	m = press(t, m, "o", "y")
	if len(spawned) != 2 {
		t.Fatal("y shortcut did not spawn")
	}
}

func TestResumeInThisTerminal(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab)
	m = press(t, m, "o", tea.KeyLeft) // Cancel(2) → This terminal(1)
	if m.confirmSel != 1 {
		t.Fatalf("sel = %d", m.confirmSel)
	}
	m = press(t, m, tea.KeyEnter)
	if len(m.execOnExit) != 3 || m.execOnExit[0] != "/bin/sh" ||
		!strings.Contains(m.execOnExit[2], "claude --resume") {
		t.Fatalf("execOnExit = %v", m.execOnExit)
	}
	if m.confirmResume != nil {
		t.Fatal("dialog should be closed")
	}
}

func TestRestoreDialogFlow(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab)
	// Fabricate a recover-state session in place.
	m.visible[0].State = "MISSING_SOURCE"
	m.visible[0].SourcePath = ""
	m.visible[0].BackupPath = "/tmp/backup/fake.jsonl"

	m = press(t, m, "r")
	if m.confirmRestore == nil || m.confirmSel != 1 {
		t.Fatalf("restore dialog state: %v sel=%d", m.confirmRestore != nil, m.confirmSel)
	}
	view := m.View()
	for _, want := range []string{"Restore session?", "Restore", "Cancel", "fake.jsonl"} {
		if !strings.Contains(view, want) {
			t.Fatalf("restore dialog missing %q", want)
		}
	}
	// tab toggles the selection; enter on Cancel closes without restoring.
	m = press(t, m, tea.KeyTab)
	if m.confirmSel != 0 {
		t.Fatal("tab did not select Restore")
	}
	m = press(t, m, tea.KeyTab, tea.KeyEnter)
	if m.confirmRestore != nil {
		t.Fatal("enter on Cancel did not close")
	}
}

func TestJumpUsesLivePID(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab)
	m.visible[0].LiveStatus = "open"
	m.visible[0].LivePID = 4242

	var focused []int
	focusSession = func(pid int) error {
		focused = append(focused, pid)
		return nil
	}
	defer func() { focusSession = nil }()

	m = press(t, m, "o")
	if len(focused) != 1 || focused[0] != 4242 {
		t.Fatalf("focused = %v", focused)
	}
	if m.confirmResume != nil {
		t.Fatal("open sessions must jump, not dialog")
	}
}

func TestResumeCommandPerAgent(t *testing.T) {
	claude := resumeCommand(Session{Target: "claude", ID: "abc"}, "/p/x")
	if claude != `cd '/p/x' && claude --resume abc` {
		t.Fatalf("claude command = %s", claude)
	}
	codex := resumeCommand(Session{Target: "codex", ID: "def"}, "/p y")
	if codex != `cd '/p y' && codex resume def` {
		t.Fatalf("codex command = %s", codex)
	}
}

func TestUpdateOfferFlow(t *testing.T) {
	m := buildEnv(t)
	applied := []string{}
	updateApply = func(tag string) (string, error) {
		applied = append(applied, tag)
		return "/fake/bin/session-protect", nil
	}
	defer func() { updateApply = nil }()

	next, _ := m.Update(updateAvailableMsg("v1.2.3"))
	m = next.(model)
	if m.updateOffer != "v1.2.3" || m.confirmSel != 1 {
		t.Fatalf("offer state: %q sel=%d", m.updateOffer, m.confirmSel)
	}
	view := m.View()
	for _, want := range []string{"Update to v1.2.3?", "latest", "v1.2.3", "Update", "Later"} {
		if !strings.Contains(view, want) {
			t.Fatalf("update dialog missing %q", want)
		}
	}

	// Reflexive enter = Later: dismissed, nothing applied.
	m = press(t, m, tea.KeyEnter)
	if m.updateOffer != "" || len(applied) != 0 {
		t.Fatalf("enter on Later applied: %v", applied)
	}

	// Offer again; arrow to Update; enter applies and quits to relaunch.
	next, _ = m.Update(updateAvailableMsg("v1.2.3"))
	m = next.(model)
	m = press(t, m, tea.KeyLeft)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.updateBusy || cmd == nil {
		t.Fatal("accept did not start the update")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	if len(applied) != 1 || applied[0] != "v1.2.3" {
		t.Fatalf("applied = %v", applied)
	}
	if len(m.execOnExit) == 0 || m.execOnExit[0] != "/fake/bin/session-protect" {
		t.Fatalf("execOnExit = %v", m.execOnExit)
	}
}

func TestRescueDialogFlow(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab, "x") // reveal lost
	// cursor onto the lost session
	for i, s := range m.visible {
		if s.State == "LOST" {
			m.sCursor = i
		}
	}

	var rebuilt, exported []string
	var dirs []string
	rescueReconstruct = func(_ config.Config, id string, _ string, dir string) (string, string, error) {
		rebuilt = append(rebuilt, id)
		dirs = append(dirs, dir)
		return "9e9e9e9e-0000-4000-8000-000000000000", "/fake/new.jsonl", nil
	}
	rescueExport = func(_ config.Config, _ string, id string, _ string, dir string) (string, error) {
		exported = append(exported, id)
		dirs = append(dirs, dir)
		return "/fake/exports/x.md", nil
	}
	defer func() { rescueReconstruct = nil; rescueExport = nil }()

	m = press(t, m, "r")
	if m.confirmRescue == nil || m.confirmSel != 2 {
		t.Fatalf("rescue dialog state: %v sel=%d", m.confirmRescue != nil, m.confirmSel)
	}
	view := m.View()
	for _, want := range []string{"Rescue lost session?", "Rebuild…", "Export prompts", "Cancel", "the lost one",
		"without touching anything"} { // Cancel is highlighted: its description shows
		if !strings.Contains(view, want) {
			t.Fatalf("rescue dialog missing %q", want)
		}
	}
	if strings.Contains(view, "Rebuild with AI") {
		t.Fatal("the rebuild flavors belong to page 2")
	}
	// The description follows the highlight.
	if view := press(t, m, tea.KeyLeft).View(); !strings.Contains(view, "markdown file") {
		t.Fatal("highlighting Export did not switch the description")
	}
	// Rebuild… advances to page 2 with Back as the safe default; esc
	// returns to page 1.
	m2 := press(t, m, tea.KeyLeft, tea.KeyLeft, tea.KeyEnter)
	if !m2.rescueHow || m2.confirmSel != 2 {
		t.Fatalf("page 2 state: how=%v sel=%d", m2.rescueHow, m2.confirmSel)
	}
	if view := m2.View(); !strings.Contains(view, "Rebuild how?") || !strings.Contains(view, "Rebuild with AI") {
		t.Fatal("page 2 missing the rebuild flavors")
	}
	m2 = press(t, m2, tea.KeyEsc)
	if m2.rescueHow || m2.confirmRescue == nil {
		t.Fatal("esc should return to page 1")
	}

	// Reflexive enter = Cancel.
	m = press(t, m, tea.KeyEnter)
	if m.confirmRescue != nil || len(rebuilt)+len(exported) != 0 {
		t.Fatal("enter on Cancel acted")
	}

	// Export path: choosing it opens the destination stage, prefilled with
	// the session's project directory once that physically exists.
	if err := os.MkdirAll(expandTilde(m2ProjectDir(t, m)), 0o755); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, "r", tea.KeyLeft, tea.KeyEnter)
	if m.rescueDest == nil || m.rescueAction != "export" {
		t.Fatalf("destination stage: dest=%v action=%q", m.rescueDest != nil, m.rescueAction)
	}
	if view := m.View(); !strings.Contains(view, "Export prompts — where?") {
		t.Fatal("destination stage view missing")
	}
	if m.rescueInput == "" {
		t.Fatal("destination not prefilled")
	}
	if view := m.View(); !strings.Contains(view, "use this directory") || !strings.Contains(view, "+ new folder") {
		t.Fatal("picker rows missing")
	}
	m = press(t, m, tea.KeyEnter) // cursor starts on "use this directory"
	if len(exported) != 1 || exported[0] != "cccc3333-0000-0000-0000-000000000003" {
		t.Fatalf("exported = %v", exported)
	}
	if len(dirs) != 1 || dirs[0] != expandTilde(m2ProjectDir(t, m)) {
		t.Fatalf("export dir = %v, want project dir", dirs)
	}
	if !strings.Contains(m.notice, "exported") {
		t.Fatalf("notice = %q", m.notice)
	}

	// Esc from the destination stage returns to the rescue dialog.
	m = press(t, m, "r", tea.KeyLeft, tea.KeyEnter, tea.KeyEsc)
	if m.rescueDest != nil || m.confirmRescue == nil {
		t.Fatal("esc did not return to the rescue dialog")
	}
	m = press(t, m, tea.KeyEsc)

	// Rebuild path (mechanical): page 1 Rebuild…, then page 2 Rebuild,
	// then confirm the prefilled project directory.
	m = press(t, m, "r", tea.KeyLeft, tea.KeyLeft, tea.KeyEnter, tea.KeyLeft, tea.KeyLeft, tea.KeyEnter)
	if m.rescueDest == nil || m.rescueAction != "rebuild" {
		t.Fatal("rebuild destination stage missing")
	}
	m = press(t, m, tea.KeyEnter)
	if len(rebuilt) != 1 || rebuilt[0] != "cccc3333-0000-0000-0000-000000000003" {
		t.Fatalf("rebuilt = %v", rebuilt)
	}
	if !strings.Contains(m.notice, "rebuilt as 9e9e9e9e") {
		t.Fatalf("notice = %q", m.notice)
	}

	// Completion opens the resume dialog for the NEW session, o-style,
	// with Later as the safe default.
	if m.confirmResume == nil || m.confirmResume.ID != "9e9e9e9e-0000-4000-8000-000000000000" {
		t.Fatalf("resume offer = %+v", m.confirmResume)
	}
	if view := m.View(); !strings.Contains(view, "Rebuilt as 9e9e9e9e") || !strings.Contains(view, "Later") {
		t.Fatal("rebuild-complete dialog missing")
	}
	if m.confirmSel != 2 {
		t.Fatal("Later must be the default")
	}
	// "This terminal" execs the resume on exit.
	m = press(t, m, tea.KeyLeft)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(m.execOnExit) == 0 || !strings.Contains(strings.Join(m.execOnExit, " "), "claude --resume 9e9e9e9e") {
		t.Fatalf("execOnExit = %v", m.execOnExit)
	}
	if m.resumeHeading != "" {
		t.Fatal("heading not cleared")
	}
}

func TestRescuePickerFilterAndJump(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab, "x")
	for i, s := range m.visible {
		if s.State == "LOST" {
			m.sCursor = i
		}
	}
	project := expandTilde(m2ProjectDir(t, m))
	for _, dir := range []string{"inner", "other", "third"} {
		if err := os.MkdirAll(filepath.Join(project, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var dirs []string
	rescueExport = func(_ config.Config, _ string, _ string, _ string, dir string) (string, error) {
		dirs = append(dirs, dir)
		return "/fake/x.md", nil
	}
	defer func() { rescueExport = nil }()
	m = press(t, m, "r", tea.KeyLeft, tea.KeyEnter) // export picker

	// Right-arrow on "use this directory" must be inert — never confirm.
	m = press(t, m, tea.KeyRight, tea.KeyRight, tea.KeyRight)
	if m.rescueDest == nil || len(dirs) != 0 {
		t.Fatalf("right arrow acted: dest=%v dirs=%v", m.rescueDest != nil, dirs)
	}

	// Letters do nothing while browsing; / enters filter mode like the
	// main list, and the filter narrows rows with the first match live.
	m = press(t, m, "inn")
	if m.rescueMode != "" || m.rescueFilter != "" {
		t.Fatalf("stray typing registered: mode=%q text=%q", m.rescueMode, m.rescueFilter)
	}
	m = press(t, m, "/", "inn")
	if got := m.rescueSubdirs(); m.rescueMode != "filter" || len(got) != 1 || got[0] != "inner" || m.rescueCursor != 2 {
		t.Fatalf("filter: mode=%q %v cursor=%d", m.rescueMode, got, m.rescueCursor)
	}
	if view := m.View(); !strings.Contains(view, "/inn") {
		t.Fatal("filter text not rendered")
	}
	// The RENDERED list must be the filtered one — the highlight and the
	// action work on it, so showing unfiltered rows points enter at the
	// wrong directory.
	if view := m.View(); strings.Contains(view, "other/") || strings.Contains(view, "third/") {
		t.Fatal("view shows unfiltered rows")
	}
	m = press(t, m, tea.KeyEnter) // descend into the match
	if want := tildePath(filepath.Join(project, "inner")); m.rescueInput != want || m.rescueMode != "" {
		t.Fatalf("descend: input=%q mode=%q", m.rescueInput, m.rescueMode)
	}

	// Backspace only edits text: it drains the filter, exits the mode,
	// and then does nothing — never up-dir, never closing the picker.
	here := m.rescueInput
	m = press(t, m, "/", "xy", tea.KeyBackspace, tea.KeyBackspace, tea.KeyBackspace)
	if m.rescueMode != "" {
		t.Fatalf("mode = %q", m.rescueMode)
	}
	m = press(t, m, tea.KeyBackspace, tea.KeyBackspace)
	if m.rescueDest == nil || m.rescueInput != here {
		t.Fatal("backspace navigated or closed the picker")
	}

	// Esc clears an active filter before it closes the picker.
	m = press(t, m, "/", "xyz")
	if m.rescueFilter != "xyz" {
		t.Fatalf("filter = %q", m.rescueFilter)
	}
	m = press(t, m, tea.KeyEsc)
	if m.rescueMode != "" || m.rescueDest == nil {
		t.Fatal("esc should only clear the filter")
	}

	// ~ opens a path jump, rendered as such.
	m = press(t, m, "~", "/somewhere/else")
	if view := m.View(); !strings.Contains(view, "go to: ~/somewhere/else") {
		t.Fatal("jump text not rendered")
	}
	m = press(t, m, tea.KeyEnter)
	if m.rescueInput != "~/somewhere/else" || m.rescueMode != "" {
		t.Fatalf("jump: input=%q mode=%q", m.rescueInput, m.rescueMode)
	}
	if view := m.View(); !strings.Contains(view, "will be created") {
		t.Fatal("jump target should show will-be-created")
	}
}

func TestLostSessionShowsRescuedAfterRebuild(t *testing.T) {
	m := buildEnv(t)
	const lost = "cccc3333-0000-0000-0000-000000000003"
	const rebuilt = "9e9e9e9e-0000-4000-8000-000000000000"
	if err := os.MkdirAll(m.cfg.BackupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	audit.Append(m.cfg.BackupRoot, []audit.Entry{{
		Time: time.Now(), Action: "reconstruct-ai", Target: "claude",
		SessionID: rebuilt, From: lost,
	}})
	find := func(projects []*Project, id string) *Session {
		for _, project := range projects {
			for i := range project.Sessions {
				if project.Sessions[i].ID == id {
					return &project.Sessions[i]
				}
			}
		}
		return nil
	}

	// An audit entry whose rebuild no longer exists anywhere must NOT
	// produce a rescued label pointing at nothing.
	found := find(Scan(m.cfg), lost)
	if found == nil || len(found.RebuiltAs) != 0 {
		t.Fatalf("dangling rebuild must not link: %+v", found)
	}

	// With the rebuilt transcript on disk the link appears, and the
	// reconstruction inherits the original's title — it is absent from
	// prompt history and must not display as a bare uuid.
	slug := ""
	for _, c := range found.ProjectPath {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			slug += string(c)
		} else {
			slug += "-"
		}
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "projects", slug, rebuilt+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","cwd":"` + found.ProjectPath + `","sessionId":"` + rebuilt + `","message":{"role":"user","content":"the lost one"}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects := Scan(m.cfg)
	found = find(projects, lost)
	if found == nil || len(found.RebuiltAs) != 1 || found.RebuiltAs[0] != rebuilt {
		t.Fatalf("lost session not linked: %+v", found)
	}
	rebuiltSession := find(projects, rebuilt)
	if rebuiltSession == nil || rebuiltSession.Title != "the lost one" {
		t.Fatalf("rebuilt title not inherited: %+v", rebuiltSession)
	}
	label, _ := sessionStateFor(*found)
	if !strings.Contains(label, "rescued") {
		t.Fatalf("state label = %q", label)
	}
	// The facet key stays lost so the lost filter still matches.
	if displayState(*found) != "lost" {
		t.Fatalf("facet key = %q", displayState(*found))
	}

	// A stale hits row picks up the new state on rescan.
	m.hits = []Hit{{Session: Session{ID: lost, State: "LOST"}}}
	next, _ := m.Update(rescanMsg(projects))
	m = next.(model)
	if len(m.hits[0].Session.RebuiltAs) != 1 {
		t.Fatal("hits row not refreshed by rescan")
	}
	m.projects = projects

	// The rescue dialog on a rescued original lists its reconstructions
	// and warns another will be created alongside.
	rescued := *find(projects, lost)
	m.projects = projects
	m = m.pressRescue(rescued)
	if m.confirmSel != 0 {
		t.Fatalf("Resume must be the default, sel=%d", m.confirmSel)
	}
	view := m.View()
	for _, want := range []string{"already rebuilt", rebuilt[:8], "ANOTHER session", "Resume"} {
		if !strings.Contains(view, want) {
			t.Fatalf("rescue dialog missing %q", want)
		}
	}
	// Enter on the Resume default routes straight to the rebuild's
	// resume dialog.
	m = press(t, m, tea.KeyEnter)
	if m.confirmRescue != nil || m.confirmResume == nil || m.confirmResume.ID != rebuilt {
		t.Fatalf("Resume routed to %+v", m.confirmResume)
	}
	m.confirmResume, m.resumeHeading = nil, ""

	// o on the rescued original routes to its newest rebuild.
	m = m.pressOpen(rescued)
	if m.confirmResume == nil || m.confirmResume.ID != rebuilt {
		t.Fatalf("o routed to %+v, want the rebuild", m.confirmResume)
	}
	if !strings.Contains(m.resumeHeading, "rebuild") {
		t.Fatalf("heading = %q", m.resumeHeading)
	}
	m.confirmResume, m.resumeHeading = nil, ""

	// A second reconstruction: o now opens a chooser listing both, and
	// picking one lands in its resume dialog.
	const second = "1a1a1a1a-0000-4000-8000-000000000000"
	audit.Append(m.cfg.BackupRoot, []audit.Entry{{
		Time: time.Now(), Action: "reconstruct", Target: "claude",
		SessionID: second, From: lost,
	}})
	secondPath := filepath.Join(filepath.Dir(path), second+".jsonl")
	if err := os.WriteFile(secondPath, []byte(strings.ReplaceAll(line, rebuilt, second)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects = Scan(m.cfg)
	m.projects = projects
	rescued = *find(projects, lost)
	if len(rescued.RebuiltAs) != 2 || rescued.RebuiltAs[0] != second {
		t.Fatalf("RebuiltAs = %v, want newest first", rescued.RebuiltAs)
	}
	m = m.pressOpen(rescued)
	if m.rebuildChoice == nil || m.confirmResume != nil {
		t.Fatal("several rebuilds must open the chooser")
	}
	view = m.View()
	for _, want := range []string{"Resume which rebuild?", second[:8], rebuilt[:8]} {
		if !strings.Contains(view, want) {
			t.Fatalf("chooser missing %q", want)
		}
	}
	// Cancel is default; arrow to the first (newest) rebuild and confirm.
	if m.confirmSel != 2 {
		t.Fatalf("chooser default = %d, want Cancel", m.confirmSel)
	}
	m = press(t, m, tea.KeyLeft, tea.KeyLeft, tea.KeyEnter)
	if m.rebuildChoice != nil || m.confirmResume == nil || m.confirmResume.ID != second {
		t.Fatalf("chosen resume = %+v", m.confirmResume)
	}
	m.confirmResume, m.resumeHeading = nil, ""

	// The inspector's middle tab becomes Recovery for lost sessions,
	// listing the reconstructions and the export state.
	m.detail = &rescued
	m.detailTab = 1
	view = m.View()
	for _, want := range []string{"Recovery", rebuilt[:8], "export", "reconstruct-ai"} {
		if !strings.Contains(view, want) {
			t.Fatalf("recovery tab missing %q", want)
		}
	}
	m.detail = nil

	// A rebuilt session's overview names its original.
	rebuiltSession = find(projects, rebuilt)
	m.detail = rebuiltSession
	m.detailTab = 0
	if view := m.View(); !strings.Contains(view, "rebuilt") || !strings.Contains(view, lost[:8]) {
		t.Fatal("overview missing rebuilt-from lineage")
	}
}

func TestRescuePickerNavigation(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab, "x")
	for i, s := range m.visible {
		if s.State == "LOST" {
			m.sCursor = i
		}
	}
	project := expandTilde(m2ProjectDir(t, m))
	if err := os.MkdirAll(filepath.Join(project, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	rescueExport = func(_ config.Config, _ string, _ string, _ string, dir string) (string, error) {
		dirs = append(dirs, dir)
		return "/fake/x.md", nil
	}
	defer func() { rescueExport = nil }()

	// Open the export picker: rows are use / .. / inner/ / + new folder.
	m = press(t, m, "r", tea.KeyLeft, tea.KeyEnter)
	if m.rescueDest == nil {
		t.Fatal("picker did not open")
	}

	// Descend into inner, then confirm.
	m = press(t, m, tea.KeyDown, tea.KeyDown, tea.KeyEnter)
	if want := tildePath(filepath.Join(project, "inner")); m.rescueInput != want {
		t.Fatalf("descend: input = %q, want %q", m.rescueInput, want)
	}
	// .. climbs back up.
	m = press(t, m, tea.KeyDown, tea.KeyEnter)
	if m.rescueInput != tildePath(project) {
		t.Fatalf("up: input = %q", m.rescueInput)
	}

	// New folder: last row starts naming; the typed name joins the path
	// without touching the disk until confirm.
	m = press(t, m, tea.KeyUp, tea.KeyEnter) // wraps to "+ new folder…"
	if !m.rescueNaming {
		t.Fatal("naming stage did not start")
	}
	m = press(t, m, "fresh", tea.KeyEnter)
	want := tildePath(filepath.Join(project, "fresh"))
	if m.rescueNaming || m.rescueInput != want {
		t.Fatalf("naming: input = %q, want %q", m.rescueInput, want)
	}
	if dirIsPresent(filepath.Join(project, "fresh")) {
		t.Fatal("folder must not exist before confirm")
	}
	if view := m.View(); !strings.Contains(view, "will be created") {
		t.Fatal("missing will-be-created note")
	}
	m = press(t, m, tea.KeyEnter) // cursor reset to "use this directory"
	if len(dirs) != 1 || dirs[0] != filepath.Join(project, "fresh") {
		t.Fatalf("export dir = %v", dirs)
	}
}

// m2ProjectDir returns the lost fixture session's project path.
func m2ProjectDir(t *testing.T, m model) string {
	t.Helper()
	for _, s := range m.visible {
		if s.State == "LOST" {
			return tildePath(s.ProjectPath)
		}
	}
	t.Fatal("no lost session in fixture")
	return ""
}

func TestOrphanedFolderMarker(t *testing.T) {
	m := buildEnv(t)
	// Delete the physical project dir out from under its sessions.
	if err := os.RemoveAll(m.folders[0].Path); err != nil {
		t.Fatal(err)
	}
	m.rebuild()
	if !m.folders[0].HomeGone {
		t.Fatal("HomeGone not detected")
	}
	if view := m.View(); !strings.Contains(view, "⌂!") {
		t.Fatal("orphan marker missing from folder row")
	}
}

func TestRescueAIFlow(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab, "x")
	for i, s := range m.visible {
		if s.State == "LOST" {
			m.sCursor = i
		}
	}
	assistModels = func(config.Assist) []assist.ModelOption {
		return []assist.ModelOption{
			{Backend: "claude", Model: "sonnet"},
			{Backend: "claude", Model: "opus"},
		}
	}
	var used []string
	rescueReconstructAI = func(_ config.Config, opt assist.ModelOption, id string, _ string, dir string) (string, string, error) {
		used = append(used, opt.Label()+" for "+id+" in "+dir)
		return "abcd1234-0000-4000-8000-000000000000", "/fake.jsonl", nil
	}
	defer func() { assistModels = assist.AvailableModels; rescueReconstructAI = nil }()

	// r → Rebuild… (page 1) → Rebuild with AI (page 2), then confirm the
	// prefilled destination.
	m = press(t, m, "r", tea.KeyLeft, tea.KeyLeft, tea.KeyEnter, tea.KeyLeft, tea.KeyEnter)
	if m.rescueDest == nil || m.rescueAction != "rebuild-ai" {
		t.Fatal("destination stage did not open")
	}
	m = press(t, m, tea.KeyEnter)
	if m.rescueAI == nil {
		t.Fatal("AI stage did not open")
	}
	if m.rescueModels[m.rescueModel].Model != "opus" {
		t.Fatalf("opus not default: %v", m.rescueModels[m.rescueModel])
	}
	view := m.View()
	for _, want := range []string{"Rebuild with AI?", "opus · claude", "NOT recovered"} {
		if !strings.Contains(view, want) {
			t.Fatalf("AI dialog missing %q", want)
		}
	}
	// ↑/↓ changes model; Cancel default guards enter.
	m = press(t, m, tea.KeyDown)
	if m.rescueModels[m.rescueModel].Model != "sonnet" {
		t.Fatal("model cycle failed")
	}
	m = press(t, m, tea.KeyUp, tea.KeyEnter)
	if m.rescueAI != nil || len(used) != 0 {
		t.Fatal("enter on Cancel acted")
	}
	if !strings.Contains(m.notice, "cancelled") {
		t.Fatalf("cancel must announce itself: %q", m.notice)
	}
	// Full accept.
	m = press(t, m, "r", tea.KeyLeft, tea.KeyLeft, tea.KeyEnter, tea.KeyLeft, tea.KeyEnter, tea.KeyEnter, tea.KeyLeft)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.rescueBusy || cmd == nil {
		t.Fatal("accept did not start synthesis")
	}
	// One ctrl+c mid-synthesis arms the guard instead of quitting.
	next, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if quitCmd != nil || !m.rescueQuitArm {
		t.Fatal("first ctrl+c must warn, not quit")
	}
	if view := m.View(); !strings.Contains(view, "ctrl+c again") {
		t.Fatal("quit warning not rendered")
	}
	m = deliver(t, m, cmd)
	if len(used) != 1 || !strings.Contains(used[0], "opus · claude for cccc3333") {
		t.Fatalf("used = %v", used)
	}
	if !strings.Contains(used[0], " in /") {
		t.Fatalf("destination not passed through: %v", used)
	}
	if !strings.Contains(m.notice, "rebuilt with AI as abcd1234") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestFacetFilterFlow(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions pane
	base := len(m.visible)

	m = press(t, m, "f")
	if !m.showFilter || len(m.filterItems) == 0 {
		t.Fatal("filter page did not open")
	}
	view := m.View()
	for _, want := range []string{"STATE", "AGENT", "MODIFIED", "lost", "claude"} {
		if !strings.Contains(view, want) {
			t.Fatalf("filter page missing %q", want)
		}
	}

	// Toggle "lost" (find its row), apply.
	for i, item := range m.filterItems {
		if item.kind == "state" && item.key == "lost" {
			m.filterCursor = i
		}
	}
	m = press(t, m, tea.KeySpace, tea.KeyEsc)
	if m.showFilter {
		t.Fatal("esc did not close")
	}
	if len(m.visible) != 1 || m.visible[0].State != "LOST" {
		t.Fatalf("lost-only filter wrong: %+v", m.visible)
	}
	if !strings.Contains(m.View(), "f:lost") {
		t.Fatal("footer chip missing")
	}

	// Clear restores everything.
	m = press(t, m, "f", "c", tea.KeyEsc)
	if len(m.visible) != base {
		t.Fatalf("clear did not restore: %d vs %d", len(m.visible), base)
	}

	// Agent facet: claude selects all here (fixture is claude-only).
	m = press(t, m, "f")
	for i, item := range m.filterItems {
		if item.kind == "agent" && item.key == "claude" {
			m.filterCursor = i
		}
	}
	m = press(t, m, tea.KeySpace, tea.KeyEsc)
	if len(m.visible) != base {
		t.Fatalf("claude facet wrong: %d", len(m.visible))
	}
}

func TestInspectorActsOnSession(t *testing.T) {
	m := buildEnv(t)
	m = press(t, m, tea.KeyEnter, tea.KeyTab) // app sessions

	// o from the inspector: closed session → resume dialog for THAT session.
	m = press(t, m, "i")
	if m.detail == nil {
		t.Fatal("inspector did not open")
	}
	inspected := m.detail.ID
	m = press(t, m, "o")
	if m.detail != nil {
		t.Fatal("o should close the inspector")
	}
	if m.confirmResume == nil || m.confirmResume.ID != inspected {
		t.Fatalf("resume dialog session = %v, want %s", m.confirmResume, inspected)
	}
	// The resume must land in the session's own project directory, not
	// wherever the browser is rooted (the browser here is rooted at work,
	// one level above the session's project).
	if base := filepath.Base(m.confirmResume.ProjectPath); base != "app" {
		t.Fatalf("resume project = %q, want the session's own dir", m.confirmResume.ProjectPath)
	}
	m = press(t, m, tea.KeyEsc)

	// r from the inspector on a lost session → rescue dialog.
	m = press(t, m, "x") // reveal lost
	for i, s := range m.visible {
		if s.State == "LOST" {
			m.sCursor = i
		}
	}
	m = press(t, m, "i")
	if m.detail == nil || m.detail.State != "LOST" {
		t.Fatal("inspector should open on the lost session")
	}
	lostID := m.detail.ID
	m = press(t, m, "r")
	if m.detail != nil {
		t.Fatal("r should close the inspector")
	}
	if m.confirmRescue == nil || m.confirmRescue.ID != lostID {
		t.Fatalf("rescue dialog session = %v, want %s", m.confirmRescue, lostID)
	}
}

func TestAskHistoryReplay(t *testing.T) {
	m := buildEnv(t)
	assistModels = func(config.Assist) []assist.ModelOption {
		return []assist.ModelOption{{Backend: "claude", Model: "sonnet"}}
	}
	var asked []string
	assistRank = func(_ config.Assist, _ assist.ModelOption, query string, _ []assist.Candidate) ([]assist.Match, error) {
		asked = append(asked, query)
		return []assist.Match{{ID: "aaaa1111-0000-0000-0000-000000000001", Reason: "matched alpha"}}, nil
	}
	defer func() { assistModels = assist.AvailableModels; assistRank = nil }()
	fire := func(m model, query string) model {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
		m = next.(model)
		m.askInput = ""
		m = press(t, m, query)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(model)
		m = deliver(t, m, cmd)
		return m
	}

	// Two searches land in history, newest first, persisted with results.
	m = fire(m, "first query")
	m = fire(m, "second query")
	if len(m.askHistory) != 2 || m.askHistory[0].Query != "second query" || m.askHistory[1].Query != "first query" {
		t.Fatalf("history = %+v", m.askHistory)
	}
	persisted := loadAskHistory(m.cfg)
	if len(persisted) != 2 || persisted[0].Query != "second query" {
		t.Fatalf("persisted = %+v", persisted)
	}
	if len(persisted[0].Results) != 1 || persisted[0].Results[0].ID != "aaaa1111-0000-0000-0000-000000000001" {
		t.Fatalf("results not saved: %+v", persisted[0].Results)
	}

	// Reopen: the list renders with the saved marker; ↓ recalls entries,
	// ↑ returns to the draft.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = next.(model)
	m.askInput = ""
	if view := m.View(); !strings.Contains(view, "recent searches") || !strings.Contains(view, "1 saved") {
		t.Fatal("history list or saved marker not rendered")
	}
	m = press(t, m, "dra")
	m = press(t, m, tea.KeyDown)
	if m.askInput != "second query" || m.askHistSel != 0 {
		t.Fatalf("recall: %q sel=%d", m.askInput, m.askHistSel)
	}
	m = press(t, m, tea.KeyDown)
	if m.askInput != "first query" {
		t.Fatalf("recall 2: %q", m.askInput)
	}
	m = press(t, m, tea.KeyUp, tea.KeyUp)
	if m.askInput != "dra" || m.askHistSel != -1 {
		t.Fatalf("draft restore: %q sel=%d", m.askInput, m.askHistSel)
	}

	// Enter on a recalled search replays the SAVED results — no model
	// call, sessions rehydrated live, the saved age shown.
	before := len(asked)
	m = press(t, m, tea.KeyDown, tea.KeyDown) // first query
	m = press(t, m, tea.KeyEnter)
	if len(asked) != before {
		t.Fatalf("cached replay called the model: %v", asked)
	}
	if !m.showHits || len(m.hits) != 1 || m.hits[0].Session.ID != "aaaa1111-0000-0000-0000-000000000001" {
		t.Fatalf("replayed hits = %+v", m.hits)
	}
	if m.hits[0].Session.Title == "" {
		t.Fatal("replayed session not rehydrated from the scan")
	}
	if m.hitsCached.IsZero() {
		t.Fatal("cached marker missing")
	}
	if view := m.View(); !strings.Contains(view, "saved") {
		t.Fatal("saved age not shown")
	}

	// A fresh run of the same text (not recalled) still asks the model.
	m = press(t, m, tea.KeyEsc)
	m = fire(m, "first query")
	if len(asked) != before+1 {
		t.Fatalf("fresh run did not ask the model: %v", asked)
	}
	if !m.hitsCached.IsZero() {
		t.Fatal("fresh results wrongly marked cached")
	}
	if m.askHistory[0].Query != "first query" || len(m.askHistory) != 2 {
		t.Fatalf("reorder: %+v", m.askHistory)
	}
}

func TestAskPageStaysOpenWhileWorking(t *testing.T) {
	m := buildEnv(t)
	assistModels = func(config.Assist) []assist.ModelOption {
		return []assist.ModelOption{{Backend: "claude", Model: "sonnet"}}
	}
	assistRank = func(_ config.Assist, _ assist.ModelOption, _ string, _ []assist.Candidate) ([]assist.Match, error) {
		return []assist.Match{{ID: "aaaa1111-0000-0000-0000-000000000001", Reason: "match"}}, nil
	}
	defer func() { assistModels = assist.AvailableModels; assistRank = nil }()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = next.(model)
	m.askInput = ""
	m = press(t, m, "find it")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	// The page stays open, visibly working, and ignores stray enters.
	if !m.showAsk || !m.hitsBusy {
		t.Fatalf("page closed during work: showAsk=%v busy=%v", m.showAsk, m.hitsBusy)
	}
	if view := m.View(); !strings.Contains(view, "asking sonnet") {
		t.Fatal("working line missing")
	}
	m = press(t, m, tea.KeyEnter, "zz")
	if m.askInput != "find it" {
		t.Fatal("edits must be ignored while working")
	}
	// Results arriving close the page and open the pane.
	m = deliver(t, m, cmd)
	if m.showAsk || !m.showHits || len(m.hits) != 1 {
		t.Fatalf("delivery: showAsk=%v showHits=%v hits=%d", m.showAsk, m.showHits, len(m.hits))
	}

	// Esc while working backgrounds the page; results still arrive.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = next.(model)
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m = press(t, m, tea.KeyEsc)
	if m.showAsk {
		t.Fatal("esc did not background the working page")
	}
	m = deliver(t, m, cmd)
	if !m.showHits {
		t.Fatal("backgrounded search did not deliver")
	}
}
