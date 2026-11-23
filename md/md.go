// Package md renders a post-it's Markdown: as HTML for the web app and as
// styled text for the terminal. Both use the same goldmark parser with the
// GitHub extensions (tables, strikethrough, task lists, autolinks).
package md

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

var gm = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(linkTargets{}, 100))),
	goldmark.WithRendererOptions(
		html.WithHardWraps(), // a new line in a post-it is a new line
		renderer.WithNodeRenderers(util.Prioritized(safeHTML{}, 100)),
	),
)

// Parse parses src into a Markdown syntax tree.
func Parse(src []byte) ast.Node {
	return gm.Parser().Parse(text.NewReader(src))
}

// firstLine returns the first line of body (leading blank lines skipped) and
// everything after it.
func firstLine(body string) (string, string) {
	line, rest, _ := strings.Cut(strings.TrimLeft(body, "\n"), "\n")
	return line, strings.Trim(rest, "\n")
}

// Title is the note's first line as plain text: Markdown markers such as
// "# ", "- [ ] " or "**" are dropped.
func Title(body string) string {
	line, _ := firstLine(body)
	line = strings.TrimSpace(line)
	src := []byte(line)
	var b strings.Builder
	ast.Walk(Parse(src), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			b.Write(n.Segment.Value(src))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.AutoLink:
			b.Write(n.Label(src))
		case *ast.RawHTML:
			writeSegments(&b, n.Segments, src)
		case *ast.HTMLBlock, *ast.CodeBlock, *ast.FencedCodeBlock:
			writeLines(&b, n, src)
		}
		return ast.WalkContinue, nil
	})
	if t := strings.TrimSpace(b.String()); t != "" {
		return t
	}
	return line // e.g. "---": better the marker than nothing
}

// Rest is everything after the title line.
func Rest(body string) string {
	_, rest := firstLine(body)
	return rest
}

// HTML renders src as HTML. Raw HTML in the source is shown as text, unsafe
// link targets (javascript: …) are dropped, and links open in a new tab.
func HTML(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := gm.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark escapes, raw HTML is escaped by safeHTML
}

func writeSegments(b *strings.Builder, segs *text.Segments, src []byte) {
	for i := range segs.Len() {
		s := segs.At(i)
		b.Write(s.Value(src))
	}
}

func writeLines(b *strings.Builder, n ast.Node, src []byte) {
	writeSegments(b, n.Lines(), src)
}

// linkTargets makes links open in a new tab without access to the opener.
type linkTargets struct{}

func (linkTargets) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Link, *ast.AutoLink:
			n.SetAttributeString("target", []byte("_blank"))
			n.SetAttributeString("rel", []byte("noopener noreferrer"))
		}
		return ast.WalkContinue, nil
	})
}

// safeHTML replaces goldmark's handling of raw HTML ("<!-- raw HTML omitted
// -->") with the escaped source, so "<b>" in a note stays readable. Images
// become links: the CSP would block foreign images anyway.
type safeHTML struct{}

func (safeHTML) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindRawHTML, renderRawHTML)
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
	reg.Register(ast.KindImage, renderImage)
}

func renderRawHTML(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		segs := n.(*ast.RawHTML).Segments
		for i := range segs.Len() {
			s := segs.At(i)
			_, _ = w.WriteString(template.HTMLEscapeString(string(s.Value(src))))
		}
	}
	return ast.WalkSkipChildren, nil
}

func renderHTMLBlock(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var b strings.Builder
	writeLines(&b, n, src)
	if hb := n.(*ast.HTMLBlock); hb.HasClosure() {
		b.Write(hb.ClosureLine.Value(src))
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	_, _ = w.WriteString("<p>")
	for i, l := range lines {
		if i > 0 {
			_, _ = w.WriteString("<br>\n")
		}
		_, _ = w.WriteString(template.HTMLEscapeString(l))
	}
	_, _ = w.WriteString("</p>\n")
	return ast.WalkSkipChildren, nil
}

func renderImage(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	img := n.(*ast.Image)
	alt := plain(img, src)
	if alt == "" {
		alt = string(img.Destination)
	}
	if html.IsDangerousURL(img.Destination) {
		_, _ = w.WriteString(template.HTMLEscapeString(alt))
		return ast.WalkSkipChildren, nil
	}
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML(util.URLEscape(img.Destination, true)))
	_, _ = w.WriteString(`" target="_blank" rel="noopener noreferrer">🖼 `)
	_, _ = w.WriteString(template.HTMLEscapeString(alt))
	_, _ = w.WriteString("</a>")
	return ast.WalkSkipChildren, nil
}

// plain is the text inside n without markup.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
		case *ast.String:
			b.Write(c.Value)
		case *ast.AutoLink:
			b.Write(c.Label(src))
		case *extast.TaskCheckBox:
			// handled by the list marker
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
