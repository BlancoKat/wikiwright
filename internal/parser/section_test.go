package parser

import (
	"strings"
	"testing"
)

func TestSectionsSkipNonSourceHeadings(t *testing.T) {
	src := "" +
		"lead\n" +
		"== Alpha ==\n" +
		"a\n" +
		"=== Beta ===\n" +
		"b\n" +
		"<nowiki>\n== Hidden ==\n</nowiki>\n" +
		"<!--\n== Also hidden ==\n-->\n" +
		"{{box|\n== In template ==\n}}\n" +
		"{|\n== In table ==\n|}\n" +
		"<includeonly>\n== Transcluded ==\n</includeonly>\n" +
		"== Gamma ==\nc\n"
	got := Sections(src)
	if len(got) != 3 {
		t.Fatalf("sections = %d, want 3 (Alpha, Beta, Gamma)", len(got))
	}
	if got[0].Title != "Alpha" || got[0].Index != 1 || got[0].Level != 2 {
		t.Fatalf("first %#v", got[0])
	}
	if got[1].Title != "Beta" || got[1].Index != 2 || got[1].Level != 3 {
		t.Fatalf("second %#v", got[1])
	}
	if got[2].Title != "Gamma" || got[2].Index != 3 {
		t.Fatalf("third %#v", got[2])
	}
	text, sec, ok := LookupSection(src, "2")
	if !ok || sec.Title != "Beta" || !strings.Contains(text, "=== Beta ===") || strings.Contains(text, "Gamma") || strings.Contains(text, "== Alpha ==") {
		t.Fatalf("lookup %q %#v %v", text, sec, ok)
	}
	lead, _, ok := LookupSection(src, "0")
	if !ok || strings.TrimSpace(lead) != "lead" {
		t.Fatalf("lead %q", lead)
	}
}

func TestReplaceAndAppendSection(t *testing.T) {
	src := "lead\n== Alpha ==\na\n=== Beta ===\nb\n== Gamma ==\nc\n"
	out, ok := ReplaceSection(src, "2", "=== Beta ===\nchanged")
	if !ok || !strings.Contains(out, "changed") || !strings.Contains(out, "== Alpha ==") || !strings.Contains(out, "== Gamma ==") {
		t.Fatalf("replace beta: %q", out)
	}
	if strings.Contains(out, "\nb\n") {
		t.Fatalf("old beta body kept: %q", out)
	}
	out, ok = ReplaceSection(src, "1", "== Alpha ==\nAA")
	if !ok || strings.Contains(out, "Beta") || !strings.Contains(out, "AA") || !strings.Contains(out, "== Gamma ==") {
		t.Fatalf("replace parent: %q", out)
	}
	if _, ok = ReplaceSection(src, "9", "nope"); ok {
		t.Fatal("missing section reported success")
	}
	added := AppendNewSection(src, " [[Extra|More]] ", "body")
	if !strings.HasSuffix(strings.TrimRight(added, "\n"), "== [[Extra|More]] ==\n\nbody") && !strings.Contains(added, "== [[Extra|More]] ==\n\nbody") {
		t.Fatalf("append: %q", added)
	}
	if NewSectionSummary(" [[Extra|More]] ") != "/* More */ new section" {
		t.Fatalf("summary %q", NewSectionSummary(" [[Extra|More]] "))
	}
	if SectionEditSummary("'''Alpha'''") != "/* Alpha */ " {
		t.Fatalf("prefill %q", SectionEditSummary("'''Alpha'''"))
	}
}
