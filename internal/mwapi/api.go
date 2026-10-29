// Package mwapi imports a MediaWiki site through api.php.
// Reading is the default. A private wiki may log in first; that login is the
// only non-read request this package sends. The password is never written to
// a URL, a log line, or an error string.
package mwapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BlancoKat/wikiwright/internal/ingest"
	"github.com/BlancoKat/wikiwright/internal/mwxml"
	"github.com/BlancoKat/wikiwright/internal/store"
)

// Options controls a live import.
type Options struct {
	API           string
	ProjectNS     string
	FilesDir      string
	DownloadFiles bool
	UserAgent     string
	Client        *http.Client
	Logf          func(string, ...any)
	// Delay is the pause between API calls. Zero uses 120ms. Negative skips it.
	Delay time.Duration
	// Username logs in before reading. A name containing @ is a bot password
	// (Name@botname) and uses action=login. Any other name uses action=clientlogin.
	// Password comes from the caller (the CLI reads WIKIWRIGHT_SOURCE_PASSWORD).
	// It is never a flag, and it is never logged.
	Username string
	Password string
	// Cache is an XML file written after pages are fetched and before they are
	// reindexed. When the file already exists and is non-empty, Import reads it
	// and does not download page text again. Pages already stored in st are
	// also skipped during a fetch, so a rerun after a crash does not repeat
	// those downloads. Users already in st are never removed.
	Cache string
	// ImagesOnly downloads files and reindexes, and does not fetch pages.
	// Use it when the wiki text is already imported.
	ImagesOnly bool
}

// FileStats is the image-download summary printed at the end of an import.
type FileStats struct {
	Downloaded int
	Skipped    int
	Failed     int
}

func (s FileStats) add(o FileStats) FileStats {
	return FileStats{
		Downloaded: s.Downloaded + o.Downloaded,
		Skipped:    s.Skipped + o.Skipped,
		Failed:     s.Failed + o.Failed,
	}
}

const (
	fileWorkers        = 4
	fileConnectTimeout = 10 * time.Second
)

// Import pulls pages (full history, main slot) and optionally files.
// Page text is written into the database as each page arrives, and again from
// a cache file when one is set, so a crash during reindex does not require
// another download. Existing users are left in place.
func Import(ctx context.Context, st *store.Store, opt Options) error {
	if err := prepareClient(&opt); err != nil {
		return err
	}
	api := strings.TrimSpace(opt.API)
	if opt.ImagesOnly {
		if api == "" {
			return fmt.Errorf("missing api url")
		}
		if err := login(ctx, opt, api); err != nil {
			return err
		}
		stats, err := downloadAllFiles(ctx, opt, api, st)
		logFileStats(opt, stats)
		if err != nil {
			return err
		}
		return ingest.Reindex(st, opt.ProjectNS)
	}
	pages, fromCache, err := pagesFromCache(opt)
	if err != nil {
		return err
	}
	if fromCache {
		opt.Logf("api: reused %d pages from %s", len(pages), opt.Cache)
	}
	needNet := !fromCache || opt.DownloadFiles
	if api == "" && needNet {
		return fmt.Errorf("missing api url")
	}
	if needNet {
		if err := login(ctx, opt, api); err != nil {
			return err
		}
	}
	if !fromCache {
		pages, err = fetchPages(ctx, st, opt, api)
		if err != nil {
			return err
		}
	}
	if err := ingest.Apply(st, pages, opt.ProjectNS); err != nil {
		return err
	}
	if opt.DownloadFiles {
		stats, err := downloadAllFiles(ctx, opt, api, st)
		logFileStats(opt, stats)
		if err != nil {
			return err
		}
		if err := ingest.Reindex(st, opt.ProjectNS); err != nil {
			return err
		}
	}
	return nil
}

func logFileStats(opt Options, stats FileStats) {
	opt.Logf("api: images: downloaded %d, skipped %d, failed %d", stats.Downloaded, stats.Skipped, stats.Failed)
}

func fetchPages(ctx context.Context, st *store.Store, opt Options, api string) ([]mwxml.Page, error) {
	namespaces := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	var titles []string
	for _, ns := range namespaces {
		got, err := allPages(ctx, opt, api, ns)
		if err != nil {
			return nil, err
		}
		titles = append(titles, got...)
	}
	var pages []mwxml.Page
	skipped := 0
	for i, rawTitle := range titles {
		stored, err := pageStored(st, opt.ProjectNS, rawTitle)
		if err != nil {
			return nil, err
		}
		if stored {
			skipped++
			continue
		}
		revs, err := revisions(ctx, opt, api, rawTitle)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rawTitle, err)
		}
		pg := mwxml.Page{Title: rawTitle, Revisions: revs}
		// Keep each finished page even if a later page, or reindex, crashes.
		if err := ingest.StorePages(st, []mwxml.Page{pg}, opt.ProjectNS); err != nil {
			return nil, err
		}
		pages = append(pages, pg)
		if (len(pages))%25 == 0 {
			opt.Logf("api: fetched %d new pages (%d/%d listed)", len(pages), i+1, len(titles))
		}
	}
	opt.Logf("api: %d pages listed, %d already stored, %d fetched", len(titles), skipped, len(pages))
	if opt.Cache != "" && skipped == 0 {
		if err := writeCache(opt.Cache, pages); err != nil {
			return nil, err
		}
		opt.Logf("api: wrote cache %s", opt.Cache)
	} else if opt.Cache != "" && skipped > 0 {
		opt.Logf("api: left %s unused; %d pages were already in the database", opt.Cache, skipped)
	}
	return pages, nil
}

func pageStored(st *store.Store, project, rawTitle string) (bool, error) {
	t, ok := mwxml.DBKey(rawTitle, project)
	if !ok {
		return false, nil
	}
	pg, err := st.PageByDBKey(t.DBKey)
	if err != nil {
		return false, err
	}
	return pg.ID != 0 && pg.LatestRevID != 0, nil
}

func pagesFromCache(opt Options) ([]mwxml.Page, bool, error) {
	path := strings.TrimSpace(opt.Cache)
	if path == "" {
		return nil, false, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if fi.IsDir() {
		return nil, false, fmt.Errorf("cache %s is a directory", path)
	}
	if fi.Size() == 0 {
		return nil, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	pages, _, err := mwxml.Parse(f)
	if err != nil {
		return nil, false, fmt.Errorf("cache %s: %w", path, err)
	}
	return pages, true, nil
}

func writeCache(path string, pages []mwxml.Page) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	werr := mwxml.Write(f, pages)
	cerr := f.Close()
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if cerr != nil {
		os.Remove(tmp)
		return cerr
	}
	return os.Rename(tmp, path)
}

func allPages(ctx context.Context, opt Options, api string, ns int) ([]string, error) {
	var out []string
	cont := ""
	for {
		q := url.Values{
			"action": {"query"}, "list": {"allpages"}, "apnamespace": {fmt.Sprint(ns)},
			"aplimit": {"max"}, "format": {"json"}, "formatversion": {"2"},
		}
		if cont != "" {
			q.Set("apcontinue", cont)
		}
		var resp struct {
			Continue map[string]string `json:"continue"`
			Query    struct {
				Allpages []struct {
					Title string `json:"title"`
				} `json:"allpages"`
			} `json:"query"`
		}
		if err := get(ctx, opt, api, q, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Query.Allpages {
			out = append(out, p.Title)
		}
		cont = resp.Continue["apcontinue"]
		if cont == "" {
			break
		}
	}
	return out, nil
}

func revisions(ctx context.Context, opt Options, api, title string) ([]mwxml.Revision, error) {
	var all []mwxml.Revision
	cont := ""
	for {
		q := url.Values{
			"action": {"query"}, "prop": {"revisions"}, "titles": {title},
			"rvslots": {"main"}, "rvprop": {"ids|timestamp|user|comment|content"},
			"rvlimit": {"max"}, "format": {"json"}, "formatversion": {"2"},
		}
		if cont != "" {
			q.Set("rvcontinue", cont)
		}
		var resp struct {
			Continue map[string]string `json:"continue"`
			Query    struct {
				Pages []struct {
					Missing   bool   `json:"missing"`
					Title     string `json:"title"`
					Revisions []struct {
						RevID     int64  `json:"revid"`
						ParentID  int64  `json:"parentid"`
						User      string `json:"user"`
						Timestamp string `json:"timestamp"`
						Comment   string `json:"comment"`
						Minor     bool   `json:"minor"`
						Slots     map[string]struct {
							ContentModel  string `json:"contentmodel"`
							ContentFormat string `json:"contentformat"`
							Content       string `json:"content"`
							Star          string `json:"*"`
						} `json:"slots"`
					} `json:"revisions"`
				} `json:"pages"`
			} `json:"query"`
		}
		if err := get(ctx, opt, api, q, &resp); err != nil {
			return nil, err
		}
		if len(resp.Query.Pages) == 0 || resp.Query.Pages[0].Missing {
			return nil, nil
		}
		for _, rev := range resp.Query.Pages[0].Revisions {
			text := ""
			model := ""
			if slot, ok := rev.Slots["main"]; ok {
				text = slot.Content
				if text == "" {
					text = slot.Star
				}
				model = slot.ContentModel
			}
			ts, _ := time.Parse(time.RFC3339, rev.Timestamp)
			all = append(all, mwxml.Revision{
				MWRevID:  rev.RevID,
				ParentID: rev.ParentID,
				Time:     ts,
				User:     rev.User,
				Comment:  rev.Comment,
				Minor:    rev.Minor,
				Text:     text,
				Model:    model,
			})
		}
		cont = resp.Continue["rvcontinue"]
		if cont == "" {
			break
		}
	}
	// API returns newest first. Store oldest first so the last insert is current
	// and parent ids already exist when we care about them.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

type fileJob struct {
	url, name, mime, user string
	size                  int64
	w, h                  int
	shared                bool
}

func downloadAllFiles(ctx context.Context, opt Options, api string, st *store.Store) (FileStats, error) {
	local, err := downloadImages(ctx, opt, api, st)
	if err != nil {
		return local, err
	}
	shared, err := downloadShared(ctx, opt, api, st)
	return local.add(shared), err
}

func downloadImages(ctx context.Context, opt Options, api string, st *store.Store) (FileStats, error) {
	if err := mkdir(opt.FilesDir); err != nil {
		return FileStats{}, err
	}
	cont := ""
	var jobs []fileJob
	for {
		q := url.Values{
			"action": {"query"}, "list": {"allimages"}, "ailimit": {"max"},
			"aiprop": {"url|mime|size|dimensions|timestamp|user|comment"},
			"format": {"json"}, "formatversion": {"2"},
		}
		if cont != "" {
			q.Set("aicontinue", cont)
		}
		var resp struct {
			Continue map[string]string `json:"continue"`
			Query    struct {
				Allimages []struct {
					Name      string `json:"name"`
					URL       string `json:"url"`
					MIME      string `json:"mime"`
					Size      int64  `json:"size"`
					Width     int    `json:"width"`
					Height    int    `json:"height"`
					Timestamp string `json:"timestamp"`
					User      string `json:"user"`
					Comment   string `json:"comment"`
				} `json:"allimages"`
			} `json:"query"`
		}
		if err := get(ctx, opt, api, q, &resp); err != nil {
			return FileStats{}, err
		}
		for _, im := range resp.Query.Allimages {
			if im.URL == "" || im.Name == "" {
				continue
			}
			jobs = append(jobs, fileJob{
				url: im.URL, name: im.Name, mime: im.MIME, user: im.User,
				size: im.Size, w: im.Width, h: im.Height,
			})
		}
		cont = resp.Continue["aicontinue"]
		if cont == "" {
			break
		}
	}
	return runFileJobs(ctx, opt, st, jobs), nil
}

// downloadShared fetches File: targets that are not in the local image
// directory. MediaWiki reports those as missing pages with an image URL
// when InstantCommons (or another shared repo) has the file. SVG is kept
// for these imports; browser uploads still reject it.
func downloadShared(ctx context.Context, opt Options, api string, st *store.Store) (FileStats, error) {
	rows, err := st.DB.Query(`SELECT DISTINCT to_dbkey FROM links WHERE kind='link' AND to_dbkey LIKE 'File:%'`)
	if err != nil {
		return FileStats{}, err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return FileStats{}, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return FileStats{}, err
	}
	rows.Close()
	// The store uses one connection. Look up files only after the query is closed.
	var missing []string
	for _, key := range keys {
		name := strings.TrimPrefix(key, "File:")
		name = strings.ReplaceAll(name, "_", " ")
		f, err := st.File(name)
		if err != nil {
			return FileStats{}, err
		}
		if f.Name == "" {
			missing = append(missing, "File:"+name)
		}
	}
	var jobs []fileJob
	for i := 0; i < len(missing); i += 20 {
		end := i + 20
		if end > len(missing) {
			end = len(missing)
		}
		chunk := missing[i:end]
		q := url.Values{
			"action": {"query"}, "prop": {"imageinfo"}, "titles": {strings.Join(chunk, "|")},
			"iiprop": {"url|mime|size|dimensions"},
			"format": {"json"}, "formatversion": {"2"},
		}
		var resp struct {
			Query struct {
				Pages []struct {
					Title     string `json:"title"`
					Missing   bool   `json:"missing"`
					ImageInfo []struct {
						URL    string `json:"url"`
						MIME   string `json:"mime"`
						Size   int64  `json:"size"`
						Width  int    `json:"width"`
						Height int    `json:"height"`
					} `json:"imageinfo"`
				} `json:"pages"`
			} `json:"query"`
		}
		if err := get(ctx, opt, api, q, &resp); err != nil {
			return FileStats{}, err
		}
		for _, p := range resp.Query.Pages {
			if len(p.ImageInfo) == 0 || p.ImageInfo[0].URL == "" {
				continue
			}
			info := p.ImageInfo[0]
			name := strings.TrimPrefix(p.Title, "File:")
			jobs = append(jobs, fileJob{
				url: info.URL, name: name, mime: info.MIME,
				size: info.Size, w: info.Width, h: info.Height, shared: true,
			})
		}
	}
	return runFileJobs(ctx, opt, st, jobs), nil
}

func runFileJobs(ctx context.Context, opt Options, st *store.Store, jobs []fileJob) FileStats {
	var stats FileStats
	if len(jobs) == 0 {
		return stats
	}
	var mu sync.Mutex
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		opt.Logf(format, args...)
	}
	sem := make(chan struct{}, fileWorkers)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(job fileJob) {
			defer wg.Done()
			defer func() { <-sem }()
			d, s, f := takeFile(ctx, opt, st, job, logf)
			mu.Lock()
			stats.Downloaded += d
			stats.Skipped += s
			stats.Failed += f
			mu.Unlock()
		}(job)
	}
	wg.Wait()
	return stats
}

func takeFile(ctx context.Context, opt Options, st *store.Store, job fileJob, logf func(string, ...any)) (downloaded, skipped, failed int) {
	name := ingest.CleanName(job.name)
	if name == "" {
		logf("api: skip file %s: bad name", job.name)
		return 0, 0, 1
	}
	if ctx.Err() != nil {
		logf("api: skip file %s: %v", name, ctx.Err())
		return 0, 0, 1
	}
	// A file stored under a different first-letter case lives at that name on disk.
	if existing, err := st.File(name); err != nil {
		logf("api: skip file %s: %v", name, err)
		return 0, 0, 1
	} else if existing.Name != "" {
		name = existing.Name
	}
	disk := filepath.Join(opt.FilesDir, name)
	if fi, err := os.Stat(disk); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		f, err := st.File(name)
		if err != nil {
			logf("api: skip file %s: %v", name, err)
			return 0, 0, 1
		}
		if f.Name == "" {
			if err := ingest.RecordExisting(st, opt.FilesDir, name, job.user, job.mime, job.w, job.h); err != nil {
				logf("api: file %s is on disk but could not be recorded (%v); downloading again", name, err)
			} else {
				return 0, 1, 0
			}
		} else {
			return 0, 1, 0
		}
	}
	raw := upgradeFileURL(opt.API, job.url)
	if err := fetchFile(ctx, opt, raw, name, job.mime, job.size, job.w, job.h, job.user, st, job.shared); err != nil {
		logf("api: skip file %s: %v", name, err)
		return 0, 0, 1
	}
	return 1, 0, 0
}

// upgradeFileURL rewrites a same-host http:// file URL to https:// when the
// API itself is HTTPS. Port 80 is dropped so the file is not requested on the
// cleartext port. Other hosts, including Wikimedia Commons, are unchanged.
func upgradeFileURL(api, raw string) string {
	apiURL, err1 := url.Parse(strings.TrimSpace(api))
	fileURL, err2 := url.Parse(strings.TrimSpace(raw))
	if err1 != nil || err2 != nil || apiURL == nil || fileURL == nil {
		return raw
	}
	if !strings.EqualFold(apiURL.Scheme, "https") || !strings.EqualFold(fileURL.Scheme, "http") {
		return raw
	}
	if apiURL.Hostname() == "" || !strings.EqualFold(apiURL.Hostname(), fileURL.Hostname()) {
		return raw
	}
	fileURL.Scheme = "https"
	if fileURL.Port() == "80" {
		fileURL.Host = fileURL.Hostname()
	}
	return fileURL.String()
}

func fileHTTPClient(opt Options) *http.Client {
	if opt.Client == nil {
		return &http.Client{Timeout: 60 * time.Second, Transport: fileTransport()}
	}
	c := *opt.Client
	if c.Transport == nil {
		c.Transport = fileTransport()
	}
	if c.Timeout == 0 {
		c.Timeout = 60 * time.Second
	}
	return &c
}

func fileTransport() *http.Transport {
	dial := &net.Dialer{Timeout: fileConnectTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dial.DialContext,
		TLSHandshakeTimeout:   fileConnectTimeout,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func fetchFile(ctx context.Context, opt Options, rawURL, name, mime string, size int64, w, h int, user string, st *store.Store, shared bool) error {
	name = ingest.CleanName(name)
	if name == "" {
		return fmt.Errorf("bad name")
	}
	rawURL = upgradeFileURL(opt.API, rawURL)
	if opt.UserAgent == "" {
		opt.UserAgent = "Wikiwright/1.0 (https://github.com/BlancoKat/wikiwright; read-only)"
	}
	client := fileHTTPClient(opt)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("status %s", res.Status)
	}
	body := io.LimitReader(res.Body, 100<<20)
	var rec store.FileRec
	if shared {
		rec, err = ingest.SaveImported(st, opt.FilesDir, name, body, 100<<20, user, "")
	} else {
		rec, err = ingest.SaveUpload(st, opt.FilesDir, name, body, 100<<20, user, "")
	}
	if err != nil {
		return err
	}
	if rec.MIME == "" && mime != "" {
		rec.MIME = mime
	}
	if rec.Width == 0 {
		rec.Width = w
		rec.Height = h
	}
	if size > 0 && rec.Size == 0 {
		rec.Size = size
	}
	return st.UpsertFile(rec)
}

func get(ctx context.Context, opt Options, api string, q url.Values, dest any) error {
	if err := prepareClient(&opt); err != nil {
		return err
	}
	if action := q.Get("action"); action != "" && action != "query" {
		return fmt.Errorf("refusing non-read request action=%s", action)
	}
	if opt.Password != "" && strings.Contains(q.Encode(), opt.Password) {
		return fmt.Errorf("refusing to put the source password in a URL")
	}
	u, err := url.Parse(api)
	if err != nil {
		return err
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := opt.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("api status %s", res.Status)
	}
	var probe struct {
		Error *struct {
			Code string `json:"code"`
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("api json: %w", err)
	}
	if probe.Error != nil {
		return fmt.Errorf("api %s: %s", probe.Error.Code, scrub(opt.Password, probe.Error.Info))
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return err
	}
	if d := opt.delay(); d > 0 {
		time.Sleep(d)
	}
	return nil
}

func (opt Options) delay() time.Duration {
	if opt.Delay < 0 {
		return 0
	}
	if opt.Delay == 0 {
		return 120 * time.Millisecond
	}
	return opt.Delay
}

func mkdir(dir string) error {
	if dir == "" {
		return fmt.Errorf("files directory is empty")
	}
	return os.MkdirAll(dir, 0o755)
}
