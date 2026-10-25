package mwapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/store"
)

func TestSharedSVG(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8"><rect width="12" height="8"/></svg>`)
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			if q.Get("apnamespace") == "0" {
				_, _ = w.Write([]byte(`{"query":{"allpages":[{"title":"Glossary"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
		case q.Get("list") == "allimages":
			_, _ = w.Write([]byte(`{"query":{"allimages":[]}}`))
		case q.Get("prop") == "revisions":
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Glossary","revisions":[{"revid":1,"parentid":0,"user":"Ada","timestamp":"2020-01-02T00:00:00Z","comment":"","minor":false,"slots":{"main":{"content":"[[File:Beacon.svg|Beacon|150px]]"}}}]}]}}`))
		case q.Get("prop") == "imageinfo":
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"File:Beacon.svg","missing":true,"imageinfo":[{"url":"http://` + r.Host + `/beacon.svg","mime":"image/svg+xml","size":80,"width":252,"height":346}]}]}}`))
		default:
			http.Error(w, "unexpected "+r.URL.RawQuery, 500)
		}
	})
	mux.HandleFunc("/beacon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(svg)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	st, err := store.Open(dir + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// The handler captured r.Host, which is correct for the httptest server.
	err = Import(context.Background(), st, Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Miscellany",
		FilesDir:      dir + "/files",
		DownloadFiles: true,
		Client:        srv.Client(),
		Delay:         -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.File("Beacon.svg")
	if err != nil || f.Name == "" || f.MIME != "image/svg+xml" || f.Width != 252 {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestImportFakeAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			if q.Get("apnamespace") == "0" {
				_, _ = w.Write([]byte(`{"query":{"allpages":[{"pageid":1,"ns":0,"title":"Studio"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
		case q.Get("list") == "allimages":
			_, _ = w.Write([]byte(`{"query":{"allimages":[]}}`))
		case q.Get("prop") == "revisions":
			_, _ = w.Write([]byte(`{"query":{"pages":[{"pageid":1,"ns":0,"title":"Studio","revisions":[{"revid":3,"parentid":0,"user":"Quinn","timestamp":"2024-06-01T12:00:00Z","comment":"paint","minor":false,"slots":{"main":{"contentmodel":"wikitext","contentformat":"text/x-wiki","content":"Oil on canvas"}}}]}]}}`))
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Import(context.Background(), st, Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Demo Wiki",
		DownloadFiles: false,
		Client:        srv.Client(),
		Delay:         -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, ok, err := st.ContentByDBKey("Studio")
	if err != nil || !ok || body != "Oil on canvas" {
		t.Fatalf("%q %v %v", body, ok, err)
	}
	pg, err := st.PageByDBKey("Studio")
	if err != nil || pg.ID == 0 {
		t.Fatal(pg, err)
	}
	rev, err := st.ResolveOldID(pg.ID, 3)
	if err != nil || rev.MWRevID != 3 || rev.UserText != "Quinn" {
		t.Fatalf("%+v %v", rev, err)
	}
	if rev.Timestamp.UTC().Format(time.RFC3339) != "2024-06-01T12:00:00Z" {
		t.Fatalf("time %s", rev.Timestamp)
	}
	if strings.Contains(body, "{") {
		t.Fatal("slot metadata leaked")
	}
}

func TestImportCacheAndSkipStored(t *testing.T) {
	var revCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			if q.Get("apnamespace") == "0" {
				_, _ = w.Write([]byte(`{"query":{"allpages":[{"title":"Equals"},{"title":"Other"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
		case q.Get("prop") == "revisions":
			revCalls++
			title := q.Get("titles")
			body := "plain"
			id := 8
			if title == "Equals" {
				body = "==="
				id = 7
			}
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"` + title + `","revisions":[{"revid":` + strconv.Itoa(id) + `,"parentid":0,"user":"Ada","timestamp":"2020-01-02T00:00:00Z","comment":"","minor":false,"slots":{"main":{"content":"` + body + `"}}}]}]}}`))
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	st, err := store.Open(dir + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser("kat", "hash", true); err != nil {
		t.Fatal(err)
	}
	cache := dir + "/cache.xml"
	opt := Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Draft Wiki",
		DownloadFiles: false,
		Client:        srv.Client(),
		Delay:         -1,
		Cache:         cache,
		Logf:          func(string, ...any) {},
	}
	if err := Import(context.Background(), st, opt); err != nil {
		t.Fatal(err)
	}
	if revCalls != 2 {
		t.Fatalf("revision calls %d", revCalls)
	}
	body, ok, err := st.ContentByDBKey("Equals")
	if err != nil || !ok || body != "===" {
		t.Fatalf("%q %v %v", body, ok, err)
	}
	u, err := st.UserByName("kat")
	if err != nil || u.ID == 0 || !u.IsAdmin {
		t.Fatalf("user lost %+v %v", u, err)
	}
	revCalls = 0
	if err := Import(context.Background(), st, opt); err != nil {
		t.Fatal(err)
	}
	if revCalls != 0 {
		t.Fatalf("cache rerun downloaded %d revision lists", revCalls)
	}
	revs, err := st.Revisions(mustPage(t, st, "Equals"))
	if err != nil || len(revs) != 1 {
		t.Fatalf("revs %+v %v", revs, err)
	}

	// No cache file: a second wiki still skips pages that are already stored.
	st2, err := store.Open(dir + "/wiki2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	opt.Cache = ""
	revCalls = 0
	if err := Import(context.Background(), st2, opt); err != nil {
		t.Fatal(err)
	}
	if revCalls != 2 {
		t.Fatalf("fresh fetch calls %d", revCalls)
	}
	revCalls = 0
	if err := Import(context.Background(), st2, opt); err != nil {
		t.Fatal(err)
	}
	if revCalls != 0 {
		t.Fatalf("stored pages were downloaded again: %d", revCalls)
	}
}

func mustPage(t *testing.T, st *store.Store, dbkey string) int64 {
	t.Helper()
	pg, err := st.PageByDBKey(dbkey)
	if err != nil || pg.ID == 0 {
		t.Fatal(pg, err)
	}
	return pg.ID
}
