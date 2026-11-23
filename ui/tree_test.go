package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"postit/store"
)

func TestSubprojectKeys(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, name := range []string{"A", "B", "C"} {
		if _, err := st.CreateProject(name); err != nil {
			t.Fatal(err)
		}
	}
	var model tea.Model = New(st, nil)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press := func(keys ...string) {
		for _, k := range keys {
			var msg tea.KeyMsg
			switch k {
			case "enter":
				msg = tea.KeyMsg{Type: tea.KeyEnter}
			case "esc":
				msg = tea.KeyMsg{Type: tea.KeyEsc}
			default:
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
			}
			model, _ = model.Update(msg)
		}
	}
	tree := func() string {
		var out []string
		for _, p := range model.(Model).projects {
			out = append(out, strings.Repeat("-", p.Depth)+p.Name)
		}
		return strings.Join(out, ",")
	}
	selected := func() string {
		m := model.(Model)
		p, _ := m.currentProject()
		return p.Name
	}

	// C under A: targets are (top), A, B; start on (top), so j once = A.
	press("G", "m")
	if m := model.(Model); m.mode != modeMove || !strings.Contains(m.View(), "Unterprojekt von") {
		t.Fatal("m did not open the parent picker")
	}
	press("j", "enter")
	if got := tree(); got != "Inbox,A,-C,B" || selected() != "C" {
		t.Fatalf("after m: %s, selected %s", got, selected())
	}
	if !strings.Contains(model.(Model).View(), "  C") {
		t.Fatal("C not indented in the project pane")
	}

	// K/J stays among siblings: C is A's only child, B stays where it is.
	press("K")
	if got := tree(); got != "Inbox,A,-C,B" {
		t.Fatalf("K on only child: %s", got)
	}

	// A cannot go under its own child: C is not offered.
	press("g", "j", "m")
	m := model.(Model)
	for _, tg := range m.moveTargets() {
		if tg.Name == "C" || tg.Name == "A" || tg.IsInbox {
			t.Fatalf("invalid target offered: %s", tg.Name)
		}
	}
	press("esc")

	// Back to the top level.
	press("j", "m", "k", "k", "enter")
	if got := tree(); got != "Inbox,A,B,C" {
		t.Fatalf("C back to top: %s", got)
	}
}
