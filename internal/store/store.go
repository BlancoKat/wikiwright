// Package store is the per-wiki SQLite database.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is one wiki database.
type Store struct {
	DB *sql.DB
}

// Open creates the file and schema if needed.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(schema); err != nil {
		return err
	}
	return s.addColumnIfMissing("sessions", "grants", `ALTER TABLE sessions ADD COLUMN grants TEXT NOT NULL DEFAULT ''`)
}

func (s *Store) addColumnIfMissing(table, column, alter string) error {
	rows, err := s.DB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.DB.Exec(alter)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS pages (
  id INTEGER PRIMARY KEY,
  namespace INTEGER NOT NULL,
  title TEXT NOT NULL,
  dbkey TEXT NOT NULL UNIQUE,
  is_redirect INTEGER NOT NULL DEFAULT 0,
  redirect_target TEXT NOT NULL DEFAULT '',
  latest_rev_id INTEGER NOT NULL DEFAULT 0,
  touched TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS revisions (
  id INTEGER PRIMARY KEY,
  page_id INTEGER NOT NULL,
  user_text TEXT NOT NULL,
  timestamp TEXT NOT NULL,
  comment TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  minor INTEGER NOT NULL DEFAULT 0,
  mw_rev_id INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_rev_page ON revisions(page_id, id);
CREATE INDEX IF NOT EXISTS idx_rev_mw ON revisions(mw_rev_id);
CREATE TABLE IF NOT EXISTS links (
  from_page INTEGER NOT NULL,
  to_dbkey TEXT NOT NULL,
  kind TEXT NOT NULL,
  sortkey TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (from_page, to_dbkey, kind)
);
CREATE INDEX IF NOT EXISTS idx_links_to ON links(to_dbkey, kind);
CREATE TABLE IF NOT EXISTS files (
  name TEXT PRIMARY KEY,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  uploaded_by TEXT NOT NULL DEFAULT '',
  uploaded_at TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  totp_secret TEXT NOT NULL DEFAULT '',
  totp_enabled INTEGER NOT NULL DEFAULT 0,
  is_admin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  csrf TEXT NOT NULL,
  expires TEXT NOT NULL,
  pending_totp TEXT NOT NULL DEFAULT '',
  needs_totp INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS login_attempts (
  id INTEGER PRIMARY KEY,
  ip TEXT NOT NULL,
  username TEXT NOT NULL,
  at TEXT NOT NULL,
  success INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS watches (
  user_id INTEGER NOT NULL,
  page_id INTEGER NOT NULL,
  PRIMARY KEY (user_id, page_id)
);
CREATE TABLE IF NOT EXISTS bot_passwords (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL,
  app_id TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  grants TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(user_id, app_id)
);
CREATE TABLE IF NOT EXISTS settings (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Page is a stored article.
type Page struct {
	ID             int64
	Namespace      int
	Title          string
	DBKey          string
	IsRedirect     bool
	RedirectTarget string
	LatestRevID    int64
	Touched        time.Time
}

// Revision is one edit.
type Revision struct {
	ID        int64
	PageID    int64
	UserText  string
	Timestamp time.Time
	Comment   string
	Content   string
	Minor     bool
	MWRevID   int64
	ParentID  int64
}

// User is an account that can log in.
type User struct {
	ID           int64
	Name         string
	PasswordHash string
	TOTPSecret   string
	TOTPEnabled  bool
	IsAdmin      bool
	CreatedAt    time.Time
}

// Session is a login session. NeedsTOTP means the password was accepted
// and a second factor is still required before the session can read or edit.
// Grants limits a bot-password session. An empty grant list is a normal login
// and can do everything that account can do.
type Session struct {
	Token       string
	UserID      int64
	CSRF        string
	Expires     time.Time
	PendingTOTP string
	NeedsTOTP   bool
	Grants      string
	User        User
}

// Can reports whether this session may perform grant (edit, create, or upload).
func (sess Session) Can(grant string) bool {
	if strings.TrimSpace(sess.Grants) == "" {
		return true
	}
	for _, g := range strings.Split(sess.Grants, ",") {
		if strings.TrimSpace(g) == grant {
			return true
		}
	}
	return false
}

// FileRec is an uploaded file.
type FileRec struct {
	Name        string
	MIME        string
	Size        int64
	Width       int
	Height      int
	UploadedBy  string
	UploadedAt  time.Time
	Description string
}

// CategoryRef is a category annotation.
type CategoryRef struct {
	Name    string
	SortKey string
}

func (s *Store) UpsertPage(ns int, title, dbkey string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.DB.Exec(`INSERT INTO pages(namespace, title, dbkey, touched) VALUES(?,?,?,?)
		ON CONFLICT(dbkey) DO UPDATE SET namespace=excluded.namespace, title=excluded.title, touched=excluded.touched`,
		ns, title, dbkey, now)
	if err != nil {
		return 0, err
	}
	if id, err := res.LastInsertId(); err == nil && id > 0 {
		// ON CONFLICT update may return 0 on some sqlite builds; look up.
		var existing int64
		if err := s.DB.QueryRow(`SELECT id FROM pages WHERE dbkey=?`, dbkey).Scan(&existing); err == nil {
			return existing, nil
		}
		return id, nil
	}
	var id int64
	err = s.DB.QueryRow(`SELECT id FROM pages WHERE dbkey=?`, dbkey).Scan(&id)
	return id, err
}

// AddRevision inserts a revision. If mwRevID is non-zero and unused, it becomes
// the primary key so imported permalinks (?oldid=) keep working.
func (s *Store) AddRevision(pageID int64, user, comment, content string, ts time.Time, minor bool, mwRevID, parentID int64) (int64, error) {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	minorN := 0
	if minor {
		minorN = 1
	}
	var id int64
	if mwRevID > 0 {
		var existing int64
		err := s.DB.QueryRow(`SELECT id FROM revisions WHERE mw_rev_id=?`, mwRevID).Scan(&existing)
		if err == nil {
			// Already imported. Do not insert a second copy or move latest.
			return existing, nil
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
		var taken int64
		err = s.DB.QueryRow(`SELECT id FROM revisions WHERE id=?`, mwRevID).Scan(&taken)
		if err == sql.ErrNoRows {
			_, err = s.DB.Exec(`INSERT INTO revisions(id, page_id, user_text, timestamp, comment, content, minor, mw_rev_id, parent_id)
				VALUES(?,?,?,?,?,?,?,?,?)`,
				mwRevID, pageID, user, ts.UTC().Format(time.RFC3339), comment, content, minorN, mwRevID, parentID)
			if err != nil {
				return 0, err
			}
			id = mwRevID
		} else if err != nil {
			return 0, err
		} else {
			res, err := s.DB.Exec(`INSERT INTO revisions(page_id, user_text, timestamp, comment, content, minor, mw_rev_id, parent_id)
				VALUES(?,?,?,?,?,?,?,?)`,
				pageID, user, ts.UTC().Format(time.RFC3339), comment, content, minorN, mwRevID, parentID)
			if err != nil {
				return 0, err
			}
			id, _ = res.LastInsertId()
		}
	} else {
		res, err := s.DB.Exec(`INSERT INTO revisions(page_id, user_text, timestamp, comment, content, minor, mw_rev_id, parent_id)
			VALUES(?,?,?,?,?,?,0,?)`,
			pageID, user, ts.UTC().Format(time.RFC3339), comment, content, minorN, parentID)
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
	}
	_, err := s.DB.Exec(`UPDATE pages SET latest_rev_id=?, touched=? WHERE id=?`, id, ts.UTC().Format(time.RFC3339), pageID)
	return id, err
}

func (s *Store) SetRedirect(pageID int64, target string) error {
	flag := 0
	if target != "" {
		flag = 1
	}
	_, err := s.DB.Exec(`UPDATE pages SET is_redirect=?, redirect_target=? WHERE id=?`, flag, target, pageID)
	return err
}

func (s *Store) ReplaceAnnotations(pageID int64, links []string, cats []CategoryRef) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM links WHERE from_page=?`, pageID); err != nil {
		return err
	}
	for _, to := range links {
		if to == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO links(from_page, to_dbkey, kind, sortkey) VALUES(?,?, 'link', '')`, pageID, to); err != nil {
			return err
		}
	}
	for _, c := range cats {
		if c.Name == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO links(from_page, to_dbkey, kind, sortkey) VALUES(?,?, 'category', ?)`, pageID, "Category:"+strings.ReplaceAll(c.Name, " ", "_"), c.SortKey); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanPage(sc interface{ Scan(...any) error }) (Page, error) {
	var p Page
	var redir, touched string
	var isRedir int
	err := sc.Scan(&p.ID, &p.Namespace, &p.Title, &p.DBKey, &isRedir, &redir, &p.LatestRevID, &touched)
	if err != nil {
		return Page{}, err
	}
	p.IsRedirect = isRedir != 0
	p.RedirectTarget = redir
	p.Touched, _ = time.Parse(time.RFC3339, touched)
	return p, nil
}

const pageCols = `id, namespace, title, dbkey, is_redirect, redirect_target, latest_rev_id, touched`

func (s *Store) PageByDBKey(dbkey string) (Page, error) {
	row := s.DB.QueryRow(`SELECT `+pageCols+` FROM pages WHERE dbkey=?`, dbkey)
	p, err := scanPage(row)
	if err == sql.ErrNoRows {
		return Page{}, nil
	}
	return p, err
}

func (s *Store) PageByID(id int64) (Page, error) {
	row := s.DB.QueryRow(`SELECT `+pageCols+` FROM pages WHERE id=?`, id)
	p, err := scanPage(row)
	if err == sql.ErrNoRows {
		return Page{}, nil
	}
	return p, err
}

func scanRev(sc interface{ Scan(...any) error }) (Revision, error) {
	var r Revision
	var ts string
	var minor int
	err := sc.Scan(&r.ID, &r.PageID, &r.UserText, &ts, &r.Comment, &r.Content, &minor, &r.MWRevID, &r.ParentID)
	if err != nil {
		return Revision{}, err
	}
	r.Minor = minor != 0
	r.Timestamp, _ = time.Parse(time.RFC3339, ts)
	return r, nil
}

const revCols = `id, page_id, user_text, timestamp, comment, content, minor, mw_rev_id, parent_id`

func (s *Store) LatestRevision(pageID int64) (Revision, error) {
	var latest int64
	if err := s.DB.QueryRow(`SELECT latest_rev_id FROM pages WHERE id=?`, pageID).Scan(&latest); err != nil {
		return Revision{}, err
	}
	if latest == 0 {
		return Revision{}, nil
	}
	return s.RevisionByID(latest)
}

func (s *Store) RevisionByID(id int64) (Revision, error) {
	row := s.DB.QueryRow(`SELECT `+revCols+` FROM revisions WHERE id=?`, id)
	r, err := scanRev(row)
	if err == sql.ErrNoRows {
		return Revision{}, nil
	}
	return r, err
}

// ResolveOldID prefers an imported MediaWiki revision id, then an internal id.
func (s *Store) ResolveOldID(pageID, oldid int64) (Revision, error) {
	row := s.DB.QueryRow(`SELECT `+revCols+` FROM revisions WHERE page_id=? AND mw_rev_id=? ORDER BY id LIMIT 1`, pageID, oldid)
	r, err := scanRev(row)
	if err == nil && r.ID != 0 {
		return r, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return Revision{}, err
	}
	row = s.DB.QueryRow(`SELECT `+revCols+` FROM revisions WHERE page_id=? AND id=?`, pageID, oldid)
	r, err = scanRev(row)
	if err == sql.ErrNoRows {
		return Revision{}, nil
	}
	return r, err
}

func (s *Store) Revisions(pageID int64) ([]Revision, error) {
	rows, err := s.DB.Query(`SELECT `+revCols+` FROM revisions WHERE page_id=? ORDER BY timestamp DESC, id DESC`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		r, err := scanRev(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DBKeys() (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT dbkey FROM pages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		m[k] = true
	}
	return m, rows.Err()
}

func (s *Store) AllPages() ([]Page, error) {
	rows, err := s.DB.Query(`SELECT ` + pageCols + ` FROM pages ORDER BY namespace, title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PagesInNamespace(ns int) ([]Page, error) {
	rows, err := s.DB.Query(`SELECT `+pageCols+` FROM pages WHERE namespace=? ORDER BY title COLLATE NOCASE`, ns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RC is one recent change.
type RC struct {
	Revision
	DBKey string
	Title string
	NS    int
}

func (s *Store) RecentChanges(limit int) ([]RC, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT r.id, r.page_id, r.user_text, r.timestamp, r.comment, r.content, r.minor, r.mw_rev_id, r.parent_id,
		p.dbkey, p.title, p.namespace
		FROM revisions r JOIN pages p ON p.id=r.page_id
		ORDER BY r.timestamp DESC, r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RC
	for rows.Next() {
		var rc RC
		var ts string
		var minor int
		if err := rows.Scan(&rc.ID, &rc.PageID, &rc.UserText, &ts, &rc.Comment, &rc.Content, &minor, &rc.MWRevID, &rc.ParentID, &rc.DBKey, &rc.Title, &rc.NS); err != nil {
			return nil, err
		}
		rc.Minor = minor != 0
		rc.Timestamp, _ = time.Parse(time.RFC3339, ts)
		out = append(out, rc)
	}
	return out, rows.Err()
}

// Hit is a search result.
type Hit struct {
	Page    Page
	Snippet string
}

func (s *Store) Search(q string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 50
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	like := "%" + escapeLike(q) + "%"
	rows, err := s.DB.Query(`SELECT p.id, p.namespace, p.title, p.dbkey, p.is_redirect, p.redirect_target, p.latest_rev_id, p.touched, r.content
		FROM pages p JOIN revisions r ON r.id=p.latest_rev_id
		WHERE p.title LIKE ? ESCAPE '\' OR r.content LIKE ? ESCAPE '\'
		ORDER BY p.namespace, p.title COLLATE NOCASE LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var redir, touched, content string
		var isRedir int
		if err := rows.Scan(&h.Page.ID, &h.Page.Namespace, &h.Page.Title, &h.Page.DBKey, &isRedir, &redir, &h.Page.LatestRevID, &touched, &content); err != nil {
			return nil, err
		}
		h.Page.IsRedirect = isRedir != 0
		h.Page.RedirectTarget = redir
		h.Page.Touched, _ = time.Parse(time.RFC3339, touched)
		h.Snippet = snippet(content, q)
		out = append(out, h)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func snippet(content, q string) string {
	low := strings.ToLower(content)
	idx := strings.Index(low, strings.ToLower(q))
	if idx < 0 {
		if len(content) > 160 {
			return content[:160] + "…"
		}
		return content
	}
	start := idx - 60
	if start < 0 {
		start = 0
	}
	end := idx + len(q) + 80
	if end > len(content) {
		end = len(content)
	}
	s := content[start:end]
	s = strings.ReplaceAll(s, "\n", " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(content) {
		s += "…"
	}
	return s
}

func (s *Store) RandomArticle() (Page, error) {
	row := s.DB.QueryRow(`SELECT ` + pageCols + ` FROM pages WHERE namespace=0 AND is_redirect=0 ORDER BY RANDOM() LIMIT 1`)
	p, err := scanPage(row)
	if err == sql.ErrNoRows {
		return Page{}, nil
	}
	return p, err
}

func (s *Store) WhatLinksHere(dbkey string) ([]Page, error) {
	rows, err := s.DB.Query(`SELECT `+pageCols+` FROM pages WHERE id IN (
		SELECT from_page FROM links WHERE to_dbkey=? AND kind='link') ORDER BY title COLLATE NOCASE`, dbkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Outgoing(pageID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT to_dbkey FROM links WHERE from_page=? AND kind='link'`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

type CatCount struct {
	Name  string
	Count int
}

func (s *Store) CategoryCounts() ([]CatCount, error) {
	rows, err := s.DB.Query(`SELECT to_dbkey, COUNT(*) FROM links WHERE kind='category' GROUP BY to_dbkey ORDER BY to_dbkey COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatCount
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(key, "Category:")
		name = strings.ReplaceAll(name, "_", " ")
		out = append(out, CatCount{Name: name, Count: n})
	}
	return out, rows.Err()
}

func (s *Store) CategoryMembers(name string) ([]Page, error) {
	key := "Category:" + strings.ReplaceAll(name, " ", "_")
	rows, err := s.DB.Query(`SELECT `+pageCols+` FROM pages p JOIN links l ON l.from_page=p.id
		WHERE l.kind='category' AND l.to_dbkey=? ORDER BY l.sortkey COLLATE NOCASE, p.title COLLATE NOCASE`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PageCategories(pageID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT to_dbkey FROM links WHERE from_page=? AND kind='category' ORDER BY to_dbkey`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(k, "Category:")
		out = append(out, strings.ReplaceAll(name, "_", " "))
	}
	return out, rows.Err()
}

func (s *Store) Counts() (pages, articles, files int, err error) {
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pages); err != nil {
		return
	}
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM pages WHERE namespace=0 AND is_redirect=0`).Scan(&articles); err != nil {
		return
	}
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&files)
	return
}

func (s *Store) ContentByDBKey(dbkey string) (string, bool, error) {
	var content string
	err := s.DB.QueryRow(`SELECT r.content FROM pages p JOIN revisions r ON r.id=p.latest_rev_id WHERE p.dbkey=?`, dbkey).Scan(&content)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return content, true, nil
}

func (s *Store) UpsertFile(f FileRec) error {
	if f.UploadedAt.IsZero() {
		f.UploadedAt = time.Now().UTC()
	}
	_, err := s.DB.Exec(`INSERT INTO files(name, mime, size, width, height, uploaded_by, uploaded_at, description)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET mime=excluded.mime, size=excluded.size, width=excluded.width, height=excluded.height,
			uploaded_by=excluded.uploaded_by, uploaded_at=excluded.uploaded_at, description=excluded.description`,
		f.Name, f.MIME, f.Size, f.Width, f.Height, f.UploadedBy, f.UploadedAt.UTC().Format(time.RFC3339), f.Description)
	return err
}

// AllFiles lists every stored file, ordered by name.
func (s *Store) AllFiles() ([]FileRec, error) {
	rows, err := s.DB.Query(`SELECT name, mime, size, width, height, uploaded_by, uploaded_at, description FROM files ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRec
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) File(name string) (FileRec, error) {
	// MediaWiki treats spaces and underscores as the same character in file names.
	spaced := strings.ReplaceAll(name, "_", " ")
	f, err := s.fileByName(spaced)
	if err != nil || f.Name != "" {
		return f, err
	}
	return s.fileByName(strings.ReplaceAll(spaced, " ", "_"))
}

func scanFile(sc interface{ Scan(...any) error }) (FileRec, error) {
	var f FileRec
	var at string
	err := sc.Scan(&f.Name, &f.MIME, &f.Size, &f.Width, &f.Height, &f.UploadedBy, &at, &f.Description)
	if err != nil {
		return FileRec{}, err
	}
	f.UploadedAt, _ = time.Parse(time.RFC3339, at)
	return f, nil
}

func (s *Store) fileByName(name string) (FileRec, error) {
	row := s.DB.QueryRow(`SELECT name, mime, size, width, height, uploaded_by, uploaded_at, description FROM files WHERE name=?`, name)
	f, err := scanFile(row)
	if err == sql.ErrNoRows {
		return FileRec{}, nil
	}
	return f, err
}

func (s *Store) CreateUser(name, hash string, admin bool) error {
	adm := 0
	if admin {
		adm = 1
	}
	_, err := s.DB.Exec(`INSERT INTO users(name, password_hash, is_admin, created_at) VALUES(?,?,?,?)`,
		name, hash, adm, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) UserByName(name string) (User, error) {
	var u User
	var totpEn, adm int
	var created string
	err := s.DB.QueryRow(`SELECT id, name, password_hash, totp_secret, totp_enabled, is_admin, created_at FROM users WHERE name=? COLLATE NOCASE`, name).
		Scan(&u.ID, &u.Name, &u.PasswordHash, &u.TOTPSecret, &totpEn, &adm, &created)
	if err == sql.ErrNoRows {
		return User{}, nil
	}
	if err != nil {
		return User{}, err
	}
	u.TOTPEnabled = totpEn != 0
	u.IsAdmin = adm != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, nil
}

func (s *Store) UserByID(id int64) (User, error) {
	var u User
	var totpEn, adm int
	var created string
	err := s.DB.QueryRow(`SELECT id, name, password_hash, totp_secret, totp_enabled, is_admin, created_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Name, &u.PasswordHash, &u.TOTPSecret, &totpEn, &adm, &created)
	if err == sql.ErrNoRows {
		return User{}, nil
	}
	if err != nil {
		return User{}, err
	}
	u.TOTPEnabled = totpEn != 0
	u.IsAdmin = adm != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, nil
}

func (s *Store) SetPassword(userID int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, userID)
	return err
}

func (s *Store) SetTOTP(userID int64, secret string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.DB.Exec(`UPDATE users SET totp_secret=?, totp_enabled=? WHERE id=?`, secret, en, userID)
	return err
}

func (s *Store) CreateSession(userID int64, token, csrf string, expires time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, csrf, expires) VALUES(?,?,?,?)`,
		token, userID, csrf, expires.UTC().Format(time.RFC3339))
	return err
}

// CreateChallenge starts a session that still needs a TOTP code.
func (s *Store) CreateChallenge(userID int64, token, csrf string, expires time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, csrf, expires, needs_totp) VALUES(?,?,?,?,1)`,
		token, userID, csrf, expires.UTC().Format(time.RFC3339))
	return err
}

// FinishChallenge marks a TOTP challenge as a full session.
func (s *Store) FinishChallenge(token string, expires time.Time) error {
	_, err := s.DB.Exec(`UPDATE sessions SET needs_totp=0, expires=? WHERE token=?`,
		expires.UTC().Format(time.RFC3339), token)
	return err
}

func (s *Store) CreateBotSession(userID int64, token, csrf, grants string, expires time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token, user_id, csrf, expires, grants) VALUES(?,?,?,?,?)`,
		token, userID, csrf, expires.UTC().Format(time.RFC3339), grants)
	return err
}

func (s *Store) Session(token string) (Session, error) {
	var sess Session
	var exp string
	var needs int
	err := s.DB.QueryRow(`SELECT token, user_id, csrf, expires, pending_totp, needs_totp, grants FROM sessions WHERE token=?`, token).
		Scan(&sess.Token, &sess.UserID, &sess.CSRF, &exp, &sess.PendingTOTP, &needs, &sess.Grants)
	if err == sql.ErrNoRows {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, err
	}
	sess.NeedsTOTP = needs != 0
	sess.Expires, _ = time.Parse(time.RFC3339, exp)
	if time.Now().After(sess.Expires) {
		_ = s.DeleteSession(token)
		return Session{}, nil
	}
	u, err := s.UserByID(sess.UserID)
	if err != nil {
		return Session{}, err
	}
	sess.User = u
	return sess, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

func (s *Store) SetPendingTOTP(token, secret string) error {
	_, err := s.DB.Exec(`UPDATE sessions SET pending_totp=? WHERE token=?`, secret, token)
	return err
}

func (s *Store) RecordLogin(ip, username string, success bool) error {
	n := 0
	if success {
		n = 1
	}
	_, err := s.DB.Exec(`INSERT INTO login_attempts(ip, username, at, success) VALUES(?,?,?,?)`,
		ip, username, time.Now().UTC().Format(time.RFC3339), n)
	return err
}

// LoginFailures counts failed attempts for an IP since t.
func (s *Store) LoginFailures(ip string, since time.Time) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM login_attempts WHERE ip=? AND success=0 AND at>=?`,
		ip, since.UTC().Format(time.RFC3339)).Scan(&n)
	return n, err
}

// Watching reports whether the account watches the page.
func (s *Store) Watching(userID, pageID int64) (bool, error) {
	if userID == 0 || pageID == 0 {
		return false, nil
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM watches WHERE user_id=? AND page_id=?`, userID, pageID).Scan(&n)
	return n > 0, err
}

// SetWatch adds or removes a watch. There is no watchlist page yet; the edit
// form uses this so the checkbox keeps its state.
func (s *Store) SetWatch(userID, pageID int64, on bool) error {
	if userID == 0 || pageID == 0 {
		return nil
	}
	if on {
		_, err := s.DB.Exec(`INSERT INTO watches(user_id, page_id) VALUES(?,?) ON CONFLICT(user_id, page_id) DO NOTHING`, userID, pageID)
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM watches WHERE user_id=? AND page_id=?`, userID, pageID)
	return err
}

// Begin starts a transaction.
func (s *Store) Begin() (*sql.Tx, error) { return s.DB.Begin() }

// BotPassword is a named password for one account. The secret is stored only
// as a hash.
type BotPassword struct {
	ID        int64
	UserID    int64
	AppID     string
	Grants    string
	CreatedAt time.Time
}

// UpsertBotPassword stores or replaces the hash for userID's appID.
func (s *Store) UpsertBotPassword(userID int64, appID, hash, grants string) error {
	_, err := s.DB.Exec(`INSERT INTO bot_passwords(user_id, app_id, password_hash, grants, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(user_id, app_id) DO UPDATE SET password_hash=excluded.password_hash, grants=excluded.grants, created_at=excluded.created_at`,
		userID, appID, hash, grants, time.Now().UTC().Format(time.RFC3339))
	return err
}

// BotPasswords lists an account's bot passwords without the hashes.
func (s *Store) BotPasswords(userID int64) ([]BotPassword, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, app_id, grants, created_at FROM bot_passwords WHERE user_id=? ORDER BY app_id COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotPassword
	for rows.Next() {
		var b BotPassword
		var at string
		if err := rows.Scan(&b.ID, &b.UserID, &b.AppID, &b.Grants, &at); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, b)
	}
	return out, rows.Err()
}

// BotPasswordHash returns the stored hash and grants. ok is false when the name does not exist.
func (s *Store) BotPasswordHash(userID int64, appID string) (hash, grants string, ok bool, err error) {
	err = s.DB.QueryRow(`SELECT password_hash, grants FROM bot_passwords WHERE user_id=? AND app_id=?`, userID, appID).Scan(&hash, &grants)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return hash, grants, true, nil
}

// DeleteBotPassword removes one bot password.
func (s *Store) DeleteBotPassword(userID int64, appID string) error {
	_, err := s.DB.Exec(`DELETE FROM bot_passwords WHERE user_id=? AND app_id=?`, userID, appID)
	return err
}

// Users lists accounts. Password hashes are not returned.
func (s *Store) Users() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, name, totp_enabled, is_admin, created_at FROM users ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var totpEn, adm int
		var created string
		if err := rows.Scan(&u.ID, &u.Name, &totpEn, &adm, &created); err != nil {
			return nil, err
		}
		u.TOTPEnabled = totpEn != 0
		u.IsAdmin = adm != 0
		u.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// PagesWhere selects pages matching a SQL fragment that starts with WHERE or is empty.
func (s *Store) queryPages(q string, args ...any) ([]Page, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PrefixPages lists pages in a namespace whose title starts with prefix.
func (s *Store) PrefixPages(ns int, prefix string, limit int) ([]Page, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	prefix = strings.TrimSpace(prefix)
	like := escapeLike(strings.ReplaceAll(prefix, " ", "_")) + "%"
	return s.queryPages(`SELECT `+pageCols+` FROM pages WHERE namespace=? AND dbkey LIKE ? ESCAPE '\' ORDER BY dbkey COLLATE NOCASE LIMIT ?`, ns, like, limit)
}

// NewPages lists pages by the time of their first revision, newest first.
func (s *Store) NewPages(limit int) ([]Page, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return s.queryPages(`SELECT `+pageCols+` FROM pages p ORDER BY (
		SELECT MIN(timestamp) FROM revisions r WHERE r.page_id=p.id
	) DESC, p.id DESC LIMIT ?`, limit)
}

// UncategorizedPages lists articles that are not in any category.
func (s *Store) UncategorizedPages(limit int) ([]Page, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.queryPages(`SELECT `+pageCols+` FROM pages p WHERE namespace=0 AND is_redirect=0 AND NOT EXISTS (
		SELECT 1 FROM links l WHERE l.from_page=p.id AND l.kind='category') ORDER BY title COLLATE NOCASE LIMIT ?`, limit)
}

// Wanted is a missing title and how many pages link to it.
type Wanted struct {
	DBKey string
	Count int
}

// WantedPages lists link targets that have no page.
func (s *Store) WantedPages(limit int) ([]Wanted, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT to_dbkey, COUNT(*) FROM links
		WHERE kind='link' AND to_dbkey NOT LIKE 'Special:%' AND to_dbkey NOT LIKE 'Media:%'
		AND to_dbkey NOT IN (SELECT dbkey FROM pages)
		GROUP BY to_dbkey ORDER BY COUNT(*) DESC, to_dbkey COLLATE NOCASE LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Wanted
	for rows.Next() {
		var w Wanted
		if err := rows.Scan(&w.DBKey, &w.Count); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// LonelyPages lists articles with no incoming links.
func (s *Store) LonelyPages(limit int) ([]Page, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.queryPages(`SELECT `+pageCols+` FROM pages p WHERE namespace=0 AND is_redirect=0 AND NOT EXISTS (
		SELECT 1 FROM links l WHERE l.kind='link' AND l.to_dbkey=p.dbkey) ORDER BY title COLLATE NOCASE LIMIT ?`, limit)
}

// Redirects lists redirect pages.
func (s *Store) Redirects(limit int) ([]Page, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.queryPages(`SELECT `+pageCols+` FROM pages WHERE is_redirect=1 ORDER BY namespace, title COLLATE NOCASE LIMIT ?`, limit)
}

// Contributions lists revisions by a user, newest first.
func (s *Store) Contributions(user string, limit int) ([]RC, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT r.id, r.page_id, r.user_text, r.timestamp, r.comment, r.content, r.minor, r.mw_rev_id, r.parent_id,
		p.dbkey, p.title, p.namespace
		FROM revisions r JOIN pages p ON p.id=r.page_id
		WHERE r.user_text=? COLLATE NOCASE
		ORDER BY r.timestamp DESC, r.id DESC LIMIT ?`, user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RC
	for rows.Next() {
		var rc RC
		var ts string
		var minor int
		if err := rows.Scan(&rc.ID, &rc.PageID, &rc.UserText, &ts, &rc.Comment, &rc.Content, &minor, &rc.MWRevID, &rc.ParentID, &rc.DBKey, &rc.Title, &rc.NS); err != nil {
			return nil, err
		}
		rc.Minor = minor != 0
		rc.Timestamp, _ = time.Parse(time.RFC3339, ts)
		out = append(out, rc)
	}
	return out, rows.Err()
}

// RecentFiles lists uploads, newest first.
func (s *Store) RecentFiles(limit int) ([]FileRec, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT name, mime, size, width, height, uploaded_by, uploaded_at, description FROM files ORDER BY uploaded_at DESC, name COLLATE NOCASE LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRec
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AllPagesLimited lists pages for the API, optionally in one namespace, starting after continueKey.
func (s *Store) AllPagesLimited(ns int, haveNS bool, prefix, continueKey string, limit int) ([]Page, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT ` + pageCols + ` FROM pages WHERE 1=1`
	var args []any
	if haveNS {
		q += ` AND namespace=?`
		args = append(args, ns)
	}
	if prefix != "" {
		q += ` AND dbkey LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(strings.ReplaceAll(prefix, " ", "_"))+"%")
	}
	if continueKey != "" {
		q += ` AND dbkey > ?`
		args = append(args, continueKey)
	}
	q += ` ORDER BY dbkey COLLATE NOCASE LIMIT ?`
	args = append(args, limit)
	return s.queryPages(q, args...)
}

// Setting reads one per-wiki setting. A missing name returns an empty value.
func (s *Store) Setting(name string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE name=?`, name).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting inserts or replaces one setting.
func (s *Store) SetSetting(name, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(name, value) VALUES(?, ?)
		ON CONFLICT(name) DO UPDATE SET value=excluded.value`, name, value)
	return err
}

// DeleteSetting removes one setting. A missing name is not an error.
func (s *Store) DeleteSetting(name string) error {
	_, err := s.DB.Exec(`DELETE FROM settings WHERE name=?`, name)
	return err
}

// Err is a small wrapper so callers can use fmt.Errorf.
func wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}
