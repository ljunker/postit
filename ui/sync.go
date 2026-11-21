package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// SyncFunc syncs the store with the server; changed reports whether the
// local data now differs so the UI must reload.
type SyncFunc func(ctx context.Context) (changed bool, err error)

const (
	syncInterval = 30 * time.Second // background sync
	syncDebounce = 2 * time.Second  // after a local change
	syncTimeout  = 15 * time.Second
)

type syncState struct {
	fn      SyncFunc
	running bool
	again   bool  // a sync was requested while one was running
	gen     int   // debounce generation: only the last change triggers
	ok      bool  // at least one sync succeeded
	err     error // result of the last sync
}

type (
	syncTickMsg     struct{}
	syncDebounceMsg struct{ gen int }
	syncDoneMsg     struct {
		changed bool
		err     error
	}
)

// syncInit syncs right away and then every syncInterval.
func (m Model) syncInit() tea.Cmd {
	if m.sync.fn == nil {
		return nil
	}
	return func() tea.Msg { return syncTickMsg{} }
}

// syncSoon schedules a sync shortly after the latest local change.
func (m *Model) syncSoon() tea.Cmd {
	if m.sync.fn == nil {
		return nil
	}
	m.sync.gen++
	gen := m.sync.gen
	return tea.Tick(syncDebounce, func(time.Time) tea.Msg { return syncDebounceMsg{gen} })
}

// startSync runs one sync in the background, or queues one if a sync is
// already running.
func (m *Model) startSync() tea.Cmd {
	if m.sync.running {
		m.sync.again = true
		return nil
	}
	m.sync.running = true
	fn := m.sync.fn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		defer cancel()
		changed, err := fn(ctx)
		return syncDoneMsg{changed, err}
	}
}

// updateSync handles the sync messages; ok is false for every other message.
func (m Model) updateSync(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case syncTickMsg:
		next := tea.Tick(syncInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
		return m, tea.Batch(m.startSync(), next), true
	case syncDebounceMsg:
		if msg.gen != m.sync.gen {
			return m, nil, true
		}
		return m, m.startSync(), true
	case syncDoneMsg:
		m.sync.running = false
		m.sync.err = msg.err
		if msg.err == nil {
			m.sync.ok = true
		}
		if msg.changed {
			var noteID int64
			if n, ok := m.currentNote(); ok {
				noteID = n.ID
			}
			m.refresh(noteID)
		}
		var cmd tea.Cmd
		if m.sync.again {
			m.sync.again = false
			cmd = m.startSync()
		}
		return m, cmd, true
	}
	return m, nil, false
}

// syncStatus is the short sync indicator at the right of the footer.
func (m Model) syncStatus() string {
	switch {
	case m.sync.fn == nil:
		return ""
	case m.sync.running:
		return mutedStyle.Render("⟳ sync")
	case m.sync.err != nil:
		return mutedStyle.Render("○ offline")
	case m.sync.ok:
		return mutedStyle.Render("✓ sync")
	}
	return ""
}
