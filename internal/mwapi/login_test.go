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
	"strings"
	"testing"

	"github.com/BlancoKat/wikiwright/internal/store"
)

func dotPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// privateWiki is a login-required MediaWiki: queries and img_auth.php fail
// until the session cookie is set by action=login or action=clientlogin.
type privateWiki struct {
	t        *testing.T
	password string
	bot      bool
	png      []byte
	posts    int
	gotNeed  bool
}

func (p *privateWiki) authed(r *http.Request) bool {
	c, err := r.Cookie("mwsession")
	return err == nil && c.Value == "auth"
}

func (p *privateWiki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.RawQuery, p.password) {
		p.t.Errorf("password appeared in the URL")
	}
	if strings.Contains(r.URL.Path, "img_auth.php") {
		if !p.authed(r) {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(p.png)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/api.php") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		p.posts++
		if err := r.ParseForm(); err != nil {
			p.t.Errorf("parse form: %v", err)
			http.Error(w, "bad", 400)
			return
		}
		action := r.PostForm.Get("action")
		if action != "login" && action != "clientlogin" {
			p.t.Errorf("non-read POST action=%s", action)
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		if p.bot && action != "login" {
			p.t.Errorf("bot account used %s", action)
		}
		if !p.bot && action != "clientlogin" {
			p.t.Errorf("main account used %s", action)
		}
		pass := r.PostForm.Get("lgpassword")
		token := r.PostForm.Get("lgtoken")
		if action == "clientlogin" {
			pass = r.PostForm.Get("password")
			token = r.PostForm.Get("logintoken")
			if r.PostForm.Get("loginreturnurl") == "" {
				p.t.Error("clientlogin missing loginreturnurl")
			}
		} else if !strings.Contains(r.PostForm.Get("lgname"), "@") {
			p.t.Errorf("bot name %q", r.PostForm.Get("lgname"))
		}
		w.Header().Set("Content-Type", "application/json")
		if pass != p.password {
			if p.bot {
				_, _ = w.Write([]byte(`{"login":{"result":"Failed","reason":"WrongPass ` + pass + `"}}`))
			} else {
				_, _ = w.Write([]byte(`{"clientlogin":{"status":"FAIL","message":"WrongPass ` + pass + `"}}`))
			}
			return
		}
		if token == "tok1" && !p.gotNeed {
			p.gotNeed = true
			_, _ = w.Write([]byte(`{"login":{"result":"NeedToken","token":"tok2"}}`))
			return
		}
		if token != "tok1" && token != "tok2" {
			p.t.Errorf("unexpected token %q", token)
		}
		http.SetCookie(w, &http.Cookie{Name: "mwsession", Value: "auth", Path: "/"})
		if p.bot {
			_, _ = w.Write([]byte(`{"login":{"result":"Success","lgusername":"Ada"}}`))
		} else {
			_, _ = w.Write([]byte(`{"clientlogin":{"status":"PASS","username":"Ada"}}`))
		}
		return
	}
	q := r.URL.Query()
	if q.Get("action") != "" && q.Get("action") != "query" {
		p.t.Errorf("non-read GET action=%s", q.Get("action"))
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if q.Get("meta") == "tokens" {
		http.SetCookie(w, &http.Cookie{Name: "mwsession", Value: "pending", Path: "/"})
		_, _ = w.Write([]byte(`{"query":{"tokens":{"logintoken":"tok1"}}}`))
		return
	}
	if !p.authed(r) {
		_, _ = w.Write([]byte(`{"error":{"code":"readapidenied","info":"You need read permission to use this module."}}`))
		return
	}
	switch {
	case q.Get("list") == "allpages":
		if q.Get("apnamespace") == "0" {
			_, _ = w.Write([]byte(`{"query":{"allpages":[{"title":"Sandbox"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"query":{"allpages":[]}}`))
	case q.Get("list") == "allimages":
		_, _ = w.Write([]byte(`{"query":{"allimages":[{"name":"Dot.png","url":"http://` + r.Host + `/w/img_auth.php/a/ab/Dot.png","mime":"image/png","size":1,"width":1,"height":1}]}}`))
	case q.Get("prop") == "revisions":
		_, _ = w.Write([]byte(`{"query":{"pages":[{"title":"Sandbox","revisions":[{"revid":9,"parentid":0,"user":"Ada","timestamp":"2024-01-02T03:04:05Z","comment":"private","minor":false,"slots":{"main":{"content":"Sandbox note"}}}]}]}}`))
	default:
		_, _ = w.Write([]byte(`{"query":{}}`))
	}
}

func TestPrivateBotLogin(t *testing.T) {
	pngBytes := dotPNG(t)
	wiki := &privateWiki{t: t, password: "s3cret-test", bot: true, png: pngBytes}
	srv := httptest.NewServer(wiki)
	defer srv.Close()
	dir := t.TempDir()
	st, err := store.Open(dir + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs []string
	err = Import(context.Background(), st, Options{
		API:           srv.URL + "/api.php",
		ProjectNS:     "Draft Wiki",
		FilesDir:      dir + "/files",
		DownloadFiles: true,
		Delay:         -1,
		Username:      "Ada@importer",
		Password:      wiki.password,
		Logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, ok, err := st.ContentByDBKey("Sandbox")
	if err != nil || !ok || body != "Sandbox note" {
		t.Fatalf("page %q ok=%v err=%v", body, ok, err)
	}
	f, err := st.File("Dot.png")
	if err != nil || f.Name == "" || f.MIME != "image/png" {
		t.Fatalf("file %+v %v", f, err)
	}
	if wiki.posts != 2 {
		t.Fatalf("login posts = %d, want 2 (NeedToken then Success)", wiki.posts)
	}
	joined := strings.Join(logs, "\n")
	if strings.Contains(joined, wiki.password) || strings.Contains(joined, "mwsession") || strings.Contains(joined, "Cookie") {
		t.Fatalf("log leaked a secret: %s", joined)
	}
}

func TestPrivateClientLogin(t *testing.T) {
	pngBytes := dotPNG(t)
	wiki := &privateWiki{t: t, password: "s3cret-test", bot: false, png: pngBytes, gotNeed: true}
	srv := httptest.NewServer(wiki)
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
		DownloadFiles: true,
		Client:        srv.Client(),
		Delay:         -1,
		Username:      "Ada",
		Password:      wiki.password,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, ok, err := st.ContentByDBKey("Sandbox")
	if err != nil || !ok || body != "Sandbox note" {
		t.Fatalf("page %q ok=%v err=%v", body, ok, err)
	}
	f, err := st.File("Dot.png")
	if err != nil || f.Name == "" {
		t.Fatalf("file %+v %v", f, err)
	}
	if wiki.posts != 1 {
		t.Fatalf("clientlogin posts = %d", wiki.posts)
	}
}

func TestPrivateLoginScrubsPassword(t *testing.T) {
	wiki := &privateWiki{t: t, password: "s3cret-test", bot: false, gotNeed: true}
	srv := httptest.NewServer(wiki)
	defer srv.Close()
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Import(context.Background(), st, Options{
		API:      srv.URL + "/api.php",
		Client:   srv.Client(),
		Delay:    -1,
		Username: "Ada",
		Password: "wrong-secret",
	})
	if err == nil {
		t.Fatal("expected login failure")
	}
	if strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("error contains the password: %v", err)
	}
}

func TestLoginRequiresEnvPassword(t *testing.T) {
	err := login(context.Background(), Options{Username: "Ada", Delay: -1}, "http://127.0.0.1/api.php")
	if err == nil || !strings.Contains(err.Error(), "WIKIWRIGHT_SOURCE_PASSWORD") {
		t.Fatalf("%v", err)
	}
}

func TestRefuseNonReadAction(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		http.Error(w, "no", 500)
	}))
	defer srv.Close()
	err := get(context.Background(), Options{Password: "s3cret-test", Delay: -1, Client: srv.Client()}, srv.URL+"/api.php", url.Values{"action": {"edit"}}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "refusing non-read") {
		t.Fatalf("%v", err)
	}
	if hit {
		t.Fatal("edit request was sent")
	}
	err = post(context.Background(), Options{Delay: -1, Client: srv.Client()}, srv.URL+"/api.php", url.Values{"action": {"edit"}, "text": {"x"}}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "refusing non-read") {
		t.Fatalf("%v", err)
	}
	if hit {
		t.Fatal("edit POST was sent")
	}
}

func TestAnonymousPrivateWikiIsDenied(t *testing.T) {
	wiki := &privateWiki{t: t, password: "s3cret-test", bot: true, gotNeed: true}
	srv := httptest.NewServer(wiki)
	defer srv.Close()
	st, err := store.Open(t.TempDir() + "/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Import(context.Background(), st, Options{
		API:    srv.URL + "/api.php",
		Client: srv.Client(),
		Delay:  -1,
	})
	if err == nil || !strings.Contains(err.Error(), "readapidenied") {
		t.Fatalf("%v", err)
	}
	if wiki.posts != 0 {
		t.Fatalf("anonymous import posted %d times", wiki.posts)
	}
}
