package web

import (
	"bytes"
	"html"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/config"
	"github.com/BlancoKat/wikiwright/internal/ingest"
	"github.com/BlancoKat/wikiwright/internal/mwxml"
)

func TestPanicReturnsErrorPage(t *testing.T) {
	rec := httptest.NewRecorder()
	guard(rec, func(http.ResponseWriter) { panic("boom") })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("type %s", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "Internal error") || !strings.Contains(rec.Body.String(), "still running") {
		t.Fatalf("body %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	guard(rec, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte("ok"))
		panic("after write")
	})
	if rec.Body.String() != "ok" {
		t.Fatalf("appended an error page: %q", rec.Body.String())
	}
}

func TestImportRenderLoginEditAndPrivate(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{
			{
				ID: "rv", Name: "Harbor Atlas", Hosts: []string{"127.0.0.1"}, PathPrefix: "/w",
				Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
				SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "rv-files"),
				ProjectNS: "Harbor Atlas", MaxUpload: 1 << 20,
				License: "Creative Commons Attribution", LicenseURL: "https://creativecommons.org/licenses/by/4.0/",
			},
			{
				ID: "sb", Name: "Draft Wiki", Hosts: []string{"127.0.0.1"}, PathPrefix: "/sb",
				Access: "private", MainPage: "Main Page", Timezone: "America/Los_Angeles",
				SQLite: filepath.Join(dir, "sb.db"), Files: filepath.Join(dir, "sb-files"),
				ProjectNS: "Draft Wiki", MaxUpload: 1 << 20,
			},
		},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	f, err := os.Open(rvDump(t))
	if err != nil {
		t.Fatal(err)
	}
	pages, _, err := mwxml.Parse(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := ingest.Apply(srv.Sites[0].Store, pages, "Harbor Atlas"); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.Hash("test-password-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Sites[0].Store.CreateUser("Ada", hash, true); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	sbHash, err := auth.Hash("sandbox-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Sites[1].Store.CreateUser("Ada", sbHash, true); err != nil {
		t.Fatal(err)
	}
	sbUser, err := srv.Sites[1].Store.UserByName("Ada")
	if err != nil || sbUser.ID == 0 {
		t.Fatal(sbUser, err)
	}
	if err := srv.Sites[1].Store.SetTOTP(sbUser.ID, secret, true); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	res := get(t, client, ts.URL+"/")
	body := readAll(t, res)
	if res.StatusCode != 200 || !strings.Contains(body, "Harbor Atlas") || !strings.Contains(body, "Draft Wiki") {
		t.Fatalf("directory %d %s", res.StatusCode, body)
	}

	res = get(t, client, ts.URL+"/w/index.php?title=Main_Page")
	body = readAll(t, res)
	if res.StatusCode != 200 {
		t.Fatalf("main %d", res.StatusCode)
	}
	for _, want := range []string{
		`id="wikiwright-shell"`, `wikiwright-skin`, `id="toc"`,
		`Category:Coast`, `action=edit&amp;redlink=1`, `class="new"`,
		`Lighthouses`, `Maps &amp; Charts`,
		`id="wikiwright-navigation"`, `id="wikiwright-top"`, `id="t-upload"`, `Upload file`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(body, `id="t-editsidebar"`) {
		t.Fatal("anonymous reader saw Edit sidebar")
	}
	if strings.Contains(body, "mw-editsection") {
		t.Fatal("anonymous reader saw section edit links")
	}
	if strings.Contains(body, "<script") {
		t.Fatal("script tag in page")
	}

	res = get(t, client, ts.URL+"/w/index.php?title=Special:Version")
	body = readAll(t, res)
	if !strings.Contains(body, "About Wikiwright") || !strings.Contains(body, "Wikiwright "+Version) || !strings.Contains(body, "MIT") {
		t.Fatalf("version page: %s", body)
	}

	res = get(t, client, ts.URL+"/sb/index.php?title=Main_Page")
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), "Special:UserLogin") {
		t.Fatalf("private redirect %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()

	// Public login, preview (including a script tag), then save.
	login(t, client, ts.URL, "/w", "Ada", "test-password-1", "")
	edit := get(t, client, ts.URL+"/w/index.php?title=Main_Page&action=edit")
	editBody := readAll(t, edit)
	for _, want := range []string{
		`id="mw-editform-cancel"`, `name="wpSummary"`, `id="wpMinoredit"`, `id="wpWatchthis"`,
		`value="Show changes"`, `value="Show preview"`, `value="Save changes"`,
		`id="editpage-copywarn"`, `Editing help`, `target="helpwindow"`,
		`id="t-editsidebar"`, `title=MediaWiki:Sidebar&amp;action=edit`,
		`Creative Commons Attribution`, `Do not submit copyrighted work without permission`,
	} {
		if !strings.Contains(editBody, want) {
			t.Fatalf("edit form missing %s", want)
		}
	}
	if !strings.Contains(editBody, `id="mw-editform-cancel" href="`+`/w/index.php?title=Main_Page"`) &&
		!strings.Contains(editBody, `id="mw-editform-cancel" href="/w/index.php?title=Main_Page"`) {
		t.Fatalf("cancel link: %s", editBody[strings.Index(editBody, "mw-editform-cancel"):])
	}
	csrf := attr(t, editBody, "csrf")
	base := attr(t, editBody, "base_rev")
	text := textarea(t, editBody) + "\n'''boldtest'''\n"
	preview := postForm(t, client, ts.URL+"/w/index.php?title=Main_Page&action=edit", url.Values{
		"csrf": {csrf}, "base_rev": {base}, "wpTextbox1": {text + "<script>alert(1)</script>"}, "wpPreview": {"1"},
	})
	prevBody := readAll(t, preview)
	if preview.StatusCode != 200 || !strings.Contains(prevBody, "<b>boldtest</b>") {
		t.Fatalf("preview %d %s", preview.StatusCode, prevBody[:min(400, len(prevBody))])
	}
	if strings.Contains(prevBody, "<script") || strings.Contains(prevBody, `href="javascript:`) {
		t.Fatal("preview kept unsafe html")
	}
	bad := postForm(t, client, ts.URL+"/w/index.php?title=Main_Page&action=edit", url.Values{
		"base_rev": {base}, "wpTextbox1": {"hijack"}, "wpSave": {"1"},
	})
	_, _ = io.Copy(io.Discard, bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf status %d", bad.StatusCode)
	}
	saved := postForm(t, client, ts.URL+"/w/index.php?title=Main_Page&action=edit", url.Values{
		"csrf": {csrf}, "base_rev": {base}, "wpTextbox1": {text}, "wpSummary": {"test edit"}, "wpSave": {"1"},
	})
	if saved.StatusCode != http.StatusSeeOther {
		b := readAll(t, saved)
		t.Fatalf("save %d %s", saved.StatusCode, b[:min(300, len(b))])
	}
	saved.Body.Close()
	again := get(t, client, ts.URL+"/w/index.php?title=Main_Page")
	againBody := readAll(t, again)
	if !strings.Contains(againBody, "<b>boldtest</b>") {
		t.Fatal("saved text missing")
	}
	hist := get(t, client, ts.URL+"/w/index.php?title=Main_Page&action=history")
	histBody := readAll(t, hist)
	if !strings.Contains(histBody, "test edit") {
		t.Fatal("history missing comment")
	}
	diffURL := regexp.MustCompile(`href="([^"]*diff=[^"]+)"`).FindStringSubmatch(histBody)
	if diffURL == nil {
		t.Fatal("no diff link")
	}
	href := strings.ReplaceAll(diffURL[1], "&amp;", "&")
	if !strings.HasPrefix(href, "http") {
		href = ts.URL + href
	}
	diffRes := get(t, client, href)
	diffBody := readAll(t, diffRes)
	if !strings.Contains(diffBody, "boldtest") || !strings.Contains(diffBody, "diff-addedline") {
		t.Fatal("diff missing the new line")
	}

	// Section editing: links, splice, new section, and a diff that does not save.
	sectionSrc := "== Alpha ==\nalpha\n=== Beta ===\nbeta\n== Gamma ==\ngamma\n{{box}}\n"
	if err := ingest.Apply(srv.Sites[0].Store, []mwxml.Page{
		{Title: "Template:Box", Revisions: []mwxml.Revision{{Text: "== From template ==\ninside"}}},
		{Title: "Sections", Revisions: []mwxml.Revision{{Text: sectionSrc}}},
		{Title: "Quiet", Revisions: []mwxml.Revision{{Text: "__NOEDITSECTION__\n== Hidden ==\n"}}},
	}, "Harbor Atlas"); err != nil {
		t.Fatal(err)
	}
	secPage := get(t, client, ts.URL+"/w/index.php?title=Sections")
	secBody := readAll(t, secPage)
	if strings.Count(secBody, `class="mw-editsection"`) != 3 {
		t.Fatalf("section links = %d\n%s", strings.Count(secBody, "mw-editsection"), secBody)
	}
	if !strings.Contains(secBody, "section=1") || !strings.Contains(secBody, "section=3") || strings.Contains(secBody, "section=4") {
		t.Fatal("template heading was numbered")
	}
	if !strings.Contains(secBody, "From template") || strings.Contains(secBody, "Edit section: From template") {
		t.Fatal("template heading link")
	}
	quiet := get(t, client, ts.URL+"/w/index.php?title=Quiet")
	quietBody := readAll(t, quiet)
	if strings.Contains(quietBody, "mw-editsection") {
		t.Fatal("__NOEDITSECTION__ still shows edit links")
	}
	anonJar, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: anonJar, CheckRedirect: client.CheckRedirect}
	anonPage := get(t, anon, ts.URL+"/w/index.php?title=Sections")
	anonBody := readAll(t, anonPage)
	if strings.Contains(anonBody, "mw-editsection") {
		t.Fatal("logged-out section links")
	}
	secEdit := get(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=2")
	secEditBody := readAll(t, secEdit)
	if !strings.Contains(textarea(t, secEditBody), "=== Beta ===") || strings.Contains(textarea(t, secEditBody), "Gamma") {
		t.Fatalf("section box: %s", textarea(t, secEditBody))
	}
	if !strings.Contains(secEditBody, `value="/* Beta */ "`) && !strings.Contains(secEditBody, `value="/* Beta */ &#32;"`) && !strings.Contains(attr(t, secEditBody, "wpSummary"), "/* Beta */") {
		t.Fatalf("summary prefill: %s", secEditBody)
	}
	secCSRF := attr(t, secEditBody, "csrf")
	secBase := attr(t, secEditBody, "base_rev")
	diffed := postForm(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=2", url.Values{
		"csrf": {secCSRF}, "base_rev": {secBase}, "wpSection": {"2"},
		"wpTextbox1": {"=== Beta ===\nbeta changed"}, "wpSummary": {"/* Beta */ "}, "wpDiff": {"Show changes"},
	})
	diffedBody := readAll(t, diffed)
	if !strings.Contains(diffedBody, "diff-addedline") || !strings.Contains(diffedBody, "beta changed") {
		t.Fatal("show changes did not render a diff")
	}
	still := get(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=2")
	if strings.Contains(textarea(t, readAll(t, still)), "beta changed") {
		t.Fatal("show changes saved the section")
	}
	savedSec := postForm(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=2", url.Values{
		"csrf": {secCSRF}, "base_rev": {secBase}, "wpSection": {"2"},
		"wpTextbox1": {"=== Beta ===\nbeta changed"}, "wpSummary": {"/* Beta */ "}, "wpSave": {"Save changes"},
		"wpWatchthis": {"1"},
	})
	if savedSec.StatusCode != http.StatusSeeOther {
		t.Fatalf("section save %d %s", savedSec.StatusCode, readAll(t, savedSec))
	}
	savedSec.Body.Close()
	after := get(t, client, ts.URL+"/w/index.php?title=Sections&action=edit")
	afterBody := readAll(t, after)
	full := textarea(t, afterBody)
	if !strings.Contains(full, "beta changed") || !strings.Contains(full, "== Alpha ==") || !strings.Contains(full, "== Gamma ==") || !strings.Contains(full, "{{box}}") {
		t.Fatalf("spliced page: %s", full)
	}
	if !regexp.MustCompile(`id="wpWatchthis"[^>]*checked`).MatchString(afterBody) {
		t.Fatal("watch checkbox did not stay checked")
	}
	newSec := get(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=new")
	newBody := readAll(t, newSec)
	if !strings.Contains(newBody, ">Subject:</label>") || strings.Contains(newBody, `id="wpMinoredit"`) {
		t.Fatal("new section form")
	}
	newCSRF := attr(t, newBody, "csrf")
	newBase := attr(t, newBody, "base_rev")
	added := postForm(t, client, ts.URL+"/w/index.php?title=Sections&action=edit&section=new", url.Values{
		"csrf": {newCSRF}, "base_rev": {newBase}, "wpSection": {"new"},
		"wpSummary": {"Extra"}, "wpTextbox1": {"more text"}, "wpSave": {"Save changes"},
	})
	if added.StatusCode != http.StatusSeeOther {
		t.Fatalf("new section %d %s", added.StatusCode, readAll(t, added))
	}
	added.Body.Close()
	hist2 := get(t, client, ts.URL+"/w/index.php?title=Sections&action=history")
	hist2Body := readAll(t, hist2)
	if !strings.Contains(hist2Body, "/* Extra */ new section") {
		t.Fatal("new section summary missing")
	}
	fresh := textarea(t, readAll(t, get(t, client, ts.URL+"/w/index.php?title=Sections&action=edit")))
	if !strings.Contains(fresh, "== Extra ==") || !strings.Contains(fresh, "more text") {
		t.Fatalf("new section text: %s", fresh)
	}
	brand := get(t, client, ts.URL+"/w/index.php?title=Brand_New&action=edit&returnto=Sections")
	brandBody := readAll(t, brand)
	if !strings.Contains(brandBody, `value="Save page"`) || !regexp.MustCompile(`id="wpWatchthis"[^>]*checked`).MatchString(brandBody) {
		t.Fatal("new page edit form")
	}
	if !strings.Contains(brandBody, `id="mw-editform-cancel" href="/w/index.php?title=Sections"`) {
		t.Fatalf("cancel for a new page: %s", brandBody)
	}

	// Upload a tiny PNG and fetch it back.
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	up := upload(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Dot.png", img.Bytes())
	if up.StatusCode != http.StatusSeeOther {
		b := readAll(t, up)
		t.Fatalf("upload %d %s", up.StatusCode, b[:min(300, len(b))])
	}
	up.Body.Close()
	pic := get(t, client, ts.URL+"/w/images/Dot.png")
	picBody := readAll(t, pic)
	if pic.StatusCode != 200 || !strings.HasPrefix(picBody, "\x89PNG") {
		t.Fatalf("image %d %q", pic.StatusCode, pic.Header.Get("Content-Type"))
	}

	// Private wiki with TOTP.
	jar2, _ := cookiejar.New(nil)
	c2 := &http.Client{Jar: jar2, CheckRedirect: client.CheckRedirect}
	code := auth.CurrentCode(secret, time.Now())
	login(t, c2, ts.URL, "/sb", "Ada", "sandbox-pass-1", code)
	priv := get(t, c2, ts.URL+"/sb/index.php?title=Main_Page")
	privBody := readAll(t, priv)
	if priv.StatusCode != 200 || !strings.Contains(privBody, "Draft Wiki") || !strings.Contains(privBody, "no text") {
		t.Fatalf("private read %d %s", priv.StatusCode, privBody[:min(200, len(privBody))])
	}
}

func rvDump(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "sample-export.xml")
}

func get(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func postForm(t *testing.T, c *http.Client, u string, v url.Values) *http.Response {
	t.Helper()
	res, err := c.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func attr(t *testing.T, body, name string) string {
	t.Helper()
	re := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %s in form", name)
	}
	return m[1]
}

func textarea(t *testing.T, body string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<textarea[^>]*>(.*?)</textarea>`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no textarea")
	}
	// The edit form writes a newline after <textarea> because browsers drop one.
	// Return the text a browser would submit.
	return strings.TrimPrefix(html.UnescapeString(m[1]), "\n")
}

func login(t *testing.T, c *http.Client, base, prefix, user, pass, totp string) {
	t.Helper()
	res := get(t, c, base+prefix+"/index.php?title=Special:UserLogin")
	body := readAll(t, res)
	token := attr(t, body, "login_token")
	res = postForm(t, c, base+prefix+"/index.php?title=Special:UserLogin", url.Values{
		"login_token": {token}, "username": {user}, "password": {pass}, "returnto": {"Main Page"},
	})
	b := readAll(t, res)
	if totp == "" {
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("login %d %s", res.StatusCode, b[:min(300, len(b))])
		}
		return
	}
	if res.StatusCode != 200 || !strings.Contains(b, "Authentication code") {
		t.Fatalf("expected totp challenge %d %s", res.StatusCode, b[:min(300, len(b))])
	}
	token = attr(t, b, "login_token")
	res = postForm(t, c, base+prefix+"/index.php?title=Special:UserLogin", url.Values{
		"login_token": {token}, "totp": {totp}, "returnto": {"Main Page"},
	})
	b = readAll(t, res)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("totp login %d %s", res.StatusCode, b[:min(300, len(b))])
	}
}

func csrfFromPage(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	res := get(t, c, u)
	return attr(t, readAll(t, res), "csrf")
}

func upload(t *testing.T, c *http.Client, u, csrf, name string, data []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", csrf)
	_ = mw.WriteField("wpDestFile", name)
	_ = mw.WriteField("wpUploadDescription", "A dot")
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
