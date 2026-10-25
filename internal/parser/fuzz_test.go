package parser

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"=",
		"==",
		"===",
		"====",
		"======",
		"=======",
		"= =",
		"== ",
		"==\t",
		"=== ",
		"== foo ==",
		"== foo ==   ",
		"== foo ==<!--c-->",
		"== foo == <!-- comment -->",
		"==foo==",
		"== foo =",
		"=foo==",
		"======= foo =======",
		"==\n===\n====\n",
		"<!--\n== foo ==\n-->\n== bar ==\n",
		"== foo ==<!-- unclosed\n== hidden ==\n",
		"<nowiki>==</nowiki>\n== visible ==\n",
		"{{box|==}}\n== Alpha ==\n",
		"{|\n|===\n|}\n",
		"[[File:A.png|===]]\n",
		"''italic'' '''bold''' [http://example.com label]\n",
		"#REDIRECT [[Other]]\n",
		"__NOTOC__\n=One=\n=Two=\n=Three=\n=Four=\n",
		"<pre>\n==\n</pre>\n",
		"===<!-- <!-- -->\n",
		"\xc0\xc0\xc0</A>",
		strings.Repeat("=", 40),
		strings.Repeat("=\n", 30),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 12_000 {
			src = src[:12_000]
		}
		res := Parse(src, Options{PageTitle: "Fuzz", SectionEdits: true, Link: func(db string) string { return "/w/index.php?title=" + db }})
		for _, w := range res.Warnings {
			if strings.HasPrefix(w, "panic:") {
				t.Fatalf("%s", w)
			}
		}
		_ = Sections(src)
		_, _, _ = LookupSection(src, "1")
		_, _ = ReplaceSection(src, "1", "replaced")
		_, _ = ReplaceSection(src, "new", "nope")
	})
}
