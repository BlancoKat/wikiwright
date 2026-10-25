package ingest

import (
	"strings"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/mwxml"
	"github.com/BlancoKat/wikiwright/internal/store"
)

const dump143 = `<?xml version="1.0" encoding="UTF-8"?>
<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11" xml:lang="en">
  <siteinfo><sitename>Demo Wiki</sitename></siteinfo>
  <page>
    <title>Studio</title>
    <ns>0</ns>
    <id>1</id>
    <revision>
      <id>10</id>
      <parentid>0</parentid>
      <timestamp>2024-01-02T03:04:05Z</timestamp>
      <contributor><username>Quinn</username><id>1</id></contributor>
      <minor/>
      <comment>sketch</comment>
      <model>wikitext</model>
      <format>text/x-wiki</format>
      <text xml:space="preserve">[[File:Sketch.png|thumb|A sketch]]</text>
      <sha1>aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</sha1>
      <content>
        <role>mediainfo</role>
        <origin>10</origin>
        <model>json</model>
        <format>application/json</format>
        <text xml:space="preserve">{"ignore":true}</text>
      </content>
    </revision>
  </page>
  <page>
    <title>Wrapped</title>
    <ns>0</ns>
    <id>2</id>
    <revision>
      <id>11</id>
      <timestamp>2024-02-02T03:04:05Z</timestamp>
      <contributor><username>Quinn</username><id>1</id></contributor>
      <content>
        <role>main</role>
        <origin>11</origin>
        <model>wikitext</model>
        <format>text/x-wiki</format>
        <text xml:space="preserve">slot body</text>
      </content>
      <content>
        <role>aux</role>
        <model>json</model>
        <format>application/json</format>
        <text xml:space="preserve">ignore aux</text>
      </content>
    </revision>
  </page>
</mediawiki>
`

func TestApply143MCRAndRVExport(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pages, _, err := mwxml.Parse(strings.NewReader(dump143))
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(st, pages, "Demo Wiki"); err != nil {
		t.Fatal(err)
	}
	studio, ok, err := st.ContentByDBKey("Studio")
	if err != nil || !ok {
		t.Fatalf("studio %v %v", ok, err)
	}
	if !strings.Contains(studio, "A sketch") || strings.Contains(studio, "ignore") {
		t.Fatalf("main slot: %q", studio)
	}
	wrapped, ok, err := st.ContentByDBKey("Wrapped")
	if err != nil || !ok || wrapped != "slot body" {
		t.Fatalf("wrapped %q %v %v", wrapped, ok, err)
	}
	rev, err := st.RevisionByID(10)
	if err != nil || rev.UserText != "Quinn" || !rev.Minor {
		t.Fatalf("rev %+v %v", rev, err)
	}
}
