package mwxml

import (
	"os"
	"strings"
	"testing"
)

func TestRVExportSchema011(t *testing.T) {
	f, err := os.Open("../../testdata/sample-export.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pages, stats, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages < 10 {
		t.Fatalf("pages %d", stats.Pages)
	}
	var main Page
	for _, p := range pages {
		if p.Title == "Main Page" {
			main = p
		}
	}
	if len(main.Revisions) != 1 {
		t.Fatalf("revs %+v", main.Revisions)
	}
	rev := main.Revisions[0]
	if rev.MWRevID != 37 {
		t.Fatalf("mw rev id %d", rev.MWRevID)
	}
	if !strings.Contains(rev.Text, "=Harbor Atlas=") {
		t.Fatalf("text %q", rev.Text[:80])
	}
	if strings.Contains(rev.Text, "&amp;") {
		t.Fatal("XML entities were not decoded")
	}
	if !strings.Contains(rev.Text, "Maps & Charts") {
		t.Fatalf("expected decoded ampersand in text:\n%s", rev.Text)
	}
	if rev.Model != "" && rev.Model != "wikitext" {
		t.Fatalf("model %s", rev.Model)
	}
}

const dump132 = `<?xml version="1.0" encoding="UTF-8"?>
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.10/" version="0.10" xml:lang="en">
  <page>
    <title>Old Page</title>
    <ns>0</ns>
    <id>3</id>
    <revision>
      <id>9</id>
      <timestamp>2019-05-01T12:00:00Z</timestamp>
      <contributor><username>Ada</username><id>1</id></contributor>
      <text xml:space="preserve">Hello from 1.32</text>
    </revision>
    <revision>
      <id>10</id>
      <parentid>9</parentid>
      <timestamp>2019-06-01T12:00:00Z</timestamp>
      <contributor><ip>203.0.113.5</ip></contributor>
      <minor/>
      <comment>tweak</comment>
      <text xml:space="preserve">Hello again</text>
    </revision>
  </page>
</mediawiki>`

// MediaWiki 1.43 dumpBackup: main slot inline, extra MCR slot wrapped in content/role.
const dump143 = `<?xml version="1.0" encoding="UTF-8"?>
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11" xml:lang="en">
  <siteinfo><generator>MediaWiki 1.43.0</generator></siteinfo>
  <page>
    <title>Demo Wiki:About</title>
    <ns>4</ns>
    <id>4</id>
    <revision>
      <id>100</id>
      <parentid>99</parentid>
      <timestamp>2026-01-15T18:30:00Z</timestamp>
      <contributor><username>Quinn</username><id>1</id></contributor>
      <comment>studio notes</comment>
      <origin>100</origin>
      <model>wikitext</model>
      <format>text/x-wiki</format>
      <text bytes="42" sha1="abc" xml:space="preserve">=Studio=

[[File:Sketch.png|thumb|A sketch]]</text>
      <sha1>abc</sha1>
      <content>
        <role>mediainfo</role>
        <origin>100</origin>
        <model>json</model>
        <format>application/json</format>
        <text bytes="13" sha1="def" xml:space="preserve">{"ignore":1}</text>
      </content>
    </revision>
    <revision>
      <id>101</id>
      <parentid>100</parentid>
      <timestamp>2026-02-01T00:00:00Z</timestamp>
      <contributor deleted="deleted"/>
      <comment deleted="deleted"/>
      <origin>101</origin>
      <model>wikitext</model>
      <format>text/x-wiki</format>
      <text bytes="0" deleted="deleted"/>
      <sha1/>
    </revision>
  </page>
  <page>
    <title>Wrapped</title>
    <ns>0</ns>
    <id>5</id>
    <revision>
      <id>8</id>
      <timestamp>2026-03-01T00:00:00Z</timestamp>
      <contributor><username>Quinn</username><id>1</id></contributor>
      <content>
        <role>main</role>
        <origin>8</origin>
        <model>wikitext</model>
        <format>text/x-wiki</format>
        <text bytes="11" sha1="aaa" xml:space="preserve">slot body</text>
      </content>
      <content>
        <slot>aux</slot>
        <model>text</model>
        <format>text/plain</format>
        <text xml:space="preserve">not the article</text>
      </content>
    </revision>
  </page>
</mediawiki>`

func TestSchema010And143MCR(t *testing.T) {
	old, _, err := Parse(strings.NewReader(dump132))
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || len(old[0].Revisions) != 2 {
		t.Fatalf("%+v", old)
	}
	if old[0].Revisions[0].Text != "Hello from 1.32" || old[0].Revisions[0].User != "Ada" {
		t.Fatalf("old first %+v", old[0].Revisions[0])
	}
	if old[0].Revisions[1].User != "203.0.113.5" || !old[0].Revisions[1].Minor || old[0].Revisions[1].ParentID != 9 {
		t.Fatalf("old second %+v", old[0].Revisions[1])
	}

	pages, stats, err := Parse(strings.NewReader(dump143))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 2 {
		t.Fatalf("pages %d", stats.Pages)
	}
	about := pages[0]
	if !strings.Contains(about.Revisions[0].Text, "[[File:Sketch.png|thumb|A sketch]]") {
		t.Fatalf("main slot lost or replaced by mediainfo: %q", about.Revisions[0].Text)
	}
	if strings.Contains(about.Revisions[0].Text, "ignore") {
		t.Fatal("non-main slot leaked into the article")
	}
	if about.Revisions[0].User != "Quinn" || about.Revisions[0].Model != "wikitext" {
		t.Fatalf("meta %+v", about.Revisions[0])
	}
	if !about.Revisions[1].Deleted || about.Revisions[1].Text != "" || about.Revisions[1].User != "" {
		t.Fatalf("deleted rev %+v", about.Revisions[1])
	}
	wrapped := pages[1]
	if wrapped.Revisions[0].Text != "slot body" {
		t.Fatalf("wrapped main slot: %q", wrapped.Revisions[0].Text)
	}
}

func TestDBKeySkipsSpecial(t *testing.T) {
	if _, ok := DBKey("Special:RecentChanges", "Harbor Atlas"); ok {
		t.Fatal("special pages are not stored")
	}
	tkey, ok := DBKey("Template:Fhb bottom", "Family History Book")
	if !ok || tkey.DBKey != "Template:Fhb_bottom" {
		t.Fatalf("%+v %v", tkey, ok)
	}
	side, ok := DBKey("MediaWiki:Sidebar", "Draft Wiki")
	if !ok || side.Namespace != 8 || side.DBKey != "MediaWiki:Sidebar" {
		t.Fatalf("sidebar %+v %v", side, ok)
	}
}

func TestParseKeepsMediaWikiSidebar(t *testing.T) {
	const dump = `<?xml version="1.0"?>
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/">
  <page>
    <title>MediaWiki:Sidebar</title>
    <ns>8</ns>
    <revision>
      <id>4</id>
      <timestamp>2020-01-02T00:00:00Z</timestamp>
      <contributor><username>Ada</username></contributor>
      <text xml:space="preserve">* navigation
** mainpage|mainpage-description
</text>
    </revision>
  </page>
</mediawiki>`
	pages, _, err := Parse(strings.NewReader(dump))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Title != "MediaWiki:Sidebar" || pages[0].NS != 8 {
		t.Fatalf("%+v", pages)
	}
	if !strings.Contains(pages[0].Revisions[0].Text, "** mainpage|mainpage-description") {
		t.Fatal(pages[0].Revisions[0].Text)
	}
}
