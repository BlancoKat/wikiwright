package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUploadDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wikiwright.yaml")
	body := "wikis:\n  - id: rv\n    name: Harbor Atlas\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Wikis[0]
	if w.MaxUpload != 20<<20 {
		t.Fatalf("max %d", w.MaxUpload)
	}
	want := []string{"png", "jpg", "jpeg", "gif", "webp", "pdf"}
	if !reflect.DeepEqual(w.UploadTypes, want) {
		t.Fatalf("types %#v", w.UploadTypes)
	}
}

func TestAccentNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wikiwright.yaml")
	body := "wikis:\n  - id: rv\n    name: Harbor Atlas\n    accent: \"#ABC\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Wikis[0].Accent != "#aabbcc" {
		t.Fatal(f.Wikis[0].Accent)
	}
	body = "wikis:\n  - id: rv\n    name: Harbor Atlas\n    accent: green\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid accent")
	}
}

func TestUploadTypesNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wikiwright.yaml")
	body := "wikis:\n  - id: rv\n    name: Harbor Atlas\n    max_upload: 1234\n    upload_types: [PNG, \".SVG\", jpeg]\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Wikis[0]
	if w.MaxUpload != 1234 {
		t.Fatalf("max %d", w.MaxUpload)
	}
	want := []string{"png", "svg", "jpeg"}
	if !reflect.DeepEqual(w.UploadTypes, want) {
		t.Fatalf("types %#v", w.UploadTypes)
	}
}
