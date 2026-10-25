package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/mwxml"
	"github.com/BlancoKat/wikiwright/internal/store"
)

func TestApplyKeepsUsersAndSkipsDuplicateRevisions(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser("kat", "hash", true); err != nil {
		t.Fatal(err)
	}
	pages := []mwxml.Page{{
		Title: "Equals",
		Revisions: []mwxml.Revision{{
			MWRevID: 42,
			User:    "Ada",
			Time:    time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
			Text:    "===\n",
		}},
	}}
	if err := Apply(st, pages, "Draft Wiki"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(st, pages, "Draft Wiki"); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByName("kat")
	if err != nil || u.ID == 0 || !u.IsAdmin || u.Name != "kat" {
		t.Fatalf("user %+v %v", u, err)
	}
	pg, err := st.PageByDBKey("Equals")
	if err != nil || pg.ID == 0 {
		t.Fatal(pg, err)
	}
	revs, err := st.Revisions(pg.ID)
	if err != nil || len(revs) != 1 || revs[0].MWRevID != 42 {
		t.Fatalf("revs %+v %v", revs, err)
	}
}

func TestReindexContinuesAfterPanic(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pages := []mwxml.Page{
		{Title: "Bad", Revisions: []mwxml.Revision{{MWRevID: 1, User: "Ada", Text: "bad", Time: time.Unix(1, 0).UTC()}}},
		{Title: "Good", Revisions: []mwxml.Revision{{MWRevID: 2, User: "Ada", Text: "[[Keep]]", Time: time.Unix(2, 0).UTC()}}},
	}
	if err := StorePages(st, pages, "Draft Wiki"); err != nil {
		t.Fatal(err)
	}
	var seen []string
	reindexHook = func(title string) {
		seen = append(seen, title)
		if title == "Bad" {
			panic("boom")
		}
	}
	defer func() { reindexHook = nil }()
	if err := Reindex(st, "Draft Wiki"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "Bad,Good" {
		t.Fatalf("seen %v", seen)
	}
}
