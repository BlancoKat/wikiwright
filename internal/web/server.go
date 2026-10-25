// Package web serves one or more Wikiwright sites.
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/BlancoKat/wikiwright/internal/config"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

// Version is the engine version reported on Special:Wikiwright.
const Version = "1.0.0"

// Site is one wiki: its configuration and its database.
type Site struct {
	Cfg    *config.Wiki
	Store  *store.Store
	Loc    *time.Location
	Secure bool
}

// Server is the multi-wiki HTTP handler.
type Server struct {
	Cfg   *config.File
	Sites []*Site
}

// Open loads every wiki database named in the configuration.
func Open(cfg *config.File) (*Server, error) {
	s := &Server{Cfg: cfg}
	for i := range cfg.Wikis {
		w := cfg.Wikis[i]
		if w.Timezone == "" {
			w.Timezone = "America/Los_Angeles"
		}
		if w.PathPrefix == "/" {
			w.PathPrefix = ""
		}
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			loc = time.UTC
		}
		st, err := store.Open(w.SQLite)
		if err != nil {
			s.Close()
			return nil, err
		}
		if w.Files != "" {
			if err := os.MkdirAll(w.Files, 0o755); err != nil {
				st.Close()
				s.Close()
				return nil, err
			}
		}
		s.Sites = append(s.Sites, &Site{Cfg: &w, Store: st, Loc: loc, Secure: cfg.CookieSecure})
	}
	return s, nil
}

// Close closes every wiki database.
func (s *Server) Close() error {
	var err error
	for _, site := range s.Sites {
		if site.Store != nil {
			if e := site.Store.Close(); e != nil {
				err = e
			}
		}
	}
	return err
}

// ListenAndServe serves the configuration's listen address.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.Cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("wikiwright %s listening on %s", Version, s.Cfg.Listen)
	return srv.ListenAndServe()
}

// ServeHTTP routes a request to static files, a wiki, or the site directory.
// A panic in a handler becomes an error page. The process keeps serving.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	guard(w, func(w http.ResponseWriter) { s.serveHTTP(w, r) })
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Cache-Control", "private")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")

	path := r.URL.Path
	if path == "/static" || strings.HasPrefix(path, "/static/") {
		// A matching ?v= is the content hash. That URL can stay cached.
		// A missing or stale v must not be cached, or a browser keeps old CSS.
		if r.URL.Query().Get("v") == staticVersion {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		}
		staticHandler.ServeHTTP(w, r)
		return
	}
	host := requestHost(r.Host)
	site, rest, ok := s.match(host, path)
	if !ok {
		if path == "/" || path == "" {
			s.directory(w, r, host)
			return
		}
		http.NotFound(w, r)
		return
	}
	s.serveSite(site, rest, w, r)
}

const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'none'; frame-src https://www.youtube-nocookie.com https://player.vimeo.com https://www.dailymotion.com; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'"

type wroteWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *wroteWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *wroteWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

// guard runs fn and, if it panics before writing a response, sends an HTML
// error page instead of crashing the process.
func guard(w http.ResponseWriter, fn func(http.ResponseWriter)) {
	bw := &wroteWriter{ResponseWriter: w}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("http panic: %v\n%s", rec, debug.Stack())
			if !bw.wrote {
				writeInternalError(w)
			}
		}
	}()
	fn(bw)
}

func writeInternalError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Internal error</title></head>
<body><h1>Internal error</h1><p>This request failed. The wiki is still running.</p></body></html>
`)
}

var staticHandler http.Handler

// staticVersion is a content hash of the embedded static files.
var staticVersion string

func init() {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	sum := sha256.New()
	err = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		sum.Write([]byte(path))
		sum.Write([]byte{0})
		sum.Write(b)
		return nil
	})
	if err != nil {
		panic(err)
	}
	staticVersion = hex.EncodeToString(sum.Sum(nil))[:16]
	staticHandler = http.StripPrefix("/static/", noDir{http.FileServer(http.FS(sub))})
}

// staticURL is a cache-busted path for an embedded file, such as wikiwright.css.
func staticURL(name string) string {
	name = strings.TrimPrefix(name, "/")
	return "/static/" + name + "?v=" + staticVersion
}

type noDir struct{ h http.Handler }

func (n noDir) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "" {
		http.NotFound(w, r)
		return
	}
	n.h.ServeHTTP(w, r)
}

func requestHost(hostport string) string {
	hostport = strings.ToLower(strings.TrimSpace(hostport))
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func (s *Server) sitesForHost(host string) []*Site {
	var exact, fallback []*Site
	for _, site := range s.Sites {
		if len(site.Cfg.Hosts) == 0 {
			fallback = append(fallback, site)
			continue
		}
		for _, h := range site.Cfg.Hosts {
			if h == host {
				exact = append(exact, site)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fallback
}

func (s *Server) match(host, path string) (*Site, string, bool) {
	if path == "" {
		path = "/"
	}
	var best *Site
	bestLen := -1
	for _, site := range s.sitesForHost(host) {
		p := site.Cfg.PathPrefix
		if p == "" {
			if bestLen < 0 {
				best = site
				bestLen = 0
			}
			continue
		}
		if path == p || strings.HasPrefix(path, p+"/") {
			if len(p) > bestLen {
				best = site
				bestLen = len(p)
			}
		}
	}
	if best == nil {
		return nil, "", false
	}
	rest := path
	if best.Cfg.PathPrefix != "" {
		rest = strings.TrimPrefix(path, best.Cfg.PathPrefix)
		if rest == "" {
			rest = "/"
		}
	}
	return best, rest, true
}

func (s *Server) directory(w http.ResponseWriter, r *http.Request, host string) {
	sites := s.sitesForHost(host)
	if len(sites) == 0 {
		sites = s.Sites
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>Wikis</title>")
	b.WriteString(`<link rel="stylesheet" href="` + esc(staticURL("wikiwright.css")) + `">`)
	b.WriteString("</head><body class=\"wikiwright-directory\"><h1>Wikis on this server</h1><ul class=\"wikiwright-plain\">")
	for _, site := range sites {
		href := site.pageURL(site.Cfg.MainPage)
		b.WriteString(`<li><a href="` + esc(href) + `">` + esc(site.Cfg.Name) + `</a>`)
		if site.Cfg.Access == "private" {
			b.WriteString(` <span class="wikiwright-muted">(login required)</span>`)
		}
		b.WriteString(`</li>`)
	}
	b.WriteString("</ul></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) serveSite(site *Site, rest string, w http.ResponseWriter, r *http.Request) {
	if rest == "/api.php" {
		site.serveAPI(w, r)
		return
	}
	if rest == "/logo" {
		site.serveLogo(w, r)
		return
	}
	if rest == "/images" || strings.HasPrefix(rest, "/images/") {
		if site.blocked(r) {
			http.Redirect(w, r, site.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape(site.Cfg.MainPage), http.StatusFound)
			return
		}
		name := strings.TrimPrefix(rest, "/images/")
		site.serveImage(w, r, name)
		return
	}
	if rest == "/" || rest == "" {
		http.Redirect(w, r, site.pageURL(site.Cfg.MainPage), http.StatusFound)
		return
	}
	titleName := ""
	switch {
	case rest == "/index.php":
		titleName = r.URL.Query().Get("title")
	case strings.HasPrefix(rest, "/index.php/"):
		titleName = strings.TrimPrefix(rest, "/index.php/")
		if q := r.URL.Query().Get("title"); q != "" {
			titleName = q
		}
	default:
		site.notFound(w, r)
		return
	}
	if titleName == "" {
		titleName = site.Cfg.MainPage
	}
	if site.blocked(r) && !publicSpecial(titleName, site.Cfg.ProjectNS) {
		http.Redirect(w, r, site.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape(titleName), http.StatusFound)
		return
	}
	site.dispatch(w, r, titleName)
}

func (s *Site) blocked(r *http.Request) bool {
	return s.Cfg.Access == "private" && !s.loggedIn(r)
}

func publicSpecial(raw, project string) bool {
	t := title.Parse(raw, project)
	if t.Namespace != title.NSSpecial {
		return false
	}
	name, _ := splitSpecial(t.Rest)
	switch name {
	case "userlogin", "login", "userlogout", "logout":
		return true
	default:
		return false
	}
}

func (s *Site) serveLogo(w http.ResponseWriter, r *http.Request) {
	path := s.logoFile()
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, path)
}

func (s *Site) logoFile() string {
	logo := strings.TrimSpace(s.Cfg.Logo)
	if logo == "" || strings.HasPrefix(logo, "http://") || strings.HasPrefix(logo, "https://") || strings.HasPrefix(logo, "/") {
		return ""
	}
	candidates := []string{logo}
	if s.Cfg.Files != "" && !strings.Contains(logo, "/") {
		candidates = append([]string{filepath.Join(s.Cfg.Files, logo)}, candidates...)
	}
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Site) serveImage(w http.ResponseWriter, r *http.Request, name string) {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	name = filepath.Base(name)
	rec, err := s.Store.File(name)
	if err != nil || rec.Name == "" {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.Cfg.Files, rec.Name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if rec.MIME != "" {
		w.Header().Set("Content-Type", rec.MIME)
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
	}
	return host
}
