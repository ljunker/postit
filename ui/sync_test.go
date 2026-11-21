package ui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"postit/store"
)

func TestSyncFlow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	calls := 0
	var fail error
	m := New(st, func(context.Context) (bool, error) { calls++; return true, fail })
	m.width, m.height = 120, 30

	// Init → tick → a running sync; a second request while running is queued.
	tick := m.Init()()
	model, cmd := m.Update(tick)
	m = model.(Model)
	if !m.sync.running || cmd == nil {
		t.Fatal("tick did not start a sync")
	}
	m.sync.gen = 7
	model, _ = m.Update(syncDebounceMsg{gen: 7})
	m = model.(Model)
	if !m.sync.again {
		t.Fatal("sync during sync not queued")
	}

	// Done → the queued sync starts at once; its result shows in the footer.
	model, cmd = m.Update(syncDoneMsg{changed: true})
	m = model.(Model)
	if !m.sync.running || cmd == nil {
		t.Fatal("queued sync not started")
	}
	done := cmd()
	if calls != 1 {
		t.Fatalf("sync called %d times", calls)
	}
	model, _ = m.Update(done)
	m = model.(Model)
	if m.syncStatus() == "" || m.sync.err != nil {
		t.Fatalf("status after success: %q %v", m.syncStatus(), m.sync.err)
	}

	// A local change schedules a debounced sync; stale debounces are ignored.
	model, cmd = m.newProject("Neu")
	m = model.(Model)
	if cmd == nil || m.sync.gen != 8 {
		t.Fatalf("no debounce after change (gen %d)", m.sync.gen)
	}
	model, cmd = m.Update(syncDebounceMsg{gen: 7})
	if cmd != nil || model.(Model).sync.running {
		t.Fatal("stale debounce started a sync")
	}

	// A failing sync shows offline.
	fail = errors.New("down")
	m.sync.running = false
	model, cmd = m.Update(syncDebounceMsg{gen: 8})
	m = model.(Model)
	model, _ = m.Update(cmd())
	if s := model.(Model).syncStatus(); s == "" || model.(Model).sync.err == nil {
		t.Fatalf("status after failure: %q", s)
	}
}

// newProject presses n in the project list, types name and confirms; it
// returns the command of the confirming key.
func (m Model) newProject(name string) (tea.Model, tea.Cmd) {
	var model tea.Model = m
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)})
	return model.Update(tea.KeyMsg{Type: tea.KeyEnter})
}
