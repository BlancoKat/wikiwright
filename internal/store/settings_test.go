package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSettingsRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.Setting("accent")
	if err != nil || v != "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := st.SetSetting("accent", "#1a365d"); err != nil {
		t.Fatal(err)
	}
	v, err = st.Setting("accent")
	if err != nil || v != "#1a365d" {
		t.Fatalf("%q %v", v, err)
	}
	if err := st.SetSetting("accent", "#6e2430"); err != nil {
		t.Fatal(err)
	}
	v, err = st.Setting("accent")
	if err != nil || v != "#6e2430" {
		t.Fatalf("replace %q %v", v, err)
	}
	if err := st.DeleteSetting("accent"); err != nil {
		t.Fatal(err)
	}
	v, err = st.Setting("accent")
	if err != nil || v != "" {
		t.Fatalf("deleted %q %v", v, err)
	}
}

func TestOpenAddsSettingsWithoutRewritingPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE pages (
		id INTEGER PRIMARY KEY,
		namespace INTEGER NOT NULL,
		title TEXT NOT NULL,
		dbkey TEXT NOT NULL UNIQUE,
		is_redirect INTEGER NOT NULL DEFAULT 0,
		redirect_target TEXT NOT NULL DEFAULT '',
		latest_rev_id INTEGER NOT NULL DEFAULT 0,
		touched TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pages(namespace, title, dbkey, touched) VALUES(0, 'Main Page', 'Main_Page', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.PageByDBKey("Main_Page")
	if err != nil || p.ID == 0 || p.Title != "Main Page" {
		t.Fatalf("%+v %v", p, err)
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pages %d %v", n, err)
	}
	if err := st.SetSetting("accent", "#6e2430"); err != nil {
		t.Fatal(err)
	}
	p, err = st.PageByDBKey("Main_Page")
	if err != nil || p.Title != "Main Page" {
		t.Fatalf("after setting %+v %v", p, err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pages after %d %v", n, err)
	}
}
