package mwapi

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlancoKat/wikiwright/internal/store"
)

func TestUpgradeFileURL(t *testing.T) {
	cases := []struct {
		api, raw, want string
	}{
		{"https://wiki.example.org/w/api.php", "http://wiki.example.org/w/images/a.png", "https://wiki.example.org/w/images/a.png"},
		{"https://wiki.example.org/w/api.php", "http://wiki.example.org:80/w/images/a.png", "https://wiki.example.org/w/images/a.png"},
		{"https://Wiki.example.org/w/api.php", "http://wiki.example.org/a.png", "https://wiki.example.org/a.png"},
		{"https://wiki.example.org/w/api.php", "http://www.wiki.example.org/a.png", "http://www.wiki.example.org/a.png"},
		{"https://wiki.example.org/w/api.php", "http://upload.wikimedia.org/wikipedia/commons/a/ab/A.png", "http://upload.wikimedia.org/wikipedia/commons/a/ab/A.png"},
		{"https://wiki.example.org/w/api.php", "https://wiki.example.org/w/images/a.png", "https://wiki.example.org/w/images/a.png"},
		{"http://127.0.0.1:9/api.php", "http://127.0.0.1:9/a.png", "http://127.0.0.1:9/a.png"},
	}
	if fileConnectTimeout != 10*time.Second || fileWorkers != 4 {
		t.Fatalf("timeout %s workers %d", fileConnectTimeout, fileWorkers)
	}
	for _, tc := range cases {
		got := upgradeFileURL(tc.api, tc.raw)
		if got != tc.want {
			t.Errorf("api %s raw %s\n got %s\nwant %s", tc.api, tc.raw, got, tc.want)
		}
	}
}

func TestImageHTTPSSkipParallelAndSummary(t *testing.T) {
	pngBytes := tinyPNG(t)
	var mu sync.Mutex
	var seen []string
	var inflight, maxInflight int32
	var goodGets atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
		case q.Get("list") == "allimages":
			host := r.Host
			_, _ = fmt.Fprintf(w, `{"query":{"allimages":[
				{"name":"One.png","url":"http://%s/one.png","mime":"image/png","size":80,"width":1,"height":1},
				{"name":"Two.png","url":"http://%s/two.png","mime":"image/png","size":80,"width":1,"height":1},
				{"name":"Three.png","url":"http://%s/three.png","mime":"image/png","size":80,"width":1,"height":1},
				{"name":"Bad.png","url":"http://%s/bad.png","mime":"image/png","size":80,"width":1,"height":1}
			]}}`, host, host, host, host)
		default:
			http.Error(w, "unexpected "+r.URL.RawQuery, 500)
		}
	})
	slow := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			old := atomic.LoadInt32(&maxInflight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInflight, old, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		goodGets.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}
	mux.HandleFunc("/one.png", slow)
	mux.HandleFunc("/two.png", slow)
	mux.HandleFunc("/three.png", slow)
	mux.HandleFunc("/bad.png", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := srv.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.Scheme+"://"+r.URL.Host+r.URL.RequestURI())
		mu.Unlock()
		cp := r.Clone(r.Context())
		cp.URL.Scheme = "http"
		return base.RoundTrip(cp)
	})}
	dir := t.TempDir()
	st, err := store.Open(dir + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs []string
	var logMu sync.Mutex
	opt := Options{
		API:           "https://" + u.Host + "/api.php",
		ProjectNS:     "Draft Wiki",
		FilesDir:      dir + "/files",
		DownloadFiles: true,
		Client:        client,
		Delay:         -1,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			logs = append(logs, fmt.Sprintf(format, args...))
			logMu.Unlock()
		},
	}
	if err := Import(context.Background(), st, opt); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&maxInflight) < 2 {
		t.Fatalf("max in flight %d", maxInflight)
	}
	if !logHas(logs, "api: images: downloaded 3, skipped 0, failed 1") {
		t.Fatalf("logs %v", logs)
	}
	for _, path := range []string{"/one.png", "/two.png", "/three.png"} {
		if !sawHTTPS(seen, u.Host, path) {
			t.Fatalf("missing https fetch of %s in %v", path, seen)
		}
	}
	f, err := st.File("One.png")
	if err != nil || f.Name == "" || f.MIME != "image/png" {
		t.Fatalf("%+v %v", f, err)
	}
	logs = nil
	goodGets.Store(0)
	if err := Import(context.Background(), st, opt); err != nil {
		t.Fatal(err)
	}
	if goodGets.Load() != 0 {
		t.Fatalf("rerun fetched %d files that were already on disk", goodGets.Load())
	}
	if !logHas(logs, "api: images: downloaded 0, skipped 3, failed 1") {
		t.Fatalf("rerun logs %v", logs)
	}
}

func TestImagesOnlySkipsPages(t *testing.T) {
	pngBytes := tinyPNG(t)
	var pageLists atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			pageLists.Add(1)
			_, _ = w.Write([]byte(`{"query":{"allpages":[{"title":"Secret"}]}}`))
		case q.Get("list") == "allimages":
			_, _ = fmt.Fprintf(w, `{"query":{"allimages":[{"name":"Dot.png","url":"http://%s/dot.png","mime":"image/png","size":80,"width":1,"height":1}]}}`, r.Host)
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	mux.HandleFunc("/dot.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	st, err := store.Open(dir + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Import(context.Background(), st, Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Draft Wiki",
		FilesDir:      dir + "/files",
		DownloadFiles: false,
		ImagesOnly:    true,
		Client:        srv.Client(),
		Delay:         -1,
		Logf:          func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageLists.Load() != 0 {
		t.Fatalf("images-only listed pages %d times", pageLists.Load())
	}
	if _, ok, _ := st.ContentByDBKey("Secret"); ok {
		t.Fatal("images-only stored a page")
	}
	f, err := st.File("Dot.png")
	if err != nil || f.Name == "" {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestImportMediaWikiSidebar(t *testing.T) {
	var sawNS8 atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Get("list") == "allpages":
			if q.Get("apnamespace") == "8" {
				sawNS8.Store(true)
				_, _ = w.Write([]byte(`{"query":{"allpages":[{"title":"MediaWiki:Sidebar"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
		case q.Get("prop") == "revisions":
			_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"MediaWiki:Sidebar","revisions":[{"revid":11,"parentid":0,"user":"Ada","timestamp":"2020-01-02T00:00:00Z","comment":"nav","minor":false,"slots":{"main":{"content":"* navigation\n** mainpage|mainpage-description\n"}}}]}]}}`))
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Import(context.Background(), st, Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Draft Wiki",
		DownloadFiles: false,
		Client:        srv.Client(),
		Delay:         -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawNS8.Load() {
		t.Fatal("namespace 8 was not requested")
	}
	body, ok, err := st.ContentByDBKey("MediaWiki:Sidebar")
	if err != nil || !ok || !bytes.Contains([]byte(body), []byte("** mainpage|mainpage-description")) {
		t.Fatalf("%q %v %v", body, ok, err)
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func logHas(logs []string, want string) bool {
	for _, line := range logs {
		if line == want {
			return true
		}
	}
	return false
}

func sawHTTPS(seen []string, host, path string) bool {
	want := "https://" + host + path
	for _, s := range seen {
		if s == want || len(s) >= len(want) && s[:len(want)] == want {
			return true
		}
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
