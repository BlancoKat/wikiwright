package parser

import (
	"strings"
	"testing"
)

func TestParseHeadingDegenerate(t *testing.T) {
	// MediaWiki 1.39: level is min(leading, trailing), capped at 6, and the
	// two runs must not overlap. A line of only "=" signs uses doHeadings,
	// which needs a non-empty interior, so "==" is not a heading and "==="
	// is an h1 whose text is "=".
	cases := []struct {
		line  string
		level int
		text  string
		ok    bool
	}{
		{line: "== Hello ==", level: 2, text: "Hello", ok: true},
		{line: "=Hello=", level: 1, text: "Hello", ok: true},
		{line: "==Hello==", level: 2, text: "Hello", ok: true},
		{line: "===Hello===", level: 3, text: "Hello", ok: true},
		{line: "======Hello======", level: 6, text: "Hello", ok: true},
		{line: "=======Hello=======", level: 6, text: "=Hello=", ok: true},
		{line: "======== Hello ========", level: 6, text: "== Hello ==", ok: true},
		{line: "=", ok: false},
		{line: "==", ok: false},
		{line: "== ", ok: false},
		{line: "==\t", ok: false},
		{line: "===", level: 1, text: "=", ok: true},
		{line: "====", level: 1, text: "==", ok: true},
		{line: "=====", level: 2, text: "=", ok: true},
		{line: "======", level: 2, text: "==", ok: true},
		{line: "=======", level: 3, text: "=", ok: true},
		{line: "========", level: 3, text: "==", ok: true},
		{line: "=========", level: 4, text: "=", ok: true},
		{line: "==========", level: 4, text: "==", ok: true},
		{line: "===========", level: 5, text: "=", ok: true},
		{line: "============", level: 5, text: "==", ok: true},
		{line: "=============", level: 6, text: "=", ok: true},
		{line: "==============", level: 6, text: "==", ok: true},
		{line: "===============", level: 6, text: "===", ok: true},
		{line: "================", level: 6, text: "====", ok: true},
		{line: "= =", level: 1, text: "", ok: true},
		{line: "== ==", level: 2, text: "", ok: true},
		{line: "=== ", level: 1, text: "=", ok: true},
		{line: "===   ", level: 1, text: "=", ok: true},
		{line: "== foo ==   ", level: 2, text: "foo", ok: true},
		{line: "== foo ==\t", level: 2, text: "foo", ok: true},
		{line: "== foo ==<!--c-->", level: 2, text: "foo", ok: true},
		{line: "== foo == <!-- comment -->", level: 2, text: "foo", ok: true},
		{line: "== foo ==<!--c-->   ", level: 2, text: "foo", ok: true},
		{line: "== foo ==  <!-- a --> <!-- b -->", level: 2, text: "foo", ok: true},
		{line: "== foo ==<!-- == -->", level: 2, text: "foo", ok: true},
		{line: "== foo ==<!-- unclosed", level: 2, text: "foo", ok: true},
		{line: "==foo==", level: 2, text: "foo", ok: true},
		{line: "== foo =", level: 1, text: "= foo", ok: true},
		{line: "= foo ==", level: 1, text: "foo =", ok: true},
		{line: "=== foo ==", level: 2, text: "= foo", ok: true},
		{line: "== foo ===", level: 2, text: "foo =", ok: true},
		{line: "=foo==", level: 1, text: "foo=", ok: true},
		{line: "==foo=", level: 1, text: "=foo", ok: true},
		{line: "======= foo =======", level: 6, text: "= foo =", ok: true},
		{line: "", ok: false},
		{line: "foo", ok: false},
		{line: "=foo", ok: false},
		{line: "foo=", ok: false},
		{line: "== foo == <!--c--> bar", ok: false},
		{line: "<!--c-->== foo ==", ok: false},
	}
	for _, tc := range cases {
		level, text, ok := parseHeading(tc.line)
		if ok != tc.ok || level != tc.level || text != tc.text {
			t.Errorf("parseHeading(%q) = (%d, %q, %v), want (%d, %q, %v)", tc.line, level, text, ok, tc.level, tc.text, tc.ok)
		}
	}
}

func TestHeadingLineIgnoresIndent(t *testing.T) {
	if _, _, ok := headingLine(" == foo =="); ok {
		t.Fatal("indented line was a heading")
	}
	level, text, ok := headingLine("== foo ==   ")
	if !ok || level != 2 || text != "foo" {
		t.Fatalf("got %d %q %v", level, text, ok)
	}
	level, text, ok = headingLine("===<!--x-->")
	if !ok || level != 1 || text != "=" {
		t.Fatalf("comment: %d %q %v", level, text, ok)
	}
}

func TestDegenerateHeadingsRender(t *testing.T) {
	res := Parse("===\n", Options{PageTitle: "Degenerate"})
	if strings.Contains(strings.Join(res.Warnings, " "), "panic:") {
		t.Fatal(res.Warnings)
	}
	if !strings.Contains(res.HTML, "<h1>") || !strings.Contains(res.HTML, `id=".3D"`) || !strings.Contains(res.HTML, ">=") {
		t.Fatalf("html:\n%s", res.HTML)
	}
	plain := Parse("==\n", Options{PageTitle: "Not a heading"})
	if strings.Contains(plain.HTML, "<h1") || strings.Contains(plain.HTML, "<h2") {
		t.Fatalf("== became a heading:\n%s", plain.HTML)
	}
	if !strings.Contains(plain.HTML, "==") {
		t.Fatalf("missing text:\n%s", plain.HTML)
	}
	commented := Parse("== foo ==<!-- note -->\n", Options{PageTitle: "C"})
	if !strings.Contains(commented.HTML, "<h2>") || !strings.Contains(commented.HTML, "foo") {
		t.Fatalf("comment heading:\n%s", commented.HTML)
	}
	secs := Sections("===\nbody\n== foo ==<!--c-->\nmore\n")
	if len(secs) != 2 || secs[0].Level != 1 || secs[0].Title != "=" || secs[1].Title != "foo" {
		t.Fatalf("sections %+v", secs)
	}
	if _, _, ok := headingLine("=="); ok {
		t.Fatal("== counted as a section heading")
	}
}

func TestInvalidUTF8CloseTag(t *testing.T) {
	res := Parse("\xc0\xc0\xc0</A>", Options{PageTitle: "utf8"})
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "panic:") {
			t.Fatal(w)
		}
	}
}

func TestParseRecoversFromPanic(t *testing.T) {
	parseHook = func() { panic("boom") }
	defer func() { parseHook = nil }()
	res := Parse("== Hello ==", Options{PageTitle: "Boom Page"})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "panic: boom") {
		t.Fatalf("warnings %#v", res.Warnings)
	}
	if !strings.Contains(res.HTML, "could not be rendered") {
		t.Fatalf("html %s", res.HTML)
	}
}
