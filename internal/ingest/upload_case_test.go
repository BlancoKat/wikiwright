package ingest

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/store"
)

func TestUploadNameCase(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	files := filepath.Join(dir, "files")
	jpg := tinyJPEG(t)

	rec, err := SaveUpload(st, files, "cert_mail_receipt.jpg", bytes.NewReader(jpg), 1<<20, "Ada", "receipt")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "Cert_mail_receipt.jpg" {
		t.Fatalf("stored %q", rec.Name)
	}
	if _, err := os.Stat(filepath.Join(files, "Cert_mail_receipt.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(files, "cert_mail_receipt.jpg")); !os.IsNotExist(err) {
		t.Fatalf("lowercase copy also written: %v", err)
	}
	got, err := st.File("cert_mail_receipt.jpg")
	if err != nil || got.Name != "Cert_mail_receipt.jpg" {
		t.Fatalf("lookup %+v %v", got, err)
	}

	again, err := SaveUpload(st, files, "cert_mail_receipt.jpg", bytes.NewReader(jpg), 1<<20, "Ada", "again")
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "Cert_mail_receipt.jpg" {
		t.Fatalf("reupload %q", again.Name)
	}
	all, err := st.AllFiles()
	if err != nil || len(all) != 1 {
		t.Fatalf("files %+v %v", all, err)
	}

	legacy := "old_scan.jpg"
	if err := os.WriteFile(filepath.Join(files, legacy), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(store.FileRec{Name: legacy, MIME: "image/jpeg", Size: int64(len(jpg))}); err != nil {
		t.Fatal(err)
	}
	replaced, err := SaveUpload(st, files, "Old_scan.jpg", bytes.NewReader(jpg), 1<<20, "Ada", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Name != legacy {
		t.Fatalf("legacy rename %q", replaced.Name)
	}
	if _, err := os.Stat(filepath.Join(files, "Old_scan.jpg")); !os.IsNotExist(err) {
		t.Fatalf("duplicate disk file: %v", err)
	}
	all, err = st.AllFiles()
	if err != nil || len(all) != 2 {
		t.Fatalf("after legacy %+v %v", all, err)
	}

	// A file sitting on disk under the old spelling is reused, not copied.
	if err := os.WriteFile(filepath.Join(files, "disk_only.jpg"), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	diskRec, err := SaveUpload(st, files, "Disk_only.jpg", bytes.NewReader(jpg), 1<<20, "Ada", "disk")
	if err != nil {
		t.Fatal(err)
	}
	if diskRec.Name != "disk_only.jpg" {
		t.Fatalf("disk-only %q", diskRec.Name)
	}
	if _, err := os.Stat(filepath.Join(files, "Disk_only.jpg")); !os.IsNotExist(err) {
		t.Fatalf("disk-only duplicate: %v", err)
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	imported, err := SaveImported(st, files, "mark.svg", bytes.NewReader(svg), 1<<20, "Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Name != "mark.svg" {
		t.Fatalf("import renamed %q", imported.Name)
	}

	resolved, err := ResolveUploadName(st, files, "fresh.png", true)
	if err != nil || resolved != "Fresh.png" {
		t.Fatalf("resolve %q %v", resolved, err)
	}
	resolved, err = ResolveUploadName(st, files, "mark.svg", false)
	if err != nil || resolved != "mark.svg" {
		t.Fatalf("import resolve %q %v", resolved, err)
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
