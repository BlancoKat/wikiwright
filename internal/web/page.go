package web

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/diff"
	"github.com/BlancoKat/wikiwright/internal/ingest"
	"github.com/BlancoKat/wikiwright/internal/parser"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

func (s *Site) dispatch(w http.ResponseWriter, r *http.Request, raw string) {
	t := title.Parse(raw, s.Cfg.ProjectNS)
	if t.Namespace == title.NSSpecial {
		s.special(w, r, t)
		return
	}
	if !t.Legal() {
		s.fail(w, r, http.StatusBadRequest, "That title cannot be stored.")
		return
	}
	switch r.URL.Query().Get("action") {
	case "edit":
		s.edit(w, r, t)
	case "history":
		s.history(w, r, t)
	case "info":
		s.info(w, r, t)
	default:
		if r.URL.Query().Get("diff") != "" {
			s.showDiff(w, r, t)
			return
		}
		s.view(w, r, t)
	}
}

func (s *Site) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.writePage(w, r, pageView{
		Status:    status,
		Heading:   http.StatusText(status),
		Action:    "view",
		DBKey:     "Special:Error",
		Namespace: title.NSSpecial,
		Body:      template.HTML(`<div class="wikiwright-error">` + esc(msg) + `</div>`),
		Session:   s.sessionView(r),
	})
}

func (s *Site) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "That path is not a wiki page. Open an article with "+s.prefix()+"/index.php?title=")
}

func (s *Site) view(w http.ResponseWriter, r *http.Request, requested title.T) {
	oldid := atoi64(r.URL.Query().Get("oldid"))
	redirectNo := r.URL.Query().Get("redirect") == "no"
	cur := requested
	var trail []title.T
	var pg store.Page
	followed := 0
	for {
		var err error
		pg, err = s.Store.PageByDBKey(cur.DBKey)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not read that page.")
			return
		}
		if pg.ID == 0 || oldid != 0 || redirectNo || !pg.IsRedirect || pg.RedirectTarget == "" || followed >= 2 {
			break
		}
		next := title.Parse(strings.ReplaceAll(pg.RedirectTarget, "_", " "), s.Cfg.ProjectNS)
		if next.DBKey == "" || next.DBKey == cur.DBKey {
			break
		}
		trail = append(trail, cur)
		cur = next
		followed++
	}
	var rev store.Revision
	var err error
	if pg.ID != 0 {
		if oldid != 0 {
			rev, err = s.Store.ResolveOldID(pg.ID, oldid)
			if err != nil || rev.ID == 0 || rev.PageID != pg.ID {
				s.fail(w, r, http.StatusNotFound, "There is no revision with that id on this page.")
				return
			}
		} else {
			rev, err = s.Store.LatestRevision(pg.ID)
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not read the latest revision.")
				return
			}
		}
	}
	var body strings.Builder
	var notices template.HTML
	var cats []string
	if oldid != 0 && rev.ID != 0 && rev.ID != pg.LatestRevID {
		notices = notice("warningbox", `This is an old revision of this page, as edited by `+esc(rev.UserText)+` at `+esc(s.fmtTime(rev.Timestamp))+`. It may differ from the <a href="`+esc(s.pageURL(cur.DBKey))+`">current revision</a>.`)
	}
	if pg.ID == 0 {
		body.WriteString(s.missingHTML(cur))
	} else {
		res := s.parse(cur.Display, rev.Content, s.canEdit(r))
		if n := warningsHTML(res.Warnings); n != "" {
			notices += n
		}
		for _, c := range res.Categories {
			cats = append(cats, c.Name)
		}
		if cur.Namespace == title.NSFile {
			body.WriteString(s.fileBox(cur.Rest))
		}
		body.WriteString(`<div class="mw-parser-output">`)
		body.WriteString(res.HTML)
		body.WriteString(`</div>`)
	}
	if cur.Namespace == title.NSCategory {
		body.WriteString(s.categoryMembersHTML(cur.Rest))
		if pg.ID == 0 {
			// Members make the category real even when the category page was never created.
			if members, err := s.Store.CategoryMembers(cur.Rest); err == nil && len(members) > 0 {
				body.Reset()
				body.WriteString(s.fileBox(""))
				body.WriteString(s.categoryMembersHTML(cur.Rest))
			}
		}
	}
	sub := template.HTML("")
	if len(trail) > 0 {
		from := trail[0]
		href := s.pageURL(from.DBKey) + "&redirect=no"
		sub = template.HTML(`<span class="mw-redirectedfrom">(Redirected from <a href="` + esc(href) + `">` + esc(from.Display) + `</a>)</span>`)
	}
	subj, talk := subjectTalk(cur, s.Cfg.ProjectNS)
	talkPage, _ := s.Store.PageByDBKey(talk.DBKey)
	s.writePage(w, r, pageView{
		Heading:     cur.Display,
		Action:      "view",
		DBKey:       cur.DBKey,
		Namespace:   cur.Namespace,
		ContentSub:  sub,
		Notices:     notices,
		Body:        template.HTML(body.String()),
		Categories:  cats,
		Session:     s.sessionView(r),
		IsArticle:   true,
		Exists:      pg.ID != 0,
		TalkExists:  talkPage.ID != 0,
		Subject:     subj,
		Talk:        talk,
		RevID:       rev.ID,
		LastMod:     rev.Timestamp,
		PrintURL:    s.publicURL(r, cur.DBKey),
		SearchQuery: "",
	})
}

func (s *Site) missingHTML(t title.T) string {
	href := s.pageURL(t.DBKey) + "&action=edit"
	return `<div class="noarticletext"><p>There is currently no text on this page. You can <a class="new" href="` + esc(href) + `">create this page</a>, or log in if editing asks you to.</p></div>`
}

func (s *Site) fileBox(name string) string {
	if name == "" {
		return ""
	}
	f, err := s.Store.File(name)
	if err != nil || f.Name == "" {
		return `<div class="wikiwright-notice">No file has been uploaded under this name.</div>`
	}
	src := s.prefix() + "/images/" + url.PathEscape(f.Name)
	if f.MIME == "application/pdf" || !strings.HasPrefix(f.MIME, "image/") {
		return `<div class="fullMedia"><a href="` + esc(src) + `">` + esc(f.Name) + `</a> <span class="wikiwright-muted">(` + esc(f.MIME) + `)</span></div>`
	}
	wh := ""
	if f.Width > 0 {
		wh += ` width="` + strconv.Itoa(f.Width) + `"`
	}
	if f.Height > 0 {
		wh += ` height="` + strconv.Itoa(f.Height) + `"`
	}
	return `<div class="fullImageLink"><a href="` + esc(src) + `"><img src="` + esc(src) + `" alt="` + esc(f.Name) + `"` + wh + ` decoding="async"></a></div>`
}

func (s *Site) categoryMembersHTML(name string) string {
	members, err := s.Store.CategoryMembers(name)
	if err != nil {
		return `<div class="wikiwright-error">Could not list this category.</div>`
	}
	var b strings.Builder
	b.WriteString(`<div class="mw-category"><h2>Pages in category &quot;` + esc(name) + `&quot;</h2>`)
	if len(members) == 0 {
		b.WriteString(`<p>This category has no pages.</p></div>`)
		return b.String()
	}
	b.WriteString(`<p>The following ` + strconv.Itoa(len(members)) + ` pages are in this category.</p><ul>`)
	for _, p := range members {
		cls := ""
		if p.IsRedirect {
			cls = ` class="mw-redirect"`
		}
		label := p.Title
		if p.Namespace != title.NSMain {
			label = title.Canonical(p.Namespace, s.Cfg.ProjectNS) + ":" + p.Title
		}
		b.WriteString(`<li><a` + cls + ` href="` + esc(s.pageURL(p.DBKey)) + `">` + esc(label) + `</a></li>`)
	}
	b.WriteString(`</ul></div>`)
	return b.String()
}

func (s *Site) edit(w http.ResponseWriter, r *http.Request, t title.T) {
	sess := s.sessionView(r)
	if (t.Namespace == title.NSMediaWiki || t.Namespace == title.NSMediaWikiTalk) && (sess.User.ID == 0 || sess.NeedsTOTP) {
		s.fail(w, r, http.StatusForbidden, "Log in to edit the MediaWiki namespace. Logged-in editors and administrators can change these pages.")
		return
	}
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not read that page.")
		return
	}
	var rev store.Revision
	if pg.ID != 0 {
		rev, err = s.Store.LatestRevision(pg.ID)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not read the latest revision.")
			return
		}
	}
	full := rev.Content
	section := r.URL.Query().Get("section")
	returnto := r.URL.Query().Get("returnto")
	text := full
	summary := ""
	minor := false
	watch := false
	conflict := ""
	preview := template.HTML("")
	posted := false
	if pg.ID == 0 {
		watch = true
	} else if sess.User.ID != 0 {
		watch, _ = s.Store.Watching(sess.User.ID, pg.ID)
	}
	if r.Method == http.MethodPost {
		posted = true
		if sess.User.ID == 0 || sess.NeedsTOTP {
			s.fail(w, r, http.StatusForbidden, "Log in to edit.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20+8192)
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "The edit was too large or could not be read.")
			return
		}
		if r.PostForm.Has("wpSection") {
			section = r.FormValue("wpSection")
		}
		if r.PostForm.Has("returnto") {
			returnto = r.FormValue("returnto")
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload the edit form and try again.")
			return
		}
		text = r.FormValue("wpTextbox1")
		summary = strings.TrimSpace(r.FormValue("wpSummary"))
		if len(summary) > 500 {
			summary = summary[:500]
		}
		minor = r.FormValue("wpMinoredit") != ""
		watch = r.FormValue("wpWatchthis") != ""
		base := atoi64(r.FormValue("base_rev"))
		saving := r.FormValue("wpSave") != ""
		diffing := !saving && r.FormValue("wpDiff") != ""
		if section != "" && section != "new" {
			if _, _, ok := parser.LookupSection(full, section); !ok {
				s.fail(w, r, http.StatusNotFound, "That section does not exist.")
				return
			}
		}
		merged := text
		comment := summary
		anchor := ""
		if section == "new" {
			subject := summary
			merged = parser.AppendNewSection(full, subject, text)
			comment = parser.NewSectionSummary(subject)
			anchor = parser.Anchor(subject)
		} else if section != "" {
			var ok bool
			merged, ok = parser.ReplaceSection(full, section, text)
			if !ok {
				s.fail(w, r, http.StatusNotFound, "That section does not exist.")
				return
			}
			if title, ok := parser.HeadingTitle(text); ok {
				anchor = parser.Anchor(title)
			}
		}
		if saving {
			if pg.ID == 0 && !sess.Can("create") {
				s.fail(w, r, http.StatusForbidden, "This bot password cannot create pages.")
				return
			}
			if pg.ID != 0 && !sess.Can("edit") {
				s.fail(w, r, http.StatusForbidden, "This bot password cannot edit pages.")
				return
			}
			if pg.LatestRevID != base {
				conflict = "Someone else saved this page while you were editing. Review your text and save again to replace the current revision."
			} else {
				pageID, err := s.save(t, sess.User.Name, comment, merged, minor, pg.LatestRevID)
				if err != nil {
					s.fail(w, r, http.StatusInternalServerError, "Could not save the page.")
					return
				}
				if sess.User.ID != 0 && pageID != 0 {
					_ = s.Store.SetWatch(sess.User.ID, pageID, watch)
				}
				dest := s.pageURL(t.DBKey)
				if anchor != "" {
					dest += "#" + anchor
				}
				http.Redirect(w, r, dest, http.StatusSeeOther)
				return
			}
		}
		var b strings.Builder
		if diffing {
			b.WriteString(`<div class="previewnote warningbox">These are your changes. Nothing has been saved yet.</div>`)
			b.WriteString(diffTable(full, merged))
		} else {
			previewSrc := text
			if section == "new" {
				previewSrc = parser.AppendNewSection("", summary, text)
			}
			res := s.parse(t.Display, previewSrc, section == "" && s.canEdit(r))
			b.WriteString(`<div class="previewnote warningbox">This is a preview. Nothing has been saved yet.</div>`)
			b.WriteString(string(warningsHTML(res.Warnings)))
			b.WriteString(`<div class="mw-parser-output">`)
			b.WriteString(res.HTML)
			b.WriteString(`</div><hr>`)
		}
		preview = template.HTML(b.String())
		if conflict != "" {
			pg, _ = s.Store.PageByDBKey(t.DBKey)
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !posted {
		switch section {
		case "":
			text = full
		case "new":
			text = ""
		default:
			part, sec, ok := parser.LookupSection(full, section)
			if !ok {
				s.fail(w, r, http.StatusNotFound, "That section does not exist.")
				return
			}
			text = part
			if section != "0" {
				summary = parser.SectionEditSummary(sec.Title)
			}
		}
	}
	if pg.ID == 0 && strings.TrimSpace(returnto) == "" {
		returnto = s.refererTitle(r, t)
	}
	readonly := sess.User.ID == 0 || sess.NeedsTOTP
	heading := "Editing " + t.Display
	if readonly {
		heading = "View source for " + t.Display
	}
	notices := preview
	if conflict != "" {
		notices = notice("wikiwright-error", esc(conflict)) + notices
	}
	base := pg.LatestRevID
	subj, talk := subjectTalk(t, s.Cfg.ProjectNS)
	talkPage, _ := s.Store.PageByDBKey(talk.DBKey)
	s.writePage(w, r, pageView{
		Heading:   heading,
		Action:    "edit",
		DBKey:     t.DBKey,
		Namespace: t.Namespace,
		Notices:   notices,
		Body: s.editForm(editForm{
			Title: t, Text: text, Summary: summary, Section: section,
			ReturnTo: returnto, Minor: minor, Watch: watch, Base: base, Session: sess,
			ReadOnly: readonly, Exists: pg.ID != 0,
		}),
		Session:    sess,
		IsArticle:  true,
		Exists:     pg.ID != 0,
		TalkExists: talkPage.ID != 0,
		Subject:    subj,
		Talk:       talk,
		RevID:      rev.ID,
		LastMod:    rev.Timestamp,
		PrintURL:   s.publicURL(r, t.DBKey),
	})
}

type editForm struct {
	Title            title.T
	Text, Summary    string
	Section          string
	ReturnTo         string
	Minor, Watch     bool
	Base             int64
	Session          store.Session
	ReadOnly, Exists bool
}

func (s *Site) editForm(f editForm) template.HTML {
	var b strings.Builder
	action := s.pageURL(f.Title.DBKey) + "&action=edit"
	if f.Section != "" {
		action += "&section=" + url.QueryEscape(f.Section)
	}
	b.WriteString(`<form class="mw-editform" id="editform" name="editform" method="post" action="` + esc(action) + `">`)
	if !f.ReadOnly {
		b.WriteString(`<input type="hidden" name="csrf" value="` + esc(f.Session.CSRF) + `">`)
	}
	b.WriteString(`<input type="hidden" name="base_rev" value="` + strconv.FormatInt(f.Base, 10) + `">`)
	b.WriteString(`<input type="hidden" name="wpSection" value="` + esc(f.Section) + `">`)
	b.WriteString(`<input type="hidden" name="returnto" value="` + esc(f.ReturnTo) + `">`)
	ro := ""
	if f.ReadOnly {
		ro = " readonly"
		b.WriteString(`<p class="wikiwright-notice">You are not logged in, so this source is read-only. <a href="` + esc(s.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape(f.Title.Display)) + `">Log in</a> to edit.</p>`)
	}
	if !f.ReadOnly && f.Section == "new" {
		b.WriteString(`<div id="wpSummaryLabel" class="mw-summary"><label for="wpSummary">Subject:</label>`)
		b.WriteString(`<input id="wpSummary" name="wpSummary" value="` + esc(f.Summary) + `" maxlength="500" size="60" spellcheck="true" accesskey="b" title="Enter a short summary [b]">`)
		b.WriteString(`</div>`)
	}
	// Browsers ignore one leading newline inside <textarea>. MediaWiki emits
	// one after the opening tag so a page that starts with a blank line
	// round-trips. See https://www.w3.org/TR/html401/interact/forms.html#h-17.7
	b.WriteString(`<textarea tabindex="1" accesskey="," id="wpTextbox1" name="wpTextbox1" cols="80" rows="25"` + ro + `>` + "\n" + esc(f.Text) + `</textarea>`)
	if !f.ReadOnly {
		b.WriteString(`<div class="editOptions">`)
		if f.Section != "new" {
			b.WriteString(`<div id="wpSummaryLabel" class="mw-summary"><label for="wpSummary">Summary:</label>`)
			b.WriteString(`<input id="wpSummary" name="wpSummary" value="` + esc(f.Summary) + `" maxlength="500" size="60" spellcheck="true" accesskey="b" title="Enter a short summary [b]">`)
			b.WriteString(`</div>`)
		}
		b.WriteString(`<div class="editCheckboxes">`)
		if f.Exists && f.Section != "new" {
			checked := ""
			if f.Minor {
				checked = " checked"
			}
			b.WriteString(`<span id="mw-editpage-minoredit"><input type="checkbox" name="wpMinoredit" id="wpMinoredit" value="1" accesskey="i" title="Mark this as a minor edit [i]"` + checked + `> <label for="wpMinoredit">This is a minor edit</label></span>`)
		}
		checked := ""
		if f.Watch {
			checked = " checked"
		}
		b.WriteString(`<span id="mw-editpage-watch"><input type="checkbox" name="wpWatchthis" id="wpWatchthis" value="1" accesskey="w" title="Add this page to your watchlist [w]"` + checked + `> <label for="wpWatchthis">Watch this page</label></span>`)
		b.WriteString(`</div>`)
		b.WriteString(s.copyrightWarning())
		saveLabel := "Save changes"
		if !f.Exists {
			saveLabel = "Save page"
		}
		b.WriteString(`<div class="editButtons">`)
		b.WriteString(`<input type="submit" id="wpSave" name="wpSave" value="` + saveLabel + `" accesskey="s" title="Save your changes [s]">`)
		b.WriteString(`<input type="submit" id="wpPreview" name="wpPreview" value="Show preview" accesskey="p" title="Preview your changes [p]">`)
		b.WriteString(`<input type="submit" id="wpDiff" name="wpDiff" value="Show changes" accesskey="v" title="Show which changes you made to the text [v]">`)
		b.WriteString(`<span class="cancelLink"><a id="mw-editform-cancel" href="` + esc(s.cancelHref(f)) + `">Cancel</a></span>`)
		b.WriteString(`<span class="editHelp"><a href="` + esc(s.pageURL("Help:Editing")) + `" target="helpwindow">Editing help</a> (opens in new window)</span>`)
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</form>`)
	return template.HTML(b.String())
}

func (s *Site) cancelHref(f editForm) string {
	if f.Exists {
		return s.pageURL(f.Title.DBKey)
	}
	if back := strings.TrimSpace(f.ReturnTo); back != "" {
		return s.returnURL(back)
	}
	return s.pageURL(f.Title.DBKey)
}

func (s *Site) copyrightWarning() string {
	name := esc(s.Cfg.Name)
	lic := strings.TrimSpace(s.Cfg.License)
	var under string
	if lic != "" {
		shown := esc(lic)
		if s.Cfg.LicenseURL != "" {
			shown = `<a class="external" rel="nofollow" href="` + esc(s.Cfg.LicenseURL) + `">` + shown + `</a>`
		}
		under = " are considered to be released under " + shown
	} else {
		under = " may be edited, altered, or removed by other contributors"
	}
	return `<div id="editpage-copywarn">Please note that all contributions to ` + name + under + `. If you do not want your writing to be edited mercilessly and redistributed at will, then do not submit it here.<br>You are also promising us that you wrote this yourself, or copied it from a public domain or similar free resource. <b>Do not submit copyrighted work without permission!</b></div>`
}

func (s *Site) refererTitle(r *http.Request, t title.T) string {
	ref := r.Referer()
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Host != "" && !strings.EqualFold(u.Host, r.Host)) {
		return ""
	}
	if !strings.HasPrefix(u.Path, s.prefix()+"/") && u.Path != s.prefix() {
		return ""
	}
	qTitle := u.Query().Get("title")
	if qTitle == "" {
		return ""
	}
	other := title.Parse(qTitle, s.Cfg.ProjectNS)
	if !other.Legal() || other.DBKey == "" || other.DBKey == t.DBKey {
		return ""
	}
	return other.Display
}

func (s *Site) save(t title.T, user, comment, content string, minor bool, parent int64) (int64, error) {
	content = strings.ReplaceAll(content, "\x00", "")
	if len(content) > 2<<20 {
		return 0, fmt.Errorf("page is too large")
	}
	id, err := s.Store.UpsertPage(t.Namespace, t.Rest, t.DBKey)
	if err != nil {
		return 0, err
	}
	if _, err := s.Store.AddRevision(id, user, comment, content, time.Now().UTC(), minor, 0, parent); err != nil {
		return 0, err
	}
	if err := ingest.ReindexPage(s.Store, s.Cfg.ProjectNS, id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Site) history(w http.ResponseWriter, r *http.Request, t title.T) {
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not read that page.")
		return
	}
	var b strings.Builder
	if pg.ID == 0 {
		b.WriteString(`<p>This page has no history yet.</p>`)
	} else {
		revs, err := s.Store.Revisions(pg.ID)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not read history.")
			return
		}
		b.WriteString(`<table class="wikiwright-history"><thead><tr><th>Revision</th><th>Date</th><th>User</th><th>Comment</th><th>Size</th></tr></thead><tbody>`)
		for i, rev := range revs {
			cur := s.pageURL(t.DBKey) + "&oldid=" + strconv.FormatInt(rev.ID, 10)
			prev := ""
			if i+1 < len(revs) {
				prev = s.pageURL(t.DBKey) + "&oldid=" + strconv.FormatInt(revs[i+1].ID, 10) + "&diff=" + strconv.FormatInt(rev.ID, 10)
			} else if rev.ParentID != 0 {
				prev = s.pageURL(t.DBKey) + "&oldid=" + strconv.FormatInt(rev.ParentID, 10) + "&diff=" + strconv.FormatInt(rev.ID, 10)
			}
			b.WriteString(`<tr><td><a href="` + esc(cur) + `">` + strconv.FormatInt(rev.ID, 10) + `</a>`)
			if prev != "" {
				b.WriteString(` · <a href="` + esc(prev) + `">diff</a>`)
			}
			if rev.ID == pg.LatestRevID {
				b.WriteString(` · current`)
			}
			b.WriteString(`</td><td>` + esc(s.fmtTime(rev.Timestamp)) + `</td><td>` + esc(rev.UserText) + `</td><td>`)
			if rev.Minor {
				b.WriteString(`<span class="minoredit">m</span> `)
			}
			b.WriteString(esc(rev.Comment) + `</td><td>` + strconv.Itoa(len(rev.Content)) + `</td></tr>`)
		}
		b.WriteString(`</tbody></table>`)
	}
	subj, talk := subjectTalk(t, s.Cfg.ProjectNS)
	talkPage, _ := s.Store.PageByDBKey(talk.DBKey)
	s.writePage(w, r, pageView{
		Heading:    "Revision history of " + t.Display,
		Action:     "history",
		DBKey:      t.DBKey,
		Namespace:  t.Namespace,
		Body:       template.HTML(b.String()),
		Session:    s.sessionView(r),
		IsArticle:  true,
		Exists:     pg.ID != 0,
		TalkExists: talkPage.ID != 0,
		Subject:    subj,
		Talk:       talk,
		RevID:      pg.LatestRevID,
		PrintURL:   s.publicURL(r, t.DBKey),
	})
}

func (s *Site) showDiff(w http.ResponseWriter, r *http.Request, t title.T) {
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil || pg.ID == 0 {
		s.fail(w, r, http.StatusNotFound, "That page has no revisions to compare.")
		return
	}
	newID := atoi64(r.URL.Query().Get("diff"))
	oldID := atoi64(r.URL.Query().Get("oldid"))
	newRev, err := s.Store.ResolveOldID(pg.ID, newID)
	if err != nil || newRev.ID == 0 || newRev.PageID != pg.ID {
		s.fail(w, r, http.StatusNotFound, "The newer revision was not found.")
		return
	}
	if oldID == 0 {
		oldID = newRev.ParentID
	}
	oldRev, _ := s.Store.ResolveOldID(pg.ID, oldID)
	oldText, newText := "", newRev.Content
	if oldRev.ID != 0 && oldRev.PageID == pg.ID {
		oldText = oldRev.Content
	}
	var b strings.Builder
	b.WriteString(`<p>Comparing revision ` + strconv.FormatInt(oldRev.ID, 10) + ` and revision ` + strconv.FormatInt(newRev.ID, 10) + `.</p>`)
	b.WriteString(diffTable(oldText, newText))
	subj, talk := subjectTalk(t, s.Cfg.ProjectNS)
	talkPage, _ := s.Store.PageByDBKey(talk.DBKey)
	s.writePage(w, r, pageView{
		Heading:    "Difference between revisions of " + t.Display,
		Action:     "history",
		DBKey:      t.DBKey,
		Namespace:  t.Namespace,
		Body:       template.HTML(b.String()),
		Session:    s.sessionView(r),
		IsArticle:  true,
		Exists:     true,
		TalkExists: talkPage.ID != 0,
		Subject:    subj,
		Talk:       talk,
		RevID:      newRev.ID,
		LastMod:    newRev.Timestamp,
		PrintURL:   s.publicURL(r, t.DBKey),
	})
}

func (s *Site) info(w http.ResponseWriter, r *http.Request, t title.T) {
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not read that page.")
		return
	}
	var b strings.Builder
	b.WriteString(`<table class="wikitable">`)
	row := func(k, v string) {
		b.WriteString(`<tr><th>` + esc(k) + `</th><td>` + v + `</td></tr>`)
	}
	row("Title", esc(t.Display))
	row("Exists", map[bool]string{true: "yes", false: "no"}[pg.ID != 0])
	if pg.ID != 0 {
		rev, _ := s.Store.LatestRevision(pg.ID)
		revs, _ := s.Store.Revisions(pg.ID)
		row("Latest revision", strconv.FormatInt(pg.LatestRevID, 10))
		row("Revisions", strconv.Itoa(len(revs)))
		row("Size", strconv.Itoa(len(rev.Content))+" bytes")
		row("Last edited", esc(s.fmtTime(rev.Timestamp))+" by "+esc(rev.UserText))
		if pg.IsRedirect {
			row("Redirect", esc(pg.RedirectTarget))
		}
	}
	b.WriteString(`</table>`)
	subj, talk := subjectTalk(t, s.Cfg.ProjectNS)
	talkPage, _ := s.Store.PageByDBKey(talk.DBKey)
	s.writePage(w, r, pageView{
		Heading:    "Information for " + t.Display,
		Action:     "info",
		DBKey:      t.DBKey,
		Namespace:  t.Namespace,
		Body:       template.HTML(b.String()),
		Session:    s.sessionView(r),
		IsArticle:  true,
		Exists:     pg.ID != 0,
		TalkExists: talkPage.ID != 0,
		Subject:    subj,
		Talk:       talk,
		RevID:      pg.LatestRevID,
		PrintURL:   s.publicURL(r, t.DBKey),
	})
}

func (s *Site) special(w http.ResponseWriter, r *http.Request, t title.T) {
	name, arg := splitSpecial(t.Rest)
	if arg == "" {
		arg = r.URL.Query().Get("target")
	}
	switch name {
	case "userlogin", "login":
		s.handleLogin(w, r)
	case "userlogout", "logout":
		s.handleLogout(w, r)
	case "preferences":
		s.handlePreferences(w, r)
	case "search":
		s.handleSearch(w, r)
	case "random", "randompage":
		s.handleRandom(w, r)
	case "recentchanges":
		s.handleRecent(w, r, "")
	case "recentchangeslinked", "relatedchanges":
		s.handleRecent(w, r, arg)
	case "allpages":
		s.handleAllPages(w, r)
	case "categories":
		s.handleCategories(w, r)
	case "whatlinkshere":
		s.handleWhatLinks(w, r, arg)
	case "upload":
		s.handleUpload(w, r)
	case "listfiles", "filelist", "imagelist":
		s.handleListFiles(w, r)
	case "citethispage", "cite":
		s.handleCite(w, r)
	case "specialpages":
		s.handleSpecialList(w, r)
	case "prefixindex":
		s.handlePrefixIndex(w, r)
	case "newpages":
		s.handleNewPages(w, r)
	case "newfiles", "newimages":
		s.handleNewFiles(w, r)
	case "listusers":
		s.handleListUsers(w, r)
	case "changepassword":
		s.handleChangePassword(w, r)
	case "uncategorizedpages":
		s.handleUncategorized(w, r)
	case "wantedpages":
		s.handleWanted(w, r)
	case "lonelypages":
		s.handleLonely(w, r)
	case "listredirects":
		s.handleRedirects(w, r)
	case "contributions":
		s.handleContributions(w, r, arg)
	case "statistics":
		s.handleStatistics(w, r)
	case "botpasswords":
		s.handleBotPasswords(w, r)
	case "createaccount":
		s.handleCreateAccount(w, r)
	case "version", "wikiwright":
		s.handleWikiwright(w, r)
	case "appearance":
		s.handleAppearance(w, r)
	default:
		s.writeSpecial(w, r, "Unknown special page", `<p>Wikiwright does not have a special page named `+esc(t.Rest)+`.</p><p>See <a href="`+esc(s.pageURL("Special:SpecialPages"))+`">Special pages</a>.</p>`)
	}
}

func (s *Site) writeSpecial(w http.ResponseWriter, r *http.Request, heading, body string) {
	s.writePage(w, r, pageView{
		Heading:     heading,
		Action:      "view",
		DBKey:       "Special:" + strings.ReplaceAll(heading, " ", "_"),
		Namespace:   title.NSSpecial,
		Body:        template.HTML(body),
		Session:     s.sessionView(r),
		SearchQuery: r.URL.Query().Get("search"),
		PrintURL:    s.publicURL(r, "Special:"+heading),
	})
}

func splitSpecial(rest string) (name, arg string) {
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, "/"); i >= 0 {
		arg = strings.TrimSpace(rest[i+1:])
		rest = rest[:i]
	}
	name = strings.ToLower(rest)
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "_", "")
	return name, arg
}

func (s *Site) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("search"))
	if q == "" {
		q = strings.TrimSpace(r.FormValue("search"))
	}
	goBtn := r.URL.Query().Get("go") != "" || r.FormValue("go") != ""
	if q != "" && goBtn {
		t := title.Parse(q, s.Cfg.ProjectNS)
		if t.Namespace == title.NSSpecial || t.Legal() {
			if t.Namespace == title.NSSpecial {
				http.Redirect(w, r, s.pageURL(t.DBKey), http.StatusFound)
				return
			}
			pg, err := s.Store.PageByDBKey(t.DBKey)
			if err == nil && pg.ID != 0 {
				http.Redirect(w, r, s.pageURL(t.DBKey), http.StatusFound)
				return
			}
		}
	}
	var b strings.Builder
	b.WriteString(`<form method="get" action="` + esc(s.prefix()+"/index.php") + `">`)
	b.WriteString(`<input type="hidden" name="title" value="Special:Search">`)
	b.WriteString(`<input type="search" name="search" value="` + esc(q) + `" size="40"> `)
	b.WriteString(`<button type="submit" name="fulltext" value="1">Search</button></form>`)
	if q != "" {
		hits, err := s.Store.Search(q, 50)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Search failed.")
			return
		}
		if len(hits) == 0 {
			b.WriteString(`<p>No pages matched.</p>`)
		}
		for _, h := range hits {
			label := h.Page.Title
			if h.Page.Namespace != title.NSMain {
				label = title.Canonical(h.Page.Namespace, s.Cfg.ProjectNS) + ":" + h.Page.Title
			}
			b.WriteString(`<div class="wikiwright-search-hit"><a href="` + esc(s.pageURL(h.Page.DBKey)) + `">` + esc(label) + `</a>`)
			if h.Snippet != "" {
				b.WriteString(`<div class="wikiwright-muted">` + esc(h.Snippet) + `</div>`)
			}
			b.WriteString(`</div>`)
		}
	}
	s.writePage(w, r, pageView{
		Heading:     "Search",
		DBKey:       "Special:Search",
		Namespace:   title.NSSpecial,
		Body:        template.HTML(b.String()),
		Session:     s.sessionView(r),
		SearchQuery: q,
		PrintURL:    s.publicURL(r, "Special:Search"),
	})
}

func (s *Site) handleRandom(w http.ResponseWriter, r *http.Request) {
	pg, err := s.Store.RandomArticle()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not choose a page.")
		return
	}
	if pg.ID == 0 {
		s.writeSpecial(w, r, "Random page", `<p>This wiki has no articles yet.</p>`)
		return
	}
	http.Redirect(w, r, s.pageURL(pg.DBKey), http.StatusFound)
}

func (s *Site) handleRecent(w http.ResponseWriter, r *http.Request, linked string) {
	limit := 50
	rows, err := s.Store.RecentChanges(200)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not read recent changes.")
		return
	}
	heading := "Recent changes"
	allow := map[string]bool{}
	if linked != "" {
		heading = "Related changes"
		t := title.Parse(linked, s.Cfg.ProjectNS)
		out, err := s.Store.PageByDBKey(t.DBKey)
		if err == nil && out.ID != 0 {
			keys, _ := s.Store.Outgoing(out.ID)
			for _, k := range keys {
				allow[k] = true
			}
			allow[t.DBKey] = true
		}
	}
	var b strings.Builder
	b.WriteString(`<ul class="wikiwright-plain">`)
	n := 0
	for _, rc := range rows {
		if linked != "" && !allow[rc.DBKey] {
			continue
		}
		label := rc.Title
		if rc.NS != title.NSMain {
			label = title.Canonical(rc.NS, s.Cfg.ProjectNS) + ":" + rc.Title
		}
		b.WriteString(`<li>`)
		if rc.Minor {
			b.WriteString(`<span class="minoredit">m</span> `)
		}
		b.WriteString(esc(s.fmtTime(rc.Timestamp)) + ` <a href="` + esc(s.pageURL(rc.DBKey)) + `">` + esc(label) + `</a>`)
		b.WriteString(` · ` + esc(rc.UserText))
		if rc.Comment != "" {
			b.WriteString(` <span class="wikiwright-muted">(` + esc(rc.Comment) + `)</span>`)
		}
		b.WriteString(`</li>`)
		n++
		if n >= limit {
			break
		}
	}
	b.WriteString(`</ul>`)
	if n == 0 {
		b.Reset()
		b.WriteString(`<p>No changes yet.</p>`)
	}
	s.writeSpecial(w, r, heading, b.String())
}

func (s *Site) handleAllPages(w http.ResponseWriter, r *http.Request) {
	nsParam := r.URL.Query().Get("namespace")
	var pages []store.Page
	var err error
	if nsParam == "" {
		pages, err = s.Store.AllPages()
	} else {
		ns, convErr := strconv.Atoi(nsParam)
		if convErr != nil {
			s.fail(w, r, http.StatusBadRequest, "Unknown namespace.")
			return
		}
		pages, err = s.Store.PagesInNamespace(ns)
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list pages.")
		return
	}
	var b strings.Builder
	b.WriteString(s.namespaceFilter("Special:AllPages", nsParam))
	curNS := -999
	for _, p := range pages {
		if p.Namespace != curNS {
			if curNS != -999 {
				b.WriteString(`</ul>`)
			}
			name := title.Canonical(p.Namespace, s.Cfg.ProjectNS)
			if name == "" {
				name = "(Articles)"
			}
			b.WriteString(`<h2>` + esc(name) + `</h2><ul class="wikiwright-plain">`)
			curNS = p.Namespace
		}
		cls := ""
		if p.IsRedirect {
			cls = ` class="mw-redirect"`
		}
		b.WriteString(`<li><a` + cls + ` href="` + esc(s.pageURL(p.DBKey)) + `">` + esc(p.Title) + `</a></li>`)
	}
	if curNS != -999 {
		b.WriteString(`</ul>`)
	}
	if len(pages) == 0 {
		b.WriteString(`<p>This wiki has no pages yet.</p>`)
	}
	s.writeSpecial(w, r, "All pages", b.String())
}

func (s *Site) handleCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.Store.CategoryCounts()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list categories.")
		return
	}
	var b strings.Builder
	if len(cats) == 0 {
		b.WriteString(`<p>No categories yet.</p>`)
	} else {
		b.WriteString(`<ul class="wikiwright-plain">`)
		for _, c := range cats {
			b.WriteString(`<li>` + s.categoryAnchor(c.Name, "", false) + ` <span class="wikiwright-muted">(` + strconv.Itoa(c.Count) + `)</span></li>`)
		}
		b.WriteString(`</ul>`)
	}
	s.writeSpecial(w, r, "Categories", b.String())
}

func (s *Site) handleWhatLinks(w http.ResponseWriter, r *http.Request, arg string) {
	if arg == "" {
		arg = r.URL.Query().Get("target")
	}
	var b strings.Builder
	b.WriteString(`<form method="get" action="` + esc(s.prefix()+"/index.php") + `"><input type="hidden" name="title" value="Special:WhatLinksHere">`)
	b.WriteString(`<label>Page <input type="text" name="target" value="` + esc(arg) + `"></label> <button type="submit">Go</button></form>`)
	if arg != "" {
		t := title.Parse(arg, s.Cfg.ProjectNS)
		pages, err := s.Store.WhatLinksHere(t.DBKey)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not list links.")
			return
		}
		b.WriteString(`<h2>Links to ` + esc(t.Display) + `</h2>`)
		if len(pages) == 0 {
			b.WriteString(`<p>No pages link here.</p>`)
		} else {
			b.WriteString(`<ul class="wikiwright-plain">`)
			for _, p := range pages {
				label := p.Title
				if p.Namespace != title.NSMain {
					label = title.Canonical(p.Namespace, s.Cfg.ProjectNS) + ":" + p.Title
				}
				b.WriteString(`<li><a href="` + esc(s.pageURL(p.DBKey)) + `">` + esc(label) + `</a></li>`)
			}
			b.WriteString(`</ul>`)
		}
	}
	s.writeSpecial(w, r, "What links here", b.String())
}

func (s *Site) handleUpload(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	if sess.User.ID == 0 || sess.NeedsTOTP {
		s.writeSpecial(w, r, "Upload file", `<p><a href="`+esc(s.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape("Special:Upload"))+`">Log in</a> to upload a file. `+esc(s.uploadAcceptLabel())+`. Up to `+esc(s.uploadLimitLabel())+`.</p>`)
		return
	}
	if !sess.Can("upload") {
		s.fail(w, r, http.StatusForbidden, "This bot password cannot upload files.")
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxUpload+1<<20)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			s.fail(w, r, http.StatusBadRequest, "The upload could not be read, or it is larger than the limit.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		fh, hdr, err := r.FormFile("wpUploadFile")
		if err != nil {
			s.writeSpecial(w, r, "Upload file", `<div class="wikiwright-error">Choose a file to upload.</div>`+string(s.uploadForm(sess, r.FormValue("wpDestFile"), "")))
			return
		}
		defer fh.Close()
		name := strings.TrimSpace(r.FormValue("wpDestFile"))
		if name == "" {
			name = hdr.Filename
		}
		name = ingest.CleanName(name)
		desc := r.FormValue("wpUploadDescription")
		if name == "" || !uploadExtAllowed(name, s.uploadTypes()) {
			msg := "That file type is not allowed. Allowed types: " + s.uploadAcceptLabel() + "."
			s.writeSpecial(w, r, "Upload file", `<div class="wikiwright-error">`+esc(msg)+`</div>`+string(s.uploadForm(sess, r.FormValue("wpDestFile"), "")))
			return
		}
		exists, err := s.uploadTargetExists(name)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not check whether that file already exists.")
			return
		}
		if exists && r.FormValue("wpIgnoreWarning") == "" {
			warn := "A file named " + name + " is already on this wiki. Choose the file again and check the box to replace it. The File page keeps the new revision."
			s.writeSpecial(w, r, "Upload file", string(s.uploadForm(sess, name, warn)))
			return
		}
		var rec store.FileRec
		body := io.LimitReader(fh, s.Cfg.MaxUpload+1)
		if uploadAllowsSVG(s.uploadTypes()) && strings.EqualFold(filepath.Ext(name), ".svg") {
			rec, err = ingest.SaveImported(s.Store, s.Cfg.Files, name, body, s.Cfg.MaxUpload, sess.User.Name, desc)
		} else {
			rec, err = ingest.SaveUpload(s.Store, s.Cfg.Files, name, body, s.Cfg.MaxUpload, sess.User.Name, desc)
		}
		if err != nil {
			s.writeSpecial(w, r, "Upload file", `<div class="wikiwright-error">`+esc(err.Error())+`</div>`+string(s.uploadForm(sess, name, "")))
			return
		}
		t := title.Parse("File:"+rec.Name, s.Cfg.ProjectNS)
		if err := s.recordUpload(t, sess.User.Name, rec.Name, desc); err != nil {
			s.writeSpecial(w, r, "Upload file", `<div class="wikiwright-error">`+esc(err.Error())+`</div>`+string(s.uploadForm(sess, rec.Name, "")))
			return
		}
		http.Redirect(w, r, s.pageURL(t.DBKey), http.StatusSeeOther)
		return
	}
	s.writeSpecial(w, r, "Upload file", string(s.uploadForm(sess, "", "")))
}

func (s *Site) recordUpload(t title.T, user, name, desc string) error {
	summary := strings.TrimSpace(desc)
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	comment := summary
	if len(comment) > 500 {
		comment = comment[:500]
	}
	pg, err := s.Store.PageByDBKey(t.DBKey)
	if err != nil {
		return err
	}
	var content string
	parent := int64(0)
	if pg.ID == 0 {
		body := summary
		if body == "" {
			body = "Uploaded file."
		}
		content = "== Summary ==\n\n" + body
		if comment == "" {
			comment = "Upload " + name
		}
	} else {
		parent = pg.LatestRevID
		if summary == "" {
			existing, _, err := s.Store.ContentByDBKey(t.DBKey)
			if err != nil {
				return err
			}
			content = existing
			comment = "Re-uploaded " + name
		} else {
			content = "== Summary ==\n\n" + summary
		}
	}
	_, err = s.save(t, user, comment, content, false, parent)
	return err
}

func (s *Site) uploadTargetExists(name string) (bool, error) {
	f, err := s.Store.File(name)
	if err != nil {
		return false, err
	}
	if f.Name != "" {
		return true, nil
	}
	disk := filepath.Join(s.Cfg.Files, ingest.CleanName(name))
	fi, err := os.Stat(disk)
	if err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		return true, nil
	}
	return false, nil
}

func (s *Site) uploadTypes() []string {
	if len(s.Cfg.UploadTypes) == 0 {
		return []string{"png", "jpg", "jpeg", "gif", "webp", "pdf"}
	}
	return s.Cfg.UploadTypes
}

func (s *Site) uploadAcceptLabel() string {
	var parts []string
	seen := map[string]bool{}
	for _, raw := range s.uploadTypes() {
		ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
		if ext == "" {
			continue
		}
		label := ext
		if ext == "jpeg" {
			label = "jpg"
		}
		if seen[label] {
			continue
		}
		seen[label] = true
		parts = append(parts, strings.ToUpper(label))
	}
	if len(parts) == 0 {
		return "No file types are allowed"
	}
	return strings.Join(parts, ", ")
}

func (s *Site) uploadLimitLabel() string {
	n := s.Cfg.MaxUpload
	if n >= 1<<20 && n%(1<<20) == 0 {
		return strconv.FormatInt(n>>20, 10) + " MB"
	}
	if n >= 1<<10 {
		kb := n / (1 << 10)
		if n%(1<<10) != 0 {
			kb++
		}
		return strconv.FormatInt(kb, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func uploadExtAllowed(name string, types []string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "jpeg" {
		ext = "jpg"
	}
	for _, raw := range types {
		t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
		if t == "jpeg" {
			t = "jpg"
		}
		if t != "" && t == ext {
			return true
		}
	}
	return false
}

func uploadAllowsSVG(types []string) bool {
	for _, raw := range types {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(raw), "."), "svg") {
			return true
		}
	}
	return false
}

func (s *Site) uploadForm(sess store.Session, dest, warning string) template.HTML {
	var b strings.Builder
	b.WriteString(`<p>` + esc(s.uploadAcceptLabel()) + `. Up to ` + esc(s.uploadLimitLabel()) + `.</p>`)
	if warning != "" {
		b.WriteString(`<div class="wikiwright-notice">` + esc(warning) + `</div>`)
	}
	b.WriteString(`<form class="wikiwright-login" method="post" enctype="multipart/form-data" action="` + esc(s.pageURL("Special:Upload")) + `">`)
	b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	b.WriteString(`<label>File <input type="file" name="wpUploadFile" required></label>`)
	b.WriteString(`<label>Destination file name <input type="text" name="wpDestFile" value="` + esc(dest) + `" placeholder="optional, defaults to the uploaded name"></label>`)
	b.WriteString(`<label>Summary <textarea name="wpUploadDescription" rows="4"></textarea></label>`)
	if warning != "" {
		b.WriteString(`<label><input type="checkbox" name="wpIgnoreWarning" value="1"> Replace the file that is already there</label>`)
	}
	b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Upload</button></div></form>`)
	return template.HTML(b.String())
}

func (s *Site) handleListFiles(w http.ResponseWriter, r *http.Request) {
	files, err := s.Store.AllFiles()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list files.")
		return
	}
	if len(files) == 0 {
		s.writeSpecial(w, r, "File list", `<p>No files have been uploaded.</p>`)
		return
	}
	var b strings.Builder
	b.WriteString(`<table class="wikitable"><thead><tr><th>File</th><th>Size</th><th>Type</th><th>Uploader</th></tr></thead><tbody>`)
	for _, f := range files {
		page := s.pageURL("File:" + f.Name)
		raw := s.prefix() + "/images/" + url.PathEscape(f.Name)
		b.WriteString(`<tr><td><a href="` + esc(page) + `">` + esc(f.Name) + `</a> <span class="wikiwright-muted">(<a href="` + esc(raw) + `">file</a>)</span></td>`)
		b.WriteString(`<td>` + esc(formatBytes(f.Size)) + `</td><td>` + esc(f.MIME) + `</td><td>` + esc(f.UploadedBy) + `</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)
	s.writeSpecial(w, r, "File list", b.String())
}

func formatBytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	if n < 1<<20 {
		return strconv.FormatInt(n/1024, 10) + " KB"
	}
	return strconv.FormatInt(n/(1<<20), 10) + " MB"
}

func (s *Site) handleCite(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("page")
	if page == "" {
		page = s.Cfg.MainPage
	}
	t := title.Parse(page, s.Cfg.ProjectNS)
	when := time.Now().In(s.Loc).Format("2 January 2006")
	u := s.publicURL(r, t.DBKey)
	cite := t.Display + ". " + s.Cfg.Name + ", retrieved " + when + ". " + u
	body := `<p>Cite this page:</p><pre>` + esc(cite) + `</pre>`
	s.writeSpecial(w, r, "Cite this page", body)
}

func (s *Site) handleSpecialList(w http.ResponseWriter, r *http.Request) {
	groups := []struct {
		heading string
		links   []struct{ title, note string }
	}{
		{"Lists of pages", []struct{ title, note string }{
			{"Special:AllPages", "Every page, with a namespace filter"},
			{"Special:PrefixIndex", "Pages whose titles start with a prefix"},
			{"Special:Categories", "Categories and how many pages they hold"},
			{"Special:UncategorizedPages", "Articles that are not in a category"},
			{"Special:WantedPages", "Titles that are linked but do not exist"},
			{"Special:LonelyPages", "Articles with no incoming links"},
			{"Special:ListRedirects", "Redirect pages"},
			{"Special:NewPages", "Newest pages"},
		}},
		{"Changes and search", []struct{ title, note string }{
			{"Special:RecentChanges", "Latest edits"},
			{"Special:RecentChangesLinked", "Recent changes in pages linked from a title"},
			{"Special:Search", "Search page text"},
			{"Special:Random", "A random article"},
			{"Special:WhatLinksHere", "Pages that link to a title"},
			{"Special:Contributions", "Edits by one account"},
		}},
		{"Users", []struct{ title, note string }{
			{"Special:CreateAccount", "An administrator creates an account"},
			{"Special:ListUsers", "Accounts on this wiki"},
			{"Special:BotPasswords", "Named passwords for scripts"},
			{"Special:Preferences", "Password and authenticator"},
			{"Special:ChangePassword", "Change your password"},
			{"Special:UserLogin", "Log in"},
		}},
		{"Files", []struct{ title, note string }{
			{"Special:Upload", "Upload a file"},
			{"Special:ListFiles", "Files that have been uploaded"},
			{"Special:NewFiles", "Newest files"},
		}},
		{"About this wiki", []struct{ title, note string }{
			{"Special:Appearance", "Accent color for this wiki"},
			{"Special:Statistics", "Page, article, and file counts"},
			{"Special:Wikiwright", "About Wikiwright and the version running here"},
			{"Special:Version", "Opens the About Wikiwright page"},
			{"Special:CiteThisPage", "A citation for a page"},
		}},
	}
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(`<h2>` + esc(g.heading) + `</h2><ul class="wikiwright-plain">`)
		for _, l := range g.links {
			b.WriteString(`<li><a href="` + esc(s.pageURL(l.title)) + `">` + esc(strings.TrimPrefix(l.title, "Special:")) + `</a> <span class="wikiwright-muted">` + esc(l.note) + `</span></li>`)
		}
		b.WriteString(`</ul>`)
	}
	s.writeSpecial(w, r, "Special pages", b.String())
}

func (s *Site) handleWikiwright(w http.ResponseWriter, r *http.Request) {
	appearance := esc(s.pageURL("Special:Appearance"))
	body := `<p>Wikiwright is a lightweight wiki engine that runs as a single program. It reads and writes MediaWiki-style wikitext, and it stores each wiki in its own SQLite file.</p>` +
		`<p>This copy is Wikiwright ` + esc(Version) + `.</p>` +
		`<p>Wikiwright is available under the MIT license.</p>` +
		`<p>Developed using GrokBot.</p>` +
		`<p>Copyright 2026 Katherine W. Prawl</p>` +
		`<p>The source is at <a class="external" rel="nofollow" href="https://github.com/BlancoKat/wikiwright">github.com/BlancoKat/wikiwright</a>.</p>` +
		`<h2>What you can do here</h2>` +
		`<p>Pages use common wikitext. Templates, categories, and images work. An administrator can choose this wiki's accent color on <a href="` + appearance + `">Appearance</a>. Scripts can sign in with bot passwords and use a MediaWiki-compatible api.php subset.</p>` +
		`<h2>Help with editing</h2>` +
		`<p>The markup is the same as MediaWiki. For how to format a page, see <a class="external" rel="nofollow" href="https://www.mediawiki.org/wiki/Help:Formatting">Help:Formatting</a> and <a class="external" rel="nofollow" href="https://www.mediawiki.org/wiki/Help:Contents">Help:Contents</a>.</p>`
	s.writeSpecial(w, r, "About Wikiwright", body)
}

func diffTable(oldText, newText string) string {
	ops := diff.Lines(oldText, newText)
	var b strings.Builder
	b.WriteString(`<table class="diff">`)
	for _, op := range ops {
		switch op.Kind {
		case diff.Equal:
			b.WriteString(`<tr><td class="diff-marker"></td><td>` + esc(op.Text) + `</td><td class="diff-marker"></td><td>` + esc(op.Text) + `</td></tr>`)
		case diff.Delete:
			b.WriteString(`<tr><td class="diff-marker">−</td><td class="diff-deletedline">` + esc(op.Text) + `</td><td class="diff-marker"></td><td></td></tr>`)
		case diff.Insert:
			b.WriteString(`<tr><td class="diff-marker"></td><td></td><td class="diff-marker">+</td><td class="diff-addedline">` + esc(op.Text) + `</td></tr>`)
		}
	}
	b.WriteString(`</table>`)
	return b.String()
}

func (s *Site) canEdit(r *http.Request) bool {
	sess := s.sessionView(r)
	return sess.User.ID != 0 && !sess.NeedsTOTP
}

func (s *Site) parse(pageTitle, src string, sectionEdits bool) parser.Result {
	keys, err := s.Store.DBKeys()
	if err != nil {
		keys = map[string]bool{}
	}
	pages, articles, files, _ := s.Store.Counts()
	return parser.Parse(src, parser.Options{
		PageTitle:    pageTitle,
		ProjectNS:    s.Cfg.ProjectNS,
		Now:          time.Now().In(s.Loc),
		Counts:       parser.Counts{Pages: pages, Articles: articles, Files: files},
		SectionEdits: sectionEdits,
		GetTemplate: func(dbkey string) (string, bool) {
			body, ok, err := s.Store.ContentByDBKey(dbkey)
			if err != nil || !ok {
				return "", false
			}
			return body, true
		},
		Exists: func(dbkey string) bool { return keys[dbkey] },
		File: func(name string) (parser.FileInfo, bool) {
			f, err := s.Store.File(name)
			if err != nil || f.Name == "" {
				return parser.FileInfo{}, false
			}
			return parser.FileInfo{Width: f.Width, Height: f.Height, MIME: f.MIME}, true
		},
		Link: func(dbkey string) string {
			return s.prefix() + "/index.php?title=" + title.URLQuery(dbkey)
		},
		RawFile: func(name string) string {
			return s.prefix() + "/images/" + url.PathEscape(name)
		},
	})
}

func (s *Site) publicURL(r *http.Request, dbkey string) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + s.pageURL(dbkey)
}

func subjectTalk(t title.T, project string) (subject, talk title.T) {
	if t.Namespace < 0 {
		return t, t
	}
	ns := t.Namespace
	rest := t.Rest
	if ns%2 == 1 {
		talk = t
		if ns-1 == title.NSMain {
			subject = title.Parse(rest, project)
		} else {
			subject = title.Parse(title.Canonical(ns-1, project)+":"+rest, project)
		}
		return subject, talk
	}
	subject = t
	talk = title.Parse(title.Canonical(ns+1, project)+":"+rest, project)
	return subject, talk
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
