package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"postit/md"
	"postit/store"
)

const (
	tileTextW  = 20 // text width inside a tile
	tileTextH  = 3  // header line + two title lines
	tileGap    = 1  // blank columns between tiles
	tileOuterW = tileTextW + 2 /*padding*/ + 2 /*border*/ + tileGap
	tileOuterH = tileTextH + 2 /*border*/
)

// boardCols is how many tiles fit next to each other in width columns.
func boardCols(width int) int {
	return max(1, width/tileOuterW)
}

func renderTile(n store.Note, selected bool) string {
	c := colorByName(n.Color)
	base := lipgloss.NewStyle().Background(c.BG).Foreground(c.FG)

	// Header: priority on the left, pin on the right; every segment carries
	// the background so inner resets don't leave holes.
	left := base.Foreground(prioColor).Bold(true).Render(prioMarker(n.Priority))
	right := ""
	if n.Pinned {
		right = base.Render(pinMark)
	}
	gap := tileTextW - lipgloss.Width(left) - lipgloss.Width(right)
	header := left + base.Render(strings.Repeat(" ", max(0, gap))) + right

	title := md.Title(n.Body)
	if title == "" {
		title = "(leer)"
	}
	lines := strings.Split(ansi.Wrap(title, tileTextW, ""), "\n")
	if len(lines) > tileTextH-1 {
		rest := strings.Join(lines[tileTextH-2:], " ")
		lines = append(lines[:tileTextH-2], ansi.Truncate(rest, tileTextW-1, "")+"…")
	}
	titleStyle := base.Bold(true).Width(tileTextW)
	body := []string{header}
	for _, l := range lines {
		body = append(body, titleStyle.Render(l))
	}
	for len(body) < tileTextH {
		body = append(body, base.Width(tileTextW).Render(""))
	}

	st := base.Padding(0, 1).MarginRight(tileGap)
	if selected {
		st = st.Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#FFFFFF"))
	} else {
		st = st.Border(lipgloss.RoundedBorder()).BorderForeground(c.BG)
	}
	return st.Render(strings.Join(body, "\n"))
}

// renderBoard draws notes as a grid into width x height and returns the
// (possibly adjusted) scroll offset in rows so the selection stays visible.
func renderBoard(notes []store.Note, sel, offset, width, height int, focused bool) (string, int) {
	if len(notes) == 0 {
		msg := mutedStyle.Render("Keine Post-its.  n = neues Post-it")
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, msg), 0
	}
	cols := boardCols(width)
	totalRows := (len(notes) + cols - 1) / cols
	visRows := max(1, height/tileOuterH)
	if totalRows > visRows { // reserve one line for the scroll indicator
		visRows = max(1, (height-1)/tileOuterH)
	}
	selRow := sel / cols
	if selRow < offset {
		offset = selRow
	}
	if selRow >= offset+visRows {
		offset = selRow - visRows + 1
	}

	var rows []string
	for r := offset; r < min(totalRows, offset+visRows); r++ {
		var tiles []string
		for i := r * cols; i < min(len(notes), (r+1)*cols); i++ {
			tiles = append(tiles, renderTile(notes[i], focused && i == sel))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, tiles...))
	}
	out := lipgloss.JoinVertical(lipgloss.Left, rows...)
	if totalRows > visRows {
		more := ""
		if offset > 0 {
			more += "▲ "
		}
		if offset+visRows < totalRows {
			more += "▼"
		}
		out = lipgloss.JoinVertical(lipgloss.Left, out, mutedStyle.Render(more))
	}
	return out, offset
}
