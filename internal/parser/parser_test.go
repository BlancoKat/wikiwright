package parser

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/mwxml"
)

func existsSet(keys ...string) func(string) bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return func(k string) bool { return m[k] }
}

func TestMainPageSnippet(t *testing.T) {
	src := `=Atlas=

: A short field guide.

==[[Lighthouses]]==

: Towers along the channel

==[[Field Notes]], Charts, & Sketches==

----

[[Category:Finance]]
[[Category:Shopping]]
[[Category:Coast]]
[[Category:Travel]]
`
	// Pad with headings so the TOC threshold (4) is met.
	src = "=A=\n=B=\n" + src
	res := Parse(src, Options{
		PageTitle: "Main Page",
		Exists:    existsSet("Lighthouses"),
	})
	if !strings.Contains(res.HTML, `id="Lighthouses"`) {
		t.Fatalf("heading id missing:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `title="Lighthouses"`) {
		t.Fatalf("link missing:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `class="new"`) || !strings.Contains(res.HTML, "Field_Notes") {
		t.Fatalf("expected a red link for Field Notes:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<hr />") {
		t.Fatal("missing hr")
	}
	if !strings.Contains(res.HTML, `id="toc"`) {
		t.Fatal("missing toc")
	}
	if len(res.Categories) != 4 {
		t.Fatalf("categories %#v", res.Categories)
	}
	if strings.Contains(res.HTML, "Category:Finance") {
		t.Fatal("category link rendered inline")
	}
}

func TestImageAndLists(t *testing.T) {
	src := "==Tips==\n\n* [https://example.com/charts Reading a chart]\n** a public example\n\n: [[File:Chart&Soundings.png|center|512px]]\n\n# Channel\n#* Marked by buoys\n# Basin\n"
	res := Parse(src, Options{
		PageTitle: "Charts",
		File: func(name string) (FileInfo, bool) {
			if name == "Chart&Soundings.png" {
				return FileInfo{Width: 512, Height: 512}, true
			}
			return FileInfo{}, false
		},
	})
	if !strings.Contains(res.HTML, `src="/w/images/Chart%26Soundings.png"`) {
		t.Fatalf("image src:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `class="center"`) || !strings.Contains(res.HTML, `width="512"`) {
		t.Fatalf("center size:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `class="external text"`) || !strings.Contains(res.HTML, "example.com") {
		t.Fatalf("external:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<ol>") || !strings.Contains(res.HTML, "<ul>") {
		t.Fatalf("lists:\n%s", res.HTML)
	}
}

func TestHeadingFragmentMatchesMediaWiki(t *testing.T) {
	src := "==Ready? Yes!!==\n\n=Salt & Pepper=\n\n==North, South and East==\n\n==What now?==\n"
	res := Parse(src, Options{PageTitle: "Main Page"})
	for _, id := range []string{"Ready.3F_Yes.21.21", "Salt_.26_Pepper", "North.2C_South_and_East", "What_now.3F"} {
		if !strings.Contains(res.HTML, `id="`+id+`"`) {
			t.Errorf("missing id %s\n%s", id, res.HTML)
		}
	}
}

func TestYouTubeBlockquotePreCode(t *testing.T) {
	src := "<blockquote>\nA chart is a picture with [[Depths|soundings]].\n</blockquote>\n\n<youtube>https://youtu.be/8wMKq7HvbKw</youtube>\n\n<pre>\nalpha  beta  gamma\n</pre>\n\nUse <code>echo hello</code> here.\n\n''italic'' and '''bold''' and ''''not quite''''' and '''N.B.'''\n"
	res := Parse(src, Options{PageTitle: "Charts", Exists: existsSet("Depths")})
	if !strings.Contains(res.HTML, "<blockquote>") || !strings.Contains(res.HTML, "Depths") {
		t.Fatalf("blockquote:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `src="https://www.youtube-nocookie.com/embed/8wMKq7HvbKw"`) {
		t.Fatalf("youtube:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "sandbox=") {
		t.Fatal("iframe missing sandbox")
	}
	if !strings.Contains(res.HTML, "<pre>") || !strings.Contains(res.HTML, "alpha  beta") {
		t.Fatalf("pre:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<code>") || !strings.Contains(res.HTML, "echo hello") {
		t.Fatalf("code:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<i>italic</i>") || !strings.Contains(res.HTML, "<b>bold</b>") || !strings.Contains(res.HTML, "<b>N.B.</b>") {
		t.Fatalf("quotes:\n%s", res.HTML)
	}
}

func TestTemplateWithLeadingSpace(t *testing.T) {
	tpl := "----\n\n [[Guide Index|Table of Contents]]\n\n[[Category:Coast]]\n[[Category:Notes]]\n[[Category:Places]]\n[[Category:History]]\n"
	src := "=Field guide: North spit=\n\n* [[Guide North|North spit]]\n\n{{guide bottom}}\n"
	res := Parse(src, Options{
		PageTitle: "North spit",
		GetTemplate: func(dbkey string) (string, bool) {
			if dbkey == "Template:Guide_bottom" {
				return tpl, true
			}
			return "", false
		},
		Exists: existsSet("Guide_Index", "Guide_North"),
	})
	if !strings.Contains(res.HTML, "<hr />") {
		t.Fatalf("template hr:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "Guide_Index") || !strings.Contains(res.HTML, "<pre>") {
		t.Fatalf("template link should be in a pre, got:\n%s", res.HTML)
	}
	if len(res.Categories) != 4 {
		t.Fatalf("cats %#v", res.Categories)
	}
}

func TestTemplateParamsAndNoInclude(t *testing.T) {
	tpl := "<noinclude>docs</noinclude>Hello {{{1|friend}}} and {{{name|anon}}}.<includeonly>ONLYTRANSCLUDE</includeonly>"
	res := Parse("{{greet|Quinn|name=Ada}}", Options{
		PageTitle: "X",
		GetTemplate: func(dbkey string) (string, bool) {
			if dbkey == "Template:Greet" {
				return tpl, true
			}
			return "", false
		},
	})
	if strings.Contains(res.HTML, "docs") {
		t.Fatalf("noinclude leaked: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "Hello Quinn") || !strings.Contains(res.HTML, "Ada") {
		t.Fatalf("params: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "ONLYTRANSCLUDE") {
		t.Fatalf("includeonly missing: %s", res.HTML)
	}
	// Viewing the template page hides includeonly.
	view := Parse(tpl, Options{PageTitle: "Template:Greet"})
	if strings.Contains(view.HTML, "ONLYTRANSCLUDE") || !strings.Contains(view.HTML, "docs") {
		t.Fatalf("view of template: %s", view.HTML)
	}
}

func TestOnlyInclude(t *testing.T) {
	tpl := "hidden <onlyinclude>visible {{{1}}}</onlyinclude> also hidden"
	res := Parse("{{box|yes}}", Options{
		PageTitle:   "X",
		GetTemplate: func(string) (string, bool) { return tpl, true },
	})
	if !strings.Contains(res.HTML, "visible yes") || strings.Contains(res.HTML, "hidden") {
		t.Fatalf("%s", res.HTML)
	}
}

func TestWikitableBookmarks(t *testing.T) {
	src := `{|
! command
! action
|-
| sudo pacman -Syy
| refresh package database
|-
| sudo pacman -S <name of package>
| installs package
|}`
	res := Parse(src, Options{PageTitle: "Bookmarks"})
	if !strings.Contains(res.HTML, "<th>") || !strings.Contains(res.HTML, "pacman -Syy") {
		t.Fatalf("%s", res.HTML)
	}
	if strings.Contains(res.HTML, "<name") {
		t.Fatalf("placeholder became a tag:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "&lt;name of package&gt;") {
		t.Fatalf("placeholder not escaped:\n%s", res.HTML)
	}
}

func TestSectionEditLinksSkipTemplates(t *testing.T) {
	tpl := "== From template ==\ntpl text"
	src := "== Alpha ==\na\n{{box}}\n== Beta ==\nb\n"
	res := Parse(src, Options{
		PageTitle:    "Page",
		SectionEdits: true,
		GetTemplate: func(dbkey string) (string, bool) {
			if dbkey == "Template:Box" {
				return tpl, true
			}
			return "", false
		},
		Link: func(dbkey string) string { return "/w/index.php?title=" + dbkey },
	})
	if strings.Count(res.HTML, `class="mw-editsection"`) != 2 {
		t.Fatalf("edit links: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "section=1") || !strings.Contains(res.HTML, "section=2") || strings.Contains(res.HTML, "section=3") {
		t.Fatalf("section numbers: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "From template") || !strings.Contains(res.HTML, `id="Alpha"`) || !strings.Contains(res.HTML, `id="Beta"`) {
		t.Fatalf("headings: %s", res.HTML)
	}
	// The template heading itself has no edit link.
	if strings.Contains(res.HTML, "Edit section: From template") {
		t.Fatalf("template heading is editable: %s", res.HTML)
	}
	no := Parse("__NOEDITSECTION__\n== Alpha ==\n", Options{PageTitle: "P", SectionEdits: true})
	if strings.Contains(no.HTML, "mw-editsection") {
		t.Fatal("NOEDITSECTION ignored")
	}
	via := Parse("== Alpha ==\n{{hide}}", Options{
		PageTitle:    "P",
		SectionEdits: true,
		GetTemplate:  func(string) (string, bool) { return "__NOEDITSECTION__", true },
	})
	if strings.Contains(via.HTML, "mw-editsection") {
		t.Fatal("NOEDITSECTION from a template ignored")
	}
	off := Parse("== Alpha ==\n", Options{PageTitle: "P", SectionEdits: false})
	if strings.Contains(off.HTML, "mw-editsection") {
		t.Fatal("links shown when the reader cannot edit")
	}
}

func TestMagicTOCRedirectAndNowiki(t *testing.T) {
	src := "__NOTOC__\n=One=\n=Two=\n=Three=\n=Four=\n<nowiki>''not bold'' {{not a template}}</nowiki>\n"
	res := Parse(src, Options{PageTitle: "T"})
	if strings.Contains(res.HTML, `id="toc"`) {
		t.Fatal("NOTOC ignored")
	}
	if !strings.Contains(res.HTML, "&#39;&#39;not bold&#39;&#39;") && !strings.Contains(res.HTML, "''not bold''") {
		t.Fatalf("nowiki: %s", res.HTML)
	}
	red := Parse("#REDIRECT [[Main Page#Top]]\n[[Category:Meta]]", Options{
		PageTitle: "Alias",
		Exists:    existsSet("Main_Page"),
	})
	if !red.IsRedirect || red.Redirect != "Main_Page" || red.RedirectFragment != "Top" {
		t.Fatalf("redirect %#v", red)
	}
	if len(red.Categories) != 1 || red.Categories[0].Name != "Meta" {
		t.Fatalf("redirect cats %#v", red.Categories)
	}
}

func TestUnclosedComment(t *testing.T) {
	src := "Visible\n<!--\n==[[Workshop Hours]]==\n--[[Category:Notes]] --!>\n"
	res := Parse(src, Options{PageTitle: "Main Page"})
	if strings.Contains(res.HTML, "Workshop") || strings.Contains(res.HTML, "Notes") {
		t.Fatalf("unclosed comment leaked:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "Visible") {
		t.Fatalf("kept text missing:\n%s", res.HTML)
	}
	if len(res.Categories) != 0 {
		t.Fatalf("category inside comment: %#v", res.Categories)
	}
}

func TestExternalLinkWithInternalLabel(t *testing.T) {
	src := `[https://example.com/guide A guide to [[Tools|hand tools]] ]`
	res := Parse(src, Options{PageTitle: "Notes", Exists: existsSet("Tools")})
	if !strings.Contains(res.HTML, `href="https://example.com/guide"`) {
		t.Fatalf("url truncated:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `title="Tools"`) || !strings.Contains(res.HTML, "hand tools") {
		t.Fatalf("inner link:\n%s", res.HTML)
	}
}

func TestQuoteTag(t *testing.T) {
	src := "<q>The lamp uses [[Brass & Glass|brass]]</q>"
	res := Parse(src, Options{PageTitle: "Lamp", Exists: existsSet("Brass_&_Glass")})
	if !strings.Contains(res.HTML, "<q>") || !strings.Contains(res.HTML, "</q>") {
		t.Fatalf("q tag:\n%s", res.HTML)
	}
	if strings.Contains(res.HTML, "wikiwright-warning") || !strings.Contains(res.HTML, "brass") {
		t.Fatalf("q body:\n%s", res.HTML)
	}
}

func TestEmbedVideoFunctionAndUnsafe(t *testing.T) {
	src := "{{#ev:youtube|dQw4w9WgXcQ|300|right|A caption}}\n{{#nope:evil}}\n<script>alert(1)</script>\n[javascript:alert(1) click]\n"
	res := Parse(src, Options{PageTitle: "Videos", Now: time.Date(2026, 7, 23, 17, 17, 0, 0, time.UTC)})
	if !strings.Contains(res.HTML, "youtube-nocookie.com/embed/dQw4w9WgXcQ") {
		t.Fatalf("ev: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "floatright") || !strings.Contains(res.HTML, "A caption") {
		t.Fatalf("align/caption: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "wikiwright-warning") || !strings.Contains(res.HTML, "#nope") {
		t.Fatalf("unknown function: %s warnings=%v", res.HTML, res.Warnings)
	}
	if strings.Contains(res.HTML, "<script") || strings.Contains(res.HTML, `href="javascript:`) {
		t.Fatalf("xss leaked:\n%s", res.HTML)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warnings")
	}
}

func TestThumbAndMissingFile(t *testing.T) {
	src := "[[File:Harbor-side.jpg|thumb|220px|The harbor]]\n[[File:Missing.png|400px]]\n[[:Category:Coast|see the coast]]\n"
	res := Parse(src, Options{
		PageTitle: "Lighthouses",
		File: func(name string) (FileInfo, bool) {
			if name == "Harbor-side.jpg" {
				return FileInfo{Width: 800, Height: 600}, true
			}
			return FileInfo{}, false
		},
		Exists: existsSet("Category:Coast"),
	})
	if !strings.Contains(res.HTML, `class="thumb tright"`) || !strings.Contains(res.HTML, "The harbor") {
		t.Fatalf("thumb:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, `class="new"`) || !strings.Contains(res.HTML, "Missing.png") {
		t.Fatalf("missing file:\n%s", res.HTML)
	}
	if len(res.Categories) != 0 {
		t.Fatalf("forced category link should not categorize: %#v", res.Categories)
	}
	if !strings.Contains(res.HTML, "Category:Coast") {
		t.Fatalf("category link:\n%s", res.HTML)
	}
}

func TestRealRVExportRenders(t *testing.T) {
	f, err := os.Open("../../testdata/sample-export.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pages, _, err := mwxml.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	var main string
	for _, p := range pages {
		if p.Title == "Main Page" && len(p.Revisions) > 0 {
			main = p.Revisions[len(p.Revisions)-1].Text
		}
	}
	if main == "" {
		t.Fatal("main page not in export")
	}
	res := Parse(main, Options{PageTitle: "Main Page", ProjectNS: "Harbor Atlas"})
	if !strings.Contains(res.HTML, "Lighthouses") {
		t.Fatalf("real main page did not render links:\n%s", res.HTML[:min(500, len(res.HTML))])
	}
	if len(res.Categories) < 4 {
		t.Fatalf("cats %#v", res.Categories)
	}
	if strings.Contains(res.HTML, "<script") {
		t.Fatal("script in real page render")
	}
}

func TestTranscludedTemplateComments(t *testing.T) {
	noteTop := "\n----\n\n<!-- special categories -->"
	note := "\n<!-- usual categories -->\n\n[[Category:Notes]]\n[[Category:Drafts]]\n[[Category:Archive]]"
	src := "{{Note top}}\nA short entry.\n{{Note}}\n"
	res := Parse(src, Options{
		PageTitle: "2020-01-02",
		GetTemplate: func(dbkey string) (string, bool) {
			switch dbkey {
			case "Template:Note_top":
				return noteTop, true
			case "Template:Note":
				return note, true
			default:
				return "", false
			}
		},
	})
	if strings.Contains(res.HTML, "&lt;!--") || strings.Contains(res.HTML, "<!--") {
		t.Fatalf("template comment visible:\n%s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<hr") || !strings.Contains(res.HTML, "A short entry.") {
		t.Fatalf("template content missing:\n%s", res.HTML)
	}
	got := map[string]bool{}
	for _, c := range res.Categories {
		got[c.Name] = true
	}
	for _, name := range []string{"Notes", "Drafts", "Archive"} {
		if !got[name] {
			t.Fatalf("categories %#v", res.Categories)
		}
	}
	// A comment inside nowiki is literal text, not a comment.
	kept := Parse("{{box}}", Options{
		PageTitle:   "N",
		GetTemplate: func(string) (string, bool) { return "<nowiki><!-- show --></nowiki>", true },
	})
	if !strings.Contains(kept.HTML, "&lt;!-- show --&gt;") && !strings.Contains(kept.HTML, "<!-- show -->") {
		t.Fatalf("nowiki comment: %s", kept.HTML)
	}
	// An unclosed comment ends at the template, not the rest of the page.
	eaten := Parse("Before {{hide}} After", Options{
		PageTitle: "E",
		GetTemplate: func(string) (string, bool) {
			return "kept\n<!--\n[[Category:Secret]]", true
		},
	})
	if strings.Contains(eaten.HTML, "Secret") || strings.Contains(eaten.HTML, "&lt;!--") {
		t.Fatalf("unclosed template comment:\n%s cats=%#v", eaten.HTML, eaten.Categories)
	}
	if !strings.Contains(eaten.HTML, "Before") || !strings.Contains(eaten.HTML, "After") || !strings.Contains(eaten.HTML, "kept") {
		t.Fatalf("page text around template:\n%s", eaten.HTML)
	}
	if len(eaten.Categories) != 0 {
		t.Fatalf("category in template comment: %#v", eaten.Categories)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
