package web

import (
	"embed"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

//go:embed static templates
var assets embed.FS

var layoutTmpl = template.Must(template.ParseFS(assets, "templates/layout.html"))

const defaultSidebar = `* navigation
** mainpage|mainpage-description
** recentchanges-url|recentchanges
** randompage-url|randompage
** Special:Wikiwright|About Wikiwright
* SEARCH
* TOOLBOX
`

var mwMessages = map[string]string{
	"mainpage-description": "Main page",
	"help-mediawiki":       "About Wikiwright",
	"recentchanges":        "Recent changes",
	"randompage":           "Random page",
	"upload":               "Upload file",
	"specialpages":         "Special pages",
	"currentevents":        "Current events",
	"portal":               "Community portal",
	"sitesupport":          "Donations",
}

type pageView struct {
	Status      int
	Heading     string
	Action      string
	DBKey       string
	Namespace   int
	ContentSub  template.HTML
	Notices     template.HTML
	Body        template.HTML
	Categories  []string
	Session     store.Session
	IsArticle   bool
	Exists      bool
	TalkExists  bool
	Subject     title.T
	Talk        title.T
	RevID       int64
	LastMod     time.Time
	SearchQuery string
	PrintURL    string
}

type layoutData struct {
	HTMLTitle         string
	BodyClass         string
	HeadExtra         template.HTML
	Personal          template.HTML
	BannerClass       string
	SiteName          string
	MainHref          string
	SearchAction      string
	SearchPlaceholder string
	SearchQuery       string
	Namespaces        template.HTML
	More              template.HTML
	Views             template.HTML
	Heading           string
	SiteSub           string
	ContentSub        template.HTML
	Notices           template.HTML
	Body              template.HTML
	Catlinks          template.HTML
	Logo              template.HTML
	Sidebar           template.HTML
	Toolbox           template.HTML
	PageTools         template.HTML
	CatSidebar        template.HTML
	Footer            template.HTML
	PrintFooter       string
	Stylesheet        string
}

func esc(s string) string { return html.EscapeString(s) }

func (s *Site) writePage(w http.ResponseWriter, r *http.Request, pv pageView) {
	if pv.Heading == "" {
		pv.Heading = s.Cfg.Name
	}
	if pv.Action == "" {
		pv.Action = "view"
	}
	data := layoutData{
		HTMLTitle:         pv.Heading + " - " + s.Cfg.Name,
		BodyClass:         s.bodyClass(pv),
		HeadExtra:         template.HTML(s.accentCSS()),
		Personal:          s.personalHTML(pv),
		BannerClass:       "mw-wiki-title",
		SiteName:          s.Cfg.Name,
		MainHref:          s.pageURL(s.Cfg.MainPage),
		SearchAction:      s.prefix() + "/index.php",
		SearchPlaceholder: "Search " + s.Cfg.Name,
		SearchQuery:       pv.SearchQuery,
		Namespaces:        s.namespacesHTML(pv),
		More:              s.moreHTML(pv),
		Views:             s.viewsHTML(pv),
		Heading:           pv.Heading,
		SiteSub:           "From " + s.Cfg.Name,
		ContentSub:        pv.ContentSub,
		Notices:           pv.Notices,
		Body:              pv.Body,
		Catlinks:          s.catlinksHTML(pv.Categories, pv.DBKey),
		Logo:              s.logoHTML(),
		Sidebar:           template.HTML(s.sidebarHTML()),
		Toolbox:           s.toolboxHTML(pv),
		PageTools:         s.pageToolsHTML(pv),
		CatSidebar:        s.catSidebarHTML(pv.Categories, pv.DBKey),
		Footer:            s.footerHTML(pv),
		PrintFooter:       "Retrieved from " + pv.PrintURL,
		Stylesheet:        staticURL("wikiwright.css"),
	}
	if len([]rune(s.Cfg.Name)) > 18 {
		data.BannerClass += " long"
	}
	status := pv.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := layoutTmpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		// Headers are already sent. Leave a short note in the log via the server.
		_, _ = w.Write([]byte("\n<!-- template: " + esc(err.Error()) + " -->"))
	}
}

func (s *Site) bodyClass(pv pageView) string {
	ns := pv.Namespace
	kind := "ns-subject"
	if ns >= 0 && ns%2 == 1 {
		kind = "ns-talk"
	}
	page := "page-" + cssToken(pv.DBKey)
	root := pv.DBKey
	if i := strings.Index(root, "/"); i >= 0 {
		root = root[:i]
	}
	return strings.Join([]string{
		"wikiwright-skin", "wikiwright-ltr",
		"ns-" + strconv.Itoa(ns), kind, page, "rootpage-" + cssToken(root),
		"action-" + cssToken(pv.Action),
	}, " ")
}

func cssToken(s string) string {
	if s == "" {
		return "Special"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (s *Site) personalHTML(pv pageView) template.HTML {
	var b strings.Builder
	logged := pv.Session.User.ID != 0 && !pv.Session.NeedsTOTP
	if logged {
		name := pv.Session.User.Name
		b.WriteString(`<a class="wikiwright-user-name" href="` + esc(s.pageURL("User:"+name)) + `">` + esc(name) + `</a>`)
		b.WriteString(`<a href="` + esc(s.pageURL("Special:Preferences")) + `">Preferences</a>`)
		logout := s.pageURL("Special:UserLogout") + "&token=" + url.QueryEscape(pv.Session.CSRF)
		b.WriteString(`<a href="` + esc(logout) + `">Log out</a>`)
	} else {
		ret := ""
		if pv.DBKey != "" {
			ret = "&returnto=" + url.QueryEscape(strings.ReplaceAll(pv.DBKey, "_", " "))
		}
		b.WriteString(`<a href="` + esc(s.pageURL("Special:UserLogin")+ret) + `" accesskey="o">Log in</a>`)
	}
	return template.HTML(b.String())
}

func (s *Site) namespacesHTML(pv pageView) template.HTML {
	if !pv.IsArticle {
		item := li("ca-nstab-special", "selected", aSpan(s.pageURL(pv.DBKey), "Special page", ""))
		return template.HTML(portlet("p-namespaces", "Namespaces", item, "tools-inline"))
	}
	subjClass, talkClass := "", ""
	if pv.Namespace%2 == 0 {
		subjClass = "selected"
	} else {
		talkClass = "selected"
	}
	if !pv.TalkExists && talkClass == "" {
		talkClass = "new"
	}
	if !pv.Exists && subjClass == "" {
		subjClass = "new"
	}
	label := nsTabLabel(pv.Subject.Namespace)
	subj := li("ca-nstab-"+nsTabID(pv.Subject.Namespace), subjClass, aSpan(s.pageURL(pv.Subject.DBKey), label, ` title="View the content page [c]" accesskey="c"`))
	talkHref := s.pageURL(pv.Talk.DBKey)
	if !pv.TalkExists {
		talkHref += "&action=edit&redlink=1"
		if pv.DBKey != "" && pv.DBKey != pv.Talk.DBKey {
			talkHref += "&returnto=" + title.URLQuery(pv.DBKey)
		}
	}
	talk := li("ca-talk", talkClass, aSpan(talkHref, "Discussion", ` rel="discussion" title="Discussion about the content page [t]" accesskey="t"`))
	return template.HTML(portlet("p-namespaces", "Namespaces", subj+talk, "tools-inline"))
}

func (s *Site) moreHTML(pv pageView) template.HTML {
	if !pv.IsArticle {
		return ""
	}
	item := `<li id="ca-more" class="dropdown-toggle"><span>More</span></li>`
	return template.HTML(portlet("p-more", "More", item, "tools-inline"))
}

func (s *Site) viewsHTML(pv pageView) template.HTML {
	if !pv.IsArticle {
		return ""
	}
	readClass, editClass, histClass := "", "", ""
	switch pv.Action {
	case "edit":
		editClass = "selected"
	case "history":
		histClass = "selected"
	default:
		readClass = "selected"
	}
	base := s.pageURL(pv.DBKey)
	read := li("ca-view", readClass, aSpan(base, "Read", ""))
	logged := pv.Session.User.ID != 0 && !pv.Session.NeedsTOTP
	var edit string
	if logged {
		edit = li("ca-edit", editClass, aSpan(base+"&action=edit", "Edit", ` title="Edit this page [e]" accesskey="e"`))
	} else {
		edit = li("ca-viewsource", editClass, aSpan(base+"&action=edit", "View source", ` title="This page is protected. You can view its source [e]" accesskey="e"`))
	}
	hist := li("ca-history", histClass, aSpan(base+"&action=history", "History", ` title="Past revisions of this page [h]" accesskey="h"`))
	return template.HTML(portlet("p-views", "Page actions", read+edit+hist, "tools-inline"))
}

func nsTabID(ns int) string {
	switch ns {
	case title.NSMain:
		return "main"
	case title.NSUser:
		return "user"
	case title.NSProject:
		return "project"
	case title.NSFile:
		return "image"
	case title.NSMediaWiki:
		return "mediawiki"
	case title.NSTemplate:
		return "template"
	case title.NSHelp:
		return "help"
	case title.NSCategory:
		return "category"
	default:
		return "subject"
	}
}

func nsTabLabel(ns int) string {
	switch ns {
	case title.NSMain:
		return "Page"
	case title.NSUser:
		return "User page"
	case title.NSProject:
		return "Project page"
	case title.NSFile:
		return "File"
	case title.NSMediaWiki:
		return "MediaWiki"
	case title.NSTemplate:
		return "Template"
	case title.NSHelp:
		return "Help"
	case title.NSCategory:
		return "Category"
	default:
		return "Page"
	}
}

const logoMark = `<svg class="wikiwright-mark" viewBox="0 0 64 64" aria-hidden="true"><rect x="8" y="12" width="48" height="40" rx="3" fill="none" stroke="currentColor" stroke-width="3"/><path d="M32 12v40M16 24h12M36 24h12M16 34h12M36 34h12" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"/></svg>`

func (s *Site) logoHTML() template.HTML {
	href := esc(s.pageURL(s.Cfg.MainPage))
	if s.logoFile() != "" {
		u := esc(s.prefix() + "/logo")
		return template.HTML(`<a class="wikiwright-logo-link" href="` + href + `" title="Visit the main page"><img src="` + u + `" alt=""></a>`)
	}
	return template.HTML(`<a class="wikiwright-logo-link" href="` + href + `" title="Visit the main page">` + logoMark + `</a>`)
}

func (s *Site) sidebarHTML() string {
	src, ok, err := s.Store.ContentByDBKey("MediaWiki:Sidebar")
	if err != nil || !ok || strings.TrimSpace(src) == "" {
		src = defaultSidebar
	}
	sections := parseSidebar(src)
	var ports strings.Builder
	for _, sec := range sections {
		kind := strings.ToUpper(strings.TrimSpace(sec.name))
		if kind == "SEARCH" || kind == "TOOLBOX" || kind == "LANGUAGES" {
			continue
		}
		var items string
		for _, it := range sec.items {
			href, ext := s.sidebarHref(it.target)
			label := s.sidebarLabel(it.label)
			extra := ""
			if ext {
				extra = ` class="external" rel="nofollow"`
			}
			id := "n-" + cssToken(it.label)
			items += li(id, "", `<a href="`+esc(href)+`"`+extra+`><span>`+esc(label)+`</span></a>`)
		}
		id := "wikiwright-" + cssToken(strings.ToLower(sec.name))
		if strings.EqualFold(sec.name, "navigation") {
			id = "wikiwright-navigation"
		}
		ports.WriteString(portlet(id, prettySection(sec.name), items, ""))
	}
	if ports.Len() == 0 {
		return ""
	}
	return `<div class="wikiwright-chunk">` + ports.String() + `</div>`
}

type sideItem struct{ target, label string }
type sideSection struct {
	name  string
	items []sideItem
}

func parseSidebar(src string) []sideSection {
	var out []sideSection
	var cur *sideSection
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "<!--"); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		stars := 0
		for stars < len(line) && line[stars] == '*' {
			stars++
		}
		if stars == 0 {
			continue
		}
		rest := strings.TrimSpace(line[stars:])
		if stars == 1 {
			out = append(out, sideSection{name: rest})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil || rest == "" {
			continue
		}
		target, label := rest, rest
		if i := strings.Index(rest, "|"); i >= 0 {
			target = strings.TrimSpace(rest[:i])
			label = strings.TrimSpace(rest[i+1:])
		}
		if target == "" {
			continue
		}
		if label == "" {
			label = target
		}
		cur.items = append(cur.items, sideItem{target, label})
	}
	return out
}

func prettySection(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	if name == "" {
		return "Navigation"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func (s *Site) sidebarLabel(label string) string {
	if msg, ok := mwMessages[strings.ToLower(label)]; ok {
		return msg
	}
	return strings.ReplaceAll(label, "_", " ")
}

func (s *Site) sidebarHref(target string) (string, bool) {
	low := strings.ToLower(strings.TrimSpace(target))
	switch low {
	case "mainpage":
		return s.pageURL(s.Cfg.MainPage), false
	case "helppage":
		return s.helpURL()
	case "recentchanges", "recentchanges-url":
		return s.pageURL("Special:RecentChanges"), false
	case "randompage", "randompage-url":
		return s.pageURL("Special:Random"), false
	case "upload", "upload-url":
		return s.pageURL("Special:Upload"), false
	case "specialpages":
		return s.pageURL("Special:SpecialPages"), false
	case "portal", "portal-url":
		return s.pageURL(s.Cfg.ProjectNS + ":Community portal"), false
	case "currentevents", "currentevents-url":
		return s.pageURL(s.Cfg.ProjectNS + ":Current events"), false
	case "sitesupport", "sitesupport-url":
		return s.pageURL(s.Cfg.ProjectNS + ":Site support"), false
	}
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return target, true
	}
	return s.pageURL(target), false
}

func (s *Site) helpURL() (string, bool) {
	body, ok, err := s.Store.ContentByDBKey("MediaWiki:Helppage")
	if err == nil && ok {
		t := strings.TrimSpace(body)
		if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
			return t, true
		}
		if t != "" {
			return s.pageURL(t), false
		}
	}
	return s.pageURL("Special:Wikiwright"), false
}

func (s *Site) toolboxHTML(pv pageView) template.HTML {
	items := li("t-upload", "", aSpan(s.pageURL("Special:Upload"), "Upload file", ` title="Upload files [u]" accesskey="u"`))
	items += li("t-specialpages", "", aSpan(s.pageURL("Special:SpecialPages"), "Special pages", ` title="A list of all special pages [q]" accesskey="q"`))
	if pv.IsArticle && pv.DBKey != "" {
		href := s.pageURL("Special:CiteThisPage") + "&page=" + url.QueryEscape(strings.ReplaceAll(pv.DBKey, "_", " "))
		if pv.RevID != 0 {
			href += "&id=" + strconv.FormatInt(pv.RevID, 10)
		}
		items += li("t-cite", "", aSpan(href, "Cite this page", ` title="Information on how to cite this page"`))
	}
	if pv.Session.User.ID != 0 && !pv.Session.NeedsTOTP {
		items += li("t-editsidebar", "", aSpan(s.pageURL("MediaWiki:Sidebar")+"&action=edit", "Edit sidebar", ` title="Edit the sidebar"`))
	}
	if appearanceAdmin(pv.Session) {
		items += li("t-appearance", "", aSpan(s.pageURL("Special:Appearance"), "Appearance", ` title="Accent color for this wiki"`))
	}
	return template.HTML(portlet("p-tb", "Wiki tools", items, ""))
}

func (s *Site) pageToolsHTML(pv pageView) template.HTML {
	var items string
	if pv.IsArticle && pv.DBKey != "" {
		disp := strings.ReplaceAll(pv.DBKey, "_", " ")
		items += li("t-whatlinkshere", "", aSpan(s.pageURL("Special:WhatLinksHere/"+disp), "What links here", ` title="A list of all wiki pages that link here [j]" accesskey="j"`))
		items += li("t-recentchangeslinked", "", aSpan(s.pageURL("Special:RecentChangesLinked/"+disp), "Related changes", ` rel="nofollow" title="Recent changes in pages linked from this page [k]" accesskey="k"`))
		items += li("t-print", "", aSpan(s.pageURL(pv.DBKey)+"&printable=yes", "Printable version", ` rel="alternate" title="Printable version of this page [p]" accesskey="p"`))
		if pv.RevID != 0 {
			items += li("t-permalink", "", aSpan(s.pageURL(pv.DBKey)+"&oldid="+strconv.FormatInt(pv.RevID, 10), "Permanent link", ` title="Permanent link to this revision of the page"`))
		}
		items += li("t-info", "", aSpan(s.pageURL(pv.DBKey)+"&action=info", "Page information", ` title="More information about this page"`))
	} else {
		items += li("t-mainpage", "", aSpan(s.pageURL(s.Cfg.MainPage), "Main page", ""))
		items += li("t-recentchanges", "", aSpan(s.pageURL("Special:RecentChanges"), "Recent changes", ""))
	}
	return template.HTML(portlet("wikiwright-page-tools", "This page", items, "tools-inline"))
}

func (s *Site) catlinksHTML(cats []string, from string) template.HTML {
	if len(cats) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div id="catlinks" class="catlinks" data-mw="interface"><div id="mw-normal-catlinks" class="mw-normal-catlinks"><a href="`)
	b.WriteString(esc(s.pageURL("Special:Categories")))
	b.WriteString(`" title="Special:Categories">Categories</a>: <ul>`)
	for _, c := range cats {
		b.WriteString(`<li>`)
		b.WriteString(s.categoryAnchor(c, from, false))
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul></div></div>`)
	return template.HTML(b.String())
}

func (s *Site) catSidebarHTML(cats []string, from string) template.HTML {
	if len(cats) == 0 {
		return ""
	}
	var items string
	for _, c := range cats {
		items += `<li>` + s.categoryAnchor(c, from, false) + `</li>`
	}
	return template.HTML(`<section class="wikiwright-chunk" id="wikiwright-categories"><h2>Categories</h2><ul class="wikiwright-plain">` + items + `</ul></section>`)
}

func (s *Site) footerHTML(pv pageView) template.HTML {
	var b strings.Builder
	if !pv.LastMod.IsZero() {
		b.WriteString(`<li id="lastmod">This page was last edited on ` + esc(s.fmtTime(pv.LastMod)) + `.</li>`)
	}
	b.WriteString(`<li id="copyright">`)
	b.WriteString(s.licenseHTML())
	b.WriteString(`</li>`)
	b.WriteString(`<li id="privacy"><a href="` + esc(s.pageURL(s.Cfg.ProjectNS+":Privacy policy")) + `">Privacy policy</a></li>`)
	b.WriteString(`<li id="about"><a href="` + esc(s.pageURL(s.Cfg.ProjectNS+":About")) + `">About ` + esc(s.Cfg.Name) + `</a></li>`)
	b.WriteString(`<li id="disclaimer"><a href="` + esc(s.pageURL(s.Cfg.ProjectNS+":General disclaimer")) + `">Disclaimers</a></li>`)
	b.WriteString(`<li id="poweredby"><a href="` + esc(s.pageURL("Special:Wikiwright")) + `">Powered by Wikiwright</a></li>`)
	return template.HTML(b.String())
}

func (s *Site) licenseHTML() string {
	text := strings.TrimSpace(s.Cfg.License)
	if text == "" {
		return "Content license is not set for this wiki."
	}
	if s.Cfg.LicenseURL != "" {
		return `Content is available under <a class="external" rel="nofollow" href="` + esc(s.Cfg.LicenseURL) + `">` + esc(text) + `</a> unless otherwise noted.`
	}
	return esc(text)
}

func (s *Site) fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.Loc).Format("2 January 2006, at 15:04")
}

func (s *Site) prefix() string { return s.Cfg.PathPrefix }

// categoryAnchor matches the usual wiki convention: a category with no category
// page is a red link to the edit URL, in the footer and in the sidebar.
func (s *Site) categoryAnchor(name, from string, span bool) string {
	t := title.Parse("Category:"+name, s.Cfg.ProjectNS)
	pg, err := s.Store.PageByDBKey(t.DBKey)
	exists := err == nil && pg.ID != 0
	href := s.pageURL(t.DBKey)
	class := ""
	titleAttr := t.Display
	if !exists {
		href += "&action=edit&redlink=1"
		if from != "" && from != t.DBKey {
			href += "&returnto=" + title.URLQuery(from)
		}
		class = ` class="new"`
		titleAttr += " (page does not exist)"
	}
	inner := esc(name)
	if span {
		inner = "<span>" + inner + "</span>"
	}
	return `<a href="` + esc(href) + `"` + class + ` title="` + esc(titleAttr) + `">` + inner + `</a>`
}

func (s *Site) pageURL(raw string) string {
	t := title.Parse(raw, s.Cfg.ProjectNS)
	key := t.DBKey
	if key == "" {
		key = strings.ReplaceAll(strings.TrimSpace(raw), " ", "_")
	}
	return s.prefix() + "/index.php?title=" + title.URLQuery(key)
}

func (s *Site) returnURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n\\") {
		return s.pageURL(s.Cfg.MainPage)
	}
	return s.pageURL(raw)
}

func portlet(id, label, lis, extra string) string {
	cls := "wikiwright-portlet"
	if extra != "" {
		cls += " " + extra
	}
	return `<div role="navigation" class="` + cls + `" id="` + id + `" aria-labelledby="` + id + `-label"><h3 id="` + id + `-label">` + esc(label) + `</h3><div class="wikiwright-portlet-body"><ul>` + lis + `</ul></div></div>`
}

func li(id, class, inner string) string {
	c := ""
	if class != "" {
		c = ` class="` + class + `"`
	}
	i := ""
	if id != "" {
		i = ` id="` + id + `"`
	}
	return `<li` + i + c + `>` + inner + `</li>`
}

func aSpan(href, text, extra string) string {
	return `<a href="` + esc(href) + `"` + extra + `><span>` + esc(text) + `</span></a>`
}

func warningsHTML(ws []string) template.HTML {
	if len(ws) == 0 {
		return ""
	}
	if len(ws) > 8 {
		ws = ws[:8]
	}
	var b strings.Builder
	b.WriteString(`<div class="wikiwright-notice">`)
	for i, w := range ws {
		if i > 0 {
			b.WriteString("<br>")
		}
		b.WriteString(esc(w))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func notice(class, msg string) template.HTML {
	return template.HTML(`<div class="` + class + `">` + msg + `</div>`)
}
