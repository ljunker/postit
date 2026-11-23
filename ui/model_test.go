package ui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"postit/store"
)

func TestMoveProjectKeys(t *testing.T) {
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
	press := func(k tea.KeyMsg) {
		model, _ = model.Update(k)
	}
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
	order := func() string {
		var out []string
		for _, p := range model.(Model).projects {
			out = append(out, p.Name)
		}
		return strings.Join(out, ",")
	}
	selected := func() string {
		m := model.(Model)
		p, _ := m.currentProject()
		return p.Name
	}

	press(key('G')) // C
	press(key('K'))
	if got := order(); got != "Inbox,A,C,B" || selected() != "C" {
		t.Fatalf("after K: %s, selected %s", got, selected())
	}
	press(tea.KeyMsg{Type: tea.KeyShiftUp})
	press(key('K')) // already right below the inbox: nothing happens
	if got := order(); got != "Inbox,C,A,B" || selected() != "C" {
		t.Fatalf("after shift+up and K: %s, selected %s", got, selected())
	}
	press(key('J'))
	if got := order(); got != "Inbox,A,C,B" || selected() != "C" {
		t.Fatalf("after J: %s, selected %s", got, selected())
	}

	press(key('g')) // inbox
	press(key('J'))
	if got := order(); got != "Inbox,A,C,B" || model.(Model).msg == "" {
		t.Fatalf("inbox moved or no message: %s", got)
	}
}
