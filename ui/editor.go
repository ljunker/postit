package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"

	"postit/store"
)

// editor shows one post-it full size and lets the user edit its text.
type editor struct {
	note store.Note // ID 0 = not saved yet
	ta   textarea.Model
}

func newEditor(n store.Note) editor {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.Placeholder = "Titel in der ersten Zeile …"
	ta.SetValue(n.Body)
	ta.Focus()
	e := editor{note: n, ta: ta}
	e.applyColor()
	return e
}

func (e *editor) applyColor() {
	c := colorByName(e.note.Color)
	base := lipgloss.NewStyle().Background(c.BG).Foreground(c.FG)
	s := textarea.Style{
		Base:             base,
		CursorLine:       base,
		CursorLineNumber: base,
		EndOfBuffer:      base,
		LineNumber:       base,
		Placeholder:      base.Foreground(lipgloss.Color("#7A7A7A")).Italic(true),
		Prompt:           base,
		Text:             base,
	}
	e.ta.FocusedStyle = s
	e.ta.BlurredStyle = s
}

func (e *editor) cycleColor(dir int) {
	e.note.Color = cycleColor(e.note.Color, dir)
	e.applyColor()
}

// editorTextSize is the textarea size for a screen area of width x height.
func editorTextSize(width, height int) (int, int) {
	boxW := min(width-6, 72)  // inner width incl. padding
	boxH := min(height-4, 22) // inner height incl. padding
	return max(10, boxW-4), max(3, boxH-4)
}

func (e *editor) resize(width, height int) {
	w, h := editorTextSize(width, height)
	e.ta.SetWidth(w)
	e.ta.SetHeight(h)
}

func (e editor) view(width, height int) string {
	c := colorByName(e.note.Color)
	base := lipgloss.NewStyle().Background(c.BG).Foreground(c.FG)
	textW, _ := editorTextSize(width, height)

	status := fmt.Sprintf("Farbe: %s   Prio: %s", c.Label, prioLabel(e.note.Priority))
	if m := prioMarker(e.note.Priority); m != "" {
		status += " " + m
	}
	if e.note.Pinned {
		status += "   " + pinMark + " angepinnt"
	}
	statusLine := base.Bold(true).Width(textW).Render(status)
	blank := base.Width(textW).Render("")

	content := strings.Join([]string{statusLine, blank, e.ta.View()}, "\n")
	box := base.
		Padding(1, 2).
		Border(lipgloss.ThickBorder()).
		BorderForeground(c.BG).
		Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
