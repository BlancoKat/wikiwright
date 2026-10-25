package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

func (s *Site) namespaceOptions(selected string) string {
	var b strings.Builder
	all := ""
	if selected == "" {
		all = ` selected`
	}
	b.WriteString(`<option value="" ` + all + `>All</option>`)
	for ns := 0; ns <= 15; ns++ {
		label := title.Canonical(ns, s.Cfg.ProjectNS)
		if ns == title.NSMain {
			label = "(Articles)"
		}
		sel := ""
		if selected == strconv.Itoa(ns) {
			sel = ` selected`
		}
		b.WriteString(`<option value="` + strconv.Itoa(ns) + `"` + sel + `>` + esc(label) + `</option>`)
	}
	return b.String()
}

func (s *Site) namespaceFilter(pageTitle, selected string) string {
	return `<form method="get" action="` + esc(s.prefix()+"/index.php") + `">` +
		`<input type="hidden" name="title" value="` + esc(pageTitle) + `">` +
		`<label>Namespace <select name="namespace">` + s.namespaceOptions(selected) + `</select></label> ` +
		`<button type="submit">Show</button></form>`
}

func (s *Site) pageLabel(p store.Page) string {
	if p.Namespace == title.NSMain {
		return p.Title
	}
	prefix := title.Canonical(p.Namespace, s.Cfg.ProjectNS)
	if prefix == "" {
		return p.Title
	}
	return prefix + ":" + p.Title
}

func (s *Site) pageList(pages []store.Page, empty string) string {
	if len(pages) == 0 {
		return `<p>` + esc(empty) + `</p>`
	}
	var b strings.Builder
	b.WriteString(`<ul class="wikiwright-plain">`)
	for _, p := range pages {
		cls := ""
		if p.IsRedirect {
			cls = ` class="mw-redirect"`
		}
		b.WriteString(`<li><a` + cls + ` href="` + esc(s.pageURL(p.DBKey)) + `">` + esc(s.pageLabel(p)) + `</a></li>`)
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func (s *Site) handlePrefixIndex(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	nsParam := r.URL.Query().Get("namespace")
	if _, ok := r.URL.Query()["namespace"]; !ok {
		nsParam = "0"
	}
	ns := 0
	if nsParam != "" {
		var err error
		ns, err = strconv.Atoi(nsParam)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "Unknown namespace.")
			return
		}
	}
	var b strings.Builder
	b.WriteString(`<form method="get" action="` + esc(s.prefix()+"/index.php") + `">`)
	b.WriteString(`<input type="hidden" name="title" value="Special:PrefixIndex">`)
	b.WriteString(`<label>Prefix <input type="text" name="prefix" value="` + esc(prefix) + `"></label> `)
	b.WriteString(`<label>Namespace <select name="namespace">` + s.namespaceOptions(nsParam) + `</select></label> `)
	b.WriteString(`<button type="submit">Show</button></form>`)
	if strings.TrimSpace(prefix) == "" {
		b.WriteString(`<p class="wikiwright-muted">Enter a prefix to list matching titles.</p>`)
		s.writeSpecial(w, r, "Prefix index", b.String())
		return
	}
	lookup := prefix
	if nsParam != "" && ns != title.NSMain {
		lookup = title.Canonical(ns, s.Cfg.ProjectNS) + ":" + prefix
	}
	var pages []store.Page
	var err error
	if nsParam == "" {
		pages, err = s.Store.AllPagesLimited(0, false, lookup, "", 200)
	} else {
		pages, err = s.Store.PrefixPages(ns, lookup, 200)
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list pages.")
		return
	}
	b.WriteString(s.pageList(pages, "No pages start with that prefix."))
	s.writeSpecial(w, r, "Prefix index", b.String())
}

func (s *Site) handleNewPages(w http.ResponseWriter, r *http.Request) {
	pages, err := s.Store.NewPages(50)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list new pages.")
		return
	}
	s.writeSpecial(w, r, "New pages", s.pageList(pages, "No pages yet."))
}

func (s *Site) handleNewFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.Store.RecentFiles(50)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list files.")
		return
	}
	if len(files) == 0 {
		s.writeSpecial(w, r, "New files", `<p>No files have been uploaded.</p>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<table class="wikitable"><thead><tr><th>File</th><th>Uploaded</th><th>Size</th><th>Uploader</th></tr></thead><tbody>`)
	for _, f := range files {
		page := s.pageURL("File:" + f.Name)
		raw := s.prefix() + "/images/" + url.PathEscape(f.Name)
		b.WriteString(`<tr><td><a href="` + esc(page) + `">` + esc(f.Name) + `</a> <span class="wikiwright-muted">(<a href="` + esc(raw) + `">file</a>)</span></td>`)
		b.WriteString(`<td>` + esc(s.fmtTime(f.UploadedAt)) + `</td><td>` + esc(formatBytes(f.Size)) + `</td><td>` + esc(f.UploadedBy) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	s.writeSpecial(w, r, "New files", b.String())
}

func (s *Site) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.Users()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list accounts.")
		return
	}
	if len(users) == 0 {
		s.writeSpecial(w, r, "User list", `<p>This wiki has no accounts.</p>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<table class="wikitable"><thead><tr><th>Username</th><th>Administrator</th><th>Authenticator</th><th>Created</th></tr></thead><tbody>`)
	for _, u := range users {
		admin, totp := "no", "no"
		if u.IsAdmin {
			admin = "yes"
		}
		if u.TOTPEnabled {
			totp = "yes"
		}
		b.WriteString(`<tr><td>` + esc(u.Name) + `</td><td>` + admin + `</td><td>` + totp + `</td><td>` + esc(s.fmtTime(u.CreatedAt)) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	s.writeSpecial(w, r, "User list", b.String())
}

func (s *Site) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	if sess.User.ID == 0 {
		http.Redirect(w, r, s.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape("Special:ChangePassword"), http.StatusFound)
		return
	}
	msg := ""
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "Could not read the form.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		msg = string(s.changePassword(sess, r))
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var b strings.Builder
	b.WriteString(msg)
	b.WriteString(`<p>Changing the password for <strong>` + esc(sess.User.Name) + `</strong>.</p>`)
	b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:ChangePassword")) + `">`)
	b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	b.WriteString(`<label>Current password <input type="password" name="current" autocomplete="current-password"></label>`)
	b.WriteString(`<label>New password <input type="password" name="newpass" autocomplete="new-password"></label>`)
	b.WriteString(`<label>Repeat new password <input type="password" name="again" autocomplete="new-password"></label>`)
	b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Change password</button></div></form>`)
	s.writeSpecial(w, r, "Change password", b.String())
}

func (s *Site) handleUncategorized(w http.ResponseWriter, r *http.Request) {
	pages, err := s.Store.UncategorizedPages(100)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list pages.")
		return
	}
	s.writeSpecial(w, r, "Uncategorized pages", s.pageList(pages, "Every article is in a category."))
}

func (s *Site) handleWanted(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.WantedPages(100)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list wanted pages.")
		return
	}
	if len(rows) == 0 {
		s.writeSpecial(w, r, "Wanted pages", `<p>No missing pages are linked.</p>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<ul class="wikiwright-plain">`)
	for _, row := range rows {
		label := strings.ReplaceAll(row.DBKey, "_", " ")
		b.WriteString(`<li><a href="` + esc(s.pageURL(row.DBKey)) + `">` + esc(label) + `</a> <span class="wikiwright-muted">(` + strconv.Itoa(row.Count) + `)</span></li>`)
	}
	b.WriteString(`</ul>`)
	s.writeSpecial(w, r, "Wanted pages", b.String())
}

func (s *Site) handleLonely(w http.ResponseWriter, r *http.Request) {
	pages, err := s.Store.LonelyPages(100)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list pages.")
		return
	}
	s.writeSpecial(w, r, "Lonely pages", s.pageList(pages, "Every article is linked from somewhere."))
}

func (s *Site) handleRedirects(w http.ResponseWriter, r *http.Request) {
	pages, err := s.Store.Redirects(200)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list redirects.")
		return
	}
	s.writeSpecial(w, r, "Redirects", s.pageList(pages, "This wiki has no redirects."))
}

func (s *Site) handleContributions(w http.ResponseWriter, r *http.Request, arg string) {
	user := strings.TrimSpace(arg)
	if user == "" {
		user = strings.TrimSpace(r.URL.Query().Get("user"))
	}
	var b strings.Builder
	b.WriteString(`<form method="get" action="` + esc(s.prefix()+"/index.php") + `">`)
	b.WriteString(`<input type="hidden" name="title" value="Special:Contributions">`)
	b.WriteString(`<label>Username <input type="text" name="user" value="` + esc(user) + `"></label> `)
	b.WriteString(`<button type="submit">Show</button></form>`)
	if user == "" {
		b.WriteString(`<p class="wikiwright-muted">Enter a username.</p>`)
		s.writeSpecial(w, r, "User contributions", b.String())
		return
	}
	rows, err := s.Store.Contributions(user, 50)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list contributions.")
		return
	}
	if len(rows) == 0 {
		b.WriteString(`<p>No edits by ` + esc(user) + `.</p>`)
		s.writeSpecial(w, r, "User contributions", b.String())
		return
	}
	b.WriteString(`<ul class="wikiwright-plain">`)
	for _, rc := range rows {
		label := rc.Title
		if rc.NS != title.NSMain {
			label = title.Canonical(rc.NS, s.Cfg.ProjectNS) + ":" + rc.Title
		}
		b.WriteString(`<li>`)
		if rc.Minor {
			b.WriteString(`<span class="minoredit">m</span> `)
		}
		b.WriteString(esc(s.fmtTime(rc.Timestamp)) + ` <a href="` + esc(s.pageURL(rc.DBKey)) + `">` + esc(label) + `</a>`)
		if rc.Comment != "" {
			b.WriteString(` <span class="wikiwright-muted">(` + esc(rc.Comment) + `)</span>`)
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
	s.writeSpecial(w, r, "User contributions", b.String())
}

func (s *Site) handleStatistics(w http.ResponseWriter, r *http.Request) {
	pages, articles, files, err := s.Store.Counts()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not count pages.")
		return
	}
	body := `<table class="wikitable">` +
		`<tr><th>Pages</th><td>` + strconv.Itoa(pages) + `</td></tr>` +
		`<tr><th>Articles</th><td>` + strconv.Itoa(articles) + `</td></tr>` +
		`<tr><th>Files</th><td>` + strconv.Itoa(files) + `</td></tr>` +
		`</table>`
	s.writeSpecial(w, r, "Statistics", body)
}
