// Package parser renders a practical subset of MediaWiki wikitext: the
// constructs a small wiki actually needs, plus the safety net around them.
package parser

import (
	"fmt"
	"html"
	"log"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/title"
)

// FileInfo is what the renderer needs to size an image.
type FileInfo struct {
	Width, Height int
	MIME          string
}

// Counts are optional magic-word values.
type Counts struct {
	Pages, Articles, Files int
}

// Options supplies page context and callbacks. Nil callbacks are safe.
type Options struct {
	PageTitle   string
	ProjectNS   string
	GetTemplate func(dbkey string) (string, bool)
	Exists      func(dbkey string) bool
	File        func(name string) (FileInfo, bool)
	Link        func(dbkey string) string
	RawFile     func(name string) string
	Now         time.Time
	Counts      Counts
	// SectionEdits adds an [edit] link on each heading from this page.
	// Headings produced by templates never get one. __NOEDITSECTION__ hides them.
	SectionEdits bool
}

// Cat is a category placed on the page.
type Cat struct {
	Name    string
	SortKey string
}

// Result is the rendered page.
type Result struct {
	HTML             string
	Categories       []Cat
	Links            []string
	Warnings         []string
	Redirect         string
	RedirectFragment string
	IsRedirect       bool
}

type literal struct {
	kind               string
	text               string
	service, id, align string
	caption            string
	w, h               int
}

type head struct {
	level    int
	id       string
	plain    string
	number   string
	tocLevel int
}

type parser struct {
	opt           Options
	saved         map[string]literal
	seq           int
	result        Result
	stack         []string
	depth         int
	expands       int
	heads         []head
	ids           map[string]bool
	noTOC         bool
	forceTOC      bool
	noEditSection bool
	explicit      bool
	sectionN      int
	tplDepth      int
	extN          int
	seenLink      map[string]bool
}

const (
	tocMarker     = "\x00TOC\x00"
	tocAutoMarker = "\x00TOCAUTO\x00"
)

// Parse renders wikitext to HTML. The HTML is escaped except for tags this
// package itself emits. A panic while rendering one page is logged and
// returned as a warning; it does not escape this function.
func Parse(src string, opt Options) (res Result) {
	defer func() {
		if rec := recover(); rec != nil {
			res = panicResult(opt.PageTitle, rec)
		}
	}()
	res = parseSource(src, opt)
	return res
}

func panicResult(title string, rec any) Result {
	if title == "" {
		title = "(no title)"
	}
	log.Printf("parser: panic on %q: %v\n%s", title, rec, debug.Stack())
	return Result{
		HTML:     `<p class="wikiwright-warning">This page could not be rendered.</p>`,
		Warnings: []string{fmt.Sprintf("panic: %v", rec)},
	}
}

// parseHook is for tests that need Parse to survive a panic.
var parseHook func()

func parseSource(src string, opt Options) Result {
	if parseHook != nil {
		parseHook()
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now().UTC()
	}
	p := &parser{
		opt:      opt,
		saved:    map[string]literal{},
		ids:      map[string]bool{},
		seenLink: map[string]bool{},
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	src = p.protect(src)
	src = stripComments(src)
	src = stripForView(src)
	src = p.expand(src, nil, 0)
	// Templates may have introduced nowiki/pre.
	src = p.protect(src)
	src = p.extractMagic(src)
	if dbkey, frag, rest, ok := p.detectRedirect(src); ok {
		p.result.IsRedirect = true
		p.result.Redirect = dbkey
		p.result.RedirectFragment = frag
		// Keep parsing the remainder so categories on redirect pages are kept.
		body := p.renderBlocks(rest)
		_ = body
		p.result.HTML = p.redirectBox(dbkey, frag)
		return p.result
	}
	htmlOut := p.renderBlocks(src)
	htmlOut = p.placeTOC(htmlOut)
	p.result.HTML = htmlOut
	return p.result
}

func (p *parser) warn(msg string) {
	p.result.Warnings = append(p.result.Warnings, msg)
}

func (p *parser) token(lit literal) string {
	p.seq++
	k := "T" + strconv.Itoa(p.seq)
	p.saved[k] = lit
	return "\x00" + k + "\x00"
}

func (p *parser) protect(s string) string {
	re := regexp.MustCompile(`(?is)<(nowiki|pre)\b[^>]*>(.*?)</(?:nowiki|pre)>`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		sub := re.FindStringSubmatch(m)
		kind := strings.ToLower(sub[1])
		return p.token(literal{kind: kind, text: sub[2]})
	})
}

func stripComments(s string) string {
	re := regexp.MustCompile(`(?s)<!--.*?-->`)
	s = re.ReplaceAllString(s, "")
	// MediaWiki drops an unclosed <!-- through the end of the page.
	// Hill Country Makers writes the closer as --!>, which is not a closer.
	if i := strings.Index(s, "<!--"); i >= 0 {
		s = s[:i]
	}
	return s
}

func stripForView(s string) string {
	s = regexp.MustCompile(`(?is)<includeonly\b[^>]*>.*?</includeonly>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)</?noinclude\b[^>]*>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)</?onlyinclude\b[^>]*>`).ReplaceAllString(s, "")
	return s
}

func stripForTransclusion(s string) string {
	if regexp.MustCompile(`(?is)<onlyinclude\b`).MatchString(s) {
		re := regexp.MustCompile(`(?is)<onlyinclude\b[^>]*>(.*?)</onlyinclude>`)
		var b strings.Builder
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			b.WriteString(m[1])
		}
		s = b.String()
	}
	s = regexp.MustCompile(`(?is)<noinclude\b[^>]*>.*?</noinclude>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)</?includeonly\b[^>]*>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)</?onlyinclude\b[^>]*>`).ReplaceAllString(s, "")
	return s
}

func (p *parser) extractMagic(s string) string {
	re := regexp.MustCompile(`(?i)__(NOTOC|FORCETOC|TOC|NOEDITSECTION|NONEWSECTIONLINK|NOINDEX|INDEX|STATICREDIRECT)__`)
	return re.ReplaceAllStringFunc(s, func(m string) string {
		switch strings.ToUpper(strings.Trim(m, "_")) {
		case "NOTOC":
			p.noTOC = true
		case "FORCETOC":
			p.forceTOC = true
		case "TOC":
			p.explicit = true
			return "\n" + tocMarker + "\n"
		case "NOEDITSECTION":
			p.noEditSection = true
		}
		return ""
	})
}

func (p *parser) detectRedirect(s string) (dbkey, frag, rest string, ok bool) {
	trim := strings.TrimLeft(s, " \t\n")
	if len(trim) < 9 || !strings.HasPrefix(strings.ToLower(trim), "#redirect") {
		return "", "", s, false
	}
	i := strings.Index(trim, "[[")
	if i < 0 {
		return "", "", s, false
	}
	j := strings.Index(trim[i:], "]]")
	if j < 0 {
		return "", "", s, false
	}
	inner := trim[i+2 : i+j]
	if bar := strings.Index(inner, "|"); bar >= 0 {
		inner = inner[:bar]
	}
	inner = strings.TrimSpace(inner)
	if h := strings.Index(inner, "#"); h >= 0 {
		frag = inner[h+1:]
		inner = inner[:h]
	}
	t := title.Parse(inner, p.opt.ProjectNS)
	after := i + j + 2
	if nl := strings.Index(trim[after:], "\n"); nl >= 0 {
		rest = trim[after+nl+1:]
	}
	return t.DBKey, frag, rest, t.DBKey != ""
}

func (p *parser) redirectBox(dbkey, frag string) string {
	label := strings.ReplaceAll(dbkey, "_", " ")
	return `<div class="wikiwright-notice">Redirect to ` + p.linkHTML(dbkey, frag, label, false) + `</div>`
}

func (p *parser) expand(s string, params map[string]string, depth int) string {
	if depth > 20 || p.expands > 2000 {
		p.warn("Template expansion stopped at the depth limit")
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x00' {
			if j := strings.IndexByte(s[i+1:], '\x00'); j >= 0 {
				b.WriteString(s[i : i+2+j])
				i += 2 + j
				continue
			}
		}
		if strings.HasPrefix(s[i:], "{{{") || strings.HasPrefix(s[i:], "{{") {
			full, next, ok := scanBraces(s, i)
			if !ok {
				b.WriteByte(s[i])
				i++
				continue
			}
			p.expands++
			if strings.HasPrefix(full, "{{{") && strings.HasSuffix(full, "}}}") && len(full) >= 6 {
				b.WriteString(p.substParam(full[3:len(full)-3], params, depth))
			} else if len(full) >= 4 {
				b.WriteString(p.evalTemplate(full[2:len(full)-2], params, depth))
			} else {
				b.WriteString(full)
			}
			i = next
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func scanBraces(s string, start int) (full string, next int, ok bool) {
	depth := 0
	i := start
	for i < len(s) {
		if s[i] == '\x00' {
			j := strings.IndexByte(s[i+1:], '\x00')
			if j >= 0 {
				i += 2 + j
				continue
			}
		}
		if s[i] == '{' {
			depth++
			i++
			continue
		}
		if s[i] == '}' {
			depth--
			i++
			if depth == 0 {
				return s[start:i], i, true
			}
			if depth < 0 {
				return "", start, false
			}
			continue
		}
		i++
	}
	return "", start, false
}

func (p *parser) substParam(inner string, params map[string]string, depth int) string {
	parts := splitTop(inner, "|")
	name := strings.TrimSpace(parts[0])
	if params != nil {
		if v, ok := params[name]; ok {
			return v
		}
	}
	if len(parts) > 1 {
		// Default may itself contain templates.
		return p.expand(strings.TrimSpace(strings.Join(parts[1:], "|")), params, depth+1)
	}
	return "{{{" + inner + "}}}"
}

func (p *parser) evalTemplate(inner string, params map[string]string, depth int) string {
	parts := splitTop(inner, "|")
	head := strings.TrimSpace(parts[0])
	head = strings.TrimSpace(p.expand(head, params, depth+1))
	var args []string
	named := map[string]string{}
	pos := 1
	for _, raw := range parts[1:] {
		k, v, isNamed := splitNamed(raw)
		v = strings.TrimSpace(p.expand(v, params, depth+1))
		if isNamed {
			key := strings.TrimSpace(k)
			named[key] = v
			if _, err := strconv.Atoi(key); err == nil {
				named[key] = v
			}
		} else {
			v = strings.TrimSpace(v)
			named[strconv.Itoa(pos)] = v
			args = append(args, v)
			pos++
		}
	}
	// Parser function: name before the first colon, when that name is known or starts with #.
	if fname, first, isFn := splitFunc(head); isFn {
		all := []string{}
		if first != "" || strings.Contains(head, ":") {
			all = append(all, first)
		}
		all = append(all, args...)
		// Named args are uncommon for these functions; pass positional only.
		return p.magic(fname, all, "{{"+strings.TrimSpace(inner)+"}}")
	}
	if isVar(head) {
		return p.magic(head, nil, "{{"+head+"}}")
	}
	dbkey := title.TransclusionKey(head, p.opt.ProjectNS)
	if dbkey == "" {
		return "{{" + inner + "}}"
	}
	if p.opt.GetTemplate == nil {
		return "[[" + strings.ReplaceAll(dbkey, "_", " ") + "]]"
	}
	body, ok := p.opt.GetTemplate(dbkey)
	if !ok {
		return "[[" + strings.ReplaceAll(dbkey, "_", " ") + "]]"
	}
	body = stripForTransclusion(body)
	// Nowiki is protected first so a comment inside it stays literal. Comments
	// in the template itself are removed here, before the body is spliced into
	// the page. The page's own comments were already removed, and that pass
	// does not see text that arrives from a template.
	body = p.protect(body)
	body = stripComments(body)
	// Markers keep headings that this template produces out of section numbering.
	return "\x00TPL+\x00" + p.expand(body, named, depth+1) + "\x00TPL-\x00"
}

func splitFunc(head string) (name, first string, ok bool) {
	i := strings.Index(head, ":")
	if i < 0 {
		return "", "", false
	}
	name = strings.TrimSpace(head[:i])
	first = head[i+1:]
	l := strings.ToLower(name)
	if strings.HasPrefix(l, "#") || knownFunc(l) {
		return l, first, true
	}
	return "", "", false
}

func knownFunc(l string) bool {
	switch l {
	case "lc", "uc", "lcfirst", "ucfirst", "padleft", "padright", "urlencode", "anchorencode", "localurl", "fullurl", "ns", "int":
		return true
	default:
		return false
	}
}

func isVar(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "pagename", "pagenamee", "fullpagename", "fullpagenamee", "namespace", "sitename",
		"currentyear", "currentmonth", "currentmonthname", "currentday", "currenttime", "currentdayname",
		"numberofpages", "numberofarticles", "numberoffiles", "!", "fullurl", "localurl":
		return true
	default:
		return false
	}
}

func (p *parser) magic(name string, args []string, original string) string {
	l := strings.ToLower(strings.TrimSpace(name))
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	page := title.Parse(p.opt.PageTitle, p.opt.ProjectNS)
	switch l {
	case "!":
		return "|"
	case "pagename":
		return page.Rest
	case "pagenamee":
		return strings.ReplaceAll(page.Rest, " ", "_")
	case "fullpagename":
		return page.Display
	case "fullpagenamee":
		return page.DBKey
	case "namespace":
		return title.Canonical(page.Namespace, p.opt.ProjectNS)
	case "sitename":
		if p.opt.ProjectNS != "" {
			return p.opt.ProjectNS
		}
		return "Wiki"
	case "currentyear":
		return p.opt.Now.Format("2006")
	case "currentmonth":
		return p.opt.Now.Format("01")
	case "currentmonthname":
		return p.opt.Now.Format("January")
	case "currentday":
		return strconv.Itoa(p.opt.Now.Day())
	case "currenttime":
		return p.opt.Now.Format("15:04")
	case "currentdayname":
		return p.opt.Now.Weekday().String()
	case "numberofpages":
		return strconv.Itoa(p.opt.Counts.Pages)
	case "numberofarticles":
		return strconv.Itoa(p.opt.Counts.Articles)
	case "numberoffiles":
		return strconv.Itoa(p.opt.Counts.Files)
	case "lc":
		return strings.ToLower(arg(0))
	case "uc":
		return strings.ToUpper(arg(0))
	case "lcfirst":
		return lowerFirst(arg(0))
	case "ucfirst":
		return upperFirst(arg(0))
	case "urlencode", "anchorencode":
		return wikiQuery(strings.ReplaceAll(arg(0), " ", "_"))
	case "localurl", "fullurl":
		t := title.Parse(arg(0), p.opt.ProjectNS)
		if p.opt.Link != nil {
			return p.opt.Link(t.DBKey)
		}
		return "/w/index.php?title=" + wikiQuery(t.DBKey)
	case "ns":
		n, _ := strconv.Atoi(strings.TrimSpace(arg(0)))
		return title.Canonical(n, p.opt.ProjectNS)
	case "#ev", "#evt", "#evp", "#evu", "#evl":
		return p.embedVideo(arg(0), arg(1), arg(2), arg(3), arg(4))
	default:
		if strings.HasPrefix(l, "#") {
			p.warn("Unknown parser function " + l)
			return p.token(literal{kind: "html", text: `<span class="wikiwright-warning" title="Unknown parser function">` + html.EscapeString(original) + `</span>`})
		}
		return original
	}
}

func (p *parser) embedVideo(service, id, dim, align, caption string) string {
	service = strings.ToLower(strings.TrimSpace(service))
	id = strings.TrimSpace(id)
	w, h := parseDim(dim)
	align = strings.ToLower(strings.TrimSpace(align))
	switch align {
	case "left", "right", "center", "inline", "none", "":
	default:
		// EmbedVideo allows the description to shift left when alignment is omitted
		// and a caption lands in the alignment column. If align isn't a keyword,
		// treat it as the caption and ignore the bogus alignment.
		if caption == "" {
			caption = align
		}
		align = ""
	}
	src, ok := embedSrc(service, id)
	if !ok {
		p.warn("Unsupported or invalid video embed (" + service + ")")
		raw := "{{#ev:" + service + "|" + id + "}}"
		return p.token(literal{kind: "html", text: `<span class="wikiwright-warning" title="Video embed not rendered">` + html.EscapeString(raw) + `</span>`})
	}
	if w == 0 {
		w = 480
	}
	if h == 0 {
		h = w * 9 / 16
	}
	return p.token(literal{kind: "video", service: service, id: id, align: align, caption: caption, w: w, h: h, text: src})
}

func embedSrc(service, raw string) (string, bool) {
	switch service {
	case "youtube", "youtu":
		id := youtubeID(raw)
		if id == "" {
			return "", false
		}
		return "https://www.youtube-nocookie.com/embed/" + id, true
	case "vimeo":
		id := vimeoID(raw)
		if id == "" {
			return "", false
		}
		return "https://player.vimeo.com/video/" + id, true
	case "dailymotion":
		id := dailymotionID(raw)
		if id == "" {
			return "", false
		}
		return "https://www.dailymotion.com/embed/video/" + id, true
	default:
		return "", false
	}
}

var (
	ytRE  = regexp.MustCompile(`(?i)(?:youtube\.com/(?:watch\?(?:[^"'\s]*&)?v=|embed/|shorts/|v/)|youtu\.be/)([A-Za-z0-9_-]{11})`)
	ytID  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimRE = regexp.MustCompile(`(?i)vimeo\.com/(?:video/)?(\d{6,12})`)
	vimID = regexp.MustCompile(`^\d{6,12}$`)
	dmRE  = regexp.MustCompile(`(?i)dailymotion\.com/(?:video|embed/video)/([A-Za-z0-9]+)`)
	dmID  = regexp.MustCompile(`^[A-Za-z0-9]{4,32}$`)
)

func youtubeID(s string) string {
	s = strings.TrimSpace(s)
	if m := ytRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if ytID.MatchString(s) {
		return s
	}
	return ""
}

func vimeoID(s string) string {
	s = strings.TrimSpace(s)
	if m := vimRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if vimID.MatchString(s) {
		return s
	}
	return ""
}

func dailymotionID(s string) string {
	s = strings.TrimSpace(s)
	if m := dmRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if dmID.MatchString(s) {
		return s
	}
	return ""
}

func parseDim(s string) (int, int) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, "px")
	if s == "" {
		return 0, 0
	}
	if i := strings.Index(s, "x"); i > 0 {
		w, _ := strconv.Atoi(s[:i])
		h, _ := strconv.Atoi(s[i+1:])
		return w, h
	}
	w, _ := strconv.Atoi(s)
	return w, 0
}

func splitTop(s, sep string) []string {
	var parts []string
	var cur strings.Builder
	brace, link := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\x00' {
			j := strings.IndexByte(s[i+1:], '\x00')
			if j >= 0 {
				cur.WriteString(s[i : i+2+j])
				i += 1 + j
				continue
			}
		}
		switch s[i] {
		case '{':
			brace++
		case '}':
			if brace > 0 {
				brace--
			}
		case '[':
			link++
		case ']':
			if link > 0 {
				link--
			}
		}
		if brace == 0 && link == 0 && strings.HasPrefix(s[i:], sep) {
			parts = append(parts, cur.String())
			cur.Reset()
			i += len(sep) - 1
			continue
		}
		cur.WriteByte(s[i])
	}
	parts = append(parts, cur.String())
	return parts
}

func splitNamed(raw string) (k, v string, named bool) {
	brace, link := 0, 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			brace++
		case '}':
			if brace > 0 {
				brace--
			}
		case '[':
			link++
		case ']':
			if link > 0 {
				link--
			}
		case '=':
			if brace == 0 && link == 0 {
				return raw[:i], raw[i+1:], true
			}
		}
	}
	return "", raw, false
}

func lowerFirst(s string) string {
	return mapFirst(s, strings.ToLower)
}

func upperFirst(s string) string {
	return mapFirst(s, strings.ToUpper)
}

func mapFirst(s string, fn func(string) string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	mapped := []rune(fn(string(r[0])))
	if len(mapped) == 0 {
		return s
	}
	r[0] = mapped[0]
	return string(r)
}

func wikiQuery(dbkey string) string {
	return strings.ReplaceAll(urlQueryEscape(dbkey), "%3A", ":")
}

func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteByte('+')
		} else {
			b.WriteString("%")
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
