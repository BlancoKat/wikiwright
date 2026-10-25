package mwxml

import (
	"strings"
	"testing"
	"time"
)

func TestWriteRoundTrip(t *testing.T) {
	in := []Page{{
		Title: "Equals",
		NS:    0,
		Revisions: []Revision{{
			MWRevID:  9,
			ParentID: 0,
			Time:     time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
			User:     "Ada",
			Comment:  "a < b & c",
			Minor:    true,
			Text:     "===\n[[A & B]]",
			Model:    "wikitext",
		}, {
			MWRevID: 10,
			User:    "203.0.113.5",
			Time:    time.Date(2020, 2, 2, 0, 0, 0, 0, time.UTC),
			Deleted: true,
		}},
	}}
	var buf strings.Builder
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	pages, _, err := Parse(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err, buf.String())
	}
	if len(pages) != 1 || pages[0].Title != "Equals" || len(pages[0].Revisions) != 2 {
		t.Fatalf("%+v\n%s", pages, buf.String())
	}
	rev := pages[0].Revisions[0]
	if rev.MWRevID != 9 || !rev.Minor || rev.User != "Ada" || rev.Comment != "a < b & c" || rev.Text != "===\n[[A & B]]" {
		t.Fatalf("%+v", rev)
	}
	if !pages[0].Revisions[1].Deleted || pages[0].Revisions[1].User != "203.0.113.5" {
		t.Fatalf("deleted %+v", pages[0].Revisions[1])
	}
}
