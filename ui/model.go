// Package ui is the Bubble Tea interface: projects on the left, a board of
// post-its on the right, and a full-size editor for a single post-it.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"postit/store"
)

type mode int

const (
	modeProjects mode = iota // focus on the project list
	modeBoard                // focus on the board
	modeEdit                 // editing one post-it
	modeInput                // typing a project name
	modeConfirm              // waiting for y/n
	modeMove                 // choosing a project to move a post-it to
)

const projectPaneW = 28 // outer width of the project pane

type Model struct {
	st *store.Store

	mode     mode
	prevMode mode // where to return after input / confirm / move
	width    int
	height   int
	showHelp bool
	msg      string // error or info shown in the status line

	projects []store.Project
	projSel  int
	projOff  int

	notes    []store.Note
	noteSel  int
	boardOff int

	editor editor

	input       textinput.Model
	inputLabel  string
	inputSubmit func(*Model, string) error

	confirmText   string
	confirmAction func(*Model) error

	moveSel int

	sync syncState
}

// New builds the UI. sync may be nil when syncing is not configured.
func New(st *store.Store, sync SyncFunc) Model {
	ti := textinput.New()
	ti.CharLimit = 60
	m := Model{st: st, input: ti, sync: syncState{fn: sync}}
	m.reloadProjects()
	m.reloadNotes(0)
	return m
}

func (m Model) Init() tea.Cmd { return m.syncInit() }

// ---- data ---------------------------------------------------------------

func (m *Model) fail(err error) bool {
	if err != nil {
		m.msg = "Fehler: " + err.Error()
		return true
	}
	return false
}

func (m *Model) currentProject() (store.Project, bool) {
	if m.projSel < 0 || m.projSel >= len(m.projects) {
		return store.Project{}, false
	}
	return m.projects[m.projSel], true
}

func (m *Model) currentNote() (store.Note, bool) {
	if m.noteSel < 0 || m.noteSel >= len(m.notes) {
		return store.Note{}, false
	}
	return m.notes[m.noteSel], true
}

func (m *Model) reloadProjects() {
	var curID int64
	if p, ok := m.currentProject(); ok {
		curID = p.ID
	}
	ps, err := m.st.ListProjects()
	if m.fail(err) {
		return
	}
	m.projects = ps
	m.selectProject(curID)
}

func (m *Model) selectProject(id int64) {
	for i, p := range m.projects {
		if p.ID == id {
			m.projSel = i
			return
		}
	}
	m.projSel = max(0, min(m.projSel, len(m.projects)-1))
}

// reloadNotes reloads the current project's notes and keeps noteID selected
// if it is still there (pin and priority change the order).
func (m *Model) reloadNotes(noteID int64) {
	p, ok := m.currentProject()
	if !ok {
		m.notes = nil
		return
	}
	ns, err := m.st.ListNotes(p.ID)
	if m.fail(err) {
		return
	}
	m.notes = ns
	for i, n := range ns {
		if n.ID == noteID {
			m.noteSel = i
			return
		}
	}
	m.noteSel = max(0, min(m.noteSel, len(ns)-1))
}

// refresh reloads everything after a change to note noteID.
func (m *Model) refresh(noteID int64) {
	m.reloadProjects()
	m.reloadNotes(noteID)
}

// ---- update -------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if model, cmd, ok := m.updateSync(msg); ok {
		return model, cmd
	}
	before := m.st.Changes()
	model, cmd := m.update(msg)
	if m.st.Changes() != before {
		mm := model.(Model)
		return mm, tea.Batch(cmd, mm.syncSoon())
	}
	return model, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.mode == modeEdit {
			m.editor.resize(m.width, m.mainHeight())
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if m.mode == modeEdit {
				m.saveEditor()
			}
			return m, tea.Quit
		}
		if m.mode != modeEdit && m.mode != modeInput {
			m.msg = ""
		}
		switch m.mode {
		case modeProjects:
			return m.updateProjects(msg)
		case modeBoard:
			return m.updateBoard(msg)
		case modeEdit:
			return m.updateEditor(msg)
		case modeInput:
			return m.updateInput(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modeMove:
			return m.updateMove(msg)
		}
	}
	if m.mode == modeEdit {
		var cmd tea.Cmd
		m.editor.ta, cmd = m.editor.ta.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateProjects(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	prev := m.projSel
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "j", "down":
		m.projSel = min(m.projSel+1, len(m.projects)-1)
	case "k", "up":
		m.projSel = max(m.projSel-1, 0)
	case "g", "home":
		m.projSel = 0
	case "G", "end":
		m.projSel = len(m.projects) - 1
	case "enter", "l", "right", "tab":
		m.mode = modeBoard
	case "n":
		return m.startInput("Neues Projekt", "", func(m *Model, name string) error {
			id, err := m.st.CreateProject(name)
			if err == nil {
				m.reloadProjects()
				m.selectProject(id)
				m.noteSel, m.boardOff = 0, 0
				m.reloadNotes(0)
			}
			return err
		})
	case "r":
		p, ok := m.currentProject()
		if !ok || p.IsInbox {
			m.msg = "Die Inbox kann nicht umbenannt werden."
			break
		}
		return m.startInput("Umbenennen", p.Name, func(m *Model, name string) error {
			err := m.st.RenameProject(p.ID, name)
			m.reloadProjects()
			return err
		})
	case "d", "x":
		p, ok := m.currentProject()
		if !ok || p.IsInbox {
			m.msg = "Die Inbox kann nicht gelöscht werden."
			break
		}
		text := fmt.Sprintf("Projekt „%s“ mit %d Post-it(s) löschen?", p.Name, p.NoteCount)
		m.startConfirm(text, func(m *Model) error {
			err := m.st.DeleteProject(p.ID)
			m.reloadProjects()
			m.noteSel, m.boardOff = 0, 0
			m.reloadNotes(0)
			return err
		})
	}
	if m.projSel != prev {
		m.noteSel, m.boardOff = 0, 0
		m.reloadNotes(0)
	}
	return m, nil
}

func (m Model) updateBoard(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	cols := boardCols(m.boardInnerWidth())
	n, hasNote := m.currentNote()
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "tab", "esc":
		m.mode = modeProjects
	case "h", "left":
		if m.noteSel%cols == 0 {
			m.mode = modeProjects
		} else {
			m.noteSel--
		}
	case "l", "right":
		if m.noteSel%cols < cols-1 && m.noteSel+1 < len(m.notes) {
			m.noteSel++
		}
	case "j", "down":
		if m.noteSel+cols < len(m.notes) {
			m.noteSel += cols
		} else if (len(m.notes)-1)/cols > m.noteSel/cols {
			m.noteSel = len(m.notes) - 1
		}
	case "k", "up":
		if m.noteSel-cols >= 0 {
			m.noteSel -= cols
		}
	case "g", "home":
		m.noteSel = 0
	case "G", "end":
		m.noteSel = max(0, len(m.notes)-1)
	case "n", "a":
		p, ok := m.currentProject()
		if ok {
			return m.openEditor(store.Note{ProjectID: p.ID, Color: Palette[0].Name})
		}
	case "enter", "e":
		if hasNote {
			return m.openEditor(n)
		}
	case "c", "C":
		if hasNote {
			dir := 1
			if k.String() == "C" {
				dir = -1
			}
			m.fail(m.st.SetColor(n.ID, cycleColor(n.Color, dir)))
			m.refresh(n.ID)
		}
	case "p", " ":
		if hasNote {
			m.fail(m.st.TogglePinned(n.ID))
			m.refresh(n.ID)
		}
	case "+", "=", "-", "0", "1", "2", "3":
		if hasNote {
			prio := n.Priority
			switch s := k.String(); s {
			case "+", "=":
				prio++
			case "-":
				prio--
			default:
				prio = int(s[0] - '0')
			}
			m.fail(m.st.SetPriority(n.ID, prio))
			m.refresh(n.ID)
		}
	case "m":
		if hasNote && len(m.projects) > 1 {
			m.prevMode, m.mode = m.mode, modeMove
			m.moveSel = 0
		} else if hasNote {
			m.msg = "Kein anderes Projekt vorhanden – mit n im Projekt-Bereich anlegen."
		}
	case "d", "x":
		if hasNote {
			title := n.Title()
			if title == "" {
				title = "(leer)"
			}
			m.startConfirm(fmt.Sprintf("Post-it „%s“ löschen?", ansi.Truncate(title, 40, "…")), func(m *Model) error {
				err := m.st.DeleteNote(n.ID)
				m.refresh(0)
				return err
			})
		}
	}
	return m, nil
}

// ---- editor -------------------------------------------------------------

func (m Model) openEditor(n store.Note) (tea.Model, tea.Cmd) {
	m.editor = newEditor(n)
	m.editor.resize(m.width, m.mainHeight())
	m.mode = modeEdit
	return m, nil
}

// saveEditor stores the editor's note. A new note with no text is dropped.
func (m *Model) saveEditor() {
	n := m.editor.note
	n.Body = strings.TrimRight(m.editor.ta.Value(), " \n")
	if n.ID == 0 {
		if strings.TrimSpace(n.Body) == "" {
			return
		}
		id, err := m.st.CreateNote(n.ProjectID, n.Body, n.Color)
		if m.fail(err) {
			return
		}
		n.ID = id
	}
	m.fail(m.st.SaveNote(n))
	m.refresh(n.ID)
}

func (m Model) updateEditor(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.editor
	switch k.String() {
	case "esc", "ctrl+s":
		m.saveEditor()
		m.mode = modeBoard
		return m, nil
	case "ctrl+p":
		e.note.Pinned = !e.note.Pinned
		return m, nil
	case "ctrl+n":
		e.cycleColor(1)
		return m, nil
	case "ctrl+b":
		e.cycleColor(-1)
		return m, nil
	case "alt+up", "ctrl+up":
		e.note.Priority = min(store.MaxPriority, e.note.Priority+1)
		return m, nil
	case "alt+down", "ctrl+down":
		e.note.Priority = max(0, e.note.Priority-1)
		return m, nil
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(k)
	return m, cmd
}

// ---- input / confirm / move ----------------------------------------------

func (m Model) startInput(label, value string, submit func(*Model, string) error) (tea.Model, tea.Cmd) {
	m.prevMode, m.mode = m.mode, modeInput
	m.inputLabel = label
	m.inputSubmit = submit
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m Model) updateInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = m.prevMode
		m.input.Blur()
		return m, nil
	case "enter":
		m.mode = m.prevMode
		m.input.Blur()
		if strings.TrimSpace(m.input.Value()) != "" {
			m.fail(m.inputSubmit(&m, m.input.Value()))
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Model) startConfirm(text string, action func(*Model) error) {
	m.prevMode, m.mode = m.mode, modeConfirm
	m.confirmText = text
	m.confirmAction = action
}

func (m Model) updateConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = m.prevMode
	if s := k.String(); s == "y" || s == "j" || s == "enter" {
		m.fail(m.confirmAction(&m))
	}
	return m, nil
}

// moveTargets are all projects except the one the note is in.
func (m *Model) moveTargets() []store.Project {
	p, _ := m.currentProject()
	var out []store.Project
	for _, q := range m.projects {
		if q.ID != p.ID {
			out = append(out, q)
		}
	}
	return out
}

func (m Model) updateMove(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	targets := m.moveTargets()
	switch k.String() {
	case "esc", "q":
		m.mode = m.prevMode
	case "j", "down":
		m.moveSel = min(m.moveSel+1, len(targets)-1)
	case "k", "up":
		m.moveSel = max(m.moveSel-1, 0)
	case "enter":
		m.mode = m.prevMode
		n, ok := m.currentNote()
		if ok && m.moveSel < len(targets) {
			t := targets[m.moveSel]
			if !m.fail(m.st.MoveNote(n.ID, t.ID)) {
				m.msg = fmt.Sprintf("Verschoben nach „%s“.", t.Name)
			}
			m.refresh(0)
		}
	}
	return m, nil
}

// ---- view ---------------------------------------------------------------

func (m Model) footerHeight() int {
	if m.showHelp && (m.mode == modeProjects || m.mode == modeBoard) {
		return len(fullHelp)
	}
	return 1
}

func (m Model) mainHeight() int { return max(3, m.height-m.footerHeight()) }

func (m Model) boardInnerWidth() int { return max(1, m.width-projectPaneW-2) }

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	h := m.mainHeight()
	var main string
	if m.mode == modeEdit {
		main = m.editor.view(m.width, h)
	} else {
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.viewProjects(h), m.viewBoard(h))
	}
	return lipgloss.JoinVertical(lipgloss.Left, main, m.viewFooter())
}

func (m *Model) viewProjects(h int) string {
	innerW, innerH := projectPaneW-2, h-2
	lines := []string{headerStyle.Render("Projekte"), ""}
	listH := max(1, innerH-len(lines))
	if m.projSel < m.projOff {
		m.projOff = m.projSel
	}
	if m.projSel >= m.projOff+listH {
		m.projOff = m.projSel - listH + 1
	}
	for i := m.projOff; i < min(len(m.projects), m.projOff+listH); i++ {
		p := m.projects[i]
		count := fmt.Sprint(p.NoteCount)
		name := ansi.Truncate(p.Name, innerW-len(count)-3, "…")
		pad := innerW - 2 - lipgloss.Width(name) - len(count)
		line := name + strings.Repeat(" ", max(1, pad)) + count
		switch {
		case i == m.projSel && m.mode == modeProjects:
			line = selectedStyle.Render("› " + line)
		case i == m.projSel:
			line = cursorStyle.Render("› " + line)
		default:
			line = "  " + line
		}
		lines = append(lines, line)
	}
	st := paneStyle
	if m.mode == modeProjects {
		st = activePaneStyle
	}
	return st.Width(innerW).Height(innerH).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

func (m *Model) viewBoard(h int) string {
	innerW, innerH := m.boardInnerWidth()-2, h-2
	p, _ := m.currentProject()
	header := headerStyle.Render(p.Name) + mutedStyle.Render(fmt.Sprintf("  %d Post-it(s)", len(m.notes)))

	var body string
	if m.mode == modeMove {
		body = m.viewMove(innerW, innerH-2)
	} else {
		body, m.boardOff = renderBoard(m.notes, m.noteSel, m.boardOff, innerW, innerH-2, m.mode == modeBoard || m.mode == modeConfirm)
	}
	st := paneStyle
	if m.mode == modeBoard || m.mode == modeMove {
		st = activePaneStyle
	}
	return st.Width(innerW).Height(innerH).MaxHeight(h).Render(header + "\n\n" + body)
}

func (m Model) viewMove(w, h int) string {
	n, _ := m.currentNote()
	lines := []string{headerStyle.Render("Verschieben nach …"), mutedStyle.Render(ansi.Truncate(n.Title(), 30, "…")), ""}
	for i, t := range m.moveTargets() {
		if i == m.moveSel {
			lines = append(lines, selectedStyle.Render("› "+t.Name+" "))
		} else {
			lines = append(lines, "  "+t.Name)
		}
	}
	box := activePaneStyle.Padding(1, 3).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

var fullHelp = [][2]string{
	{"tab", "Fokus Projekte ⇄ Board"},
	{"hjkl / ←↓↑→", "bewegen"},
	{"n", "neues Projekt / Post-it"},
	{"enter", "Post-it öffnen und bearbeiten"},
	{"c / C", "Farbe vor / zurück"},
	{"p", "anpinnen"},
	{"+ - 0-3", "Priorität"},
	{"m", "Post-it in anderes Projekt verschieben"},
	{"r / d", "Projekt umbenennen / löschen bzw. Post-it löschen"},
	{"? / q", "Hilfe / beenden"},
}

func helpLine(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, helpKeyStyle.Render(pairs[i])+" "+helpDescStyle.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

func (m Model) viewFooter() string {
	switch m.mode {
	case modeInput:
		return headerStyle.Render(m.inputLabel+": ") + m.input.View() + "  " + helpLine("enter", "ok", "esc", "abbrechen")
	case modeConfirm:
		return errStyle.Render(m.confirmText) + "  " + helpLine("y", "ja", "n", "nein")
	}
	if m.msg != "" {
		st := mutedStyle
		if strings.HasPrefix(m.msg, "Fehler") {
			st = errStyle
		}
		return st.Render(m.msg)
	}
	var s string
	switch m.mode {
	case modeEdit:
		s = helpLine("esc", "speichern & zurück", "ctrl+n/b", "Farbe", "ctrl+p", "pin", "alt+↑/↓", "Prio")
	case modeMove:
		s = helpLine("j/k", "wählen", "enter", "verschieben", "esc", "abbrechen")
	case modeProjects:
		if m.showHelp {
			return m.viewFullHelp()
		}
		s = helpLine("n", "neu", "r", "umbenennen", "d", "löschen", "enter", "öffnen", "?", "hilfe", "q", "beenden")
	case modeBoard:
		if m.showHelp {
			return m.viewFullHelp()
		}
		s = helpLine("n", "neu", "enter", "bearbeiten", "c", "farbe", "p", "pin", "+/-", "prio", "m", "verschieben", "d", "löschen", "?", "hilfe")
	}
	status := m.syncStatus()
	if status == "" {
		return ansi.Truncate(s, m.width, "…")
	}
	room := m.width - lipgloss.Width(status) - 2
	s = ansi.Truncate(s, max(0, room), "…")
	return s + strings.Repeat(" ", max(2, m.width-lipgloss.Width(s)-lipgloss.Width(status))) + status
}

func (m Model) viewFullHelp() string {
	var lines []string
	for _, h := range fullHelp {
		lines = append(lines, ansi.Truncate(helpKeyStyle.Width(14).Render(h[0])+helpDescStyle.Render(h[1]), m.width, "…"))
	}
	return strings.Join(lines, "\n")
}
