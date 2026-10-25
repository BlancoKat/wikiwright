package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/color"
	"github.com/BlancoKat/wikiwright/internal/config"
)

func TestStaticStylesheetIsVersioned(t *testing.T) {
	dir := t.TempDir()
	srv, err := Open(&config.File{
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", PathPrefix: "/w", Access: "public",
			MainPage: "Main Page", SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	page := readAll(t, get(t, ts.Client(), ts.URL+"/w/index.php?title=Main_Page"))
	needle := "/static/wikiwright.css?v="
	i := strings.Index(page, needle)
	if i < 0 {
		t.Fatal("stylesheet link has no version")
	}
	ver := page[i+len(needle):]
	if j := strings.IndexAny(ver, "\"&< "); j >= 0 {
		ver = ver[:j]
	}
	if len(ver) < 8 || ver != staticVersion {
		t.Fatalf("version %q", ver)
	}
	dirPage := readAll(t, get(t, ts.Client(), ts.URL+"/"))
	if !strings.Contains(dirPage, needle+staticVersion) {
		t.Fatal("directory stylesheet is not versioned")
	}

	versioned := get(t, ts.Client(), ts.URL+"/static/wikiwright.css?v="+staticVersion)
	if !strings.Contains(versioned.Header.Get("Cache-Control"), "max-age=86400") {
		t.Fatalf("versioned cache %s", versioned.Header.Get("Cache-Control"))
	}
	versioned.Body.Close()
	plain := get(t, ts.Client(), ts.URL+"/static/wikiwright.css")
	if cc := plain.Header.Get("Cache-Control"); strings.Contains(cc, "max-age=86400") || !strings.Contains(cc, "max-age=0") {
		t.Fatalf("unversioned cache %s", cc)
	}
	plain.Body.Close()
	stale := get(t, ts.Client(), ts.URL+"/static/wikiwright.css?v=old")
	if cc := stale.Header.Get("Cache-Control"); strings.Contains(cc, "max-age=86400") {
		t.Fatalf("stale version cached %s", cc)
	}
	stale.Body.Close()
}

func TestShellSidebarIsFlushLeft(t *testing.T) {
	css := readAll(t, get(t, httpClient(t), mustSite(t).URL+"/static/wikiwright.css?v="+staticVersion))
	shell := cssRule(t, css, ".wikiwright-shell")
	for _, banned := range []string{"max-width", "margin: 0 auto", "auto"} {
		if strings.Contains(shell, banned) {
			t.Fatalf("shell is still inset: %s", shell)
		}
	}
	for _, want := range []string{"width: 100%", "margin: 0", "var(--sidebar)", "linear-gradient"} {
		if !strings.Contains(shell, want) {
			t.Fatalf("shell missing %s: %s", want, shell)
		}
	}
	if !strings.Contains(css, "max-width: 46rem") {
		t.Fatal("content column has no reading width")
	}
	logo := cssRule(t, css, ".wikiwright-logo-link")
	if !strings.Contains(logo, "justify-content: center") || !strings.Contains(logo, "margin: 0;") {
		t.Fatalf("logo is not centered over the nav panel: %s", logo)
	}
	box := cssRule(t, css, ".wikiwright-navbox")
	for _, want := range []string{"background: var(--pine-2)", "border: 0", "border-radius:", "text-align: left", "min-width: 100%"} {
		if !strings.Contains(box, want) {
			t.Fatalf("nav panel missing %s: %s", want, box)
		}
	}
	if !strings.Contains(css, "justify-content: flex-start") {
		t.Fatal("narrow layout does not keep the logo left-aligned")
	}
}

func httpClient(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func mustSite(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	srv, err := Open(&config.File{
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", PathPrefix: "/w", Access: "public",
			MainPage: "Main Page", SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	i := strings.Index(css, selector+" {")
	if i < 0 {
		t.Fatalf("missing rule %s", selector)
	}
	rest := css[i:]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatal("unclosed rule")
	}
	return rest[:j]
}

func TestAboutWikiwright(t *testing.T) {
	ts := mustSite(t)
	client := httpClient(t)
	about := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:Wikiwright"))
	version := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:Version"))
	for _, body := range []string{about, version} {
		for _, want := range []string{
			"About Wikiwright",
			"single program",
			"MediaWiki-style wikitext",
			"SQLite",
			"Wikiwright " + Version,
			"MIT license",
			"Developed using GrokBot.",
			"Copyright 2026 Katherine W. Prawl",
			"https://github.com/BlancoKat/wikiwright",
			"Templates, categories, and images",
			"bot passwords",
			"api.php subset",
			"Appearance",
			"Help with editing",
			"https://www.mediawiki.org/wiki/Help:Formatting",
			"https://www.mediawiki.org/wiki/Help:Contents",
			"The markup is the same as MediaWiki",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
		const credit = "Developed using GrokBot."
		const copyLine = "Copyright 2026 Katherine W. Prawl"
		i := strings.Index(body, credit)
		j := strings.Index(body, copyLine)
		if i < 0 || j < i || body[i+len(credit):j] != "</p><p>" {
			t.Fatal("Developed using GrokBot. is not the line before the copyright")
		}
		for _, banned := range []string{"VisualEditor", "Scribunto", "Help about MediaWiki", "Lua"} {
			if strings.Contains(body, banned) {
				t.Fatalf("about page claims %s", banned)
			}
		}
	}
	home := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if strings.Contains(home, "Help about MediaWiki") || strings.Contains(home, "mediawiki.org/wiki/Special:MyLanguage/Help:Contents") {
		t.Fatal("chrome still links to MediaWiki help")
	}
	if !strings.Contains(home, `id="poweredby"><a href="`) || !strings.Contains(home, "Powered by Wikiwright") || !strings.Contains(home, "title=Special:Wikiwright") {
		t.Fatal("footer does not link to About Wikiwright")
	}
	if !strings.Contains(home, "About Wikiwright") {
		t.Fatal("sidebar help")
	}
	pages := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:SpecialPages"))
	if !strings.Contains(pages, "Special:Wikiwright") || !strings.Contains(pages, "Special:Version") {
		t.Fatal("special pages list")
	}
}

func TestSidebarUsesAccentInkNotPaperRed(t *testing.T) {
	dir := t.TempDir()
	srv, err := Open(&config.File{
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", PathPrefix: "/w", Access: "public",
			MainPage: "Main Page", SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas", Accent: "#8a3e1d",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	site := srv.Sites[0]
	id, err := site.Store.UpsertPage(0, "Pier", "Pier")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := site.Store.AddRevision(id, "Ada", "cats", "The [[Missing buoy]].\n\n[[Category:Harbors]]\n", time.Now(), false, 0, 0); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := readAll(t, get(t, ts.Client(), ts.URL+"/w/index.php?title=Pier"))
	ink := color.Build("#8a3e1d").Ink
	if !strings.Contains(body, "--foam:"+ink) || !strings.Contains(body, "--pine:#8a3e1d") {
		t.Fatal("rust accent was not applied")
	}
	side := body[strings.Index(body, `id="wikiwright-sidebar"`):strings.Index(body, `id="content"`)]
	if !strings.Contains(side, `id="wikiwright-categories"`) || !strings.Contains(side, "Harbors") || !strings.Contains(side, `class="new"`) {
		t.Fatal("sidebar category link")
	}
	css := readAll(t, get(t, ts.Client(), ts.URL+"/static/wikiwright.css?v="+staticVersion))
	for _, want := range []string{
		".wikiwright-sidebar a.new",
		".wikiwright-top a.new",
		"color: var(--foam)",
		"width: 100%",
		"width: auto",
		"max-width: 100%",
		"max-height: 14rem",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("css missing %s", want)
		}
	}
	if strings.Contains(css, "a.new { color: var(--redlink); }") {
		t.Fatal("unscoped red link color overrides sidebar text")
	}
	if code := color.Build("#8a3e1d").InkRatio; code < 4.5 {
		t.Fatalf("on-accent ink contrast %.2f", code)
	}
	// The article's own missing link stays a paper red link. The sidebar must not share that rule.
	article := body[strings.Index(body, `id="content"`):]
	if !strings.Contains(article, `class="new"`) || !strings.Contains(article, "Missing buoy") {
		t.Fatal("article red link")
	}
}
