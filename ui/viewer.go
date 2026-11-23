package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"postit/md"
	"postit/store"
)

// viewer shows one post-it full size with its Markdown rendered.
type viewer struct {
	note store.Note
	vp   viewport.Model
}

func newViewer(n store.Note) viewer {
	vp := viewport.New(0, 0)
	vp.KeyMap.PageDown.SetKeys("pgdown", " ", "f", "ctrl+f")
	vp.KeyMap.PageUp.SetKeys("pgup", "b", "ctrl+b")
	return viewer{note: n, vp: vp}
}

// resize lays the rendered note out for a screen area of width x height.
func (v *viewer) resize(width, height int) {
	c := colorByName(v.note.Color)
	base := lipgloss.NewStyle().Background(c.BG).Foreground(c.FG)
	textW, textH := editorTextSize(width, height)
	content := md.Terminal(v.note.Body, textW, base)
	if strings.TrimSpace(v.note.Body) == "" {
		content = base.Width(textW).Italic(true).Render("(leer)")
	}
	v.vp.Width = textW
	v.vp.Height = min(textH, max(1, lipgloss.Height(content)))
	v.vp.Style = base
	v.vp.SetContent(content)
}

func (v viewer) view(width, height int) string {
	c := colorByName(v.note.Color)
	base := lipgloss.NewStyle().Background(c.BG).Foreground(c.FG)
	textW := v.vp.Width

	status := fmt.Sprintf("Farbe: %s   Prio: %s", c.Label, prioLabel(v.note.Priority))
	if m := prioMarker(v.note.Priority); m != "" {
		status += " " + m
	}
	if v.note.Pinned {
		status += "   " + pinMark + " angepinnt"
	}
	if v.vp.TotalLineCount() > v.vp.Height {
		pos := fmt.Sprintf("%3.f%%", v.vp.ScrollPercent()*100)
		gap := textW - lipgloss.Width(status) - lipgloss.Width(pos)
		status += strings.Repeat(" ", max(1, gap)) + pos
	}
	statusLine := base.Bold(true).Width(textW).Render(status)
	blank := base.Width(textW).Render("")

	content := strings.Join([]string{statusLine, blank, v.vp.View()}, "\n")
	box := base.
		Padding(1, 2).
		Border(lipgloss.ThickBorder()).
		BorderForeground(c.BG).
		Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
