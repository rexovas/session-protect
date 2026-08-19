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
	// No backend reachable: notice, no page.
	t.Setenv("PATH", t.TempDir())
	m.cfg.Assist = config.Assist{Backend: "auto", URL: "http://127.0.0.1:1"}
	m = press(t, m, tea.KeyCtrlG)
	if m.showAsk || m.notice == "" {
		t.Fatal("missing backend should notice, not open")
	}
	// Explicit ollama config opens without probing.
	m.cfg.Assist = config.Assist{Backend: "ollama", URL: "http://127.0.0.1:1"}
	m.notice = ""
	m = press(t, m, tea.KeyCtrlG)
	if !m.showAsk || m.askBackend != "ollama" {
		t.Fatalf("ask page state: %v %q", m.showAsk, m.askBackend)
	}
	m = press(t, m, "h", "i", tea.KeySpace, "x")
	if m.askInput != "hi x" {
		t.Fatalf("askInput = %q", m.askInput)
	}
	if view := m.View(); !strings.Contains(view, "ai find") || !strings.Contains(view, "hi x") {
		t.Fatal("ask view missing input")
	}
	m = press(t, m, tea.KeyEsc)
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
