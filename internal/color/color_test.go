package color

import (
	"math"
	"strings"
	"testing"
)

func TestContrastBlackWhite(t *testing.T) {
	black, _ := parse("#000000")
	white, _ := parse("#ffffff")
	got := contrast(black, white)
	if math.Abs(got-21) > 0.01 {
		t.Fatalf("black/white contrast %v", got)
	}
	if contrast(black, black) < 0.99 || contrast(black, black) > 1.01 {
		t.Fatalf("same %v", contrast(black, black))
	}
}

func TestNormalize(t *testing.T) {
	got, ok := Normalize("#ABC")
	if !ok || got != "#aabbcc" {
		t.Fatalf("%q %v", got, ok)
	}
	got, ok = Normalize("1B3832")
	if !ok || got != "#1b3832" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := Normalize("green"); ok {
		t.Fatal("named color")
	}
	if _, ok := Normalize("#1b3832ff"); ok {
		t.Fatal("eight digits")
	}
	if _, ok := Normalize(""); ok {
		t.Fatal("empty")
	}
}

func TestPresetsMeetAA(t *testing.T) {
	for _, p := range Presets() {
		th := Build(p.Hex)
		if th.Accent != p.Hex {
			t.Fatalf("%s accent %s", p.Name, th.Accent)
		}
		if !th.OK() {
			t.Fatalf("%s not AA: ink %s %.2f border %s %.2f link %s %.2f",
				p.Name, th.Ink, th.InkRatio, th.Border, th.BorderRatio, th.Link, th.LinkRatio)
		}
		if th.InkRatio < aaText || th.LinkRatio < aaText || th.BorderRatio < aaUI {
			t.Fatalf("%s ratios ink %.2f link %.2f border %.2f", p.Name, th.InkRatio, th.LinkRatio, th.BorderRatio)
		}
		if th.Ink != "#ffffff" && th.Ink != "#141210" && th.Ink != "#000000" {
			t.Fatalf("%s ink %s", p.Name, th.Ink)
		}
	}
}

func TestLightAccentIsAdjusted(t *testing.T) {
	th := Build("#fff3a0")
	if th.Accent != "#fff3a0" {
		t.Fatal(th.Accent)
	}
	if th.Ink != "#141210" && th.Ink != "#000000" {
		t.Fatalf("ink %s ratio %.2f", th.Ink, th.InkRatio)
	}
	if th.InkRatio < aaText {
		t.Fatalf("ink ratio %.2f", th.InkRatio)
	}
	if th.Border == th.Accent || th.BorderRatio < aaUI {
		t.Fatalf("border %s %.2f", th.Border, th.BorderRatio)
	}
	if th.Link == th.Accent || th.LinkRatio < aaText {
		t.Fatalf("link %s %.2f", th.Link, th.LinkRatio)
	}
	if !th.OK() || th.Warning() != "" {
		t.Fatal(th.Warning())
	}
}

func TestNavyAndBurgundyKeepHue(t *testing.T) {
	for _, hex := range []string{"#1a365d", "#6e2430"} {
		th := Build(hex)
		if th.Accent != hex || th.Ink != "#ffffff" {
			t.Fatalf("%s ink %s", hex, th.Ink)
		}
		if th.Link != hex && th.LinkRatio < aaText {
			t.Fatalf("%s link %s %.2f", hex, th.Link, th.LinkRatio)
		}
		paper, _ := parse(Paper)
		link, _ := parse(th.Link)
		if contrast(link, paper) < aaText {
			t.Fatalf("%s link contrast", hex)
		}
	}
}

func TestDefaultAndInvalid(t *testing.T) {
	th := Build("nope")
	if th.Accent != DefaultAccent || !th.OK() {
		t.Fatalf("%+v", th)
	}
	if !IsPreset(DefaultAccent) || IsPreset("#fff3a0") {
		t.Fatal("preset check")
	}
}

func TestSlatePanelTintKeepsInk(t *testing.T) {
	th := Build("#3d4c5c")
	if th.Plate == th.Accent {
		t.Fatal("panel tint is the same as the accent")
	}
	plate, _ := parse(th.Plate)
	ink, _ := parse(th.Ink)
	if ratio := contrast(plate, ink); ratio < aaText {
		t.Fatalf("ink %s on panel %s is %.2f", th.Ink, th.Plate, ratio)
	}
}

func TestRustAccentInkContrasts(t *testing.T) {
	th := Build("#8a3e1d")
	if th.Ink != "#ffffff" && th.Ink != "#141210" && th.Ink != "#000000" {
		t.Fatalf("ink %s", th.Ink)
	}
	bg, _ := parse("#8a3e1d")
	ink, _ := parse(th.Ink)
	if contrast(bg, ink) < aaText || th.InkRatio < aaText {
		t.Fatalf("rust ink %s ratio %.2f", th.Ink, th.InkRatio)
	}
	red, _ := parse("#8d2e2e")
	if contrast(bg, red) >= aaText {
		t.Fatalf("paper red on rust is readable (%.2f); the sidebar must not rely on that", contrast(bg, red))
	}
}

func TestPineWarningOnlyWhenBelowThreshold(t *testing.T) {
	pine := Build("#1b3832")
	if pine.Warning() != "" {
		t.Fatal(pine.Warning())
	}
	if FormatRatio(pine.InkRatio) != "12.66:1" || FormatRatio(pine.LinkRatio) != "10.95:1" || FormatRatio(pine.BorderRatio) != "10.95:1" {
		t.Fatalf("ink %s link %s border %s", FormatRatio(pine.InkRatio), FormatRatio(pine.LinkRatio), FormatRatio(pine.BorderRatio))
	}
	if pine.InkRatio < aaText || pine.LinkRatio < aaText || pine.BorderRatio < aaUI {
		t.Fatalf("pine ratios ink %.2f link %.2f border %.2f", pine.InkRatio, pine.LinkRatio, pine.BorderRatio)
	}
	low := Theme{InkRatio: 12.66, BorderRatio: 2.99, LinkRatio: 10.95}
	if low.Warning() == "" || !strings.Contains(low.Warning(), "2.99:1") || strings.Contains(low.Warning(), "12.66:1") {
		t.Fatal(low.Warning())
	}
	passing := Theme{InkRatio: 4.5, BorderRatio: 3, LinkRatio: 4.5}
	if passing.Warning() != "" {
		t.Fatal(passing.Warning())
	}
}

func TestUnreachableContrast(t *testing.T) {
	mid, _ := parse("#777777")
	_, ratio, ok := fit(mid, []rgb{mid}, 21)
	if ok || ratio >= 21 {
		t.Fatalf("ok %v ratio %v", ok, ratio)
	}
	bad := Theme{InkOK: false, InkRatio: 4.2, BorderOK: true, LinkOK: true}
	if bad.OK() || bad.Warning() == "" || !strings.Contains(bad.Warning(), "4.20:1") {
		t.Fatal(bad.Warning())
	}
}

func TestRootCSSIsHexOnly(t *testing.T) {
	css := Build("#1a365d").RootCSS()
	if strings.Contains(css, "<") || !strings.Contains(css, "--pine:#1a365d") || !strings.Contains(css, "--foam:#ffffff") {
		t.Fatal(css)
	}
}
