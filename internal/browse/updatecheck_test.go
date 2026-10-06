package browse

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rexovas/session-protect/internal/update"
	"github.com/rexovas/session-protect/internal/version"
)

// stubReleaseCheck makes this a release build whose check reports latest.
func stubReleaseCheck(t *testing.T, latest string) *time.Duration {
	t.Helper()
	channel := version.Channel
	version.Channel = "release"
	var interval time.Duration
	updateCheck = func(_ string, every time.Duration) (string, bool) {
		interval = every
		return latest, true
	}
	t.Cleanup(func() {
		version.Channel = channel
		updateCheck = update.ThrottledCheck
	})
	return &interval
}

// A window left open re-checks once the interval has passed, so it still
// learns about a release published after it launched.
func TestUpdateCheckRepeatsWhileOpen(t *testing.T) {
	interval := stubReleaseCheck(t, "v9.9.9")
	m := model{lastUpdateCheck: time.Now()}
	if _, cmd := m.maybeCheckUpdate(); cmd != nil {
		t.Fatal("re-checked before the interval passed")
	}
	m.lastUpdateCheck = time.Now().Add(-updateCheckEvery - time.Minute)
	next, cmd := m.maybeCheckUpdate()
	if cmd == nil || time.Since(next.lastUpdateCheck) > time.Minute {
		t.Fatal("no re-check after the interval")
	}
	msg, ok := cmd().(updateAvailableMsg)
	if !ok || string(msg) != "v9.9.9" {
		t.Fatalf("check returned %v", msg)
	}
	if *interval != updateCheckEvery {
		t.Fatalf("cache window = %v, want %v", *interval, updateCheckEvery)
	}
	next.updateOffer = "v9.9.9"
	next.lastUpdateCheck = time.Time{}
	if _, cmd := next.maybeCheckUpdate(); cmd != nil {
		t.Fatal("re-checked while an offer is already showing")
	}
}

// "Later" skips that version for the session; a newer one is still offered.
func TestDeclinedUpdateNotReoffered(t *testing.T) {
	m := model{}
	next, _ := m.Update(updateAvailableMsg("v9.9.9"))
	m = next.(model)
	if m.updateOffer != "v9.9.9" {
		t.Fatal("offer not shown")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.updateOffer != "" || m.updateDeclined != "v9.9.9" {
		t.Fatalf("decline not recorded: offer=%q declined=%q", m.updateOffer, m.updateDeclined)
	}
	next, _ = m.Update(updateAvailableMsg("v9.9.9"))
	if next.(model).updateOffer != "" {
		t.Fatal("declined version offered again")
	}
	next, _ = m.Update(updateAvailableMsg("v10.0.0"))
	if next.(model).updateOffer != "v10.0.0" {
		t.Fatal("newer version not offered after declining an older one")
	}
}

// Source builds never check.
func TestNoUpdateCheckOnSourceBuild(t *testing.T) {
	channel := version.Channel
	version.Channel = "source"
	defer func() { version.Channel = channel }()
	m := model{}
	if _, cmd := m.maybeCheckUpdate(); cmd != nil {
		t.Fatal("source build ran the release check")
	}
}
