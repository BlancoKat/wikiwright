package web

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/config"
)

func TestParseSidebar(t *testing.T) {
	src := "  * navigation\n" +
		"  ** mainpage|mainpage-description <!-- home -->\n" +
		"# comment\n" +
		"** https://example.com/docs|Docs\n" +
		"* SEARCH\n" +
		"* TOOLBOX\n"
	secs := parseSidebar(src)
	if len(secs) != 3 || secs[0].name != "navigation" || len(secs[0].items) != 2 {
		t.Fatalf("%+v", secs)
	}
	if secs[0].items[0].target != "mainpage" || secs[0].items[0].label != "mainpage-description" {
		t.Fatalf("item %+v", secs[0].items[0])
	}
	if secs[0].items[1].target != "https://example.com/docs" || secs[0].items[1].label != "Docs" {
		t.Fatalf("item %+v", secs[0].items[1])
	}
	if secs[1].name != "SEARCH" || secs[2].name != "TOOLBOX" {
		t.Fatalf("%+v", secs)
	}
}

func TestSidebarAndUpload(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", Hosts: []string{"127.0.0.1"}, PathPrefix: "/w",
			Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas", MaxUpload: 1 << 20,
			UploadTypes: []string{"png", "jpg", "jpeg", "gif", "webp", "pdf"},
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

	anon := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	for _, want := range []string{
		`id="wikiwright-navigation"`, `Main page`, `Recent changes`, `Random page`, `About Wikiwright`,
		`id="t-upload"`, `Upload file`,
	} {
		if !strings.Contains(anon, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(anon, `id="t-editsidebar"`) {
		t.Fatal("anonymous page offered Edit sidebar")
	}
	upAnon := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:Upload"))
	if !strings.Contains(upAnon, "Log in") {
		t.Fatal(upAnon)
	}
	src := get(t, client, ts.URL+"/w/index.php?title=Main_Page&action=edit")
	if src.StatusCode != 200 {
		t.Fatalf("view source %d", src.StatusCode)
	}
	src.Body.Close()
	mw := get(t, client, ts.URL+"/w/index.php?title=MediaWiki:Sidebar&action=edit")
	mwBody := readAll(t, mw)
	if mw.StatusCode != http.StatusForbidden || !strings.Contains(mwBody, "MediaWiki namespace") {
		t.Fatalf("mediawiki edit %d %s", mw.StatusCode, mwBody)
	}
	emptyList := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:ListFiles"))
	if !strings.Contains(emptyList, "No files have been uploaded") {
		t.Fatal(emptyList)
	}

	login(t, client, ts.URL, "/w", "Ada", "test-password-1", "")
	home := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(home, `id="t-editsidebar"`) || !strings.Contains(home, `title=MediaWiki:Sidebar&amp;action=edit`) {
		t.Fatal("logged-in tools missing Edit sidebar")
	}
	edit := readAll(t, get(t, client, ts.URL+"/w/index.php?title=MediaWiki:Sidebar&action=edit"))
	if !strings.Contains(edit, `name="wpTextbox1"`) {
		t.Fatal("sidebar edit form")
	}
	saved := postForm(t, client, ts.URL+"/w/index.php?title=MediaWiki:Sidebar&action=edit", url.Values{
		"csrf": {attr(t, edit, "csrf")}, "base_rev": {attr(t, edit, "base_rev")},
		"wpTextbox1": {"* navigation\n** Main Page|Home\n** https://example.com/docs|Docs\n"},
		"wpSummary":  {"nav"}, "wpSave": {"Save changes"},
	})
	if saved.StatusCode != http.StatusSeeOther {
		t.Fatalf("save sidebar %d %s", saved.StatusCode, readAll(t, saved))
	}
	saved.Body.Close()
	custom := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(custom, ">Home</span>") || !strings.Contains(custom, `href="https://example.com/docs"`) || !strings.Contains(custom, `class="external"`) {
		t.Fatalf("custom sidebar: %s", custom)
	}

	pngBytes := onePixelPNG(t)
	csrf := csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload")
	first := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrf, "Dot.png", pngBytes, nil)
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload %d %s", first.StatusCode, readAll(t, first))
	}
	first.Body.Close()
	st := srv.Sites[0].Store
	pg, err := st.PageByDBKey("File:Dot.png")
	if err != nil || pg.ID == 0 {
		t.Fatal(pg, err)
	}
	revs, err := st.Revisions(pg.ID)
	if err != nil || len(revs) != 1 || revs[0].Comment != "A dot" || !strings.Contains(revs[0].Content, "== Summary ==") || !strings.Contains(revs[0].Content, "A dot") {
		t.Fatalf("%+v %v", revs, err)
	}
	again := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Dot.png", pngBytes, nil)
	againBody := readAll(t, again)
	if again.StatusCode != 200 || !strings.Contains(againBody, "already on this wiki") || !strings.Contains(againBody, `name="wpIgnoreWarning"`) || !strings.Contains(againBody, `value="Dot.png"`) {
		t.Fatalf("overwrite warning %d %s", again.StatusCode, againBody)
	}
	revs, _ = st.Revisions(pg.ID)
	if len(revs) != 1 {
		t.Fatalf("warning stored a revision: %+v", revs)
	}
	replaced := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Dot.png", pngBytes, map[string]string{
		"wpIgnoreWarning": "1", "wpUploadDescription": "replaced",
	})
	if replaced.StatusCode != http.StatusSeeOther {
		t.Fatalf("replace %d %s", replaced.StatusCode, readAll(t, replaced))
	}
	replaced.Body.Close()
	revs, _ = st.Revisions(pg.ID)
	if len(revs) != 2 || revs[0].Comment != "replaced" || !strings.Contains(revs[0].Content, "replaced") {
		t.Fatalf("history %+v", revs)
	}
	list := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:ListFiles"))
	if !strings.Contains(list, "Dot.png") || !strings.Contains(list, "image/png") || !strings.Contains(list, "/w/images/Dot.png") {
		t.Fatal(list)
	}
	pages := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:SpecialPages"))
	if !strings.Contains(pages, "Special:ListFiles") {
		t.Fatal("special pages list")
	}
	bad := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Note.svg", pngBytes, nil)
	badBody := readAll(t, bad)
	if bad.StatusCode != 200 || !strings.Contains(badBody, "not allowed") {
		t.Fatalf("svg %d %s", bad.StatusCode, badBody)
	}
	if f, _ := st.File("Note.svg"); f.Name != "" {
		t.Fatal("rejected file was stored")
	}
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadExtra(t *testing.T, c *http.Client, u, csrf, name string, data []byte, extra map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", csrf)
	_ = mw.WriteField("wpDestFile", name)
	desc := "A dot"
	if extra != nil && extra["wpUploadDescription"] != "" {
		desc = extra["wpUploadDescription"]
	}
	_ = mw.WriteField("wpUploadDescription", desc)
	for k, v := range extra {
		if k == "wpUploadDescription" {
			continue
		}
		_ = mw.WriteField(k, v)
	}
	part, err := mw.CreateFormFile("wpUploadFile", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, u, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
