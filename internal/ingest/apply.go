// Package ingest loads parsed dumps and image directories into a wiki store
// and rebuilds links, categories, and redirects.
package ingest

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/webp"

	"github.com/BlancoKat/wikiwright/internal/mwxml"
	"github.com/BlancoKat/wikiwright/internal/parser"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

// reindexHook, when set by tests, runs just before a page is parsed.
var reindexHook func(title string)

func init() {
	// webp registers itself on import in current x/image, but call DecodeConfig
	// explicitly below so a missing registration is still handled.
	_ = webp.DecodeConfig
}

// Apply writes pages and revisions, then reparses every page. The last revision
// in each page becomes current. MediaWiki revision ids are preserved as Wikiwright
// revision ids when they are free, so oldid permalinks from the original wiki
// keep working. Accounts in the users table are not read or written.
func Apply(st *store.Store, pages []mwxml.Page, project string) error {
	if err := StorePages(st, pages, project); err != nil {
		return err
	}
	return Reindex(st, project)
}

// StorePages writes pages and revisions and does not reindex. A revision whose
// MediaWiki id is already stored is left unchanged, so a rerun does not
// duplicate history. Users are not touched.
func StorePages(st *store.Store, pages []mwxml.Page, project string) error {
	for _, pg := range pages {
		t, ok := mwxml.DBKey(pg.Title, project)
		if !ok {
			continue
		}
		id, err := st.UpsertPage(t.Namespace, t.Rest, t.DBKey)
		if err != nil {
			return fmt.Errorf("page %s: %w", pg.Title, err)
		}
		for _, rev := range pg.Revisions {
			text := rev.Text
			comment := rev.Comment
			if rev.Deleted {
				text = ""
				if comment == "" {
					comment = "(text deleted in source wiki)"
				}
			}
			user := rev.User
			if user == "" {
				user = "unknown"
			}
			if _, err := st.AddRevision(id, user, comment, text, rev.Time, rev.Minor, rev.MWRevID, rev.ParentID); err != nil {
				return fmt.Errorf("rev %d of %s: %w", rev.MWRevID, pg.Title, err)
			}
		}
	}
	return nil
}

// Reindex reparses every latest revision.
func Reindex(st *store.Store, project string) error {
	pages, err := st.AllPages()
	if err != nil {
		return err
	}
	for _, pg := range pages {
		if err := ReindexPage(st, project, pg.ID); err != nil {
			return err
		}
	}
	return nil
}

// ReindexPage reparses one page's latest revision. A panic is logged with the
// page title and swallowed so the rest of the wiki can still be indexed.
func ReindexPage(st *store.Store, project string, pageID int64) (err error) {
	disp := fmt.Sprintf("page id %d", pageID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("reindex: panic on %q: %v\n%s", disp, rec, debug.Stack())
			err = nil
		}
	}()
	pg, err := st.PageByID(pageID)
	if err != nil {
		return err
	}
	if pg.ID == 0 {
		return nil
	}
	rev, err := st.LatestRevision(pageID)
	if err != nil {
		return err
	}
	keys, err := st.DBKeys()
	if err != nil {
		return err
	}
	disp = pg.Title
	if pg.Namespace != title.NSMain {
		disp = title.Canonical(pg.Namespace, project) + ":" + pg.Title
	}
	if reindexHook != nil {
		reindexHook(disp)
	}
	res := parser.Parse(rev.Content, options(st, project, disp, keys))
	redir := ""
	if res.IsRedirect {
		redir = res.Redirect
	}
	if err := st.SetRedirect(pageID, redir); err != nil {
		return err
	}
	var cats []store.CategoryRef
	for _, c := range res.Categories {
		cats = append(cats, store.CategoryRef{Name: c.Name, SortKey: c.SortKey})
	}
	return st.ReplaceAnnotations(pageID, res.Links, cats)
}

func options(st *store.Store, project, page string, keys map[string]bool) parser.Options {
	return parser.Options{
		PageTitle: page,
		ProjectNS: project,
		GetTemplate: func(dbkey string) (string, bool) {
			body, ok, err := st.ContentByDBKey(dbkey)
			if err != nil || !ok {
				return "", false
			}
			return body, true
		},
		Exists: func(dbkey string) bool { return keys[dbkey] },
		File: func(name string) (parser.FileInfo, bool) {
			f, err := st.File(name)
			if err != nil || f.Name == "" {
				return parser.FileInfo{}, false
			}
			return parser.FileInfo{Width: f.Width, Height: f.Height, MIME: f.MIME}, true
		},
	}
}

// Images copies an images directory (flat or MediaWiki's hashed layout) into
// the wiki file store and records each file. Thumbnails under a "thumb"
// directory are skipped.
func Images(st *store.Store, srcDir, destDir string) (int, error) {
	if srcDir == "" {
		return 0, nil
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", srcDir)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, err
	}
	n := 0
	err = filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if strings.EqualFold(fi.Name(), "thumb") || strings.EqualFold(fi.Name(), "temp") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(fi.Name()))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".svg":
		default:
			return nil
		}
		name := strings.ReplaceAll(fi.Name(), "_", " ")
		if err := copyFile(path, filepath.Join(destDir, filenameOnDisk(name))); err != nil {
			return err
		}
		rec, err := describeFile(filepath.Join(destDir, filenameOnDisk(name)), name, true)
		if err != nil {
			return err
		}
		if err := st.UpsertFile(rec); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// SaveUpload stores an uploaded file under destDir and records it.
// SVG is rejected here. Imports use SaveImported so a diagram from the
// source wiki, including a Wikimedia Commons SVG, can still be served.
func SaveUpload(st *store.Store, destDir, name string, r io.Reader, max int64, user, desc string) (store.FileRec, error) {
	return saveStored(st, destDir, name, r, max, user, desc, false, true)
}

// SaveImported is SaveUpload plus SVG, for files copied from a wiki rather than a browser upload.
func SaveImported(st *store.Store, destDir, name string, r io.Reader, max int64, user, desc string) (store.FileRec, error) {
	return saveStored(st, destDir, name, r, max, user, desc, true, false)
}

func saveStored(st *store.Store, destDir, name string, r io.Reader, max int64, user, desc string, allowSVG, canonicalize bool) (store.FileRec, error) {
	var err error
	name, err = ResolveUploadName(st, destDir, name, canonicalize)
	if err != nil {
		return store.FileRec{}, err
	}
	if name == "" {
		return store.FileRec{}, fmt.Errorf("missing file name")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return store.FileRec{}, err
	}
	disk := filepath.Join(destDir, filenameOnDisk(name))
	f, err := os.Create(disk)
	if err != nil {
		return store.FileRec{}, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if err != nil {
		os.Remove(disk)
		return store.FileRec{}, err
	}
	if n > max {
		os.Remove(disk)
		return store.FileRec{}, fmt.Errorf("file exceeds the %d byte upload limit", max)
	}
	if err := f.Close(); err != nil {
		return store.FileRec{}, err
	}
	rec, err := describeFile(disk, name, allowSVG)
	if err != nil {
		os.Remove(disk)
		return store.FileRec{}, err
	}
	if !allowedMIME(rec.MIME, allowSVG) {
		os.Remove(disk)
		return store.FileRec{}, fmt.Errorf("file type %s is not allowed", rec.MIME)
	}
	rec.UploadedBy = user
	rec.Description = desc
	if err := st.UpsertFile(rec); err != nil {
		return store.FileRec{}, err
	}
	return rec, nil
}

// RecordExisting notes a file that is already in destDir. It does not copy
// or truncate the file. Imports use this to resume after a partial download.
func RecordExisting(st *store.Store, destDir, name, user, mime string, w, h int) error {
	name = CleanName(name)
	if name == "" {
		return fmt.Errorf("missing file name")
	}
	disk := filepath.Join(destDir, filenameOnDisk(name))
	rec, err := describeFile(disk, name, true)
	if err != nil {
		return err
	}
	if (rec.MIME == "" || rec.MIME == "application/octet-stream") && mime != "" {
		rec.MIME = mime
	}
	if rec.Width == 0 && w > 0 {
		rec.Width = w
		rec.Height = h
	}
	rec.UploadedBy = user
	return st.UpsertFile(rec)
}

// ResolveUploadName chooses the name to store for an upload.
// A file already recorded under the same name, aside from the first letter's
// case or spaces versus underscores, keeps that stored name so a re-upload
// replaces it. When canonicalize is set and nothing is stored yet, the first
// letter is uppercased so the database row, the file on disk, and the File:
// title agree. A file already on disk under that name, including a different
// first letter, is kept too. Imports pass canonicalize false so a new source
// name is stored as given.
func ResolveUploadName(st *store.Store, destDir, name string, canonicalize bool) (string, error) {
	name = CleanName(name)
	if name == "" {
		return "", nil
	}
	if st != nil {
		f, err := st.File(name)
		if err != nil {
			return "", err
		}
		if f.Name != "" {
			return f.Name, nil
		}
	}
	if destDir != "" {
		diskName, ok, err := foldedDiskName(destDir, name)
		if err != nil {
			return "", err
		}
		if ok {
			return diskName, nil
		}
	}
	if canonicalize {
		return upperFirst(name), nil
	}
	return name, nil
}

// foldedDiskName returns a file already in destDir under the same name,
// ignoring first-letter case and spaces versus underscores.
func foldedDiskName(destDir, name string) (string, bool, error) {
	exact := filepath.Join(destDir, filenameOnDisk(name))
	ok, err := regularFile(exact)
	if err != nil {
		return "", false, err
	}
	if ok {
		return name, true, nil
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	var match string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !store.SameFileName(n, name) {
			continue
		}
		if match == "" || betterDiskName(n, match) {
			match = n
		}
	}
	if match == "" {
		return "", false, nil
	}
	return match, true, nil
}

func betterDiskName(candidate, current string) bool {
	cUp := candidate == upperFirst(candidate)
	uUp := current == upperFirst(current)
	if cUp != uUp {
		return cUp
	}
	return candidate < current
}

func regularFile(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}

// upperFirst matches title capitalization: one rune, Unicode simple upper case.
func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 && s == "" {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// CleanName strips any path and rejects odd characters.
func CleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return ""
	}
	return name
}

func filenameOnDisk(name string) string {
	// Keep the display name. Spaces are legal on the server filesystem.
	return name
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func describeFile(path, name string, allowSVG bool) (store.FileRec, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return store.FileRec{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return store.FileRec{}, err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	mime := sniff(head[:n], name)
	if allowSVG && isSVG(name, head[:n]) {
		mime = "image/svg+xml"
	}
	var w, h int
	if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			if cfg, _, err := image.DecodeConfig(f); err == nil {
				w, h = cfg.Width, cfg.Height
			}
		}
	}
	return store.FileRec{Name: name, MIME: mime, Size: fi.Size(), Width: w, Height: h}, nil
}

func sniff(head []byte, name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	// Prefer the extension when the content matches a known image, so a JPEG
	// named .jpg is image/jpeg even if DetectContentType is vague.
	detected := httpDetect(head)
	switch ext {
	case ".jpg", ".jpeg":
		if strings.HasPrefix(detected, "image/jpeg") || detected == "application/octet-stream" {
			return "image/jpeg"
		}
	case ".png":
		if strings.HasPrefix(detected, "image/png") || detected == "application/octet-stream" {
			return "image/png"
		}
	case ".gif":
		if strings.HasPrefix(detected, "image/gif") || detected == "application/octet-stream" {
			return "image/gif"
		}
	case ".webp":
		if strings.Contains(detected, "webp") || detected == "application/octet-stream" || detected == "image/webp" {
			return "image/webp"
		}
	case ".pdf":
		if detected == "application/pdf" || (len(head) >= 4 && string(head[:4]) == "%PDF") {
			return "application/pdf"
		}
	}
	return detected
}

func httpDetect(head []byte) string {
	// local copy of the small set we care about, so this package does not need net/http just for sniffing
	if len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
		return "image/jpeg"
	}
	if len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(head) >= 4 && string(head[:4]) == "%PDF" {
		return "application/pdf"
	}
	return "application/octet-stream"
}

func allowedMIME(m string, allowSVG bool) bool {
	switch m {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "application/pdf":
		return true
	case "image/svg+xml":
		return allowSVG
	default:
		return false
	}
}

func isSVG(name string, head []byte) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".svg") {
		return false
	}
	s := strings.ToLower(string(head))
	return strings.Contains(s, "<svg")
}
