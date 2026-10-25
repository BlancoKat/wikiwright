package parser

import (
	"regexp"
	"strconv"
	"strings"
)

// Section is one wikitext section in page source. Index 0 is the lead, before
// the first heading. Later indexes follow MediaWiki: only headings in the page
// source count, not headings that come from templates, nowiki, comments, or
// includeonly.
type Section struct {
	Index int
	Level int
	Title string
	Start int
	End   int
}

// Sections lists editable sections in source order. The lead is included only
// when the page has no headings; otherwise index 0 is still reachable through
// LookupSection.
func Sections(src string) []Section {
	src = normNL(src)
	var heads []Section
	var st scanState
	idx := 1
	for i := 0; i <= len(src); {
		if i == len(src) {
			break
		}
		rel := strings.IndexByte(src[i:], '\n')
		abs := len(src)
		if rel >= 0 {
			abs = i + rel
		}
		line := src[i:abs]
		if st.clear() {
			if level, text, ok := headingLine(line); ok {
				heads = append(heads, Section{Index: idx, Level: level, Title: text, Start: i})
				idx++
			}
		}
		st.advance(line)
		if rel < 0 {
			break
		}
		i = abs + 1
	}
	for i := range heads {
		heads[i].End = len(src)
		for j := i + 1; j < len(heads); j++ {
			if heads[j].Level <= heads[i].Level {
				heads[i].End = heads[j].Start
				break
			}
		}
	}
	return heads
}

// LookupSection returns the wikitext of section id ("0", "1", ...).
func LookupSection(src, id string) (string, Section, bool) {
	src = normNL(src)
	n, ok := sectionNumber(id)
	if !ok {
		return "", Section{}, false
	}
	heads := Sections(src)
	if n == 0 {
		end := len(src)
		if len(heads) > 0 {
			end = heads[0].Start
		}
		return strings.TrimRight(src[:end], "\n"), Section{Index: 0, End: end}, true
	}
	for _, h := range heads {
		if h.Index == n {
			return strings.TrimRight(src[h.Start:h.End], "\n"), h, true
		}
	}
	return "", Section{}, false
}

// ReplaceSection splices newText over section id. Missing sections return
// false and leave the page unchanged. newText is trimmed on the right the way
// MediaWiki trims the edit box.
func ReplaceSection(src, id, newText string) (string, bool) {
	src = normNL(src)
	newText = strings.TrimRight(newText, " \t\r\n")
	n, ok := sectionNumber(id)
	if !ok {
		return src, false
	}
	heads := Sections(src)
	if n == 0 {
		end := len(src)
		if len(heads) > 0 {
			end = heads[0].Start
		}
		return joinSection(src[:0], newText, src[end:]), true
	}
	for _, h := range heads {
		if h.Index == n {
			return joinSection(src[:h.Start], newText, src[h.End:]), true
		}
	}
	return src, false
}

func joinSection(before, body, after string) string {
	if body == "" {
		return before + after
	}
	if after == "" {
		if before == "" {
			return body
		}
		return strings.TrimRight(before, "\n") + "\n\n" + body
	}
	return before + body + "\n\n" + strings.TrimLeft(after, "\n")
}

// AppendNewSection adds a level-2 section at the end of the page.
func AppendNewSection(src, subject, body string) string {
	src = normNL(src)
	body = strings.TrimRight(body, " \t\r\n")
	subject = cleanSubject(subject)
	var b strings.Builder
	if strings.TrimSpace(src) != "" {
		b.WriteString(strings.TrimRight(src, "\n"))
		b.WriteString("\n\n")
	}
	if subject != "" {
		b.WriteString("== ")
		b.WriteString(subject)
		b.WriteString(" ==\n\n")
	}
	b.WriteString(body)
	return b.String()
}

func cleanSubject(subject string) string {
	subject = strings.TrimSpace(subject)
	subject = strings.ReplaceAll(subject, "\n", " ")
	subject = strings.TrimSpace(equalsWrap.ReplaceAllString(subject, "$1"))
	return strings.Join(strings.Fields(subject), " ")
}

var equalsWrap = regexp.MustCompile(`(?s)^\s*=+\s*(.*?)\s*=+\s*$`)

// SectionEditSummary is MediaWiki's prefilled summary for an existing section.
func SectionEditSummary(title string) string {
	title = StripSectionName(title)
	if title == "" {
		return ""
	}
	return "/* " + title + " */ "
}

// NewSectionSummary is the automatic summary for section=new.
func NewSectionSummary(subject string) string {
	subject = StripSectionName(cleanSubject(subject))
	if subject == "" {
		return ""
	}
	return "/* " + subject + " */ new section"
}

// HeadingTitle is the first heading when the text itself is that section.
func HeadingTitle(text string) (string, bool) {
	_, sec, ok := LookupSection(text, "1")
	if !ok || sec.Title == "" {
		return "", false
	}
	if strings.TrimLeft(text[:sec.Start], "\n") != "" {
		return "", false
	}
	return sec.Title, true
}

// Anchor is the fragment for a section heading.
func Anchor(title string) string {
	title = StripSectionName(title)
	if title == "" {
		return ""
	}
	return legacyFragment(title)
}

var (
	sectionLink     = regexp.MustCompile(`\[\[:?([^\[\]|]+)\|([^\[\]]+)\]\]`)
	sectionLinkBare = regexp.MustCompile(`\[\[:?([^\[\]]+?)(?:\|)?\]\]`)
	sectionExt      = regexp.MustCompile(`(?i)\[(?:https?://|mailto:)[^\[\]\s]+ +([^\[\]]+)\]`)
	sectionQuotes   = regexp.MustCompile(`'{2,}`)
	sectionTags     = regexp.MustCompile(`<[^>\n]*>`)
)

// StripSectionName removes the wikitext MediaWiki drops from a section comment.
func StripSectionName(s string) string {
	s = sectionLink.ReplaceAllString(s, "$2")
	s = sectionLinkBare.ReplaceAllString(s, "$1")
	s = sectionExt.ReplaceAllString(s, "$1")
	s = sectionQuotes.ReplaceAllString(s, "")
	s = sectionTags.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func sectionNumber(id string) (int, bool) {
	id = strings.TrimSpace(id)
	if id == "" || id == "new" {
		return 0, false
	}
	n, err := strconv.Atoi(id)
	if err != nil || n < 0 {
		return 0, false
	}
	if n == 0 {
		return 0, id == "0"
	}
	// "01" is not a MediaWiki section id.
	return n, strconv.Itoa(n) == id
}

func normNL(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func headingLine(line string) (int, string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return 0, "", false
	}
	return parseHeading(strings.TrimSpace(line))
}

type scanState struct {
	brace       int
	comment     bool
	nowiki      bool
	pre         bool
	includeonly bool
	table       int
}

func (s scanState) clear() bool {
	return s.brace == 0 && !s.comment && !s.nowiki && !s.pre && !s.includeonly && s.table == 0
}

func (s *scanState) advance(line string) {
	trim := strings.TrimSpace(line)
	i := 0
	for i < len(line) {
		if s.comment {
			if strings.HasPrefix(line[i:], "-->") {
				s.comment = false
				i += 3
				continue
			}
			i++
			continue
		}
		if s.nowiki {
			if closeTagAt(line, i, "nowiki") {
				s.nowiki = false
				i = skipTag(line, i)
				continue
			}
			i++
			continue
		}
		if s.pre {
			if closeTagAt(line, i, "pre") {
				s.pre = false
				i = skipTag(line, i)
				continue
			}
			i++
			continue
		}
		if s.includeonly {
			if closeTagAt(line, i, "includeonly") {
				s.includeonly = false
				i = skipTag(line, i)
				continue
			}
			i++
			continue
		}
		if strings.HasPrefix(line[i:], "<!--") {
			s.comment = true
			i += 4
			continue
		}
		if openTagAt(line, i, "nowiki") {
			s.nowiki = true
			i = skipTag(line, i)
			continue
		}
		if openTagAt(line, i, "pre") {
			s.pre = true
			i = skipTag(line, i)
			continue
		}
		if openTagAt(line, i, "includeonly") {
			s.includeonly = true
			i = skipTag(line, i)
			continue
		}
		if line[i] == '{' && i+1 < len(line) && line[i+1] == '|' {
			i += 2
			continue
		}
		if line[i] == '{' {
			s.brace++
			i++
			continue
		}
		if line[i] == '}' && s.brace > 0 {
			s.brace--
			i++
			continue
		}
		i++
	}
	if s.comment || s.nowiki || s.pre || s.includeonly || s.brace > 0 {
		return
	}
	if strings.HasPrefix(trim, "{|") {
		s.table++
	} else if s.table > 0 && strings.HasPrefix(trim, "|}") {
		s.table--
	}
}

func openTagAt(line string, i int, name string) bool {
	if i >= len(line) || line[i] != '<' {
		return false
	}
	rest := line[i+1:]
	if len(rest) < len(name) || !strings.EqualFold(rest[:len(name)], name) {
		return false
	}
	if len(rest) == len(name) {
		return false
	}
	c := rest[len(name)]
	return c == '>' || c == ' ' || c == '\t' || c == '/'
}

func closeTagAt(line string, i int, name string) bool {
	if i+2+len(name) > len(line) || line[i] != '<' || line[i+1] != '/' {
		return false
	}
	return strings.EqualFold(line[i+2:i+2+len(name)], name)
}

func skipTag(line string, i int) int {
	j := strings.IndexByte(line[i:], '>')
	if j < 0 {
		return len(line)
	}
	return i + j + 1
}
