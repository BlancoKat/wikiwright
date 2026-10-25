package mwxml

import (
	"encoding/xml"
	"io"
	"net"
	"strings"
	"time"
)

// Write emits an export-0.10 dump that Parse can read back. It is the cache
// format for an API import: fetched pages are saved before reindex, and a
// later run can load the file instead of downloading them again.
func Write(w io.Writer, pages []Page) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "<mediawiki xmlns=\"http://www.mediawiki.org/xml/export-0.10/\" version=\"0.10\" xml:lang=\"en\">\n"); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("  ", "  ")
	for _, pg := range pages {
		if err := enc.Encode(toXMLPage(pg)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "</mediawiki>\n")
	return err
}

type xmlPage struct {
	XMLName   xml.Name `xml:"page"`
	Title     string   `xml:"title"`
	NS        int      `xml:"ns"`
	Revisions []xmlRev `xml:"revision"`
}

type xmlRev struct {
	ID        int64     `xml:"id,omitempty"`
	ParentID  int64     `xml:"parentid,omitempty"`
	Timestamp string    `xml:"timestamp"`
	Contrib   xmlUser   `xml:"contributor"`
	Minor     *struct{} `xml:"minor,omitempty"`
	Comment   string    `xml:"comment,omitempty"`
	Text      xmlText   `xml:"text"`
	Model     string    `xml:"model,omitempty"`
}

type xmlUser struct {
	Username string `xml:"username,omitempty"`
	IP       string `xml:"ip,omitempty"`
}

type xmlText struct {
	XMLSpace string `xml:"xml:space,attr,omitempty"`
	Deleted  string `xml:"deleted,attr,omitempty"`
	Body     string `xml:",chardata"`
}

func toXMLPage(pg Page) xmlPage {
	out := xmlPage{Title: xmlSafe(pg.Title), NS: pg.NS}
	for _, rev := range pg.Revisions {
		xr := xmlRev{
			ID:        rev.MWRevID,
			ParentID:  rev.ParentID,
			Timestamp: rev.Time.UTC().Format(time.RFC3339),
			Comment:   xmlSafe(rev.Comment),
			Model:     xmlSafe(rev.Model),
		}
		if rev.Time.IsZero() {
			xr.Timestamp = time.Unix(0, 0).UTC().Format(time.RFC3339)
		}
		if rev.Minor {
			xr.Minor = &struct{}{}
		}
		user := xmlSafe(rev.User)
		if user != "" && net.ParseIP(user) != nil {
			xr.Contrib.IP = user
		} else {
			xr.Contrib.Username = user
		}
		xr.Text.XMLSpace = "preserve"
		if rev.Deleted {
			xr.Text.Deleted = "deleted"
		} else {
			xr.Text.Body = xmlSafe(rev.Text)
		}
		out.Revisions = append(out.Revisions, xr)
	}
	return out
}

func xmlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0x9 || r == 0xA || r == 0xD:
			return r
		case r >= 0x20 && r <= 0xD7FF:
			return r
		case r >= 0xE000 && r <= 0xFFFD:
			return r
		case r >= 0x10000 && r <= 0x10FFFF:
			return r
		default:
			return -1
		}
	}, s)
}
