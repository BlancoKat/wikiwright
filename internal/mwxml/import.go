// Package mwxml imports MediaWiki XML dumps.
//
// It accepts the formats her wikis actually emit:
//   - schema 0.10 (MediaWiki 1.32-era): a revision's wikitext is a direct <text> child
//   - schema 0.11 (1.35 through 1.39, and 1.43 dumpBackup): the main slot is still a
//     direct <text>, preceded by <origin>, <model>, and <format>
//   - schema 0.11 multi-content revisions: extra slots are wrapped in <content><role>…</role>.
//     1.43 writes the main slot inline and every other slot inside <content>. Some
//     exporters put the main slot itself in <content><role>main</role> (or the older
//     draft element <slot>main</slot>). Those are accepted too.
//
// Non-main slots are ignored. Deleted text is stored as an empty revision so history
// stays intact. Stub dumps that only carry a text location and no body are stored
// empty and reported.
package mwxml

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/title"
)

// Stats counts what an import did.
type Stats struct {
	Pages     int
	Revisions int
	Skipped   int
	Warnings  []string
}

// Revision is one imported revision, in dump order.
type Revision struct {
	MWRevID  int64
	ParentID int64
	Time     time.Time
	User     string
	Comment  string
	Minor    bool
	Text     string
	Deleted  bool
	Model    string
}

// Page is one imported page.
type Page struct {
	Title     string
	NS        int
	Redirect  string
	Revisions []Revision
}

// Parse reads a dump and returns pages. It does not touch a database.
func Parse(r io.Reader) ([]Page, Stats, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	td := xml.NewTokenDecoder(nsStrip{dec})
	var pages []Page
	var stats Stats
	for {
		tok, err := td.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, stats, fmt.Errorf("xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "page" {
			continue
		}
		var raw rawPage
		if err := td.DecodeElement(&raw, &se); err != nil {
			return nil, stats, fmt.Errorf("page: %w", err)
		}
		pg, warn := convert(raw)
		if warn != "" {
			stats.Warnings = append(stats.Warnings, warn)
		}
		if pg.Title == "" {
			stats.Skipped++
			continue
		}
		stats.Pages++
		stats.Revisions += len(pg.Revisions)
		pages = append(pages, pg)
	}
	return pages, stats, nil
}

type rawPage struct {
	Title    string `xml:"title"`
	NS       int    `xml:"ns"`
	Redirect *struct {
		Title string `xml:"title,attr"`
	} `xml:"redirect"`
	Revisions []rawRev `xml:"revision"`
}

type rawRev struct {
	ID       int64
	ParentID int64
	Time     string
	User     string
	DeletedU bool
	Comment  string
	DelCom   bool
	Minor    bool
	HasText  bool
	Text     rawText
	Model    string
	Slots    []rawSlot
}

type rawText struct {
	Deleted string `xml:"deleted,attr"`
	Body    string `xml:",chardata"`
}

type rawSlot struct {
	Role    string
	Model   string
	HasText bool
	Text    rawText
}

type rawContrib struct {
	Deleted  string `xml:"deleted,attr"`
	Username string `xml:"username"`
	IP       string `xml:"ip"`
}

type rawComment struct {
	Deleted string `xml:"deleted,attr"`
	Value   string `xml:",chardata"`
}

func (rev *rawRev) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	inContent := false
	var slot *rawSlot
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "id":
				var v int64
				if err := d.DecodeElement(&v, &el); err != nil {
					return err
				}
				if !inContent && rev.ID == 0 {
					rev.ID = v
				}
			case "parentid":
				var v int64
				if err := d.DecodeElement(&v, &el); err != nil {
					return err
				}
				if !inContent {
					rev.ParentID = v
				}
			case "timestamp":
				var v string
				if err := d.DecodeElement(&v, &el); err != nil {
					return err
				}
				if !inContent {
					rev.Time = v
				}
			case "contributor":
				var c rawContrib
				if err := d.DecodeElement(&c, &el); err != nil {
					return err
				}
				rev.DeletedU = c.Deleted != ""
				if c.Username != "" {
					rev.User = c.Username
				} else {
					rev.User = c.IP
				}
			case "minor":
				rev.Minor = true
				if err := d.Skip(); err != nil {
					return err
				}
			case "comment":
				var c rawComment
				if err := d.DecodeElement(&c, &el); err != nil {
					return err
				}
				if !inContent {
					rev.DelCom = c.Deleted != ""
					rev.Comment = c.Value
				}
			case "model":
				var v string
				if err := d.DecodeElement(&v, &el); err != nil {
					return err
				}
				if inContent && slot != nil {
					slot.Model = v
				} else {
					rev.Model = v
				}
			case "text":
				var t rawText
				if err := d.DecodeElement(&t, &el); err != nil {
					return err
				}
				if inContent && slot != nil {
					slot.Text = t
					slot.HasText = true
				} else {
					rev.Text = t
					rev.HasText = true
				}
			case "content":
				inContent = true
				slot = &rawSlot{}
				for _, a := range el.Attr {
					if a.Name.Local == "role" && slot.Role == "" {
						slot.Role = a.Value
					}
				}
			case "role", "slot":
				var v string
				if err := d.DecodeElement(&v, &el); err != nil {
					return err
				}
				if inContent && slot != nil && slot.Role == "" {
					slot.Role = v
				}
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if el.Name.Local == "content" && inContent {
				if slot != nil {
					rev.Slots = append(rev.Slots, *slot)
				}
				slot = nil
				inContent = false
				continue
			}
			if el.Name.Local == start.Name.Local && !inContent {
				return nil
			}
		}
	}
}

func convert(raw rawPage) (Page, string) {
	pg := Page{Title: strings.TrimSpace(raw.Title), NS: raw.NS}
	if raw.Redirect != nil {
		pg.Redirect = raw.Redirect.Title
	}
	var notes []string
	for _, rr := range raw.Revisions {
		rev := Revision{
			MWRevID:  rr.ID,
			ParentID: rr.ParentID,
			User:     rr.User,
			Comment:  rr.Comment,
			Minor:    rr.Minor,
			Model:    rr.Model,
		}
		if rr.DeletedU {
			rev.User = ""
		}
		if rr.DelCom {
			rev.Comment = ""
		}
		if t, err := time.Parse(time.RFC3339, rr.Time); err == nil {
			rev.Time = t.UTC()
		}
		text, deleted, stub, model := pickMain(rr)
		if model != "" {
			rev.Model = model
		}
		rev.Text = text
		rev.Deleted = deleted
		if stub {
			notes = append(notes, fmt.Sprintf("%s rev %d has no main-slot text (stub or missing slot)", pg.Title, rr.ID))
		}
		if pg.Redirect != "" && !strings.Contains(strings.ToLower(rev.Text), "#redirect") && !deleted {
			rev.Text = "#REDIRECT [[" + pg.Redirect + "]]\n" + rev.Text
		}
		pg.Revisions = append(pg.Revisions, rev)
	}
	return pg, strings.Join(notes, "; ")
}

// pickMain chooses the wikitext body.
// Direct <text> under <revision> wins when it is present and not deleted: that is
// how 1.32–1.43 write the main slot. A <content> whose role/slot is "main" is the
// fallback for exporters that wrap every slot. Other roles are ignored.
func pickMain(rr rawRev) (text string, deleted, stub bool, model string) {
	if rr.HasText {
		model = rr.Model
		if strings.EqualFold(rr.Text.Deleted, "deleted") {
			if alt, altModel, ok := mainSlot(rr.Slots); ok {
				return alt.Body, strings.EqualFold(alt.Deleted, "deleted"), false, altModel
			}
			return "", true, false, model
		}
		return rr.Text.Body, false, false, model
	}
	if alt, altModel, ok := mainSlot(rr.Slots); ok {
		if strings.EqualFold(alt.Deleted, "deleted") {
			return "", true, false, altModel
		}
		return alt.Body, false, false, altModel
	}
	return "", false, true, rr.Model
}

func mainSlot(slots []rawSlot) (rawText, string, bool) {
	for _, s := range slots {
		role := strings.ToLower(strings.TrimSpace(s.Role))
		if role == "main" && s.HasText {
			return s.Text, s.Model, true
		}
	}
	return rawText{}, "", false
}

// DBKey canonicalizes a dump title for storage.
func DBKey(rawTitle, project string) (title.T, bool) {
	t := title.Parse(rawTitle, project)
	if !t.Legal() || t.Namespace < 0 {
		return t, false
	}
	return t, true
}

type nsStrip struct{ d *xml.Decoder }

func (n nsStrip) Token() (xml.Token, error) {
	t, err := n.d.Token()
	if err != nil {
		return nil, err
	}
	switch el := t.(type) {
	case xml.StartElement:
		el.Name.Space = ""
		for i := range el.Attr {
			el.Attr[i].Name.Space = ""
		}
		return el, nil
	case xml.EndElement:
		el.Name.Space = ""
		return el, nil
	default:
		return t, nil
	}
}
