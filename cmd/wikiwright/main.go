// Wikiwright is a small wiki engine.
//
// Copyright (c) 2026 Katherine W. Prawl. MIT License.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/config"
	"github.com/BlancoKat/wikiwright/internal/ingest"
	"github.com/BlancoKat/wikiwright/internal/mwapi"
	"github.com/BlancoKat/wikiwright/internal/mwxml"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "import":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		switch os.Args[2] {
		case "xml":
			importXML(os.Args[3:])
		case "api":
			importAPI(os.Args[3:])
		default:
			usage()
			os.Exit(2)
		}
	case "user":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		switch os.Args[2] {
		case "add":
			userAdd(os.Args[3:])
		case "password":
			userPassword(os.Args[3:])
		case "totp":
			userTOTP(os.Args[3:], true)
		case "totp-off":
			userTOTP(os.Args[3:], false)
		default:
			usage()
			os.Exit(2)
		}
	case "reindex":
		reindex(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("wikiwright", web.Version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Wikiwright %s

  wikiwright serve -config wikiwright.yaml
  wikiwright import xml -config wikiwright.yaml -wiki example -dump dump.xml [-images /path/to/images]
  wikiwright import api -config wikiwright.yaml -wiki example -api https://wiki.example.org/w/api.php [-images=false]
  wikiwright import api -config wikiwright.yaml -wiki draft -user USER -api https://draft.example.org/w/api.php [-cache draft-api.xml] [-images-only]
  wikiwright user add -config wikiwright.yaml -wiki example -name Ada [-admin] [-password ...]
  wikiwright user password -config wikiwright.yaml -wiki example -name Ada [-password ...]
  wikiwright user totp -config wikiwright.yaml -wiki example -name Ada
  wikiwright user totp-off -config wikiwright.yaml -wiki example -name Ada
  wikiwright reindex -config wikiwright.yaml -wiki example

Account passwords come from -password or WIKIWRIGHT_PASSWORD.
A private source wiki takes -user. Its password is WIKIWRIGHT_SOURCE_PASSWORD only, never a flag.
Name@botname uses a bot password. Any other name uses the main-account login.
Imports only read. Logging in is the only other request, and only when -user is set.
-cache saves the download as XML before reindex and reuses that file on the next run.
Pages already in the wiki database are not downloaded again. Existing user accounts are kept.
-images-only fetches files and does not download pages. Files already on disk are skipped.
When -api is https, same-host http:// image URLs are fetched as https://.
`, web.Version)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		die(err)
	}
	srv, err := web.Open(cfg)
	if err != nil {
		die(err)
	}
	defer srv.Close()
	if err := srv.ListenAndServe(); err != nil {
		die(err)
	}
}

func importXML(args []string) {
	fs := flag.NewFlagSet("import xml", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	dump := fs.String("dump", "", "MediaWiki XML dump")
	images := fs.String("images", "", "image directory to copy (optional)")
	_ = fs.Parse(args)
	if *id == "" || *dump == "" {
		die(fmt.Errorf("-wiki and -dump are required"))
	}
	w, st := openWiki(*cfgPath, *id)
	defer st.Close()
	f, err := os.Open(*dump)
	if err != nil {
		die(err)
	}
	defer f.Close()
	pages, stats, err := mwxml.Parse(f)
	if err != nil {
		die(err)
	}
	if err := ingest.Apply(st, pages, w.ProjectNS); err != nil {
		die(err)
	}
	fmt.Printf("imported %d pages, %d revisions", stats.Pages, stats.Revisions)
	if stats.Skipped > 0 {
		fmt.Printf(", skipped %d", stats.Skipped)
	}
	fmt.Println()
	for _, warn := range stats.Warnings {
		fmt.Println("warning:", warn)
	}
	if *images != "" {
		n, err := ingest.Images(st, *images, w.Files)
		if err != nil {
			die(err)
		}
		if err := ingest.Reindex(st, w.ProjectNS); err != nil {
			die(err)
		}
		fmt.Printf("copied %d files into %s\n", n, w.Files)
	}
}

func importAPI(args []string) {
	fs := flag.NewFlagSet("import api", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	api := fs.String("api", "", "api.php URL")
	images := fs.Bool("images", true, "download files referenced by allimages")
	imagesOnly := fs.Bool("images-only", false, "download files only; do not fetch pages")
	user := fs.String("user", "", "source wiki username; Name@botname is a bot password. Password is WIKIWRIGHT_SOURCE_PASSWORD")
	cache := fs.String("cache", "", "XML file of fetched pages. Written before reindex, and reused when it already exists")
	_ = fs.Parse(args)
	if *id == "" || *api == "" {
		die(fmt.Errorf("-wiki and -api are required"))
	}
	downloadFiles := *images || *imagesOnly
	sourcePass := ""
	// A finished cache plus -images=false never contacts the source wiki.
	if strings.TrimSpace(*user) != "" && !offlineCache(*cache, downloadFiles) {
		sourcePass = os.Getenv("WIKIWRIGHT_SOURCE_PASSWORD")
		if sourcePass == "" {
			die(fmt.Errorf("set WIKIWRIGHT_SOURCE_PASSWORD to log in; it is not accepted as a flag"))
		}
	}
	w, st := openWiki(*cfgPath, *id)
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	err := mwapi.Import(ctx, st, mwapi.Options{
		API:           *api,
		ProjectNS:     w.ProjectNS,
		FilesDir:      w.Files,
		DownloadFiles: downloadFiles,
		ImagesOnly:    *imagesOnly,
		Username:      *user,
		Password:      sourcePass,
		Cache:         *cache,
		Logf:          func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if err != nil {
		die(err)
	}
	fmt.Println("import finished")
}

func offlineCache(path string, images bool) bool {
	if images || strings.TrimSpace(path) == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

func userAdd(args []string) {
	fs := flag.NewFlagSet("user add", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	name := fs.String("name", "", "account name")
	admin := fs.Bool("admin", false, "mark the account as an administrator")
	passFlag := fs.String("password", "", "password, or set WIKIWRIGHT_PASSWORD")
	_ = fs.Parse(args)
	pass, err := readPassword(*passFlag)
	if err != nil || *id == "" || *name == "" {
		die(fmt.Errorf("-wiki, -name, and a password are required"))
	}
	_, st := openWiki(*cfgPath, *id)
	defer st.Close()
	hash, err := auth.Hash(pass, auth.ProductionParams)
	if err != nil {
		die(err)
	}
	if err := st.CreateUser(*name, hash, *admin); err != nil {
		die(err)
	}
	fmt.Printf("added user %s\n", *name)
}

func userPassword(args []string) {
	fs := flag.NewFlagSet("user password", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	name := fs.String("name", "", "account name")
	passFlag := fs.String("password", "", "password, or set WIKIWRIGHT_PASSWORD")
	_ = fs.Parse(args)
	pass, err := readPassword(*passFlag)
	if err != nil || *id == "" || *name == "" {
		die(fmt.Errorf("-wiki, -name, and a password are required"))
	}
	_, st := openWiki(*cfgPath, *id)
	defer st.Close()
	user, err := st.UserByName(*name)
	if err != nil || user.ID == 0 {
		die(fmt.Errorf("no such user"))
	}
	hash, err := auth.Hash(pass, auth.ProductionParams)
	if err != nil {
		die(err)
	}
	if err := st.SetPassword(user.ID, hash); err != nil {
		die(err)
	}
	fmt.Printf("updated password for %s\n", user.Name)
}

func userTOTP(args []string, enable bool) {
	fs := flag.NewFlagSet("user totp", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	name := fs.String("name", "", "account name")
	_ = fs.Parse(args)
	if *id == "" || *name == "" {
		die(fmt.Errorf("-wiki and -name are required"))
	}
	w, st := openWiki(*cfgPath, *id)
	defer st.Close()
	user, err := st.UserByName(*name)
	if err != nil || user.ID == 0 {
		die(fmt.Errorf("no such user"))
	}
	if !enable {
		if err := st.SetTOTP(user.ID, "", false); err != nil {
			die(err)
		}
		fmt.Printf("two-factor authentication disabled for %s\n", user.Name)
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		die(err)
	}
	if err := st.SetTOTP(user.ID, secret, true); err != nil {
		die(err)
	}
	fmt.Printf("two-factor authentication enabled for %s\n", user.Name)
	fmt.Printf("secret: %s\n", secret)
	fmt.Printf("otpauth: %s\n", auth.OTPAuthURL(w.Name, user.Name, secret))
}

func reindex(args []string) {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	cfgPath := fs.String("config", "wikiwright.yaml", "configuration file")
	id := fs.String("wiki", "", "wiki id")
	_ = fs.Parse(args)
	if *id == "" {
		die(fmt.Errorf("-wiki is required"))
	}
	w, st := openWiki(*cfgPath, *id)
	defer st.Close()
	if err := ingest.Reindex(st, w.ProjectNS); err != nil {
		die(err)
	}
	fmt.Println("reindexed", w.Name)
}

func openWiki(cfgPath, id string) (*config.Wiki, *store.Store) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		die(err)
	}
	w, err := cfg.ByID(id)
	if err != nil {
		die(err)
	}
	st, err := store.Open(w.SQLite)
	if err != nil {
		die(err)
	}
	return w, st
}

func readPassword(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if p := os.Getenv("WIKIWRIGHT_PASSWORD"); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("missing password")
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "wikiwright:", err)
	os.Exit(1)
}
