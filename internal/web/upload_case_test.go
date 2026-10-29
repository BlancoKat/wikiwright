package web

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/config"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

func TestUploadLowercaseNameAndLegacyFile(t *testing.T) {
	dir := t.TempDir()
	files := filepath.Join(dir, "files")
	cfg := &config.File{
		Listen: "127.0.0.1:43123",
		Wikis: []config.Wiki{{
			ID: "rv", Name: "Harbor Atlas", Hosts: []string{"127.0.0.1"}, PathPrefix: "/w",
			Access: "public", MainPage: "Main Page", Timezone: "America/Los_Angeles",
			SQLite: filepath.Join(dir, "rv.db"), Files: files,
			ProjectNS: "Harbor Atlas", MaxUpload: 1 << 20,
			UploadTypes: []string{"png", "jpg", "jpeg", "gif", "webp", "pdf", "svg"},
		}},
	}
	srv, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hash, err := auth.Hash("test-password-1", auth.LightParams)
	if err != nil {
		t.Fatal(err)
	}
	st := srv.Sites[0].Store
	if err := st.CreateUser("Ada", hash, true); err != nil {
		t.Fatal(err)
	}
	jpg := tinyJPEG(t)
	seedLegacyUpload(t, st, files, "cert_mail_receipt.jpg", jpg)

	ts := httptest.NewServer(srv)
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	page := readAll(t, get(t, client, ts.URL+"/w/index.php?title=File:Cert_mail_receipt.jpg"))
	if strings.Contains(page, "No file has been uploaded under this name") {
		t.Fatal("legacy file page did not find the stored file")
	}
	if !strings.Contains(page, "/w/images/cert_mail_receipt.jpg") {
		t.Fatalf("file page image src: %s", page)
	}
	for _, u := range []string{
		"/w/images/cert_mail_receipt.jpg",
		"/w/images/Cert_mail_receipt.jpg",
		"/w/images/Cert%20mail%20receipt.jpg",
	} {
		body := readAll(t, get(t, client, ts.URL+u))
		if !strings.HasPrefix(body, "\xff\xd8") {
			t.Fatalf("GET %s did not return the jpeg", u)
		}
	}

	login(t, client, ts.URL, "/w", "Ada", "test-password-1", "")
	up := upload(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "harbor_chart.jpg", jpg)
	if up.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload %d %s", up.StatusCode, readAll(t, up))
	}
	loc := up.Header.Get("Location")
	up.Body.Close()
	if !strings.Contains(loc, "title=File:Harbor_chart.jpg") {
		t.Fatalf("location %s", loc)
	}
	posted := readAll(t, get(t, client, ts.URL+loc))
	if strings.Contains(posted, "No file has been uploaded under this name") || !strings.Contains(posted, "/w/images/Harbor_chart.jpg") {
		t.Fatalf("new file page: %s", posted)
	}
	for _, u := range []string{"/w/images/harbor_chart.jpg", "/w/images/Harbor_chart.jpg"} {
		body := readAll(t, get(t, client, ts.URL+u))
		if !strings.HasPrefix(body, "\xff\xd8") {
			t.Fatalf("GET %s did not return the jpeg", u)
		}
	}

	warn := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Cert_mail_receipt.jpg", jpg, nil)
	warnBody := readAll(t, warn)
	if warn.StatusCode != 200 || !strings.Contains(warnBody, "already on this wiki") || !strings.Contains(warnBody, `value="cert_mail_receipt.jpg"`) {
		t.Fatalf("legacy reupload warning %d %s", warn.StatusCode, warnBody)
	}
	replaced := uploadExtra(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "Cert_mail_receipt.jpg", jpg, map[string]string{
		"wpIgnoreWarning": "1",
	})
	if replaced.StatusCode != http.StatusSeeOther {
		t.Fatalf("replace %d %s", replaced.StatusCode, readAll(t, replaced))
	}
	replaced.Body.Close()

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	svgUp := upload(t, client, ts.URL+"/w/index.php?title=Special:Upload", csrfFromPage(t, client, ts.URL+"/w/index.php?title=Special:Upload"), "mark.svg", svg)
	if svgUp.StatusCode != http.StatusSeeOther || !strings.Contains(svgUp.Header.Get("Location"), "title=File:Mark.svg") {
		t.Fatalf("svg upload %d %s", svgUp.StatusCode, svgUp.Header.Get("Location"))
	}
	svgUp.Body.Close()

	all, err := st.AllFiles()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, f := range all {
		names[f.Name]++
	}
	if len(all) != 3 || names["cert_mail_receipt.jpg"] != 1 || names["Harbor_chart.jpg"] != 1 || names["Mark.svg"] != 1 {
		t.Fatalf("stored names %+v", names)
	}
	entries, err := os.ReadDir(files)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		onDisk[e.Name()] = true
	}
	for _, name := range []string{"cert_mail_receipt.jpg", "Harbor_chart.jpg", "Mark.svg"} {
		if !onDisk[name] {
			t.Fatalf("missing %s in %v", name, onDisk)
		}
	}
	for _, name := range []string{"Cert_mail_receipt.jpg", "harbor_chart.jpg", "mark.svg"} {
		if onDisk[name] {
			t.Fatalf("duplicate %s in %v", name, onDisk)
		}
	}
	list := readAll(t, get(t, client, ts.URL+"/w/index.php?title=Special:ListFiles"))
	if !strings.Contains(list, "cert_mail_receipt.jpg") || !strings.Contains(list, "Harbor_chart.jpg") {
		t.Fatalf("list files: %s", list)
	}
}

func seedLegacyUpload(t *testing.T, st *store.Store, dir, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(store.FileRec{
		Name: name, MIME: "image/jpeg", Size: int64(len(data)),
		UploadedBy: "Ada", UploadedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	parsed := title.Parse("File:"+name, "Harbor Atlas")
	id, err := st.UpsertPage(parsed.Namespace, parsed.Rest, parsed.DBKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRevision(id, "Ada", "Upload "+name, "== Summary ==\n\nUploaded file.", time.Now().UTC(), false, 0, 0); err != nil {
		t.Fatal(err)
	}
}

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
