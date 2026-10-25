// Package title normalizes MediaWiki page titles the way a first-letter
// case wiki does: underscores and spaces match, and the first letter is
// uppercased.
package title

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	NSMedia         = -2
	NSSpecial       = -1
	NSMain          = 0
	NSTalk          = 1
	NSUser          = 2
	NSUserTalk      = 3
	NSProject       = 4
	NSProjectTalk   = 5
	NSFile          = 6
	NSFileTalk      = 7
	NSMediaWiki     = 8
	NSMediaWikiTalk = 9
	NSTemplate      = 10
	NSTemplateTalk  = 11
	NSHelp          = 12
	NSHelpTalk      = 13
	NSCategory      = 14
	NSCategoryTalk  = 15
)

// T is a parsed title.
type T struct {
	Namespace int
	// Rest is the title without a namespace, spaces, first letter uppercased.
	Rest string
	// DBKey is the canonical key, underscores, with a namespace prefix when needed.
	DBKey string
	// Display is the human title, spaces, with a namespace prefix when needed.
	Display string
}

type nsName struct {
	id    int
	names []string
}

func namespaces(project string) []nsName {
	proj := strings.TrimSpace(project)
	if proj == "" {
		proj = "Project"
	}
	return []nsName{
		{NSMedia, []string{"Media"}},
		{NSSpecial, []string{"Special"}},
		{NSTalk, []string{"Talk"}},
		{NSUserTalk, []string{"User talk", "User_talk"}},
		{NSUser, []string{"User"}},
		{NSProjectTalk, []string{"Project talk", "Project_talk", proj + " talk", strings.ReplaceAll(proj, " ", "_") + "_talk"}},
		{NSProject, []string{"Project", proj}},
		{NSFileTalk, []string{"File talk", "File_talk", "Image talk", "Image_talk"}},
		{NSFile, []string{"File", "Image"}},
		{NSMediaWikiTalk, []string{"MediaWiki talk", "MediaWiki_talk"}},
		{NSMediaWiki, []string{"MediaWiki"}},
		{NSTemplateTalk, []string{"Template talk", "Template_talk"}},
		{NSTemplate, []string{"Template"}},
		{NSHelpTalk, []string{"Help talk", "Help_talk"}},
		{NSHelp, []string{"Help"}},
		{NSCategoryTalk, []string{"Category talk", "Category_talk"}},
		{NSCategory, []string{"Category"}},
	}
}

// Canonical is the English canonical prefix, without a trailing colon.
func Canonical(ns int, project string) string {
	switch ns {
	case NSMedia:
		return "Media"
	case NSSpecial:
		return "Special"
	case NSMain:
		return ""
	case NSTalk:
		return "Talk"
	case NSUser:
		return "User"
	case NSUserTalk:
		return "User talk"
	case NSProject:
		if strings.TrimSpace(project) != "" {
			return strings.TrimSpace(project)
		}
		return "Project"
	case NSProjectTalk:
		if strings.TrimSpace(project) != "" {
			return strings.TrimSpace(project) + " talk"
		}
		return "Project talk"
	case NSFile:
		return "File"
	case NSFileTalk:
		return "File talk"
	case NSMediaWiki:
		return "MediaWiki"
	case NSMediaWikiTalk:
		return "MediaWiki talk"
	case NSTemplate:
		return "Template"
	case NSTemplateTalk:
		return "Template talk"
	case NSHelp:
		return "Help"
	case NSHelpTalk:
		return "Help talk"
	case NSCategory:
		return "Category"
	case NSCategoryTalk:
		return "Category talk"
	default:
		return ""
	}
}

// Parse splits a raw title. Fragments (#...) must already have been removed.
// An empty or illegal title returns a main-namespace T with OK false via Valid.
func Parse(raw, project string) T {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "_", " ")
	raw = strings.TrimSpace(raw)
	for strings.HasPrefix(raw, ":") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, ":"))
	}
	ns := NSMain
	rest := raw
	if i := strings.Index(raw, ":"); i > 0 {
		prefix := raw[:i]
		if id, ok := lookupNS(prefix, project); ok {
			ns = id
			rest = strings.TrimSpace(raw[i+1:])
		}
	}
	rest = cleanRest(rest)
	return makeT(ns, rest, project)
}

func lookupNS(prefix, project string) (int, bool) {
	want := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(prefix), "_", " "))
	want = strings.Join(strings.Fields(want), " ")
	var best int
	found := false
	bestLen := -1
	for _, ns := range namespaces(project) {
		for _, name := range ns.names {
			n := strings.ToLower(strings.ReplaceAll(name, "_", " "))
			n = strings.Join(strings.Fields(n), " ")
			if n == want && len(n) > bestLen {
				best = ns.id
				bestLen = len(n)
				found = true
			}
		}
	}
	return best, found
}

func cleanRest(rest string) string {
	rest = strings.Join(strings.Fields(strings.ReplaceAll(rest, "_", " ")), " ")
	if rest == "" {
		return ""
	}
	return upperFirst(rest)
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 && s == "" {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

func makeT(ns int, rest, project string) T {
	t := T{Namespace: ns, Rest: rest}
	prefix := Canonical(ns, project)
	if prefix == "" {
		t.Display = rest
		t.DBKey = strings.ReplaceAll(rest, " ", "_")
	} else {
		t.Display = prefix + ":" + rest
		t.DBKey = strings.ReplaceAll(prefix, " ", "_") + ":" + strings.ReplaceAll(rest, " ", "_")
	}
	// File namespace is stored under the canonical File: prefix so Image: and
	// File: land on the same page.
	if ns == NSFile {
		t.DBKey = "File:" + strings.ReplaceAll(rest, " ", "_")
		t.Display = "File:" + rest
	}
	if ns == NSFileTalk {
		t.DBKey = "File_talk:" + strings.ReplaceAll(rest, " ", "_")
		t.Display = "File talk:" + rest
	}
	return t
}

// Legal reports whether a title can be stored. Special pages are legal to link
// to but are not stored as articles.
func (t T) Legal() bool {
	if t.Rest == "" && t.Namespace != NSMain {
		return false
	}
	if t.Rest == "" && t.Namespace == NSMain {
		return false
	}
	if strings.ContainsAny(t.Rest, "#<>[]{}|") {
		return false
	}
	return true
}

// TransclusionKey resolves a template-call name such as "fhb bottom",
// "Template:Foo", or ":Main Page" to a DBKey.
func TransclusionKey(name, project string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, ":") {
		return Parse(name, project).DBKey
	}
	if i := strings.Index(name, ":"); i > 0 {
		if _, ok := lookupNS(name[:i], project); ok {
			return Parse(name, project).DBKey
		}
	}
	return Parse("Template:"+name, project).DBKey
}

// URLQuery escapes a DBKey for a title= query, leaving colons readable.
func URLQuery(dbkey string) string {
	var b strings.Builder
	for i := 0; i < len(dbkey); i++ {
		c := dbkey[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || c == ':' || c == '/' {
			b.WriteByte(c)
		} else if c == ' ' {
			b.WriteByte('_')
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}

// EqualKey reports whether two keys refer to the same page.
func EqualKey(a, b string) bool {
	return Parse(strings.ReplaceAll(a, "_", " "), "").DBKey == Parse(strings.ReplaceAll(b, "_", " "), "").DBKey
}
