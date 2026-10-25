package web

import (
	"bytes"
	"encoding/json"
	"html"
	"image"
	"image/png"
	"mime/multipart"
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

func TestAPIBotPasswordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43129",
		Wikis: []config.Wiki{{
			ID: "demo", Name: "Demo Wiki", Hosts: []string{"127.0.0.1"}, PathPrefix: "/demo",
			Access: "private", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "arts.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Demo Wiki", MaxUpload: 1 << 20,
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	site := srv.Sites[0]
	adminHash, err := auth.Hash("admin-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Store.CreateUser("Ada", adminHash, true); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()
	base := ts.URL
	apiURL := base + "/demo/api.php"

	anon := newClient()
	denied := apiGet(t, anon, apiURL+"?action=query&list=allpages&format=json&formatversion=2")
	if apiCode(denied) != "readapidenied" {
		t.Fatalf("anonymous allpages code %s", apiCode(denied))
	}
	infoDenied := apiGet(t, anon, apiURL+"?action=query&meta=siteinfo&format=json&formatversion=2")
	if apiCode(infoDenied) != "readapidenied" {
		t.Fatalf("anonymous siteinfo code %s", apiCode(infoDenied))
	}
	tok := apiGet(t, anon, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2")
	if apiCode(tok) != "" {
		t.Fatalf("login token code %s", apiCode(tok))
	}
	loginToken, _ := jmap(t, jmap(t, tok, "query"), "tokens")["logintoken"].(string)
	if !strings.HasSuffix(loginToken, `+\`) {
		t.Fatal("login token missing the MediaWiki suffix")
	}
	bad := apiPost(t, anon, apiURL, url.Values{
		"action": {"login"}, "lgname": {"Ada"}, "lgpassword": {"nope"}, "lgtoken": {"not-a-token"}, "format": {"json"},
	})
	if apiCode(bad) != "badtoken" {
		t.Fatalf("bad login token code %s", apiCode(bad))
	}

	admin := newClient()
	login(t, admin, base, "/demo", "Ada", "admin-pass-1", "")
	csrf := csrfFromPage(t, admin, base+"/demo/index.php?title=Special:CreateAccount")
	created := readAll(t, postForm(t, admin, base+"/demo/index.php?title=Special:CreateAccount", url.Values{
		"csrf": {csrf}, "wpName": {"KitchenStudio"}, "wpPassword": {"kitchen-pass-1"}, "wpPasswordAgain": {"kitchen-pass-1"},
	}))
	if !strings.Contains(created, "Created account KitchenStudio") {
		t.Fatal("account was not created")
	}
	again := postForm(t, admin, base+"/demo/index.php?title=Special:CreateAccount", url.Values{
		"csrf": {csrf}, "wpName": {"KitchenStudio"}, "wpPassword": {"kitchen-pass-1"}, "wpPasswordAgain": {"kitchen-pass-1"},
	})
	if !strings.Contains(readAll(t, again), "already in use") {
		t.Fatal("duplicate account was accepted")
	}

	studio := newClient()
	login(t, studio, base, "/demo", "KitchenStudio", "kitchen-pass-1", "")
	deniedPage := get(t, studio, base+"/demo/index.php?title=Special:CreateAccount")
	deniedBody := readAll(t, deniedPage)
	if deniedPage.StatusCode != http.StatusForbidden || !strings.Contains(deniedBody, "Only an administrator") {
		t.Fatalf("non-admin create status %d", deniedPage.StatusCode)
	}
	home := readAll(t, get(t, studio, base+"/demo/index.php?title=Main_Page"))
	if !strings.Contains(home, `id="t-specialpages"`) || !strings.Contains(home, "Special:SpecialPages") {
		t.Fatal("sidebar special pages link missing")
	}

	botPage := readAll(t, get(t, studio, base+"/demo/index.php?title=Special:BotPasswords"))
	botCSRF := attr(t, botPage, "csrf")
	made := readAll(t, postForm(t, studio, base+"/demo/index.php?title=Special:BotPasswords", url.Values{
		"csrf": {botCSRF}, "wpAction": {"create"}, "wpBotName": {"importer"},
		"wpGrantEdit": {"1"}, "wpGrantCreate": {"1"}, "wpGrantUpload": {"1"},
	}))
	loginName := mustID(t, made, "wpBotLogin")
	secret := mustID(t, made, "wpBotPassword")
	if loginName != "KitchenStudio@importer" || secret == "" || strings.Contains(made, "argon2") {
		t.Fatal("bot password was not shown once")
	}
	var stored string
	if err := site.Store.DB.QueryRow(`SELECT password_hash FROM bot_passwords WHERE app_id=?`, "importer").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || !strings.HasPrefix(stored, "$argon2id$") {
		t.Fatal("bot password was not stored as a hash")
	}
	editorMade := readAll(t, postForm(t, studio, base+"/demo/index.php?title=Special:BotPasswords", url.Values{
		"csrf": {botCSRF}, "wpAction": {"create"}, "wpBotName": {"editor"}, "wpGrantEdit": {"1"},
	}))
	editorSecret := mustID(t, editorMade, "wpBotPassword")
	if editorSecret == "" || editorSecret == secret {
		t.Fatal("editor bot password was not issued")
	}

	secretHolder, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	studioUser, err := site.Store.UserByName("KitchenStudio")
	if err != nil || studioUser.ID == 0 {
		t.Fatal(err)
	}
	if err := site.Store.SetTOTP(studioUser.ID, secretHolder, true); err != nil {
		t.Fatal(err)
	}

	apiClient := newClient()
	first := apiGet(t, apiClient, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2")
	lg := jmap(t, jmap(t, first, "query"), "tokens")["logintoken"].(string)
	mainFail := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"login"}, "lgname": {"KitchenStudio"}, "lgpassword": {"kitchen-pass-1"}, "lgtoken": {lg}, "format": {"json"},
	})
	reason, _ := jmap(t, mainFail, "login")["reason"].(string)
	if jmap(t, mainFail, "login")["result"] != "Failed" || !strings.Contains(reason, "bot password") {
		t.Fatal("main password with an authenticator was accepted")
	}

	lg = jmap(t, jmap(t, apiGet(t, apiClient, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2"), "query"), "tokens")["logintoken"].(string)
	okLogin := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"login"}, "lgname": {loginName}, "lgpassword": {secret}, "lgtoken": {lg}, "format": {"json"}, "formatversion": {"2"},
	})
	loginObj := jmap(t, okLogin, "login")
	if loginObj["result"] != "Success" || loginObj["lgusername"] != "KitchenStudio" {
		t.Fatal("bot login did not succeed as the account")
	}

	csrfTok := jmap(t, jmap(t, apiGet(t, apiClient, apiURL+"?action=query&meta=tokens&type=csrf&format=json&formatversion=2"), "query"), "tokens")["csrftoken"].(string)
	if !strings.HasSuffix(csrfTok, `+\`) || csrfTok == `+\` {
		t.Fatal("missing csrf token")
	}
	general := jmap(t, jmap(t, apiGet(t, apiClient, apiURL+"?action=query&meta=siteinfo&siprop=general&format=json&formatversion=2"), "query"), "general")
	if general["sitename"] != "Demo Wiki" || general["wikiid"] != "demo" || general["writeapi"] != true {
		t.Fatal("siteinfo")
	}
	if !strings.Contains(general["generator"].(string), "Wikiwright") {
		t.Fatal("generator")
	}

	const recipe = "See [[Missing recipe]].\n\nHello"
	posted := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"edit"}, "title": {"Recipe"}, "text": {recipe}, "summary": {"created"}, "token": {csrfTok}, "format": {"json"},
	})
	editObj := jmap(t, posted, "edit")
	if editObj["result"] != "Success" || editObj["title"] != "Recipe" || editObj["oldrevid"] != float64(0) {
		t.Fatal("create did not succeed")
	}
	if editObj["newrevid"] == float64(0) {
		t.Fatal("missing new revision")
	}
	conflict := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"edit"}, "title": {"Recipe"}, "text": {"x"}, "token": {csrfTok}, "baserevid": {"999999"}, "format": {"json"},
	})
	if apiCode(conflict) != "editconflict" {
		t.Fatalf("baserevid code %s", apiCode(conflict))
	}
	exists := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"edit"}, "title": {"Recipe"}, "text": {"x"}, "token": {csrfTok}, "createonly": {"1"}, "format": {"json"},
	})
	if apiCode(exists) != "articleexists" {
		t.Fatalf("createonly code %s", apiCode(exists))
	}
	missing := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"edit"}, "title": {"No Such Page"}, "text": {"x"}, "token": {csrfTok}, "nocreate": {"1"}, "format": {"json"},
	})
	if apiCode(missing) != "missingtitle" {
		t.Fatalf("nocreate code %s", apiCode(missing))
	}
	getEdit := apiGet(t, apiClient, apiURL+"?action=edit&title=Recipe&text=nope&token="+url.QueryEscape(csrfTok))
	if apiCode(getEdit) != "mustbeposted" {
		t.Fatalf("GET edit code %s", apiCode(getEdit))
	}

	read := apiGet(t, apiClient, apiURL+"?action=query&prop=revisions&titles=Recipe&rvprop=content|user|comment&rvslots=main&format=json&formatversion=2")
	page0 := jslice(t, jmap(t, read, "query"), "pages")[0].(map[string]any)
	rev0 := jslice(t, page0, "revisions")[0].(map[string]any)
	slot := jmap(t, jmap(t, rev0, "slots"), "main")
	if slot["content"] != recipe || rev0["user"] != "KitchenStudio" || rev0["comment"] != "created" {
		t.Fatal("revision content or author did not match")
	}
	if strings.Contains(rev0["user"].(string), "@") {
		t.Fatal("revision attributed to the bot login")
	}

	pngBytes := tinyPNG(t)
	up := apiUpload(t, apiClient, apiURL, csrfTok, "Dot.png", "A dot", pngBytes, false)
	if jmap(t, up, "upload")["result"] != "Success" || jmap(t, up, "upload")["filename"] != "Dot.png" {
		t.Fatal("upload did not succeed")
	}
	warn := apiUpload(t, apiClient, apiURL, csrfTok, "Dot.png", "ignored", pngBytes, false)
	warnings := jmap(t, jmap(t, warn, "upload"), "warnings")
	if jmap(t, warn, "upload")["result"] != "Warning" || warnings["exists"] != "Dot.png" {
		t.Fatal("overwrite was not a warning")
	}
	replaced := apiUpload(t, apiClient, apiURL, csrfTok, "Dot.png", "Replaced", pngBytes, true)
	if jmap(t, replaced, "upload")["result"] != "Success" {
		t.Fatal("ignorewarnings did not replace the file")
	}
	fileRead := apiGet(t, apiClient, apiURL+"?action=query&prop=revisions&titles=File:Dot.png&rvprop=content|user|comment&format=json&formatversion=2")
	filePage := jslice(t, jmap(t, fileRead, "query"), "pages")[0].(map[string]any)
	fileRev := jslice(t, filePage, "revisions")[0].(map[string]any)
	fileSlot := jmap(t, jmap(t, fileRev, "slots"), "main")
	if filePage["title"] != "File:Dot.png" || fileRev["user"] != "KitchenStudio" || !strings.Contains(fileSlot["content"].(string), "Replaced") {
		t.Fatal("file page history did not record the account")
	}

	rcs, err := site.Store.RecentChanges(20)
	if err != nil {
		t.Fatal(err)
	}
	sawRecipe := false
	for _, rc := range rcs {
		if strings.Contains(rc.UserText, "@") {
			t.Fatal("recent change used a bot login name")
		}
		if rc.DBKey == "Recipe" && rc.UserText == "KitchenStudio" {
			sawRecipe = true
		}
	}
	if !sawRecipe {
		t.Fatal("recent changes missed the recipe edit")
	}

	listed := apiGet(t, apiClient, apiURL+"?action=query&list=allpages&apnamespace=0&apprefix=Rec&format=json&formatversion=2")
	foundRecipe := false
	for _, item := range jslice(t, jmap(t, listed, "query"), "allpages") {
		row := item.(map[string]any)
		if row["title"] == "Recipe" {
			foundRecipe = true
		}
	}
	if !foundRecipe {
		t.Fatal("allpages missed Recipe")
	}

	sectioned := apiPost(t, apiClient, apiURL, url.Values{
		"action": {"edit"}, "title": {"Recipe"}, "section": {"new"}, "sectiontitle": {"Notes"},
		"text": {"More"}, "summary": {"added notes"}, "token": {csrfTok}, "format": {"json"},
	})
	if jmap(t, sectioned, "edit")["result"] != "Success" {
		t.Fatal("section=new failed")
	}
	after := apiGet(t, apiClient, apiURL+"?action=query&prop=revisions&titles=Recipe&rvprop=content&format=json&formatversion=2")
	afterSlot := jmap(t, jmap(t, jslice(t, jslice(t, jmap(t, after, "query"), "pages")[0].(map[string]any), "revisions")[0].(map[string]any), "slots"), "main")
	body, _ := afterSlot["content"].(string)
	if !strings.Contains(body, "Hello") || !strings.Contains(body, "== Notes ==") || !strings.Contains(body, "More") || !strings.Contains(body, "[[Missing recipe]]") {
		t.Fatal("section was not spliced in")
	}

	specials := readAll(t, get(t, studio, base+"/demo/index.php?title=Special:SpecialPages"))
	for _, needle := range []string{
		"Special:AllPages", "Special:PrefixIndex", "Special:Categories", "Special:RecentChanges",
		"Special:NewPages", "Special:WhatLinksHere", "Special:ListFiles", "Special:NewFiles",
		"Special:Upload", "Special:ListUsers", "Special:Preferences", "Special:ChangePassword",
		"Special:Random", "Special:Search", "Special:UncategorizedPages", "Special:WantedPages",
		"Special:LonelyPages", "Special:BotPasswords", "Special:CreateAccount", "Special:Contributions",
		"Special:Statistics", "Special:ListRedirects", "Special:Appearance",
	} {
		if !strings.Contains(specials, needle) {
			t.Fatalf("special pages list missing %s", needle)
		}
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:UncategorizedPages")), "Recipe") {
		t.Fatal("uncategorized")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:WantedPages")), "Missing recipe") {
		t.Fatal("wanted")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:LonelyPages")), "Recipe") {
		t.Fatal("lonely")
	}
	prefix := readAll(t, get(t, studio, base+"/demo/index.php?title=Special:PrefixIndex&prefix=Rec&namespace=0"))
	if !strings.Contains(prefix, "Recipe") {
		t.Fatal("prefix index")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:AllPages&namespace=0")), `name="namespace"`) {
		t.Fatal("namespace filter")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:NewFiles")), "Dot.png") {
		t.Fatal("new files")
	}
	usersPage := readAll(t, get(t, studio, base+"/demo/index.php?title=Special:ListUsers"))
	if !strings.Contains(usersPage, "KitchenStudio") || !strings.Contains(usersPage, "Ada") || strings.Contains(usersPage, "argon2") {
		t.Fatal("user list")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:Contributions&user=KitchenStudio")), "Recipe") {
		t.Fatal("contributions")
	}
	if !strings.Contains(readAll(t, get(t, studio, base+"/demo/index.php?title=Special:Statistics")), "Articles") {
		t.Fatal("statistics")
	}
	passPage := readAll(t, get(t, studio, base+"/demo/index.php?title=Special:ChangePassword"))
	passCSRF := attr(t, passPage, "csrf")
	wrong := readAll(t, postForm(t, studio, base+"/demo/index.php?title=Special:ChangePassword", url.Values{
		"csrf": {passCSRF}, "current": {"not-the-password"}, "newpass": {"12345678"}, "again": {"12345678"},
	}))
	if !strings.Contains(wrong, "not correct") {
		t.Fatal("change password accepted a bad current password")
	}

	editor := newClient()
	elg := jmap(t, jmap(t, apiGet(t, editor, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2"), "query"), "tokens")["logintoken"].(string)
	edLogin := apiPost(t, editor, apiURL, url.Values{
		"action": {"login"}, "lgname": {"KitchenStudio@editor"}, "lgpassword": {editorSecret}, "lgtoken": {elg}, "format": {"json"},
	})
	if jmap(t, edLogin, "login")["result"] != "Success" {
		t.Fatal("editor bot did not log in")
	}
	edCSRF := jmap(t, jmap(t, apiGet(t, editor, apiURL+"?action=query&meta=tokens&type=csrf&format=json&formatversion=2"), "query"), "tokens")["csrftoken"].(string)
	edited := apiPost(t, editor, apiURL, url.Values{
		"action": {"edit"}, "title": {"Recipe"}, "text": {body + "\n\nEdited"}, "summary": {"editor"}, "token": {edCSRF}, "format": {"json"},
	})
	if jmap(t, edited, "edit")["result"] != "Success" {
		t.Fatal("edit grant could not edit")
	}
	noCreate := apiPost(t, editor, apiURL, url.Values{
		"action": {"edit"}, "title": {"Brand New"}, "text": {"no"}, "token": {edCSRF}, "format": {"json"},
	})
	if apiCode(noCreate) != "permissiondenied" {
		t.Fatalf("create grant code %s", apiCode(noCreate))
	}
	if pg, err := site.Store.PageByDBKey("Brand_New"); err != nil || pg.ID != 0 {
		t.Fatal("edit-only bot created a page")
	}
	noUpload := apiUpload(t, editor, apiURL, edCSRF, "EditOnly.png", "no", pngBytes, false)
	if apiCode(noUpload) != "permissiondenied" {
		t.Fatalf("upload grant code %s", apiCode(noUpload))
	}
	if f, err := site.Store.File("EditOnly.png"); err != nil || f.Name != "" {
		t.Fatal("edit-only bot stored a file")
	}

	reset := readAll(t, postForm(t, studio, base+"/demo/index.php?title=Special:BotPasswords", url.Values{
		"csrf": {botCSRF}, "wpAction": {"reset"}, "wpBotName": {"editor"},
	}))
	resetSecret := mustID(t, reset, "wpBotPassword")
	if resetSecret == "" || resetSecret == editorSecret {
		t.Fatal("reset did not issue a new password")
	}
	oldTry := newClient()
	oldTok := jmap(t, jmap(t, apiGet(t, oldTry, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2"), "query"), "tokens")["logintoken"].(string)
	oldLogin := apiPost(t, oldTry, apiURL, url.Values{
		"action": {"login"}, "lgname": {"KitchenStudio@editor"}, "lgpassword": {editorSecret}, "lgtoken": {oldTok}, "format": {"json"},
	})
	if jmap(t, oldLogin, "login")["result"] != "Failed" {
		t.Fatal("old bot password still worked")
	}
	deleted := readAll(t, postForm(t, studio, base+"/demo/index.php?title=Special:BotPasswords", url.Values{
		"csrf": {botCSRF}, "wpAction": {"delete"}, "wpBotName": {"editor"},
	}))
	if !strings.Contains(deleted, "Deleted bot password") || strings.Contains(deleted, "KitchenStudio@editor") {
		t.Fatal("bot password was not deleted")
	}

	out := apiPost(t, apiClient, apiURL, url.Values{"action": {"logout"}, "token": {csrfTok}, "format": {"json"}})
	if _, ok := out["logout"]; !ok {
		t.Fatal("logout")
	}
	afterOut := apiGet(t, apiClient, apiURL+"?action=query&list=allpages&format=json&formatversion=2")
	if apiCode(afterOut) != "readapidenied" {
		t.Fatalf("after logout code %s", apiCode(afterOut))
	}
}

func TestAPIUserInfoAndAssert(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{
		Listen: "127.0.0.1:43131",
		Wikis: []config.Wiki{{
			ID: "demo", Name: "Demo Wiki", Hosts: []string{"127.0.0.1"}, PathPrefix: "/demo",
			Access: "private", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "arts.db"), Files: filepath.Join(dir, "files"),
			ProjectNS: "Demo Wiki", MaxUpload: 1 << 20,
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	site := srv.Sites[0]
	adminHash, err := auth.Hash("admin-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Store.CreateUser("Ada", adminHash, true); err != nil {
		t.Fatal(err)
	}
	botHash, err := auth.Hash("bot-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	cookHash, err := auth.Hash("cook-pass-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Store.CreateUser("Cook", cookHash, false); err != nil {
		t.Fatal(err)
	}
	user, err := site.Store.UserByName("Ada")
	if err != nil || user.ID == 0 {
		t.Fatal(err)
	}
	cook, err := site.Store.UserByName("Cook")
	if err != nil || cook.ID == 0 {
		t.Fatal(err)
	}
	if err := site.Store.UpsertBotPassword(cook.ID, "editor", botHash, "edit"); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()
	apiURL := ts.URL + "/demo/api.php"

	anon := newClient()
	info := apiGet(t, anon, apiURL+"?action=query&meta=userinfo&uiprop=groups|rights|blockinfo&format=json&formatversion=2")
	if apiCode(info) != "" {
		t.Fatalf("anonymous userinfo code %s", apiCode(info))
	}
	ui := jmap(t, jmap(t, info, "query"), "userinfo")
	if ui["anon"] != true || ui["id"] != float64(0) || ui["name"] == "" {
		t.Fatal("anonymous userinfo")
	}
	if !hasString(t, ui, "groups", "*") || hasString(t, ui, "groups", "user") {
		t.Fatal("anonymous groups")
	}
	if hasString(t, ui, "rights", "read") || hasString(t, ui, "rights", "edit") {
		t.Fatal("anonymous rights on a private wiki")
	}
	both := apiGet(t, anon, apiURL+"?action=query&meta=tokens|userinfo&type=login&format=json&formatversion=2")
	if apiCode(both) != "" || jmap(t, jmap(t, both, "query"), "userinfo")["anon"] != true {
		t.Fatal("tokens and userinfo should both be public")
	}
	if jmap(t, jmap(t, both, "query"), "tokens")["logintoken"] == "" {
		t.Fatal("missing logintoken")
	}
	if apiCode(apiGet(t, anon, apiURL+"?action=query&meta=userinfo|siteinfo&format=json&formatversion=2")) != "readapidenied" {
		t.Fatal("userinfo combined with a read stayed public")
	}
	if apiCode(apiGet(t, anon, apiURL+"?action=query&meta=userinfo&assert=user&format=json")) != "assertuserfailed" {
		t.Fatal("assert=user")
	}
	if apiCode(apiGet(t, anon, apiURL+"?action=query&meta=userinfo&assert=bot&format=json")) != "assertuserfailed" {
		t.Fatal("assert=bot")
	}

	kat := newClient()
	apiLoginAs(t, kat, apiURL, "Ada", "admin-pass-1")
	got := apiGet(t, kat, apiURL+"?action=query&meta=userinfo&uiprop=groups|rights&assert=user&format=json&formatversion=2")
	if apiCode(got) != "" {
		t.Fatalf("logged-in userinfo code %s", apiCode(got))
	}
	ui = jmap(t, jmap(t, got, "query"), "userinfo")
	if ui["id"] != float64(user.ID) || ui["name"] != "Ada" || ui["anon"] == true {
		t.Fatal("logged-in userinfo")
	}
	for _, g := range []string{"*", "user", "sysop"} {
		if !hasString(t, ui, "groups", g) {
			t.Fatalf("missing group %s", g)
		}
	}
	for _, right := range []string{"read", "edit", "createpage", "upload", "writeapi"} {
		if !hasString(t, ui, "rights", right) {
			t.Fatalf("missing right %s", right)
		}
	}
	if apiCode(apiGet(t, kat, apiURL+"?action=query&meta=userinfo&assert=bot&format=json")) != "" {
		t.Fatal("assert=bot rejected a logged-in user")
	}

	bot := newClient()
	apiLoginAs(t, bot, apiURL, "Cook@editor", "bot-pass-1")
	limited := apiGet(t, bot, apiURL+"?action=query&meta=userinfo&uiprop=groups|rights&assert=bot&format=json&formatversion=2")
	if apiCode(limited) != "" {
		t.Fatalf("bot userinfo code %s", apiCode(limited))
	}
	ui = jmap(t, jmap(t, limited, "query"), "userinfo")
	if ui["name"] != "Cook" || ui["id"] != float64(cook.ID) {
		t.Fatal("bot session userinfo")
	}
	if !hasString(t, ui, "rights", "edit") || !hasString(t, ui, "rights", "read") || hasString(t, ui, "rights", "upload") || hasString(t, ui, "rights", "createpage") {
		t.Fatal("edit-only grants")
	}
	if hasString(t, ui, "groups", "sysop") {
		t.Fatal("bot password gained admin group")
	}
}

func apiLoginAs(t *testing.T, c *http.Client, apiURL, name, pass string) {
	t.Helper()
	tok := apiGet(t, c, apiURL+"?action=query&meta=tokens&type=login&format=json&formatversion=2")
	lg, _ := jmap(t, jmap(t, tok, "query"), "tokens")["logintoken"].(string)
	res := apiPost(t, c, apiURL, url.Values{
		"action": {"login"}, "lgname": {name}, "lgpassword": {pass}, "lgtoken": {lg}, "format": {"json"},
	})
	if jmap(t, res, "login")["result"] != "Success" {
		t.Fatal("login failed")
	}
}

func hasString(t *testing.T, m map[string]any, key, want string) bool {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		t.Fatalf("missing %s", key)
	}
	for _, item := range raw {
		if item == want {
			return true
		}
	}
	return false
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func apiGet(t *testing.T, c *http.Client, raw string) map[string]any {
	t.Helper()
	return decodeAPI(t, get(t, c, raw))
}

func apiPost(t *testing.T, c *http.Client, raw string, v url.Values) map[string]any {
	t.Helper()
	return decodeAPI(t, postForm(t, c, raw, v))
}

func apiUpload(t *testing.T, c *http.Client, raw, token, name, comment string, data []byte, replace bool) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("action", "upload")
	_ = mw.WriteField("filename", name)
	_ = mw.WriteField("comment", comment)
	_ = mw.WriteField("token", token)
	_ = mw.WriteField("format", "json")
	if replace {
		_ = mw.WriteField("ignorewarnings", "1")
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, raw, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return decodeAPI(t, res)
}

func decodeAPI(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	b := readAll(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("api status %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "json") {
		t.Fatal("api response was not json")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		t.Fatalf("api json (%d bytes)", len(b))
	}
	return m
}

func apiCode(m map[string]any) string {
	errm, _ := m["error"].(map[string]any)
	if errm == nil {
		return ""
	}
	code, _ := errm["code"].(string)
	return code
}

func jmap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, _ := m[key].(map[string]any)
	if v == nil {
		t.Fatalf("missing %s", key)
	}
	return v
}

func jslice(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, _ := m[key].([]any)
	if v == nil {
		t.Fatalf("missing %s", key)
	}
	return v
}

func mustID(t *testing.T, body, id string) string {
	t.Helper()
	re := regexp.MustCompile(`id="` + regexp.QuoteMeta(id) + `">([^<]*)</`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("missing %s", id)
	}
	return html.UnescapeString(m[1])
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
