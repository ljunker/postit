package md

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// Terminal renders src for a terminal, wrapped to width columns. Every piece
// of text carries base's colors and every line is padded to width, so the
// post-it's background has no holes.
func Terminal(src string, width int, base lipgloss.Style) string {
	width = max(4, width)
	r := term{src: []byte(src), base: base, code: base.Background(shade(base.GetBackground()))}
	r.strong = base.Foreground(strong(base.GetBackground(), base.GetForeground()))
	lines := r.blocks(Parse(r.src), width)
	for i, l := range lines {
		lines[i] = r.pad(l, width)
	}
	return strings.Join(lines, "\n")
}

// shade darkens (or, on dark colors, lightens) c a little, for code.
func shade(c lipgloss.TerminalColor) lipgloss.TerminalColor {
	col, ok := c.(lipgloss.Color)
	if !ok {
		return c
	}
	cc, err := colorful.Hex(string(col))
	if err != nil {
		return c
	}
	_, _, l := cc.Hsl()
	toward := colorful.Color{} // black
	if l < 0.35 {
		toward = colorful.Color{R: 1, G: 1, B: 1}
	}
	return lipgloss.Color(cc.BlendRgb(toward, 0.12).Clamped().Hex())
}

// strong returns the text color for bold text on bg: a saturated, dark (or,
// on dark colors, light) tone of bg's hue. Some terminals (herdr, for one)
// drop the bold attribute, so bold text needs a color of its own to stand
// out. fg is returned when bg is not a hex color.
func strong(bg, fg lipgloss.TerminalColor) lipgloss.TerminalColor {
	col, ok := bg.(lipgloss.Color)
	if !ok {
		return fg
	}
	cc, err := colorful.Hex(string(col))
	if err != nil {
		return fg
	}
	h, s, l := cc.Hsl()
	if s < 0.1 { // gray has no hue of its own
		h = 210
	}
	light := 0.22
	if l < 0.35 {
		light = 0.78
	}
	return lipgloss.Color(colorful.Hsl(h, 0.85, light).Clamped().Hex())
}

type term struct {
	src    []byte
	base   lipgloss.Style
	code   lipgloss.Style
	strong lipgloss.Style // base with the color for bold text
}

// bold makes st bold and gives it the color for bold text.
func (r term) bold(st lipgloss.Style) lipgloss.Style {
	return st.Bold(true).Foreground(r.strong.GetForeground())
}

func (r term) pad(line string, width int) string {
	if w := lipgloss.Width(line); w < width {
		return line + r.base.Render(strings.Repeat(" ", width-w))
	}
	return line
}

func (r term) blank() string { return r.base.Render("") }

// blocks renders the block children of parent. Blocks are separated by a
// blank line, except the items of a tight list.
func (r term) blocks(parent ast.Node, width int) []string {
	var out []string
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		lines := r.block(c, width)
		if len(lines) == 0 {
			continue
		}
		if len(out) > 0 && !tight(c) {
			out = append(out, r.blank())
		}
		out = append(out, lines...)
	}
	return out
}

// tight reports whether c follows its previous sibling without a blank line:
// paragraphs inside the items of a tight list.
func tight(c ast.Node) bool {
	if _, ok := c.Parent().(*ast.ListItem); ok {
		if l, ok := c.Parent().Parent().(*ast.List); ok {
			return l.IsTight
		}
	}
	return false
}

func (r term) block(n ast.Node, width int) []string {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return r.wrap(r.inline(n, r.base), width)
	case *ast.Heading:
		st := r.bold(r.base)
		if n.Level == 1 {
			st = st.Underline(true)
		}
		return r.wrap(r.inline(n, st), width)
	case *ast.ThematicBreak:
		return []string{r.base.Render(strings.Repeat("─", width))}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return r.codeBlock(n, width)
	case *ast.HTMLBlock:
		var b strings.Builder
		writeLines(&b, n, r.src)
		if n.HasClosure() {
			b.Write(n.ClosureLine.Value(r.src))
		}
		return r.wrap([]run{{strings.TrimRight(b.String(), "\n"), r.base}}, width)
	case *ast.Blockquote:
		bar := r.base.Faint(true).Render("▎ ")
		inner := r.blocks(n, width-2)
		for i, l := range inner {
			inner[i] = bar + l
		}
		return inner
	case *ast.List:
		return r.list(n, width)
	case *extast.Table:
		return r.table(n, width)
	}
	return r.blocks(n, width)
}

func (r term) codeBlock(n ast.Node, width int) []string {
	var b strings.Builder
	writeLines(&b, n, r.src)
	var out []string
	for l := range strings.SplitSeq(strings.TrimRight(b.String(), "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		l = ansi.Truncate(l, width-2, "…")
		out = append(out, r.code.Render(" "+l+strings.Repeat(" ", max(0, width-2-ansi.StringWidth(l)))+" "))
	}
	return out
}

func (r term) list(l *ast.List, width int) []string {
	var out []string
	num := l.Start
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "• "
		if l.IsOrdered() {
			marker = fmt.Sprintf("%d. ", num)
			num++
		}
		if box := taskBox(item); box != nil {
			marker = "☐ "
			if box.IsChecked {
				marker = "☑ "
			}
		}
		mw := ansi.StringWidth(marker)
		inner := r.blocks(item, width-mw)
		if len(inner) == 0 {
			inner = []string{""}
		}
		if len(out) > 0 && !l.IsTight {
			out = append(out, r.blank())
		}
		for i, line := range inner {
			prefix := strings.Repeat(" ", mw)
			if i == 0 {
				prefix = marker
			}
			out = append(out, r.base.Render(prefix)+line)
		}
	}
	return out
}

// taskBox returns the checkbox of a task list item, if it is one.
func taskBox(item ast.Node) *extast.TaskCheckBox {
	if first := item.FirstChild(); first != nil {
		if box, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
			return box
		}
	}
	return nil
}

func (r term) table(t *extast.Table, width int) []string {
	var rows [][]string // rendered cells
	var widths []int
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		st := r.base
		if _, ok := row.(*extast.TableHeader); ok {
			st = r.bold(st)
		}
		var cells []string
		for i, cell := 0, row.FirstChild(); cell != nil; i, cell = i+1, cell.NextSibling() {
			s := joinRuns(r.inline(cell, st))
			cells = append(cells, s)
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], ansi.StringWidth(s))
		}
		rows = append(rows, cells)
	}
	if len(widths) == 0 {
		return nil
	}
	sep := r.base.Faint(true).Render(" │ ")
	avail := width - 3*(len(widths)-1)
	total := 0
	for _, w := range widths {
		total += w
	}
	if total > avail { // shrink every column to its share
		for i := range widths {
			widths[i] = max(1, widths[i]*avail/total)
		}
	}
	var out []string
	for ri, cells := range rows {
		var parts []string
		for i, w := range widths {
			s := ""
			if i < len(cells) {
				s = ansi.Truncate(cells[i], w, "…")
			}
			parts = append(parts, s+r.base.Render(strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))))
		}
		out = append(out, ansi.Truncate(strings.Join(parts, sep), width, ""))
		if ri == 0 && t.FirstChild().Kind() == extast.KindTableHeader {
			var rule []string
			for _, w := range widths {
				rule = append(rule, strings.Repeat("─", w))
			}
			out = append(out, r.base.Faint(true).Render(ansi.Truncate(strings.Join(rule, "─┼─"), width, "")))
		}
	}
	return out
}

// ---- inline ----------------------------------------------------------------

// run is a piece of text in one style; "\n" marks a line break.
type run struct {
	s  string
	st lipgloss.Style
}

func joinRuns(runs []run) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.st.Render(strings.ReplaceAll(r.s, "\n", " ")))
	}
	return b.String()
}

// inline collects the inline content of n as styled runs.
func (r term) inline(n ast.Node, st lipgloss.Style) []run {
	var out []run
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, r.inlineNode(c, st)...)
	}
	return out
}

func (r term) inlineNode(n ast.Node, st lipgloss.Style) []run {
	switch n := n.(type) {
	case *ast.Text:
		out := []run{{string(n.Segment.Value(r.src)), st}}
		if n.SoftLineBreak() || n.HardLineBreak() {
			out = append(out, run{"\n", st})
		}
		return out
	case *ast.String:
		return []run{{string(n.Value), st}}
	case *ast.Emphasis:
		if n.Level >= 2 {
			return r.inline(n, r.bold(st))
		}
		return r.inline(n, st.Italic(true))
	case *extast.Strikethrough:
		return r.inline(n, st.Strikethrough(true))
	case *ast.CodeSpan:
		cs := st.Background(r.code.GetBackground())
		return []run{{plain(n, r.src), cs}}
	case *ast.Link:
		out := r.inline(n, st.Underline(true))
		if dest := string(n.Destination); dest != "" && dest != plain(n, r.src) {
			out = append(out, run{" (" + dest + ")", st.Faint(true)})
		}
		return out
	case *ast.AutoLink:
		return []run{{string(n.Label(r.src)), st.Underline(true)}}
	case *ast.Image:
		alt := plain(n, r.src)
		return []run{{"🖼 " + alt, st}, {" (" + string(n.Destination) + ")", st.Faint(true)}}
	case *ast.RawHTML:
		var b strings.Builder
		writeSegments(&b, n.Segments, r.src)
		return []run{{b.String(), st}}
	case *extast.TaskCheckBox:
		return nil // shown as the list marker
	}
	return r.inline(n, st)
}

// wrap breaks runs into lines of at most width columns at spaces; words
// longer than a line are cut.
func (r term) wrap(runs []run, width int) []string {
	type tok struct {
		s     string
		st    lipgloss.Style
		space bool
		br    bool
	}
	var toks []tok
	for _, rn := range runs {
		for i, line := range strings.Split(rn.s, "\n") {
			if i > 0 {
				toks = append(toks, tok{br: true})
			}
			for j, word := range strings.Split(line, " ") {
				if j > 0 {
					toks = append(toks, tok{s: " ", st: rn.st, space: true})
				}
				if word != "" {
					toks = append(toks, tok{s: word, st: rn.st})
				}
			}
		}
	}

	var lines []string
	var cur []tok
	curW := 0
	flush := func() {
		for len(cur) > 0 && cur[len(cur)-1].space {
			cur = cur[:len(cur)-1]
		}
		var b strings.Builder
		for _, t := range cur {
			b.WriteString(t.st.Render(t.s))
		}
		lines = append(lines, b.String())
		cur, curW = nil, 0
	}
	for i := 0; i < len(toks); {
		t := toks[i]
		switch {
		case t.br:
			flush()
			i++
			continue
		case t.space:
			if curW > 0 && curW < width {
				cur = append(cur, t)
				curW++
			}
			i++
			continue
		}
		// A word may consist of several tokens ("**fett**er", or text that
		// goldmark split at "_"); keep them together.
		j, gw := i, 0
		for ; j < len(toks) && !toks[j].br && !toks[j].space; j++ {
			gw += ansi.StringWidth(toks[j].s)
		}
		if curW > 0 && curW+gw > width {
			flush()
		}
		for _, t := range toks[i:j] {
			w := ansi.StringWidth(t.s)
			for curW+w > width { // cut words that don't fit a line
				head := ansi.Truncate(t.s, width-curW, "")
				if head == "" {
					if curW == 0 {
						break // a wide character in a one-column line
					}
					flush()
					continue
				}
				cur = append(cur, tok{s: head, st: t.st})
				flush()
				t.s = t.s[len(head):]
				w = ansi.StringWidth(t.s)
			}
			if t.s != "" {
				cur = append(cur, t)
				curW += w
			}
		}
		i = j
	}
	if len(cur) > 0 || len(lines) == 0 {
		flush()
	}
	// a trailing line break (e.g. "foo\n" at the end of a paragraph) leaves no empty last line
	for len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
