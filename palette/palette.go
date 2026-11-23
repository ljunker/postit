// Package palette is the list of colors a post-it can have, shared by the
// terminal UI and the web app.
package palette

// Color is one entry of the post-it palette.
type Color struct {
	Name  string // stored in the database
	Label string // shown in the UI
	BG    string // hex background
	FG    string // hex text color
}

// Colors is the palette in display order; the first entry is the default.
var Colors = []Color{
	{"yellow", "gelb", "#FFE873", "#2B2400"},
	{"orange", "orange", "#FFBE76", "#2E1A00"},
	{"pink", "pink", "#FFA8CF", "#33001A"},
	{"red", "rot", "#FF8A80", "#330400"},
	{"purple", "lila", "#CDB0FF", "#1D0838"},
	{"blue", "blau", "#94CCFF", "#001E3A"},
	{"green", "grün", "#AEE8A6", "#062B00"},
	{"gray", "grau", "#D4D4D4", "#1E1E1E"},
}

// Index returns the position of name in Colors, or 0 for an unknown name.
func Index(name string) int {
	for i, c := range Colors {
		if c.Name == name {
			return i
		}
	}
	return 0
}

// ByName returns the color called name, or the default color.
func ByName(name string) Color { return Colors[Index(name)] }

// Valid reports whether name is a palette color.
func Valid(name string) bool {
	for _, c := range Colors {
		if c.Name == name {
			return true
		}
	}
	return false
}
