package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/config"
)

func TestLeadingBlankLineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{{
			ID: "sb", Name: "Draft Wiki", Hosts: []string{"127.0.0.1"}, PathPrefix: "/sb",
			Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "sb.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Draft Wiki", MaxUpload: 1 << 20,
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hash, err := auth.Hash("test-password-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Sites[0].Store.CreateUser("Ada", hash, true); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	login(t, client, ts.URL, "/sb", "Ada", "test-password-1", "")

	const page = "\n\nHello\n\n== Notes ==\n\nbody line"
	editURL := ts.URL + "/sb/index.php?title=Notes&action=edit"
	saveBox(t, client, editURL, "", page)
	if got := pageText(t, srv, "Notes"); got != page {
		t.Fatalf("initial save %q", got)
	}

	// A browser drops the newline that follows <textarea>. Saving what the
	// browser would submit must leave the leading blank line in place.
	for i := 0; i < 2; i++ {
		body := readAll(t, get(t, client, editURL))
		raw := rawTextarea(t, body)
		if !strings.HasPrefix(raw, "\n"+page) {
			t.Fatalf("edit box missing the padding newline: %q", raw)
		}
		saveBox(t, client, editURL, "", textarea(t, body))
		if got := pageText(t, srv, "Notes"); got != page {
			t.Fatalf("full page save %d stored %q", i+1, got)
		}
	}

	secURL := editURL + "&section=0"
	for i := 0; i < 2; i++ {
		body := readAll(t, get(t, client, secURL))
		raw := rawTextarea(t, body)
		const lead = "\n\nHello"
		if !strings.HasPrefix(raw, "\n"+lead) {
			t.Fatalf("section box: %q", raw)
		}
		saveBox(t, client, secURL, "0", textarea(t, body))
		if got := pageText(t, srv, "Notes"); got != page {
			t.Fatalf("section 0 save %d stored %q", i+1, got)
		}
	}

	notesURL := editURL + "&section=1"
	body := readAll(t, get(t, client, notesURL))
	raw := rawTextarea(t, body)
	const notes = "== Notes ==\n\nbody line"
	if !strings.HasPrefix(raw, "\n"+notes) {
		t.Fatalf("notes box: %q", raw)
	}
	saveBox(t, client, notesURL, "1", textarea(t, body))
	if got := pageText(t, srv, "Notes"); got != page {
		t.Fatalf("section 1 save stored %q", got)
	}
}

func saveBox(t *testing.T, c *http.Client, u, section, text string) {
	t.Helper()
	form := readAll(t, get(t, c, u))
	res := postForm(t, c, u, url.Values{
		"csrf": {attr(t, form, "csrf")}, "base_rev": {attr(t, form, "base_rev")},
		"wpSection": {section}, "wpTextbox1": {text}, "wpSummary": {"blank"}, "wpSave": {"Save changes"},
	})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save %d %s", res.StatusCode, readAll(t, res))
	}
	res.Body.Close()
}

func rawTextarea(t *testing.T, body string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<textarea[^>]*>(.*?)</textarea>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no textarea")
	}
	return m[1]
}

func pageText(t *testing.T, srv *Server, dbkey string) string {
	t.Helper()
	body, ok, err := srv.Sites[0].Store.ContentByDBKey(dbkey)
	if err != nil || !ok {
		t.Fatalf("page %s: %v ok=%v", dbkey, err, ok)
	}
	return body
}
