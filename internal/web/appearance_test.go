package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/color"
	"github.com/BlancoKat/wikiwright/internal/config"
	"github.com/BlancoKat/wikiwright/internal/store"
)

func TestAppearanceAdminFlag(t *testing.T) {
	if appearanceAdmin(store.Session{}) {
		t.Fatal("anonymous")
	}
	admin := store.Session{User: store.User{ID: 1, IsAdmin: true}}
	if !appearanceAdmin(admin) {
		t.Fatal("admin")
	}
	admin.Grants = "edit"
	if appearanceAdmin(admin) {
		t.Fatal("restricted bot")
	}
	admin.Grants = ""
	admin.NeedsTOTP = true
	if appearanceAdmin(admin) {
		t.Fatal("pending totp")
	}
	reader := store.Session{User: store.User{ID: 2, IsAdmin: false}}
	if appearanceAdmin(reader) {
		t.Fatal("non-admin")
	}
}

func TestAppearanceSaveContrastAndReset(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", Hosts: []string{"127.0.0.1"}, PathPrefix: "/w",
			Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas", Accent: "#1a365d",
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	site := srv.Sites[0]
	hash, err := auth.Hash("test-password-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Store.CreateUser("Ada", hash, true); err != nil {
		t.Fatal(err)
	}
	readerHash, err := auth.Hash("reader-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Store.CreateUser("Quinn", readerHash, false); err != nil {
		t.Fatal(err)
	}
	if _, err := site.Store.UpsertPage(0, "Main Page", "Main_Page"); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	css := readAll(t, get(t, client, ts.URL+"/static/wikiwright.css"))
	if !strings.Contains(css, "width: 100%") || !strings.Contains(css, "width: auto") || !strings.Contains(css, "max-height: 14rem") {
		t.Fatal("logo should use the sidebar width without a forced upscale")
	}
	if strings.Contains(css, "a.new { color: var(--redlink); }") {
		t.Fatal("red link color is not scoped to the page")
	}

	home := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(home, `id="wikiwright-accent"`) || !strings.Contains(home, "--pine:#1a365d") {
		t.Fatal("config accent was not applied")
	}
	if strings.Contains(home, "<script") {
		t.Fatal("script on the main page")
	}
	if strings.Contains(home, `id="t-appearance"`) {
		t.Fatal("anonymous toolbox linked appearance")
	}
	pages := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:SpecialPages"))
	if !strings.Contains(pages, "Special:Appearance") {
		t.Fatal("special pages list")
	}
	anon := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:Appearance"))
	navy := color.Build("#1a365d")
	for _, want := range []string{"Navy", "Burgundy", "Forest", `type="color"`, "Header text", "Only an administrator", color.FormatRatio(navy.InkRatio), "#1a365d"} {
		if !strings.Contains(anon, want) {
			t.Fatalf("read-only page missing %s", want)
		}
	}
	if strings.Contains(anon, "<script") || strings.Contains(anon, "Save color") || strings.Contains(anon, `name="csrf"`) {
		t.Fatal("anonymous appearance was editable")
	}
	if strings.Contains(anon, "cannot meet WCAG AA") {
		t.Fatal("passing navy showed the contrast warning")
	}

	readerJar, _ := cookiejar.New(nil)
	reader := &http.Client{Jar: readerJar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	login(t, reader, ts.URL, "/w", "Quinn", "reader-pass-1", "")
	readerPage := readAll(t, get(t, reader, ts.URL+"/w/index.php?title=Special:Appearance"))
	if strings.Contains(readerPage, "Save color") {
		t.Fatal("reader could save")
	}
	denied := postForm(t, reader, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf": {"nope"}, "choice": {"#6e2430"}, "wpAction": {"save"},
	})
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("reader post %d", denied.StatusCode)
	}
	denied.Body.Close()
	if got, _ := site.Store.Setting("accent"); got != "" {
		t.Fatal("reader wrote a setting")
	}

	login(t, client, ts.URL, "/w", "Ada", "test-password-1", "")
	tools := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(tools, `id="t-appearance"`) {
		t.Fatal("admin toolbox missing appearance")
	}
	res := get(t, client, ts.URL+"/w/index.php?title=Special:Appearance")
	cspHeader := res.Header.Get("Content-Security-Policy")
	body := readAll(t, res)
	if !strings.Contains(body, "Save color") || !strings.Contains(body, "<script nonce=") {
		t.Fatal("admin form")
	}
	nonce := scriptNonceFrom(t, body)
	if !strings.Contains(cspHeader, "script-src 'nonce-"+nonce+"'") || strings.Contains(cspHeader, "script-src 'none'") {
		t.Fatalf("csp %s nonce %s", cspHeader, nonce)
	}
	if strings.Contains(strings.Split(body, "<script")[0], "cannot meet WCAG AA") {
		t.Fatal("passing color rendered the contrast warning")
	}

	bad := postForm(t, client, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf": {"not-the-token"}, "choice": {"#6e2430"}, "wpAction": {"save"},
	})
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf %d", bad.StatusCode)
	}
	bad.Body.Close()

	junk := readAll(t, postForm(t, client, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf":   {csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Appearance")},
		"choice": {"custom"}, "custom": {"red"}, "wpAction": {"save"},
	}))
	if !strings.Contains(junk, "#rrggbb") {
		t.Fatal(junk)
	}
	if got, _ := site.Store.Setting("accent"); got != "" {
		t.Fatal("invalid color stored")
	}

	saved := postForm(t, client, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf":     {csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Appearance")},
		"choice":   {"#6e2430"},
		"custom":   {"#1a365d"},
		"wpAction": {"save"},
	})
	if saved.StatusCode != http.StatusSeeOther || !strings.Contains(saved.Header.Get("Location"), "saved=1") {
		t.Fatalf("save %d %s", saved.StatusCode, saved.Header.Get("Location"))
	}
	saved.Body.Close()
	if got, err := site.Store.Setting("accent"); err != nil || got != "#6e2430" {
		t.Fatalf("stored %q %v", got, err)
	}
	var pagesN int
	if err := site.Store.DB.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pagesN); err != nil || pagesN != 1 {
		t.Fatalf("pages %d %v", pagesN, err)
	}
	painted := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(painted, "--pine:#6e2430") || !strings.Contains(painted, "--foam:#ffffff") {
		t.Fatal("burgundy was not applied")
	}
	th := color.Build("#6e2430")
	if th.InkRatio < 4.5 || !strings.Contains(painted, "--teal:"+th.Link) || !strings.Contains(painted, "--accent-border:"+th.Border) {
		t.Fatalf("derived colors missing, link %s border %s", th.Link, th.Border)
	}

	light := postForm(t, client, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf":     {csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Appearance")},
		"choice":   {"custom"},
		"custom":   {"#fff3a0"},
		"wpAction": {"save"},
	})
	if light.StatusCode != http.StatusSeeOther {
		t.Fatalf("light %d", light.StatusCode)
	}
	light.Body.Close()
	lightPage := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	lightTheme := color.Build("#fff3a0")
	if !strings.Contains(lightPage, "--pine:#fff3a0") || !strings.Contains(lightPage, "--foam:"+lightTheme.Ink) {
		t.Fatal("light accent ink")
	}
	if lightTheme.Link == "#fff3a0" || !strings.Contains(lightPage, "--teal:"+lightTheme.Link) {
		t.Fatal("light accent link was not darkened")
	}
	appear := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:Appearance"))
	if !strings.Contains(appear, color.FormatRatio(lightTheme.InkRatio)) {
		t.Fatal("contrast ratio not shown")
	}

	reset := postForm(t, client, ts.URL+"/w/index.php?title=Special:Appearance", url.Values{
		"csrf":     {csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Appearance")},
		"wpAction": {"reset"},
	})
	if reset.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset %d", reset.StatusCode)
	}
	reset.Body.Close()
	if got, _ := site.Store.Setting("accent"); got != "" {
		t.Fatal("reset left a row")
	}
	back := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Main_Page"))
	if !strings.Contains(back, "--pine:#1a365d") {
		t.Fatal("reset did not return to the config accent")
	}
}

func TestDefaultPineAppearanceDoesNotWarn(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", Hosts: []string{"127.0.0.1"}, PathPrefix: "/w",
			Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "rv.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Harbor Atlas",
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body := readAll(t, get(t, ts.Client(), ts.URL+"/w/index.php?title=Special:Appearance"))
	for _, want := range []string{"12.66:1", "10.95:1", "#1b3832", "Pine"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(body, "cannot meet WCAG AA") || strings.Contains(body, "wikiwright-error") {
		t.Fatal("default pine showed a contrast warning")
	}
	low := color.Theme{InkRatio: 4.49, BorderRatio: 3, LinkRatio: 4.5}
	if low.Warning() == "" || !strings.Contains(low.Warning(), "4.49:1") {
		t.Fatal(low.Warning())
	}
}

func scriptNonceFrom(t *testing.T, body string) string {
	t.Helper()
	const needle = `nonce="`
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatal("no nonce")
	}
	rest := body[i+len(needle):]
	j := strings.IndexByte(rest, '"')
	if j <= 0 {
		t.Fatal("nonce truncated")
	}
	return rest[:j]
}
