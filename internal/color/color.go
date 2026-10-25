// Package color turns a per-wiki accent into CSS colors that stay readable.
package color

import (
	"math"
	"strconv"
	"strings"
)

const (
	// DefaultAccent is the pine green used when a wiki has no accent saved
	// and the configuration does not set one.
	DefaultAccent = "#1b3832"
	// Paper and Card are the skin backgrounds links and borders sit on.
	Paper = "#f3eee4"
	Card  = "#fffdf8"

	aaText = 4.5
	aaUI   = 3.0
)

// Preset is one swatch on Special:Appearance.
type Preset struct {
	Name string
	Hex  string
}

// Presets are the built-in accent choices. Each is dark enough for light text.
func Presets() []Preset {
	return []Preset{
		{"Pine", "#1b3832"},
		{"Navy", "#1a365d"},
		{"Teal", "#0e5c66"},
		{"Burgundy", "#6e2430"},
		{"Plum", "#4a2c5a"},
		{"Slate", "#3d4c5c"},
		{"Rust", "#8a3e1d"},
		{"Forest", "#1e4d32"},
		{"Charcoal", "#2c2a28"},
		{"Indigo", "#24305e"},
		{"Wine", "#5c2e3a"},
		{"Ocean", "#163e4a"},
	}
}

// Theme is the set of colors applied to one wiki.
type Theme struct {
	Accent      string
	Plate       string
	Ink         string
	Border      string
	Link        string
	Label       string
	InkRatio    float64
	BorderRatio float64
	LinkRatio   float64
	InkOK       bool
	BorderOK    bool
	LinkOK      bool
}

// OK reports whether header text, borders, and links all meet WCAG AA.
func (t Theme) OK() bool { return t.InkOK && t.BorderOK && t.LinkOK }

// Warning is empty when every applied ratio meets its WCAG AA threshold:
// 4.5:1 for header text and links, 3:1 for borders.
func (t Theme) Warning() string {
	var parts []string
	if t.InkRatio < aaText {
		parts = append(parts, "Header text is "+FormatRatio(t.InkRatio)+" and needs 4.5:1.")
	}
	if t.BorderRatio < aaUI {
		parts = append(parts, "Borders are "+FormatRatio(t.BorderRatio)+" and need 3:1 against the page.")
	}
	if t.LinkRatio < aaText {
		parts = append(parts, "Links are "+FormatRatio(t.LinkRatio)+" and need 4.5:1 against the page.")
	}
	if len(parts) == 0 {
		return ""
	}
	return "This color cannot meet WCAG AA. " + strings.Join(parts, " ") + " The closest readable shades are still applied."
}

// RootCSS is a custom-property list safe to embed in a style attribute or block.
// Every value is a #rrggbb color produced by this package.
func (t Theme) RootCSS() string {
	return "--pine:" + t.Accent +
		";--pine-2:" + t.Plate +
		";--foam:" + t.Ink +
		";--accent:" + t.Accent +
		";--accent-ink:" + t.Ink +
		";--accent-border:" + t.Border +
		";--teal:" + t.Link +
		";--sidebar-label:" + t.Label
}

// FormatRatio renders a contrast ratio like 7.32:1.
func FormatRatio(r float64) string {
	return strconv.FormatFloat(r, 'f', 2, 64) + ":1"
}

// Normalize accepts #rgb or #rrggbb, with or without a leading #.
func Normalize(s string) (string, bool) {
	c, ok := parse(s)
	if !ok {
		return "", false
	}
	return c.hex(), true
}

// IsPreset reports whether hex is one of the swatches.
func IsPreset(hex string) bool {
	n, ok := Normalize(hex)
	if !ok {
		return false
	}
	for _, p := range Presets() {
		if p.Hex == n {
			return true
		}
	}
	return false
}

// Build derives a readable theme from an accent color.
// An invalid accent falls back to DefaultAccent.
func Build(accent string) Theme {
	c, ok := parse(accent)
	if !ok {
		c, _ = parse(DefaultAccent)
	}
	paper, _ := parse(Paper)
	card, _ := parse(Card)
	ink, inkRatio, inkOK := inkFor(c)
	border, borderRatio, borderOK := fit(c, []rgb{paper, card}, aaUI)
	link, linkRatio, linkOK := fit(c, []rgb{paper, card}, aaText)
	return Theme{
		Accent:      c.hex(),
		Plate:       plate(c, ink).hex(),
		Ink:         ink.hex(),
		Border:      border.hex(),
		Link:        link.hex(),
		Label:       sidebarLabel(c, ink).hex(),
		InkRatio:    inkRatio,
		BorderRatio: borderRatio,
		LinkRatio:   linkRatio,
		InkOK:       inkOK,
		BorderOK:    borderOK,
		LinkOK:      linkOK,
	}
}

const nearBlackHex = "#141210"

func inkFor(bg rgb) (rgb, float64, bool) {
	white := rgb{1, 1, 1}
	near, _ := parse(nearBlackHex)
	black := rgb{}
	cw := contrast(bg, white)
	cn := contrast(bg, near)
	pick, ratio := white, cw
	if cn > cw {
		pick, ratio = near, cn
	}
	if ratio >= aaText-1e-6 {
		return pick, ratio, true
	}
	if cb := contrast(bg, black); cb > ratio {
		pick, ratio = black, cb
	}
	return pick, ratio, ratio >= aaText-1e-6
}

// plate shifts the accent slightly toward the ink color, stopping if the
// ink would fall below 4.5:1 on that plate.
func plate(bg, ink rgb) rgb {
	target := rgb{1, 1, 1}
	if ink.R < 0.5 {
		target = rgb{}
	}
	best := bg
	for i := 8; i >= 0; i-- {
		p := mix(bg, target, float64(i)/40)
		if contrast(p, ink) >= aaText-1e-6 {
			return p
		}
		best = p
	}
	return best
}

// sidebarLabel softens the ink toward the accent while contrast stays at 4.5:1.
func sidebarLabel(bg, ink rgb) rgb {
	best := ink
	for j := 20; j >= 0; j-- {
		c := mix(ink, bg, float64(j)/20*0.45)
		if contrast(c, bg) >= aaText-1e-6 {
			return c
		}
		best = c
	}
	return best
}

// fit darkens or lightens color until it reaches minRatio against every
// background. The smallest shift that passes is kept. ok is false when no
// mix along either path reaches the ratio.
func fit(color rgb, bgs []rgb, minRatio float64) (rgb, float64, bool) {
	ratio := minContrast(color, bgs)
	if ratio >= minRatio-1e-6 {
		return color, ratio, true
	}
	black := rgb{}
	white := rgb{1, 1, 1}
	var best rgb
	bestT := 2.0
	bestRatio := 0.0
	found := false
	for _, target := range []rgb{black, white} {
		for i := 1; i <= 50; i++ {
			t := float64(i) / 50
			c := mix(color, target, t)
			r := minContrast(c, bgs)
			if r+1e-9 >= minRatio {
				if !found || t < bestT-1e-9 || (math.Abs(t-bestT) < 1e-9 && r > bestRatio) {
					found = true
					best = c
					bestT = t
					bestRatio = r
				}
				break
			}
		}
	}
	if !found {
		fb, fr := black, minContrast(black, bgs)
		if wr := minContrast(white, bgs); wr > fr {
			fb, fr = white, wr
		}
		return fb, fr, false
	}
	return best, bestRatio, true
}

type rgb struct{ R, G, B float64 }

func lin(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func (c rgb) lum() float64 {
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

func contrast(a, b rgb) float64 {
	l1, l2 := a.lum(), b.lum()
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func minContrast(c rgb, bgs []rgb) float64 {
	m := 21.0
	for _, bg := range bgs {
		if r := contrast(c, bg); r < m {
			m = r
		}
	}
	return m
}

func mix(a, b rgb, t float64) rgb {
	return rgb{
		R: a.R*(1-t) + b.R*t,
		G: a.G*(1-t) + b.G*t,
		B: a.B*(1-t) + b.B*t,
	}
}

func parse(s string) (rgb, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "#")
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6:
	default:
		return rgb{}, false
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb{}, false
	}
	return rgb{
		R: float64((n>>16)&0xff) / 255,
		G: float64((n>>8)&0xff) / 255,
		B: float64(n&0xff) / 255,
	}, true
}

func (c rgb) hex() string {
	clamp := func(v float64) int {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		return int(math.Round(v * 255))
	}
	r, g, b := clamp(c.R), clamp(c.G), clamp(c.B)
	const digits = "0123456789abcdef"
	return "#" + string([]byte{
		digits[r>>4], digits[r&15],
		digits[g>>4], digits[g&15],
		digits[b>>4], digits[b&15],
	})
}
