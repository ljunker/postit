package ui

import (
	"github.com/charmbracelet/lipgloss"

	"postit/palette"
)

// NoteColor is one entry of the post-it palette.
type NoteColor struct {
	Name  string // stored in the database
	Label string // shown in the UI
	BG    lipgloss.Color
	FG    lipgloss.Color
}

// Palette is the list of colors a post-it can have; c / C cycle through it.
var Palette = func() []NoteColor {
	out := make([]NoteColor, len(palette.Colors))
	for i, c := range palette.Colors {
		out[i] = NoteColor{c.Name, c.Label, lipgloss.Color(c.BG), lipgloss.Color(c.FG)}
	}
	return out
}()

func colorIndex(name string) int {
	for i, c := range Palette {
		if c.Name == name {
			return i
		}
	}
	return 0
}

func colorByName(name string) NoteColor { return Palette[colorIndex(name)] }

// cycleColor returns the color name dir steps away from name in the palette.
func cycleColor(name string, dir int) string {
	n := len(Palette)
	return Palette[((colorIndex(name)+dir)%n+n)%n].Name
}

var (
	accent    = lipgloss.Color("#FFD866")
	dim       = lipgloss.Color("#6C6C6C")
	errColor  = lipgloss.Color("#FF6B6B")
	prioColor = lipgloss.Color("#C0001A")

	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dim)
	activePaneStyle = paneStyle.BorderForeground(accent)

	headerStyle   = lipgloss.NewStyle().Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(dim)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#1E1E1E")).Background(accent).Bold(true)
	cursorStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	errStyle      = lipgloss.NewStyle().Foreground(errColor).Bold(true)
	helpKeyStyle  = lipgloss.NewStyle().Foreground(accent)
	helpDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9A9A9A"))
)

func prioMarker(p int) string {
	switch p {
	case 1:
		return "!"
	case 2:
		return "!!"
	case 3:
		return "!!!"
	}
	return ""
}

func prioLabel(p int) string {
	switch p {
	case 1:
		return "niedrig"
	case 2:
		return "mittel"
	case 3:
		return "hoch"
	}
	return "keine"
}

const pinMark = "📌"
