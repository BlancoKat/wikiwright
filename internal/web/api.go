package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/ingest"
	"github.com/BlancoKat/wikiwright/internal/parser"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

// serveAPI is /<wiki>/api.php. JSON uses MediaWiki formatversion=2 shapes.
// A private wiki refuses unauthenticated reads. Login and a tokens-only
// query stay available so a client can sign in.
func (s *Site) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	max := s.Cfg.MaxUpload
	if max <= 0 {
		max = 20 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.apiErr(w, "badrequest", "Could not read the request.")
		return
	}
	format := r.FormValue("format")
	if format != "" && !strings.EqualFold(format, "json") {
		s.apiErr(w, "badformat", "This API returns JSON. Send format=json and formatversion=2.")
		return
	}
	if !s.apiAssert(w, r) {
		return
	}
	if s.Cfg.Access == "private" && !s.loggedIn(r) && !apiPublic(r) {
		s.apiErr(w, "readapidenied", "You need to log in to read this wiki.")
		return
	}
	switch r.FormValue("action") {
	case "", "help":
		s.apiHelp(w)
	case "login":
		s.apiLogin(w, r)
	case "logout":
		s.apiLogout(w, r)
	case "query":
		s.apiQuery(w, r)
	case "edit":
		s.apiEdit(w, r)
	case "upload":
		s.apiUpload(w, r)
	default:
		s.apiErr(w, "unknown_action", "Unrecognized action. Wikiwright supports login, logout, query, edit, and upload.")
	}
}

func apiPublic(r *http.Request) bool {
	switch r.FormValue("action") {
	case "", "help", "login", "logout":
		return true
	case "query":
		for _, part := range splitPipe(r.FormValue("meta")) {
			if part != "tokens" && part != "userinfo" {
				return false
			}
		}
		if r.FormValue("meta") == "" {
			return false
		}
		return r.FormValue("list") == "" && r.FormValue("prop") == "" && r.FormValue("titles") == "" && r.FormValue("pageids") == ""
	default:
		return false
	}
}

func (s *Site) apiHelp(w http.ResponseWriter) {
	apiWrite(w, map[string]any{
		"batchcomplete": true,
		"help": map[string]any{
			"format":        "json",
			"formatversion": 2,
			"actions":       []string{"login", "logout", "query", "edit", "upload"},
			"query":         "meta=tokens|userinfo|siteinfo, prop=revisions, list=allpages",
			"info":          "Send format=json and formatversion=2. assert=user and assert=bot both require a login. Writes require POST and a csrf token. Bot passwords log in as User@botname.",
		},
	})
}

func (s *Site) apiLoginCookieName() string {
	return "wikiwright_lgtoken_" + cookieToken(s.Cfg.ID)
}

func (s *Site) issueAPILoginToken(w http.ResponseWriter) (string, error) {
	raw, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	s.setCookie(w, s.apiLoginCookieName(), raw, 30*60)
	return raw, nil
}

func (s *Site) apiLoginTokenOK(r *http.Request, got string) bool {
	c, err := r.Cookie(s.apiLoginCookieName())
	if err != nil || c.Value == "" {
		return false
	}
	return tokensEqual(c.Value, strings.TrimSuffix(got, `+\`))
}

func (s *Site) apiLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.apiErr(w, "mustbeposted", "The login action requires a POST request.")
		return
	}
	tok := r.FormValue("lgtoken")
	if tok == "" {
		raw, err := s.issueAPILoginToken(w)
		if err != nil {
			s.apiErr(w, "internalerror", "Could not issue a login token.")
			return
		}
		apiWrite(w, map[string]any{"login": map[string]any{"result": "NeedToken", "token": raw + `+\`}})
		return
	}
	if !s.apiLoginTokenOK(r, tok) {
		s.apiErr(w, "badtoken", "Invalid login token.")
		return
	}
	ip := clientIP(r)
	n, err := s.Store.LoginFailures(ip, time.Now().Add(-failWindow))
	if err != nil {
		s.apiErr(w, "internalerror", "Could not check the login limit.")
		return
	}
	if n >= maxFailures {
		s.apiErr(w, "ratelimited", "Too many failed logins from this address. Try again in 15 minutes.")
		return
	}
	lgname := strings.TrimSpace(r.FormValue("lgname"))
	pass := r.FormValue("lgpassword")
	userName, appID, isBot := splitBotLogin(lgname)
	user, err := s.Store.UserByName(userName)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not look up that account.")
		return
	}
	var grants string
	switch {
	case isBot:
		if appID == "" || user.ID == 0 {
			s.apiLoginFailed(w, ip, lgname)
			return
		}
		hash, g, ok, err := s.Store.BotPasswordHash(user.ID, appID)
		if err != nil {
			s.apiErr(w, "internalerror", "Could not look up that bot password.")
			return
		}
		if !ok || !auth.Verify(pass, hash) {
			s.apiLoginFailed(w, ip, lgname)
			return
		}
		if strings.TrimSpace(g) == "" {
			s.apiLoginFailed(w, ip, lgname)
			return
		}
		grants = g
	default:
		if user.ID == 0 || !auth.Verify(pass, user.PasswordHash) {
			s.apiLoginFailed(w, ip, lgname)
			return
		}
		if user.TOTPEnabled {
			apiWrite(w, map[string]any{"login": map[string]any{
				"result": "Failed",
				"reason": "This account uses an authenticator. Create a bot password and log in as User@botname.",
			}})
			return
		}
	}
	token, err := auth.RandomToken(32)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not start a session.")
		return
	}
	csrf, err := auth.RandomToken(32)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not start a session.")
		return
	}
	exp := time.Now().Add(sessionTTL)
	if isBot {
		err = s.Store.CreateBotSession(user.ID, token, csrf, grants, exp)
	} else {
		err = s.Store.CreateSession(user.ID, token, csrf, exp)
	}
	if err != nil {
		s.apiErr(w, "internalerror", "Could not start a session.")
		return
	}
	_ = s.Store.RecordLogin(ip, user.Name, true)
	s.setCookie(w, s.cookieName(), token, int(sessionTTL.Seconds()))
	apiWrite(w, map[string]any{"login": map[string]any{
		"result":     "Success",
		"lguserid":   user.ID,
		"lgusername": user.Name,
	}})
}

func (s *Site) apiLoginFailed(w http.ResponseWriter, ip, name string) {
	_ = s.Store.RecordLogin(ip, name, false)
	apiWrite(w, map[string]any{"login": map[string]any{
		"result": "Failed",
		"reason": "Incorrect username or password.",
	}})
}

func splitBotLogin(name string) (user, app string, bot bool) {
	name = strings.TrimSpace(name)
	i := strings.Index(name, "@")
	if i < 0 {
		return name, "", false
	}
	return strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+1:]), true
}

func (s *Site) apiLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.apiErr(w, "mustbeposted", "The logout action requires a POST request.")
		return
	}
	sess := s.current(r)
	if sess.Token != "" {
		if !apiTokenOK(sess.CSRF, r.FormValue("token")) {
			s.apiErr(w, "badtoken", "Invalid CSRF token.")
			return
		}
		_ = s.Store.DeleteSession(sess.Token)
	}
	s.clearCookie(w, s.cookieName())
	apiWrite(w, map[string]any{"logout": map[string]any{}})
}

func (s *Site) apiQuery(w http.ResponseWriter, r *http.Request) {
	meta := splitPipe(r.FormValue("meta"))
	list := splitPipe(r.FormValue("list"))
	prop := splitPipe(r.FormValue("prop"))
	if len(meta) == 0 && len(list) == 0 && len(prop) == 0 {
		s.apiErr(w, "missingparam", "Specify meta, list, or prop.")
		return
	}
	query := map[string]any{}
	cont := map[string]any{}
	for _, m := range meta {
		switch m {
		case "tokens":
			toks, err := s.apiTokens(w, r)
			if err != nil {
				s.apiErr(w, "badvalue", err.Error())
				return
			}
			query["tokens"] = toks
		case "siteinfo":
			if err := s.apiSiteinfo(r, query); err != nil {
				s.apiErr(w, "badvalue", err.Error())
				return
			}
		case "userinfo":
			query["userinfo"] = s.apiUserInfo(r)
		default:
			s.apiErr(w, "unknown_meta", "Unknown meta module: "+m+".")
			return
		}
	}
	for _, l := range list {
		switch l {
		case "allpages":
			pages, next, err := s.apiAllPages(r)
			if err != nil {
				s.apiErr(w, "badvalue", err.Error())
				return
			}
			query["allpages"] = pages
			if next != "" {
				cont["apcontinue"] = next
				cont["continue"] = "-||"
			}
		default:
			s.apiErr(w, "unknown_list", "Unknown list module: "+l+".")
			return
		}
	}
	for _, p := range prop {
		switch p {
		case "revisions":
			pages, err := s.apiRevisions(r)
			if err != nil {
				s.apiErr(w, "badvalue", err.Error())
				return
			}
			query["pages"] = pages
		default:
			s.apiErr(w, "unknown_prop", "Unknown prop module: "+p+".")
			return
		}
	}
	out := map[string]any{"query": query}
	if len(cont) > 0 {
		out["continue"] = cont
	} else {
		out["batchcomplete"] = true
	}
	apiWrite(w, out)
}

func (s *Site) apiTokens(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	types := splitPipe(r.FormValue("type"))
	if len(types) == 0 {
		types = []string{"csrf"}
	}
	out := map[string]any{}
	sess := s.current(r)
	logged := sess.User.ID != 0 && !sess.NeedsTOTP && sess.CSRF != ""
	for _, kind := range types {
		switch kind {
		case "csrf":
			if logged {
				out["csrftoken"] = sess.CSRF + `+\`
			} else {
				out["csrftoken"] = `+\`
			}
		case "login":
			raw, err := s.issueAPILoginToken(w)
			if err != nil {
				return nil, errors.New("Could not issue a login token.")
			}
			out["logintoken"] = raw + `+\`
		default:
			return nil, errors.New("Unknown token type: " + kind + ".")
		}
	}
	return out, nil
}

// apiAssert enforces assert=user and assert=bot. A bot password is a logged-in
// user, so assert=bot does not require a separate bot right.
func (s *Site) apiAssert(w http.ResponseWriter, r *http.Request) bool {
	switch r.FormValue("assert") {
	case "", "0":
		return true
	case "user", "bot":
		if !s.loggedIn(r) {
			s.apiErr(w, "assertuserfailed", "The assertion that the user is logged in failed.")
			return false
		}
		return true
	default:
		s.apiErr(w, "badvalue", "Unknown assert value. Use user or bot.")
		return false
	}
}

// apiUserInfo is meta=userinfo. id and name are always present. An anonymous
// request sets anon. groups and rights are included when uiprop asks for them.
// Other uiprop values, such as blockinfo, are ignored.
func (s *Site) apiUserInfo(r *http.Request) map[string]any {
	sess := s.current(r)
	logged := sess.User.ID != 0 && !sess.NeedsTOTP
	info := map[string]any{}
	if logged {
		info["id"] = sess.User.ID
		info["name"] = sess.User.Name
	} else {
		info["id"] = 0
		info["name"] = clientIP(r)
		info["anon"] = true
	}
	props := map[string]bool{}
	for _, p := range splitPipe(r.FormValue("uiprop")) {
		props[p] = true
	}
	if props["groups"] {
		info["groups"] = apiGroups(sess, logged)
	}
	if props["rights"] {
		info["rights"] = s.apiRights(sess, logged)
	}
	return info
}

func apiGroups(sess store.Session, logged bool) []string {
	if !logged {
		return []string{"*"}
	}
	groups := []string{"*", "user"}
	if sess.User.IsAdmin {
		groups = append(groups, "sysop")
	}
	return groups
}

func (s *Site) apiRights(sess store.Session, logged bool) []string {
	var rights []string
	if logged || s.Cfg.Access != "private" {
		rights = append(rights, "read")
	}
	if !logged {
		if rights == nil {
			return []string{}
		}
		return rights
	}
	if sess.Can("edit") {
		rights = append(rights, "edit")
	}
	if sess.Can("create") {
		rights = append(rights, "createpage")
	}
	if sess.Can("upload") {
		rights = append(rights, "upload")
	}
	return append(rights, "writeapi")
}

func (s *Site) apiSiteinfo(r *http.Request, query map[string]any) error {
	props := splitPipe(r.FormValue("siprop"))
	if len(props) == 0 {
		props = []string{"general"}
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	prefix := s.prefix()
	for _, p := range props {
		switch p {
		case "general":
			query["general"] = map[string]any{
				"sitename":    s.Cfg.Name,
				"mainpage":    s.Cfg.MainPage,
				"generator":   "Wikiwright " + Version,
				"wikiid":      s.Cfg.ID,
				"server":      scheme + "://" + r.Host,
				"servername":  r.Host,
				"articlepath": prefix + "/index.php?title=$1",
				"scriptpath":  prefix,
				"script":      prefix + "/index.php",
				"timezone":    s.Cfg.Timezone,
				"case":        "first-letter",
				"lang":        "en",
				"writeapi":    true,
			}
		case "namespaces":
			var ns []map[string]any
			for id := -2; id <= 15; id++ {
				name := title.Canonical(id, s.Cfg.ProjectNS)
				item := map[string]any{"id": id, "name": name, "canonical": name}
				if id == title.NSMain {
					item["content"] = true
				}
				ns = append(ns, item)
			}
			query["namespaces"] = ns
		default:
			return errors.New("Unknown siprop: " + p + ".")
		}
	}
	return nil
}

func (s *Site) apiAllPages(r *http.Request) ([]map[string]any, string, error) {
	haveNS := r.Form.Has("apnamespace")
	ns := 0
	if haveNS {
		var err error
		ns, err = strconv.Atoi(r.FormValue("apnamespace"))
		if err != nil {
			return nil, "", errors.New("apnamespace must be an integer.")
		}
	}
	limit := 50
	if v := r.FormValue("aplimit"); v != "" && !strings.EqualFold(v, "max") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, "", errors.New("aplimit must be a positive integer.")
		}
		limit = n
	}
	if strings.EqualFold(r.FormValue("aplimit"), "max") {
		limit = 500
	}
	prefix := r.FormValue("apprefix")
	if haveNS && ns != title.NSMain && prefix != "" {
		prefix = title.Canonical(ns, s.Cfg.ProjectNS) + ":" + prefix
	}
	cont := strings.TrimSpace(r.FormValue("apcontinue"))
	if cont != "" {
		if parsed := title.Parse(cont, s.Cfg.ProjectNS); parsed.DBKey != "" {
			cont = parsed.DBKey
		}
	}
	rows, err := s.Store.AllPagesLimited(ns, haveNS, prefix, cont, limit+1)
	if err != nil {
		return nil, "", errors.New("Could not list pages.")
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].DBKey
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, map[string]any{
			"pageid": p.ID,
			"ns":     p.Namespace,
			"title":  s.pageLabel(p),
		})
	}
	return out, next, nil
}

func (s *Site) apiRevisions(r *http.Request) ([]map[string]any, error) {
	titles := splitPipe(r.FormValue("titles"))
	if len(titles) == 0 {
		return nil, errors.New("The titles parameter is required.")
	}
	if len(titles) > 20 {
		return nil, errors.New("At most 20 titles.")
	}
	includeContent := propWanted(r.FormValue("rvprop"), "content")
	var out []map[string]any
	for _, raw := range titles {
		t := title.Parse(raw, s.Cfg.ProjectNS)
		if t.DBKey == "" {
			out = append(out, map[string]any{"title": raw, "missing": true, "invalid": true})
			continue
		}
		pg, err := s.Store.PageByDBKey(t.DBKey)
		if err != nil {
			return nil, errors.New("Could not read that page.")
		}
		if pg.ID == 0 {
			out = append(out, map[string]any{"ns": t.Namespace, "title": t.Display, "missing": true})
			continue
		}
		rev, err := s.Store.LatestRevision(pg.ID)
		if err != nil || rev.ID == 0 {
			return nil, errors.New("Could not read the latest revision.")
		}
		item := map[string]any{
			"revid":     rev.ID,
			"parentid":  rev.ParentID,
			"user":      rev.UserText,
			"timestamp": rev.Timestamp.UTC().Format(time.RFC3339),
			"comment":   rev.Comment,
		}
		if rev.Minor {
			item["minor"] = true
		}
		if includeContent {
			item["slots"] = map[string]any{
				"main": map[string]any{
					"contentmodel":  "wikitext",
					"contentformat": "text/x-wiki",
					"content":       rev.Content,
				},
			}
		}
		out = append(out, map[string]any{
			"pageid":    pg.ID,
			"ns":        pg.Namespace,
			"title":     s.pageLabel(pg),
			"revisions": []map[string]any{item},
		})
	}
	return out, nil
}

func (s *Site) apiEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.apiErr(w, "mustbeposted", "The edit action requires a POST request.")
		return
	}
	sess := s.current(r)
	if sess.User.ID == 0 || sess.NeedsTOTP {
		s.apiErr(w, "notloggedin", "You must be logged in to edit.")
		return
	}
	if !apiTokenOK(sess.CSRF, r.FormValue("token")) {
		s.apiErr(w, "badtoken", "Invalid CSRF token.")
		return
	}
	if cm := r.FormValue("contentmodel"); cm != "" && cm != "wikitext" {
		s.apiErr(w, "badvalue", "Only the wikitext content model is supported.")
		return
	}
	if cf := r.FormValue("contentformat"); cf != "" && cf != "text/x-wiki" {
		s.apiErr(w, "badvalue", "Only the text/x-wiki content format is supported.")
		return
	}
	rawTitle := r.FormValue("title")
	if strings.TrimSpace(rawTitle) == "" {
		s.apiErr(w, "missingtitle", "The title parameter is required.")
		return
	}
	t := title.Parse(rawTitle, s.Cfg.ProjectNS)
	if t.Namespace < 0 || !t.Legal() {
		s.apiErr(w, "invalidtitle", "That title cannot be edited.")
		return
	}
	if !r.Form.Has("text") {
		s.apiErr(w, "notext", "The text parameter is required.")
		return
	}
	text := r.FormValue("text")
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not read that page.")
		return
	}
	section := r.FormValue("section")
	creating := pg.ID == 0
	if flagSet(r, "createonly") && !creating {
		s.apiErr(w, "articleexists", "The page you tried to create already exists.")
		return
	}
	if flagSet(r, "nocreate") && creating {
		s.apiErr(w, "missingtitle", "The page you tried to edit does not exist.")
		return
	}
	if creating && !sess.Can("create") {
		s.apiErr(w, "permissiondenied", "This bot password cannot create pages.")
		return
	}
	if !creating && !sess.Can("edit") {
		s.apiErr(w, "permissiondenied", "This bot password cannot edit pages.")
		return
	}
	full := ""
	if !creating {
		rev, err := s.Store.LatestRevision(pg.ID)
		if err != nil {
			s.apiErr(w, "internalerror", "Could not read the latest revision.")
			return
		}
		full = rev.Content
	}
	if r.Form.Has("baserevid") {
		base := atoi64(r.FormValue("baserevid"))
		if base != pg.LatestRevID {
			s.apiErr(w, "editconflict", "The page changed since baserevid.")
			return
		}
	}
	summary := strings.TrimSpace(r.FormValue("summary"))
	if len(summary) > 500 {
		summary = summary[:500]
	}
	content := text
	switch section {
	case "":
	case "new":
		subject := strings.TrimSpace(r.FormValue("sectiontitle"))
		if subject == "" {
			subject = summary
		}
		if subject == "" {
			subject = "New section"
		}
		content = parser.AppendNewSection(full, subject, text)
		if summary == "" {
			summary = parser.NewSectionSummary(subject)
		}
	default:
		if creating {
			s.apiErr(w, "nosuchsection", "That section does not exist.")
			return
		}
		var ok bool
		content, ok = parser.ReplaceSection(full, section, text)
		if !ok {
			s.apiErr(w, "nosuchsection", "That section does not exist.")
			return
		}
	}
	old := pg.LatestRevID
	pageID, err := s.save(t, sess.User.Name, summary, content, flagSet(r, "minor"), old)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not save the page.")
		return
	}
	rev, err := s.Store.LatestRevision(pageID)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not read the saved revision.")
		return
	}
	apiWrite(w, map[string]any{"edit": map[string]any{
		"result":       "Success",
		"pageid":       pageID,
		"title":        t.Display,
		"contentmodel": "wikitext",
		"oldrevid":     old,
		"newrevid":     rev.ID,
		"newtimestamp": rev.Timestamp.UTC().Format(time.RFC3339),
	}})
}

func (s *Site) apiUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.apiErr(w, "mustbeposted", "The upload action requires a POST request.")
		return
	}
	sess := s.current(r)
	if sess.User.ID == 0 || sess.NeedsTOTP {
		s.apiErr(w, "notloggedin", "You must be logged in to upload.")
		return
	}
	if !apiTokenOK(sess.CSRF, r.FormValue("token")) {
		s.apiErr(w, "badtoken", "Invalid CSRF token.")
		return
	}
	if !sess.Can("upload") {
		s.apiErr(w, "permissiondenied", "This bot password cannot upload files.")
		return
	}
	fh, hdr, err := r.FormFile("file")
	if err != nil {
		s.apiErr(w, "missingparam", "The file parameter is required.")
		return
	}
	defer fh.Close()
	name := strings.TrimSpace(r.FormValue("filename"))
	if name == "" && hdr != nil {
		name = hdr.Filename
	}
	name = ingest.CleanName(name)
	if name == "" {
		s.apiErr(w, "illegal-filename", "That file name is not allowed.")
		return
	}
	if !uploadExtAllowed(name, s.uploadTypes()) {
		s.apiErr(w, "filetype-banned", "That file type is not allowed. Allowed types: "+s.uploadAcceptLabel()+".")
		return
	}
	exists, err := s.uploadTargetExists(name)
	if err != nil {
		s.apiErr(w, "internalerror", "Could not check whether that file already exists.")
		return
	}
	if exists && !flagSet(r, "ignorewarnings") {
		apiWrite(w, map[string]any{"upload": map[string]any{
			"result":   "Warning",
			"warnings": map[string]any{"exists": name},
		}})
		return
	}
	max := s.Cfg.MaxUpload
	if max <= 0 {
		max = 20 << 20
	}
	comment := r.FormValue("comment")
	if alt := r.FormValue("text"); comment == "" {
		comment = alt
	}
	var rec store.FileRec
	body := io.LimitReader(fh, max+1)
	if uploadAllowsSVG(s.uploadTypes()) && strings.EqualFold(filepath.Ext(name), ".svg") {
		rec, err = ingest.SaveImported(s.Store, s.Cfg.Files, name, body, max, sess.User.Name, comment)
	} else {
		rec, err = ingest.SaveUpload(s.Store, s.Cfg.Files, name, body, max, sess.User.Name, comment)
	}
	if err != nil {
		s.apiErr(w, "verification-error", "The file was not stored.")
		return
	}
	t := title.Parse("File:"+rec.Name, s.Cfg.ProjectNS)
	if err := s.recordUpload(t, sess.User.Name, rec.Name, comment); err != nil {
		s.apiErr(w, "internalerror", "The file was stored but its page could not be updated.")
		return
	}
	apiWrite(w, map[string]any{"upload": map[string]any{
		"result":   "Success",
		"filename": rec.Name,
	}})
}

func apiTokenOK(want, got string) bool {
	return tokensEqual(want, strings.TrimSuffix(got, `+\`))
}

func flagSet(r *http.Request, name string) bool {
	if !r.Form.Has(name) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.FormValue(name))) {
	case "", "1", "true", "yes":
		return true
	default:
		return false
	}
}

func propWanted(list, name string) bool {
	list = strings.TrimSpace(list)
	if list == "" {
		return true
	}
	for _, part := range splitPipe(list) {
		if part == name {
			return true
		}
	}
	return false
}

func splitPipe(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, "|") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (s *Site) apiErr(w http.ResponseWriter, code, info string) {
	w.Header().Set("MediaWiki-API-Error", code)
	apiWrite(w, map[string]any{"error": map[string]any{"code": code, "info": info}})
}

func apiWrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}
