package md

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"Einkauf\nMilch":              "Einkauf",
		"\n\n  Einkauf  \n":           "Einkauf",
		"# Einkauf\n- Milch":          "Einkauf",
		"### **Wichtig** heute":       "Wichtig heute",
		"- [ ] Steuer machen":         "Steuer machen",
		"- [x] erledigt":              "erledigt",
		"1. erster Punkt":             "erster Punkt",
		"> Zitat":                     "Zitat",
		"`code` und ~~weg~~":          "code und weg",
		"[Doku](https://x.org) lesen": "Doku lesen",
		"<b>fett</b>":                 "<b>fett</b>",
		"---":                         "---",
	} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRest(t *testing.T) {
	if got := Rest("\nTitel\n\n- a\n- b\n\n"); got != "- a\n- b" {
		t.Errorf("Rest = %q", got)
	}
	if got := Rest("nur Titel"); got != "" {
		t.Errorf("Rest = %q", got)
	}
}

func TestHTML(t *testing.T) {
	for _, c := range []struct{ in, want, not string }{
		{"**fett** und *kursiv*", "<strong>fett</strong> und <em>kursiv</em>", ""},
		{"eins\nzwei", "eins<br", ""},
		{"- [ ] offen\n- [x] fertig", `type="checkbox"`, ""},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;", "<script>"},
		{"a <b>x</b> c", "a &lt;b&gt;x&lt;/b&gt; c", "<b>"},
		{"[x](javascript:alert(1))", `href=""`, "javascript:"},
		{"[Doku](https://x.org)", `<a href="https://x.org" target="_blank" rel="noopener noreferrer">Doku</a>`, ""},
		{"siehe https://x.org", `<a href="https://x.org" target="_blank" rel="noopener noreferrer">`, ""},
		{"![Bild](https://x.org/a.png)", `🖼 Bild</a>`, "<img"},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "<table>", ""},
		{"~~weg~~", "<del>weg</del>", ""},
	} {
		got := string(HTML(c.in))
		if !strings.Contains(got, c.want) {
			t.Errorf("HTML(%q) = %q, want it to contain %q", c.in, got, c.want)
		}
		if c.not != "" && strings.Contains(got, c.not) {
			t.Errorf("HTML(%q) = %q, must not contain %q", c.in, got, c.not)
		}
	}
	if HTML("  \n") != "" {
		t.Error("empty input should give empty HTML")
	}
}

const sample = "# Überschrift\n\nText mit **fett**, *kursiv*, `code` und einem [Link](https://example.org) " +
	"und einem sehr_langen_wort_das_nicht_in_eine_zeile_passt_und_geschnitten_wird.\n\n" +
	"- [ ] offen\n- [x] erledigt\n- normal\n  zweite Zeile\n\n1. eins\n2. zwei\n\n> Zitat\n\n" +
	"```\nfunc main() {}\n```\n\n---\n\n| Name | Wert |\n|---|---|\n| a | 1 |\n\n<b>roh</b>"

func TestTerminalPlain(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	out := Terminal(sample, 30, lipgloss.NewStyle())
	for _, want := range []string{"Überschrift", "☐ offen", "☑ erledigt", "• normal", "  zweite Zeile", "1. eins", "2. zwei", "▎ Zitat", "func main() {}", "──────", "Name", "│", "<b>roh</b>", "(https://example.org)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, no := range []string{"**", "# ", "[ ]", "```"} {
		if strings.Contains(out, no) {
			t.Errorf("output still contains markup %q:\n%s", no, out)
		}
	}
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w != 30 {
			t.Errorf("line %d has width %d, want 30: %q", i, w, l)
		}
	}
}

// With colors on, every visible cell must carry the background: after any
// reset the next printable character needs the background set again.
func TestTerminalBackgroundHasNoHoles(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	base := lipgloss.NewStyle().Background(lipgloss.Color("#FFD866")).Foreground(lipgloss.Color("#2B2400"))
	out := Terminal(sample, 30, base)
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w != 30 {
			t.Errorf("line %d has width %d, want 30", i, w)
		}
		bg := false
		p := ansi.NewParser()
		state := byte(0)
		for len(l) > 0 {
			seq, width, n, newState := ansi.DecodeSequence(l, state, p)
			state = newState
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				params := seq[2 : len(seq)-1]
				if params == "" || params == "0" {
					bg = false
				}
				if strings.Contains(params, "48;") {
					bg = true
				}
			} else if width > 0 && !bg {
				t.Fatalf("line %d: %q printed without background: %q", i, seq, l)
			}
			l = l[n:]
		}
	}
}

func TestTerminalNarrow(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	out := Terminal("ein_wort_das_viel_zu_lang_ist", 8, lipgloss.NewStyle())
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) != 8 {
			t.Errorf("width %d: %q", ansi.StringWidth(l), l)
		}
	}
	if !strings.Contains(strings.NewReplacer("\n", "", " ", "").Replace(out), "ein_wort_das_viel_zu_lang_ist") {
		t.Errorf("text lost:\n%s", out)
	}
}
