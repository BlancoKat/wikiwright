package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFileFirstLetterFold(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	for _, name := range []string{"cert_mail_receipt.jpg", "éclair.jpg", "beta_x y.jpg", "Beta_x y.jpg"} {
		if err := st.UpsertFile(FileRec{Name: name, MIME: "image/jpeg", Size: 4, UploadedAt: now}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.File("cert_mail_receipt.jpg")
	if err != nil || got.Name != "cert_mail_receipt.jpg" {
		t.Fatalf("exact %+v %v", got, err)
	}
	for _, q := range []string{"Cert_mail_receipt.jpg", "Cert mail receipt.jpg", "cert mail receipt.jpg"} {
		got, err = st.File(q)
		if err != nil || got.Name != "cert_mail_receipt.jpg" {
			t.Fatalf("query %q -> %+v %v", q, got, err)
		}
	}
	for _, q := range []string{"Cert_mail_receipt.JPG", "Xert_mail_receipt.jpg", ""} {
		got, err = st.File(q)
		if err != nil || got.Name != "" {
			t.Fatalf("query %q should miss, got %+v %v", q, got, err)
		}
	}
	got, err = st.File("Éclair.jpg")
	if err != nil || got.Name != "éclair.jpg" {
		t.Fatalf("unicode %+v %v", got, err)
	}
	got, err = st.File("Éclair.JPG")
	if err != nil || got.Name != "" {
		t.Fatalf("unicode extension %+v %v", got, err)
	}

	// Exact spelling wins when both casings are already stored.
	if err := st.UpsertFile(FileRec{Name: "Alpha.jpg", MIME: "image/jpeg", Size: 4, UploadedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(FileRec{Name: "alpha.jpg", MIME: "image/jpeg", Size: 4, UploadedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, err = st.File("Alpha.jpg")
	if err != nil || got.Name != "Alpha.jpg" {
		t.Fatalf("exact upper %+v %v", got, err)
	}
	got, err = st.File("alpha.jpg")
	if err != nil || got.Name != "alpha.jpg" {
		t.Fatalf("exact lower %+v %v", got, err)
	}

	// Mixed spaces and underscores do not exact-match either normalized form.
	got, err = st.File("Beta_x_y.jpg")
	if err != nil || got.Name != "Beta_x y.jpg" {
		t.Fatalf("prefer canonical %+v %v", got, err)
	}
}
