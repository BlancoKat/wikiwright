package parser

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/BlancoKat/wikiwright/internal/title"
)

func (p *parser) placeTOC(s string) string {
	show := false
	if p.explicit {
		show = true
	} else if !p.noTOC && (p.forceTOC || len(p.heads) >= 4) {
		show = true
	}
	toc := ""
	if show {
		toc = p.tocHTML()
	}
	if p.explicit {
		s = strings.Replace(s, tocMarker, toc, 1)
		s = strings.ReplaceAll(s, tocMarker, "")
	}
	s = strings.Replace(s, tocAutoMarker, toc, 1)
	s = strings.ReplaceAll(s, tocAutoMarker, "")
	return s
}

func (p *parser) tocHTML() string {
	if len(p.heads) == 0 {
		return ""
	}
	minLevel := 6
	for _, h := range p.heads {
		if h.level < minLevel {
			minLevel = h.level
		}
	}
	counters := make([]int, 7)
	for i := range p.heads {
		tl := p.heads[i].level - minLevel + 1
		if tl < 1 {
			tl = 1
		}
		if tl > 6 {
			tl = 6
		}
		for k := 1; k < tl; k++ {
			if counters[k] == 0 {
				counters[k] = 1
			}
		}
		counters[tl]++
		for k := tl + 1; k <= 6; k++ {
			counters[k] = 0
		}
		var parts []string
		for k := 1; k <= tl; k++ {
			parts = append(parts, strconv.Itoa(counters[k]))
		}
		p.heads[i].tocLevel = tl
		p.heads[i].number = strings.Join(parts, ".")
	}
	var b strings.Builder
	b.WriteString(`<div id="toc" class="toc"><input type="checkbox" role="button" id="toctogglecheckbox" class="toctogglecheckbox" style="display:none" /><div class="toctitle" lang="en" dir="ltr"><h2 id="mw-toc-heading">Contents</h2><span class="toctogglespan"><label class="toctogglelabel" for="toctogglecheckbox"></label></span></div>`)
	depth := 0
	for i, h := range p.heads {
		tl := h.tocLevel
		for depth < tl {
			b.WriteString("<ul>\n")
			depth++
		}
		for depth > tl {
			b.WriteString("</li>\n</ul>\n")
			depth--
		}
		if i > 0 && depth == tl {
			b.WriteString("</li>\n")
		}
		b.WriteString(`<li class="toclevel-`)
		b.WriteString(strconv.Itoa(tl))
		b.WriteString(` tocsection-`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`"><a href="#`)
		b.WriteString(html.EscapeString(h.id))
		b.WriteString(`"><span class="tocnumber">`)
		b.WriteString(html.EscapeString(h.number))
		b.WriteString(`</span> <span class="toctext">`)
		b.WriteString(html.EscapeString(h.plain))
		b.WriteString(`</span></a>`)
	}
	for depth > 0 {
		b.WriteString("</li>\n</ul>\n")
		depth--
	}
	b.WriteString("</div>\n")
	return b.String()
}

func (p *parser) renderBlocks(src string) string {
	lines, inside := p.splitLines(src)
	return p.renderPrepared(lines, inside)
}

func (p *parser) splitLines(src string) ([]string, []bool) {
	raw := strings.Split(src, "\n")
	lines := make([]string, len(raw))
	inside := make([]bool, len(raw))
	for i, line := range raw {
		lines[i], inside[i] = p.peelTemplateMarkers(line)
	}
	return lines, inside
}

func (p *parser) peelTemplateMarkers(line string) (string, bool) {
	const plus, minus = "\x00TPL+\x00", "\x00TPL-\x00"
	var b strings.Builder
	inTemplate := p.tplDepth > 0
	seen := false
	for len(line) > 0 {
		switch {
		case strings.HasPrefix(line, plus):
			p.tplDepth++
			if !seen {
				inTemplate = true
			}
			line = line[len(plus):]
		case strings.HasPrefix(line, minus):
			if p.tplDepth > 0 {
				p.tplDepth--
			}
			if !seen {
				inTemplate = p.tplDepth > 0
			}
			line = line[len(minus):]
		default:
			next := len(line)
			if i := strings.Index(line, plus); i >= 0 && i < next {
				next = i
			}
			if i := strings.Index(line, minus); i >= 0 && i < next {
				next = i
			}
			b.WriteString(line[:next])
			if next > 0 {
				seen = true
			}
			line = line[next:]
		}
	}
	return b.String(), inTemplate
}

func (p *parser) renderPrepared(lines []string, inside []bool) string {
	var b strings.Builder
	for i := 0; i < len(lines); {
		line := lines[i]
		fromTemplate := i < len(inside) && inside[i]
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		if strings.TrimSpace(line) == strings.Trim(tocMarker, "\x00") || strings.Contains(line, tocMarker) && strings.TrimSpace(line) == tocMarker {
			b.WriteString(tocMarker)
			i++
			continue
		}
		if strings.Contains(line, tocMarker) && strings.TrimSpace(strings.ReplaceAll(line, tocMarker, "")) == "" {
			b.WriteString(tocMarker)
			i++
			continue
		}
		if line[0] == ' ' {
			var buf strings.Builder
			for i < len(lines) && lines[i] != "" && lines[i][0] == ' ' {
				buf.WriteString(lines[i][1:])
				buf.WriteByte('\n')
				i++
			}
			b.WriteString("<pre>")
			b.WriteString(p.inline(buf.String()))
			b.WriteString("</pre>\n")
			continue
		}
		trim := strings.TrimSpace(line)
		low := strings.ToLower(trim)
		if strings.HasPrefix(low, "<blockquote") {
			inner, innerIn, next := consumePrepared(lines, inside, i, "blockquote")
			b.WriteString("<blockquote>")
			b.WriteString(p.renderPrepared(inner, innerIn))
			b.WriteString("</blockquote>\n")
			i = next
			continue
		}
		if strings.HasPrefix(low, "<pre") {
			inner, next := consumeElement(lines, i, "pre")
			b.WriteString("<pre>")
			b.WriteString(html.EscapeString(inner))
			b.WriteString("</pre>\n")
			i = next
			continue
		}
		if level, text, ok := parseHeading(trim); ok {
			b.WriteString(p.renderHeading(level, text, fromTemplate))
			i++
			continue
		}
		if isHR(trim) {
			b.WriteString("<hr />\n")
			i++
			continue
		}
		if strings.HasPrefix(trim, "{|") {
			table, next := p.renderTable(lines, i)
			b.WriteString(table)
			i = next
			continue
		}
		if isListLine(line) {
			var ls []listLine
			for i < len(lines) && isListLine(lines[i]) {
				ls = append(ls, explodeList(lines[i])...)
				i++
			}
			b.WriteString(p.renderList(ls))
			b.WriteByte('\n')
			continue
		}
		var para []string
		for i < len(lines) && !startsBlock(lines[i]) {
			para = append(para, lines[i])
			i++
		}
		joined := strings.TrimSpace(strings.Join(para, " "))
		if joined == "" {
			continue
		}
		inner := p.inline(joined)
		if strings.TrimSpace(inner) == "" {
			continue
		}
		if blocky(inner) {
			b.WriteString(inner)
			if !strings.HasSuffix(inner, "\n") {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteString("<p>")
		b.WriteString(inner)
		b.WriteString("</p>\n")
	}
	return b.String()
}

func startsBlock(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	if line[0] == ' ' || isListLine(line) {
		return true
	}
	trim := strings.TrimSpace(line)
	if isHR(trim) || strings.HasPrefix(trim, "{|") || strings.Contains(trim, tocMarker) {
		return true
	}
	low := strings.ToLower(trim)
	if strings.HasPrefix(low, "<blockquote") || strings.HasPrefix(low, "<pre") {
		return true
	}
	_, _, ok := parseHeading(trim)
	return ok
}

func blocky(h string) bool {
	t := strings.TrimSpace(h)
	for _, pfx := range []string{"<pre", "<div", "<blockquote", "<table", "<ul", "<ol", "<dl", "<h1", "<h2", "<h3", "<iframe"} {
		if strings.HasPrefix(t, pfx) {
			return true
		}
	}
	return false
}

func isHR(trim string) bool {
	if len(trim) < 4 {
		return false
	}
	for _, c := range trim {
		if c != '-' {
			return false
		}
	}
	return true
}

// parseHeading matches MediaWiki 1.39 (Preprocessor_Hash plus doHeadings).
// The level is the shorter of the leading and trailing "=" runs, and those
// runs must not overlap. A line that is only "=" signs is a heading when it
// has at least three: the level is min(6, (n-1)/2) and the text is the equals
// signs left in the middle, so "===" is an h1 whose text is "=". "==" is not
// a heading. Trailing spaces, tabs, and HTML comments still leave a heading.
func parseHeading(line string) (int, string, bool) {
	core := strings.Trim(stripHeadingTrail(line), " \t")
	if core == "" || core[0] != '=' {
		return 0, "", false
	}
	lead := 0
	for lead < len(core) && core[lead] == '=' {
		lead++
	}
	trail := 0
	for trail < len(core) && core[len(core)-1-trail] == '=' {
		trail++
	}
	if lead == 0 || trail == 0 {
		return 0, "", false
	}
	// The two runs are the same span. doHeadings needs a non-empty interior,
	// which means at least three equals signs: /=(.+)/= cannot match "==".
	if lead+trail > len(core) {
		n := len(core)
		if n < 3 {
			return 0, "", false
		}
		level := (n - 1) / 2
		if level > 6 {
			level = 6
		}
		if level < 1 || level > n-level {
			return 0, "", false
		}
		return level, core[level : n-level], true
	}
	level := lead
	if level > 6 {
		level = 6
	}
	if trail < level {
		level = trail
	}
	if level < 1 || level > len(core)-level {
		return 0, "", false
	}
	text := strings.Trim(core[level:len(core)-level], " \t")
	return level, text, true
}

// stripHeadingTrail drops trailing spaces, tabs, and HTML comments so a
// closing "=" run is still visible. An unclosed "<!--" runs to the end of
// the line, matching the preprocessor's visual end of a trailing comment.
func stripHeadingTrail(s string) string {
	for {
		s = strings.TrimRight(s, " \t")
		if strings.HasSuffix(s, "-->") {
			end := len(s) - 3
			open := strings.LastIndex(s[:end], "<!--")
			if open < 0 {
				break
			}
			s = s[:open]
			continue
		}
		if i := strings.LastIndex(s, "<!--"); i >= 0 && !strings.Contains(s[i+4:], "-->") {
			s = s[:i]
			continue
		}
		break
	}
	return strings.TrimRight(s, " \t")
}

func (p *parser) renderHeading(level int, text string, fromTemplate bool) string {
	inner := p.inline(text)
	plain := plainFromHTML(inner)
	id := p.uniqueID(plain)
	html5 := strings.ReplaceAll(plain, " ", "_")
	var extra string
	if html5 != "" && html5 != id {
		extra = `<span id="` + html.EscapeString(html5) + `"></span>`
	}
	p.heads = append(p.heads, head{level: level, id: id, plain: plain})
	edit := ""
	if !fromTemplate {
		p.sectionN++
		if p.opt.SectionEdits && !p.noEditSection {
			edit = p.editSectionLink(p.sectionN, plain)
		}
	}
	out := "<h" + strconv.Itoa(level) + ">" + extra + `<span class="mw-headline" id="` + html.EscapeString(id) + `">` + inner + "</span>" + edit + "</h" + strconv.Itoa(level) + ">\n"
	if len(p.heads) == 1 && !p.explicit {
		out = tocAutoMarker + out
	}
	return out
}

func (p *parser) editSectionLink(n int, plain string) string {
	href := p.href(p.currentDB()) + "&action=edit&section=" + strconv.Itoa(n)
	title := "Edit section: " + plain
	return `<span class="mw-editsection"><span class="mw-editsection-bracket">[</span><a href="` +
		html.EscapeString(href) + `" title="` + html.EscapeString(title) +
		`">edit</a><span class="mw-editsection-bracket">]</span></span>`
}

func (p *parser) uniqueID(plain string) string {
	id := legacyFragment(plain)
	if id == "" || id == "." {
		id = "section"
	}
	base := id
	n := 2
	for p.ids[id] {
		id = base + "_" + strconv.Itoa(n)
		n++
	}
	p.ids[id] = true
	return id
}

func plainFromHTML(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// legacyFragment matches MediaWiki's legacy fragment mode: spaces become
// underscores, then PHP urlencode, then '%' becomes '.' (and "%3A" becomes ':').
func legacyFragment(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	enc := phpURLEncode(s)
	enc = strings.ReplaceAll(enc, "%3A", ":")
	enc = strings.ReplaceAll(enc, "%", ".")
	return enc
}

func phpURLEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteByte('+')
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

func consumePrepared(lines []string, inside []bool, i int, tag string) ([]string, []bool, int) {
	open := regexp.MustCompile(`(?i)<` + tag + `\b[^>]*>`)
	close := regexp.MustCompile(`(?i)</` + tag + `\s*>`)
	var inner []string
	var innerIn []bool
	first := true
	for i < len(lines) && i < 100000 {
		line := lines[i]
		in := i < len(inside) && inside[i]
		if first {
			if loc := open.FindStringIndex(line); loc != nil {
				line = line[loc[1]:]
			}
			first = false
		}
		if loc := close.FindStringIndex(line); loc != nil {
			inner = append(inner, line[:loc[0]])
			innerIn = append(innerIn, in)
			return inner, innerIn, i + 1
		}
		inner = append(inner, line)
		innerIn = append(innerIn, in)
		i++
	}
	return inner, innerIn, i
}

func consumeElement(lines []string, i int, tag string) (string, int) {
	open := regexp.MustCompile(`(?i)<` + tag + `\b[^>]*>`)
	close := regexp.MustCompile(`(?i)</` + tag + `\s*>`)
	var b strings.Builder
	first := true
	for i < len(lines) && i < 100000 {
		line := lines[i]
		if first {
			if loc := open.FindStringIndex(line); loc != nil {
				line = line[loc[1]:]
			}
			first = false
		}
		if loc := close.FindStringIndex(line); loc != nil {
			b.WriteString(line[:loc[0]])
			return b.String(), i + 1
		}
		b.WriteString(line)
		b.WriteByte('\n')
		i++
	}
	return b.String(), i
}

type listLine struct {
	prefix string
	text   string
}

func isListLine(line string) bool {
	if line == "" {
		return false
	}
	switch line[0] {
	case '*', '#', ';', ':':
		return true
	default:
		return false
	}
}

func explodeList(line string) []listLine {
	if strings.HasPrefix(line, ";") {
		body := line[1:]
		if idx := colonOutside(body); idx >= 0 {
			return []listLine{
				{prefix: ";", text: strings.TrimSpace(body[:idx])},
				{prefix: ":", text: strings.TrimSpace(body[idx+1:])},
			}
		}
	}
	n := 0
	for n < len(line) && strings.ContainsRune("*#:;", rune(line[n])) {
		n++
	}
	return []listLine{{prefix: line[:n], text: strings.TrimSpace(line[n:])}}
}

func colonOutside(s string) int {
	link := 0
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "[[") {
			link++
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "]]") && link > 0 {
			link--
			i++
			continue
		}
		if s[i] == ':' && link == 0 && !strings.HasPrefix(s[i:], "://") {
			return i
		}
	}
	return -1
}

func (p *parser) renderList(lines []listLine) string {
	var b strings.Builder
	i := 0
	for i < len(lines) {
		if lines[i].prefix == "" {
			i++
			continue
		}
		kind := lines[i].prefix[0]
		fam := listFamily(kind)
		b.WriteString("<" + listTag(kind) + ">")
		for i < len(lines) && lines[i].prefix != "" && listFamily(lines[i].prefix[0]) == fam {
			pr := lines[i].prefix
			b.WriteString("<" + itemTag(pr[0]) + ">")
			if len(pr) == 1 {
				b.WriteString(p.inline(lines[i].text))
				i++
				var kids []listLine
				for i < len(lines) && lines[i].prefix != "" && listFamily(lines[i].prefix[0]) == fam && len(lines[i].prefix) > 1 {
					kids = append(kids, listLine{prefix: lines[i].prefix[1:], text: lines[i].text})
					i++
				}
				if len(kids) > 0 {
					b.WriteString(p.renderList(kids))
				}
			} else {
				var kids []listLine
				for i < len(lines) && lines[i].prefix != "" && listFamily(lines[i].prefix[0]) == fam && len(lines[i].prefix) > 1 {
					kids = append(kids, listLine{prefix: lines[i].prefix[1:], text: lines[i].text})
					i++
				}
				b.WriteString(p.renderList(kids))
			}
			b.WriteString("</" + itemTag(pr[0]) + ">")
		}
		b.WriteString("</" + listTag(kind) + ">")
	}
	return b.String()
}

func listFamily(c byte) byte {
	if c == ';' || c == ':' {
		return 'd'
	}
	return c
}

func listTag(c byte) string {
	switch c {
	case '*':
		return "ul"
	case '#':
		return "ol"
	default:
		return "dl"
	}
}

func itemTag(c byte) string {
	switch c {
	case '*', '#':
		return "li"
	case ';':
		return "dt"
	default:
		return "dd"
	}
}

type cell struct {
	header         bool
	text           string
	colspan, rspan int
}

func (p *parser) renderTable(lines []string, i int) (string, int) {
	var rows [][]cell
	var cur []cell
	caption := ""
	flush := func() {
		if len(cur) > 0 {
			rows = append(rows, cur)
			cur = nil
		}
	}
	i++ // skip {|
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trim, "|}") {
			flush()
			i++
			break
		}
		if strings.HasPrefix(trim, "|+") {
			caption = strings.TrimSpace(trim[2:])
			i++
			continue
		}
		if strings.HasPrefix(trim, "|-") {
			flush()
			i++
			continue
		}
		if strings.HasPrefix(trim, "!") || strings.HasPrefix(trim, "|") {
			header := strings.HasPrefix(trim, "!")
			body := trim[1:]
			parts := splitTop(body, "||")
			if header {
				// Header cells also split on !!, and sometimes ||.
				var hp []string
				for _, part := range parts {
					hp = append(hp, splitTop(part, "!!")...)
				}
				parts = hp
			}
			for _, part := range parts {
				cur = append(cur, parseCell(part, header))
			}
			i++
			continue
		}
		if len(cur) > 0 {
			cur[len(cur)-1].text += "\n" + line
		}
		i++
	}
	var b strings.Builder
	b.WriteString("<table>\n")
	if strings.TrimSpace(caption) != "" {
		b.WriteString("<caption>")
		b.WriteString(p.inline(caption))
		b.WriteString("</caption>\n")
	}
	b.WriteString("<tbody>\n")
	for _, row := range rows {
		b.WriteString("<tr>\n")
		for _, c := range row {
			tag := "td"
			if c.header {
				tag = "th"
			}
			b.WriteString("<" + tag)
			if c.colspan > 1 {
				b.WriteString(` colspan="` + strconv.Itoa(c.colspan) + `"`)
			}
			if c.rspan > 1 {
				b.WriteString(` rowspan="` + strconv.Itoa(c.rspan) + `"`)
			}
			b.WriteString(">")
			b.WriteString(p.inline(strings.TrimSpace(c.text)))
			b.WriteString("</" + tag + ">\n")
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody></table>\n")
	return b.String(), i
}

func parseCell(s string, header bool) cell {
	c := cell{header: header, text: s}
	if idx := attrSplit(s); idx >= 0 {
		attr := s[:idx]
		c.text = s[idx+1:]
		c.colspan = attrInt(attr, "colspan")
		c.rspan = attrInt(attr, "rowspan")
	}
	return c
}

func attrSplit(s string) int {
	brace, link := 0, 0
	for i := 0; i < len(s); i++ {
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
		case '|':
			if brace == 0 && link == 0 && strings.Contains(s[:i], "=") {
				return i
			}
		}
	}
	return -1
}

func attrInt(attr, name string) int {
	re := regexp.MustCompile(`(?i)\b` + name + `\s*=\s*"?(\d+)`)
	m := re.FindStringSubmatch(attr)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func (p *parser) inline(s string) string {
	if p.depth > 16 {
		return html.EscapeString(s)
	}
	p.depth++
	old := p.stack
	p.stack = nil
	out := p.inlineBody(s)
	out += p.closeAll()
	p.stack = old
	p.depth--
	return out
}

func (p *parser) inlineBody(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x00' {
			j := strings.IndexByte(s[i+1:], '\x00')
			if j >= 0 {
				b.WriteString(p.renderToken(s[i+1 : i+1+j]))
				i += 2 + j
				continue
			}
		}
		if s[i] == '\'' {
			n := 0
			for i+n < len(s) && s[i+n] == '\'' {
				n++
			}
			if n >= 2 {
				b.WriteString(p.applyQuotes(n))
				i += n
				continue
			}
		}
		if strings.HasPrefix(s[i:], "[[") {
			if h, next, ok := p.parseInternalLink(s, i); ok {
				b.WriteString(h)
				i = next
				continue
			}
		}
		if strings.HasPrefix(s[i:], "[http://") || strings.HasPrefix(s[i:], "[https://") || strings.HasPrefix(s[i:], "[mailto:") {
			if h, next, ok := p.parseExternal(s, i); ok {
				b.WriteString(h)
				i = next
				continue
			}
		}
		if s[i] == '<' {
			if h, next, ok := p.parseAngle(s, i); ok {
				b.WriteString(h)
				i = next
				continue
			}
		}
		if strings.HasPrefix(s[i:], "http://") || strings.HasPrefix(s[i:], "https://") {
			if h, next, ok := p.parseBareURL(s, i); ok {
				b.WriteString(h)
				i = next
				continue
			}
		}
		j := i + 1
		for j < len(s) && !inlineSpecial(s, j) {
			j++
		}
		b.WriteString(html.EscapeString(html.UnescapeString(s[i:j])))
		i = j
	}
	return b.String()
}

func inlineSpecial(s string, j int) bool {
	c := s[j]
	if c == '\x00' || c == '\'' || c == '<' || c == '[' {
		return true
	}
	if strings.HasPrefix(s[j:], "http://") || strings.HasPrefix(s[j:], "https://") {
		return true
	}
	return false
}

func (p *parser) renderToken(key string) string {
	lit, ok := p.saved[key]
	if !ok {
		return ""
	}
	switch lit.kind {
	case "nowiki":
		return html.EscapeString(lit.text)
	case "pre":
		return "<pre>" + html.EscapeString(lit.text) + "</pre>"
	case "html":
		return lit.text
	case "video":
		return p.videoHTML(lit)
	default:
		return html.EscapeString(lit.text)
	}
}

func (p *parser) videoHTML(lit literal) string {
	align := "center"
	switch lit.align {
	case "left":
		align = "floatleft"
	case "right":
		align = "floatright"
	case "inline", "none":
		align = ""
	}
	cap := ""
	if strings.TrimSpace(lit.caption) != "" {
		cap = `<div class="thumbcaption">` + p.inline(lit.caption) + `</div>`
	}
	class := "wikiwright-video"
	if align != "" {
		class += " " + align
	}
	return `<div class="` + class + `"><iframe class="wikiwright-embed" width="` + strconv.Itoa(lit.w) + `" height="` + strconv.Itoa(lit.h) + `" src="` + html.EscapeString(lit.text) + `" title="Embedded video" frameborder="0" loading="lazy" referrerpolicy="strict-origin-when-cross-origin" allowfullscreen sandbox="allow-scripts allow-same-origin allow-presentation" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture"></iframe>` + cap + `</div>`
}

func (p *parser) applyQuotes(n int) string {
	var b strings.Builder
	switch {
	case n >= 5:
		if n > 5 {
			b.WriteString(strings.Repeat("'", n-5))
		}
		wantI := !p.hasStyle("i")
		wantB := !p.hasStyle("b")
		b.WriteString(p.setStyle("i", wantI))
		b.WriteString(p.setStyle("b", wantB))
	case n == 4:
		b.WriteByte('\'')
		b.WriteString(p.setStyle("b", !p.hasStyle("b")))
	case n == 3:
		b.WriteString(p.setStyle("b", !p.hasStyle("b")))
	default:
		b.WriteString(p.setStyle("i", !p.hasStyle("i")))
	}
	return b.String()
}

func (p *parser) hasStyle(tag string) bool {
	for _, t := range p.stack {
		if t == tag {
			return true
		}
	}
	return false
}

func (p *parser) setStyle(tag string, on bool) string {
	if on {
		if p.hasStyle(tag) {
			return ""
		}
		p.stack = append(p.stack, tag)
		return "<" + tag + ">"
	}
	idx := -1
	for i, t := range p.stack {
		if t == tag {
			idx = i
		}
	}
	if idx < 0 {
		return ""
	}
	above := append([]string{}, p.stack[idx+1:]...)
	var b strings.Builder
	for i := len(p.stack) - 1; i >= idx; i-- {
		b.WriteString("</" + p.stack[i] + ">")
	}
	p.stack = p.stack[:idx]
	for _, t := range above {
		p.stack = append(p.stack, t)
		b.WriteString("<" + t + ">")
	}
	return b.String()
}

func (p *parser) closeAll() string {
	var b strings.Builder
	for i := len(p.stack) - 1; i >= 0; i-- {
		b.WriteString("</" + p.stack[i] + ">")
	}
	p.stack = nil
	return b.String()
}

func (p *parser) parseInternalLink(s string, i int) (string, int, bool) {
	end := findPair(s, i+2, "[[", "]]")
	if end < 0 {
		return "", 0, false
	}
	inner := s[i+2 : end]
	next := end + 2
	trail := ""
	for next < len(s) && s[next] >= 'a' && s[next] <= 'z' {
		trail += s[next : next+1]
		next++
	}
	parts := splitTop(inner, "|")
	target := strings.TrimSpace(parts[0])
	force := false
	for strings.HasPrefix(target, ":") {
		force = true
		target = strings.TrimSpace(target[1:])
	}
	frag := ""
	if h := strings.Index(target, "#"); h >= 0 {
		frag = target[h+1:]
		target = strings.TrimSpace(target[:h])
	}
	label := ""
	hasLabel := len(parts) > 1
	if !hasLabel {
		if target == "" {
			label = strings.ReplaceAll(frag, "_", " ")
		} else {
			label = strings.ReplaceAll(target, "_", " ")
		}
		label += trail
	} else {
		lab := strings.TrimSpace(strings.Join(parts[1:], "|"))
		if lab == "" {
			label = pipeTrick(target, p.opt.ProjectNS) + trail
		} else {
			label = lab + trail
		}
	}
	// File / category special cases use the raw option parts.
	t := title.Parse(target, p.opt.ProjectNS)
	if target != "" && !t.Legal() && t.Namespace == title.NSMain && strings.ContainsAny(t.Rest, "#<>[]{}|") {
		return html.EscapeString(s[i:next]), next, true
	}
	if !force && (t.Namespace == title.NSFile) && target != "" {
		opts := []string{}
		if len(parts) > 1 {
			opts = parts[1:]
		}
		return p.renderFile(t.Rest, opts), next, true
	}
	if !force && t.Namespace == title.NSCategory && target != "" {
		sortKey := pageSort(p.opt.PageTitle, p.opt.ProjectNS)
		if len(parts) > 1 {
			sortKey = strings.TrimSpace(parts[1])
		}
		p.result.Categories = append(p.result.Categories, Cat{Name: t.Rest, SortKey: sortKey})
		return "", next, true
	}
	if t.Namespace == title.NSMedia && target != "" {
		return p.mediaLink(t.Rest, label), next, true
	}
	if target == "" && frag != "" {
		id := legacyFragment(frag)
		return `<a href="#` + html.EscapeString(id) + `" title="` + html.EscapeString(frag) + `">` + p.inline(label) + `</a>`, next, true
	}
	if target == "" {
		return html.EscapeString(s[i:next]), next, true
	}
	return p.linkHTML(t.DBKey, frag, label, true), next, true
}

func pageSort(page, project string) string {
	t := title.Parse(page, project)
	if t.Rest == "" {
		return page
	}
	return t.Rest
}

func pipeTrick(target, project string) string {
	t := title.Parse(target, project)
	s := t.Rest
	if i := strings.LastIndex(s, "("); i > 0 && strings.HasSuffix(strings.TrimSpace(s), ")") {
		s = strings.TrimSpace(s[:i])
	}
	if i := strings.Index(s, ","); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func (p *parser) linkHTML(dbkey, frag, label string, parseLabel bool) string {
	if dbkey == "" {
		return html.EscapeString(label)
	}
	t := title.Parse(strings.ReplaceAll(dbkey, "_", " "), p.opt.ProjectNS)
	// Re-parse so display is stable. dbkey may already be canonical.
	if t.DBKey == "" {
		t.DBKey = dbkey
	}
	exists := true
	if t.Namespace != title.NSSpecial && p.opt.Exists != nil {
		exists = p.opt.Exists(t.DBKey)
		if !exists && t.Namespace == title.NSFile && p.opt.File != nil {
			if _, ok := p.opt.File(t.Rest); ok {
				exists = true
			}
		}
	}
	href := p.href(t.DBKey)
	class := ""
	titleAttr := strings.ReplaceAll(t.DBKey, "_", " ")
	if !exists {
		href += "&action=edit&redlink=1"
		if cur := p.currentDB(); cur != "" && cur != t.DBKey {
			href += "&returnto=" + title.URLQuery(cur)
		}
		class = ` class="new"`
		titleAttr += " (page does not exist)"
	} else if p.currentDB() == t.DBKey && frag == "" {
		class = ` class="mw-selflink"`
	}
	if frag != "" {
		href += "#" + legacyFragment(frag)
	}
	if t.Namespace != title.NSSpecial {
		p.noteLink(t.DBKey)
	}
	lab := label
	if parseLabel {
		lab = p.inline(label)
	} else {
		lab = html.EscapeString(label)
	}
	return `<a href="` + html.EscapeString(href) + `"` + class + ` title="` + html.EscapeString(titleAttr) + `">` + lab + `</a>`
}

func (p *parser) currentDB() string {
	return title.Parse(p.opt.PageTitle, p.opt.ProjectNS).DBKey
}

func (p *parser) href(dbkey string) string {
	if p.opt.Link != nil {
		return p.opt.Link(dbkey)
	}
	return "/w/index.php?title=" + wikiQuery(dbkey)
}

func (p *parser) noteLink(dbkey string) {
	if p.seenLink[dbkey] {
		return
	}
	p.seenLink[dbkey] = true
	p.result.Links = append(p.result.Links, dbkey)
}

func (p *parser) renderFile(name string, opts []string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	fo := p.parseFileOpts(opts)
	info, ok := FileInfo{}, false
	if p.opt.File != nil {
		info, ok = p.opt.File(name)
	}
	dbkey := "File:" + strings.ReplaceAll(name, "_", "_")
	dbkey = title.Parse("File:"+name, p.opt.ProjectNS).DBKey
	if !ok {
		return p.linkHTML(dbkey, "", "File:"+name, false)
	}
	p.noteLink(dbkey)
	pageHref := p.href(dbkey)
	raw := name
	if p.opt.RawFile != nil {
		raw = p.opt.RawFile(name)
	} else {
		raw = "/w/images/" + urlPathEscape(name)
	}
	alt := fo.alt
	if alt == "" {
		alt = name
	}
	w, h := fo.width, fo.height
	if w == 0 && (fo.format == "thumb") {
		w = 220
	}
	if w > 0 && h == 0 && info.Width > 0 && info.Height > 0 {
		h = info.Height * w / info.Width
		if h < 1 {
			h = 1
		}
	}
	if w == 0 && info.Width > 0 {
		w = info.Width
		h = info.Height
	}
	img := `<img alt="` + html.EscapeString(alt) + `" src="` + html.EscapeString(raw) + `"`
	if w > 0 {
		img += ` width="` + strconv.Itoa(w) + `"`
	}
	if h > 0 {
		img += ` height="` + strconv.Itoa(h) + `"`
	}
	img += ` decoding="async" />`
	linked := `<a href="` + html.EscapeString(pageHref) + `" class="image" title="` + html.EscapeString(alt) + `">` + img + `</a>`
	switch fo.format {
	case "thumb", "frame":
		align := fo.align
		if align == "" {
			align = "right"
		}
		cls := "thumb tright"
		switch align {
		case "left":
			cls = "thumb tleft"
		case "center":
			cls = "thumb tcenter"
		case "none":
			cls = "thumb tnone"
		}
		widthStyle := ""
		if w > 0 {
			widthStyle = ` style="width:` + strconv.Itoa(w+2) + `px"`
		}
		cap := ""
		if fo.caption != "" {
			cap = `<div class="thumbcaption">` + fo.caption + `</div>`
		}
		return `<div class="` + cls + `"><div class="thumbinner"` + widthStyle + `>` + linked + cap + `</div></div>`
	default:
		if fo.align == "center" {
			return `<div class="center"><div class="floatnone">` + linked + `</div></div>`
		}
		if fo.align == "left" {
			return `<div class="floatleft">` + linked + `</div>`
		}
		if fo.align == "right" {
			return `<div class="floatright">` + linked + `</div>`
		}
		return linked
	}
}

type fileOpts struct {
	format, align, alt, caption string
	width, height               int
}

func (p *parser) parseFileOpts(opts []string) fileOpts {
	var o fileOpts
	var unknown []string
	for _, raw := range opts {
		part := strings.TrimSpace(raw)
		l := strings.ToLower(part)
		switch l {
		case "thumb", "thumbnail":
			o.format = "thumb"
			continue
		case "frame", "framed":
			o.format = "frame"
			continue
		case "frameless":
			o.format = "frameless"
			continue
		case "left", "right", "center", "none":
			o.align = l
			continue
		case "border":
			continue
		}
		if strings.HasPrefix(l, "alt=") {
			o.alt = strings.TrimSpace(part[4:])
			continue
		}
		if strings.HasPrefix(l, "link=") || strings.HasPrefix(l, "upright") || strings.HasPrefix(l, "class=") {
			continue
		}
		if m := regexp.MustCompile(`(?i)^(\d+)px$`).FindStringSubmatch(part); m != nil {
			o.width, _ = strconv.Atoi(m[1])
			continue
		}
		if m := regexp.MustCompile(`(?i)^x(\d+)px$`).FindStringSubmatch(part); m != nil {
			o.height, _ = strconv.Atoi(m[1])
			continue
		}
		if m := regexp.MustCompile(`(?i)^(\d+)x(\d+)px$`).FindStringSubmatch(part); m != nil {
			o.width, _ = strconv.Atoi(m[1])
			o.height, _ = strconv.Atoi(m[2])
			continue
		}
		unknown = append(unknown, part)
	}
	if len(unknown) > 0 {
		last := unknown[len(unknown)-1]
		if o.format == "thumb" || o.format == "frame" {
			o.caption = p.inline(last)
		} else if o.alt == "" {
			o.alt = last
		}
	}
	return o
}

func (p *parser) mediaLink(name, label string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	raw := "/w/images/" + urlPathEscape(name)
	if p.opt.RawFile != nil {
		raw = p.opt.RawFile(name)
	}
	if label == "" {
		label = name
	}
	return `<a href="` + html.EscapeString(raw) + `" class="internal" title="` + html.EscapeString(name) + `">` + p.inline(label) + `</a>`
}

// externalEnd finds the closing bracket of an external link, skipping brackets
// that belong to an internal link in the label:
// [https://example/guide Guide to [[DeFi|Decentralized Finance]] ]
func externalEnd(s string, i int) int {
	depth := 0
	for j := i + 1; j < len(s); j++ {
		if j+1 < len(s) && s[j] == '[' && s[j+1] == '[' {
			depth++
			j++
			continue
		}
		if depth > 0 && j+1 < len(s) && s[j] == ']' && s[j+1] == ']' {
			depth--
			j++
			continue
		}
		if s[j] == ']' && depth == 0 {
			return j
		}
	}
	return -1
}

func (p *parser) parseExternal(s string, i int) (string, int, bool) {
	end := externalEnd(s, i)
	if end < 0 {
		return "", 0, false
	}
	body := s[i+1 : end]
	next := end + 1
	sp := strings.IndexAny(body, " \t")
	var u, label, class string
	if sp < 0 {
		u = body
		p.extN++
		label = "[" + strconv.Itoa(p.extN) + "]"
		class = "external autonumber"
	} else {
		u = body[:sp]
		label = strings.TrimSpace(body[sp:])
		class = "external text"
	}
	if !safeURL(u) {
		return html.EscapeString(s[i:next]), next, true
	}
	lab := label
	if sp >= 0 {
		lab = p.inline(label)
	} else {
		lab = html.EscapeString(label)
	}
	return `<a rel="nofollow" class="` + class + `" href="` + html.EscapeString(u) + `">` + lab + `</a>`, next, true
}

func (p *parser) parseBareURL(s string, i int) (string, int, bool) {
	j := i
	for j < len(s) && s[j] > ' ' && s[j] != '<' && s[j] != ']' && s[j] != '|' && s[j] != '"' && s[j] != '\'' {
		j++
	}
	u := strings.TrimRight(s[i:j], ".,;:")
	if !safeURL(u) {
		return "", 0, false
	}
	return `<a rel="nofollow" class="external free" href="` + html.EscapeString(u) + `">` + html.EscapeString(u) + `</a>`, i + len(u), true
}

func safeURL(u string) bool {
	l := strings.ToLower(u)
	if strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:") {
		if strings.ContainsAny(u, "\"'<>") {
			return false
		}
		return true
	}
	return false
}

var (
	dangerousTags = map[string]bool{
		"script": true, "style": true, "iframe": true, "object": true, "embed": true,
		"svg": true, "math": true, "link": true, "meta": true, "base": true, "form": true,
		"input": true, "button": true, "textarea": true, "frame": true, "frameset": true,
		"applet": true, "video": true, "audio": true, "img": true, "source": true,
	}
	allowedTags = map[string]bool{
		"b": true, "strong": true, "i": true, "em": true, "u": true, "s": true, "strike": true,
		"sub": true, "sup": true, "br": true, "hr": true, "p": true, "blockquote": true,
		"code": true, "tt": true, "kbd": true, "samp": true, "var": true, "small": true, "big": true,
		"center": true, "span": true, "div": true, "abbr": true, "cite": true, "q": true,
		"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
		"table": true, "thead": true, "tbody": true, "tr": true, "th": true, "td": true, "caption": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	}
)

func (p *parser) parseAngle(s string, i int) (string, int, bool) {
	name, attrs, closing, selfClose, next, ok := readTag(s, i)
	if !ok {
		return "", 0, false
	}
	lname := strings.ToLower(name)
	if lname == "nowiki" {
		inner, n2, found := untilClose(s, next, "nowiki")
		if !found {
			p.warn("Unclosed <nowiki>")
			return html.EscapeString(s[i:next]), next, true
		}
		return html.EscapeString(inner), n2, true
	}
	if lname == "youtube" || lname == "embedvideo" {
		inner, n2, found := untilClose(s, next, lname)
		if !found {
			p.warn("Unclosed <" + lname + ">")
			return html.EscapeString(s[i:next]), next, true
		}
		return p.renderEmbedTag(lname, attrs, inner), n2, true
	}
	if dangerousTags[lname] {
		_, n2, found := untilClose(s, next, lname)
		raw := s[i:next]
		if found {
			raw = s[i:n2]
			next = n2
		}
		p.warn("Stripped unsafe tag <" + lname + ">")
		return `<span class="wikiwright-warning" title="Unsafe tag removed">` + html.EscapeString(raw) + `</span>`, next, true
	}
	if lname == "code" && !closing && !selfClose {
		inner, n2, found := untilClose(s, next, "code")
		if found {
			return "<code>" + p.inline(inner) + "</code>", n2, true
		}
	}
	if !allowedTags[lname] {
		// Prose placeholders such as "<name of package>" have bare words and no close tag.
		if !strings.Contains(attrs, "=") && !selfClose && !closing {
			if _, _, found := untilClose(s, next, lname); !found {
				return "", 0, false
			}
		}
		_, n2, found := untilClose(s, next, lname)
		raw := s[i:next]
		end := next
		if found && !closing {
			raw = s[i:n2]
			end = n2
		}
		p.warn("Unknown tag <" + lname + ">")
		return `<span class="wikiwright-warning" title="Unknown tag">` + html.EscapeString(raw) + `</span>`, end, true
	}
	if selfClose || lname == "br" || lname == "hr" {
		if lname == "br" {
			return "<br />", next, true
		}
		if lname == "hr" {
			return "<hr />", next, true
		}
		return "<" + lname + " />", next, true
	}
	if closing {
		return "</" + lname + ">", next, true
	}
	extra := ""
	if lname == "td" || lname == "th" {
		if n := attrInt(attrs, "colspan"); n > 1 {
			extra += ` colspan="` + strconv.Itoa(n) + `"`
		}
		if n := attrInt(attrs, "rowspan"); n > 1 {
			extra += ` rowspan="` + strconv.Itoa(n) + `"`
		}
	}
	return "<" + lname + extra + ">", next, true
}

func (p *parser) renderEmbedTag(tag, attrs, inner string) string {
	att := parseAttrs(attrs)
	service := strings.ToLower(att["service"])
	if tag == "youtube" || service == "" && tag == "youtube" {
		service = "youtube"
	}
	if service == "" {
		service = "youtube"
	}
	dim := att["dimensions"]
	if dim == "" && att["width"] != "" {
		dim = att["width"]
		if att["height"] != "" {
			dim = strings.TrimSuffix(att["width"], "px") + "x" + strings.TrimSuffix(att["height"], "px")
		}
	}
	align := att["alignment"]
	if align == "" {
		align = att["align"]
	}
	caption := att["description"]
	tok := p.embedVideo(service, strings.TrimSpace(inner), dim, align, caption)
	if strings.HasPrefix(tok, "\x00") && strings.HasSuffix(tok, "\x00") && len(tok) >= 2 {
		return p.renderToken(strings.Trim(tok, "\x00"))
	}
	return tok
}

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`([A-Za-z_:][-A-Za-z0-9_:.]*)\s*=\s*("([^"]*)"|'([^']*)')`)
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		val := m[3]
		if m[2][0] == '\'' {
			val = m[4]
		}
		out[strings.ToLower(m[1])] = val
	}
	return out
}

func readTag(s string, i int) (name, attrs string, closing, selfClose bool, next int, ok bool) {
	if i >= len(s) || s[i] != '<' {
		return
	}
	j := i + 1
	if j < len(s) && s[j] == '/' {
		closing = true
		j++
	}
	start := j
	if j >= len(s) || !isNameStart(s[j]) {
		return
	}
	j++
	for j < len(s) && isNameChar(s[j]) {
		j++
	}
	name = s[start:j]
	k := j
	inQ := byte(0)
	for k < len(s) {
		c := s[k]
		if inQ != 0 {
			if c == inQ {
				inQ = 0
			}
			k++
			continue
		}
		if c == '"' || c == '\'' {
			inQ = c
			k++
			continue
		}
		if c == '>' {
			break
		}
		k++
	}
	if k >= len(s) || s[k] != '>' {
		return
	}
	attrs = s[j:k]
	trim := strings.TrimSpace(attrs)
	selfClose = strings.HasSuffix(trim, "/")
	if !wellFormedAttrs(trim) {
		return
	}
	return name, attrs, closing, selfClose, k + 1, true
}

func wellFormedAttrs(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i >= len(s) {
			return true
		}
		if !isNameStart(s[i]) {
			return false
		}
		i++
		for i < len(s) && isNameChar(s[i]) {
			i++
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			if i >= len(s) {
				return false
			}
			if s[i] == '"' || s[i] == '\'' {
				q := s[i]
				i++
				for i < len(s) && s[i] != q {
					i++
				}
				if i >= len(s) {
					return false
				}
				i++
			} else {
				for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' {
					i++
				}
			}
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

func untilClose(s string, from int, tag string) (inner string, next int, ok bool) {
	if from < 0 || from > len(s) {
		return "", 0, false
	}
	// Compare in place. strings.ToLower can change the length of invalid
	// UTF-8, and an index into that copy is not an index into s.
	needle := "</" + strings.ToLower(tag)
	rest := s[from:]
	idx := indexASCIIFold(rest, needle)
	if idx < 0 {
		return "", 0, false
	}
	abs := from + idx
	if abs > len(s) {
		return "", 0, false
	}
	gt := strings.IndexByte(s[abs:], '>')
	if gt < 0 || abs+gt+1 > len(s) {
		return "", 0, false
	}
	return s[from:abs], abs + gt + 1, true
}

func indexASCIIFold(s, needle string) int {
	n := len(needle)
	if n == 0 || n > len(s) {
		return -1
	}
	for i := 0; i+n <= len(s); i++ {
		if asciiEqualFold(s[i:i+n], needle) {
			return i
		}
	}
	return -1
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func findPair(s string, i int, open, close string) int {
	depth := 1
	for i < len(s) {
		if strings.HasPrefix(s[i:], open) {
			depth++
			i += len(open)
			continue
		}
		if strings.HasPrefix(s[i:], close) {
			depth--
			if depth == 0 {
				return i
			}
			i += len(close)
			continue
		}
		i++
	}
	return -1
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteString("%20")
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
