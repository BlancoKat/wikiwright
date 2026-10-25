// Package config loads the multi-wiki Wikiwright configuration.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BlancoKat/wikiwright/internal/color"
	"gopkg.in/yaml.v3"
)

// File is the top-level configuration.
type File struct {
	Listen       string `yaml:"listen"`
	CookieSecure bool   `yaml:"cookie_secure"`
	// Secret signs nothing user-visible today; reserved so sessions can be
	// rotated later. Session tokens are random and stored server-side.
	Secret string `yaml:"secret"`
	Wikis  []Wiki `yaml:"wikis"`
}

// Wiki is one site served by the process.
type Wiki struct {
	ID         string   `yaml:"id"`
	Name       string   `yaml:"name"`
	Hosts      []string `yaml:"hosts"`
	PathPrefix string   `yaml:"path_prefix"`
	Access     string   `yaml:"access"` // public or private
	MainPage   string   `yaml:"main_page"`
	License    string   `yaml:"license_text"`
	LicenseURL string   `yaml:"license_url"`
	Timezone   string   `yaml:"timezone"`
	SQLite     string   `yaml:"sqlite"`
	Files      string   `yaml:"files"`
	Logo       string   `yaml:"logo"`
	Tagline    string   `yaml:"tagline"`
	// Accent is the default header and sidebar color (#rrggbb).
	// An administrator can override it per wiki at Special:Appearance.
	// Empty uses the built-in pine.
	Accent string `yaml:"accent"`
	// ProjectNS overrides the project namespace. Defaults to Name.
	ProjectNS   string   `yaml:"project_namespace"`
	MaxUpload   int64    `yaml:"max_upload"`
	UploadTypes []string `yaml:"upload_types"`
}

// Load reads YAML from path.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Listen == "" {
		f.Listen = "127.0.0.1:43123"
	}
	if len(f.Wikis) == 0 {
		return nil, fmt.Errorf("config %s has no wikis", path)
	}
	seen := map[string]bool{}
	for i := range f.Wikis {
		w := &f.Wikis[i]
		w.ID = strings.TrimSpace(w.ID)
		if w.ID == "" {
			return nil, fmt.Errorf("wiki %d is missing id", i)
		}
		if seen[w.ID] {
			return nil, fmt.Errorf("duplicate wiki id %q", w.ID)
		}
		seen[w.ID] = true
		if w.Name == "" {
			w.Name = w.ID
		}
		if w.MainPage == "" {
			w.MainPage = "Main Page"
		}
		if w.Access == "" {
			w.Access = "public"
		}
		switch w.Access {
		case "public", "private":
		default:
			return nil, fmt.Errorf("wiki %s: access must be public or private", w.ID)
		}
		if w.Timezone == "" {
			w.Timezone = "America/Los_Angeles"
		}
		if w.SQLite == "" {
			w.SQLite = "data/" + w.ID + "/wiki.db"
		}
		if w.Files == "" {
			w.Files = "data/" + w.ID + "/files"
		}
		if w.ProjectNS == "" {
			w.ProjectNS = w.Name
		}
		if w.MaxUpload <= 0 {
			w.MaxUpload = 20 << 20
		}
		if len(w.UploadTypes) == 0 {
			w.UploadTypes = []string{"png", "jpg", "jpeg", "gif", "webp", "pdf"}
		} else {
			var clean []string
			for _, raw := range w.UploadTypes {
				t := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "."))
				if t != "" {
					clean = append(clean, t)
				}
			}
			w.UploadTypes = clean
		}
		if strings.TrimSpace(w.Accent) != "" {
			hex, ok := color.Normalize(w.Accent)
			if !ok {
				return nil, fmt.Errorf("wiki %s: accent must be a hex color like #1b3832", w.ID)
			}
			w.Accent = hex
		}
		w.PathPrefix = strings.TrimRight(w.PathPrefix, "/")
		if w.PathPrefix == "/" {
			w.PathPrefix = ""
		}
		for i, h := range w.Hosts {
			w.Hosts[i] = strings.ToLower(strings.TrimSpace(h))
		}
	}
	return &f, nil
}

// ByID finds a wiki.
func (f *File) ByID(id string) (*Wiki, error) {
	for i := range f.Wikis {
		if f.Wikis[i].ID == id {
			return &f.Wikis[i], nil
		}
	}
	return nil, fmt.Errorf("no wiki with id %q", id)
}
