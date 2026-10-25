# Wikiwright

Wikiwright is a small wiki engine written in Go. It keeps the wikitext, `index.php?title=` URLs, and `api.php` shape that a MediaWiki site already uses, and it stores each wiki in one SQLite file. There is no PHP.

Copyright (c) 2026 Katherine W. Prawl. Licensed under the MIT License. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

## Features

- Several wikis in one process, chosen by `Host` and the longest path prefix.
- Public wikis, and private wikis that require a login before a page or a file is shown.
- Accounts with argon2id passwords, optional TOTP, and bot passwords that skip the second factor.
- Section editing, a table of contents, categories, thumbnails, and red links for missing pages.
- An editable sidebar (`MediaWiki:Sidebar`) and file upload with a per-wiki size and type limit.
- A per-wiki accent color. The default is pine (`#1b3832`), or the optional `accent` key in the configuration. An administrator changes it at `Special:Appearance`.
- XML import for MediaWiki export schema 0.10 and 0.11, including 1.43 multi-content revisions.
- API import from a remote `api.php`, including a login-required wiki.
- A small write API compatible with common MediaWiki clients.

The skin is original: a reading column, a left sidebar, a header with search and account links, and a page-tools row. On a narrow screen the sidebar is behind a Menu control. It does not use MediaWiki or Timeless stylesheets or icons.

## Wikitext subset

The parser covers the constructs a small wiki tends to use:

- Headings, lists, definition lines, horizontal rules, and tables
- Internal links, external links, redirects, and categories
- Bold, italic, `<pre>`, `<code>`, `<blockquote>`, `<q>`, and `<nowiki>`
- Templates, parameters, `noinclude`, `includeonly`, and `onlyinclude`
- File syntax, including `thumb`, alignment, and pixel widths
- `<youtube>` and `{{#ev:youtube|…}}`, sandboxed
- Magic words used by the default sidebar, and `__NOTOC__` / `__TOC__` / `__NOEDITSECTION__`

Unknown tags and parser functions are escaped and marked. They do not crash the page. HTML is escaped except for markup Wikiwright itself emits. Comments are removed, including comments inside a transcluded template. An unclosed `<!--` ends at the end of that template, not the rest of the page.

Deliberately absent: Lua modules, the Cite extension, VisualEditor, and most parser functions beyond the ones above.

## Build

Go 1.26 or later. CGO is not required.

```bash
go test ./...
go build -o wikiwright ./cmd/wikiwright
./wikiwright version
```

A static Linux binary:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w' -o wikiwright ./cmd/wikiwright
```

## Run

Start from [deploy/wikiwright.example.yaml](deploy/wikiwright.example.yaml).

```bash
./wikiwright serve -config deploy/wikiwright.example.yaml
```

The example listens on `127.0.0.1:43123`. With `hosts` set, open the wiki through that hostname (a line in `/etc/hosts` is enough on one machine). An empty `hosts` list makes that wiki the fallback for any other name.

A request to `/` with no matching prefix lists the wikis for that host.

### Accounts

Passwords come from `-password` or `WIKIWRIGHT_PASSWORD`. They are not written to the log.

```bash
export WIKIWRIGHT_PASSWORD='a long passphrase'
./wikiwright user add -config wikiwright.yaml -wiki example -name Ada -admin
./wikiwright user password -wiki example -name Ada
./wikiwright user totp -wiki example -name Ada
./wikiwright user totp-off -wiki example -name Ada
```

The `totp` command prints a new secret once, for the operator to put in an authenticator. Bot passwords are created in the browser at `Special:BotPasswords`. An administrator creates accounts at `Special:CreateAccount`.

### Import from a MediaWiki API

```bash
./wikiwright import api -config wikiwright.yaml -wiki example \
  -api https://wiki.example.org/w/api.php
```

`-images=false` skips files. `-images-only` fetches files and does not download pages. The client pauses briefly between API calls. A few files download at once. Each file connection gives up after about 10 seconds. When `-api` is HTTPS, an image URL on that same host that comes back as `http://` is fetched as `https://`. A file already on disk is skipped.

A private source wiki needs an account that can read. The password is `WIKIWRIGHT_SOURCE_PASSWORD` and is not a flag. A username like `Ada@importer` is a bot password (`action=login`). Any other username uses the main-account login (`action=clientlogin`). The session cookie is kept for revisions and for file URLs, including `img_auth.php`. The password and cookies are not printed.

```bash
export WIKIWRIGHT_SOURCE_PASSWORD='the source password'
./wikiwright import api -config wikiwright.yaml -wiki draft -user 'Ada@importer' \
  -api https://draft.example.org/w/api.php -cache draft-api.xml
```

`-cache` writes the download as XML before reindexing and reuses that file on the next run. Pages already stored are not downloaded again. Existing accounts in the destination wiki are kept.

### Import from XML

```bash
./wikiwright import xml -config wikiwright.yaml -wiki example \
  -dump dump.xml -images /path/to/images
```

`Special:Export` works too. Titles, namespaces, revision ids, timestamps, and authors are kept, so old `oldid=` links still resolve. `-images` accepts a MediaWiki `images` directory (hashed layout is fine; `thumb` and `temp` are skipped). Schema 0.11 main slots are stored. Other slots, such as mediainfo JSON, are ignored.

```bash
./wikiwright reindex -config wikiwright.yaml -wiki example
```

## Write API

Each wiki serves `api.php` under its prefix, for example `/w/api.php`. Responses use `format=json` and `formatversion=2`.

| Action | What it does |
|---|---|
| `login` | Bot-password or main-account login. Returns a session cookie. |
| `query` | `meta=tokens`, `meta=userinfo`, `meta=siteinfo`, `prop=revisions`, `list=allpages` |
| `edit` | Create or replace a page, including `section=` |
| `upload` | Store a file. `ignorewarnings` is required to overwrite. |
| `logout` | End the session. |

`assert=user` and `assert=bot` both require a login. A bot-password session counts as that user. Anonymous `meta=userinfo` returns `anon`. On a private wiki, anonymous reads return `readapidenied`, except `meta=tokens` and `meta=userinfo`.

Grants on a bot password are `create`, `edit`, and `upload`. A normal login is not limited by grants.

```bash
curl -c /tmp/ww.jar -b /tmp/ww.jar \
  'http://127.0.0.1:43123/w/api.php?action=query&meta=tokens&type=login&format=json&formatversion=2'
```

Not implemented, on purpose: `action=parse`, move, delete, protect, rollback, watchlists, OAuth, and `formatversion=1`.

## Security

- The default listen address is `127.0.0.1:43123`. Do not expose that port directly. [deploy/nginx.conf](deploy/nginx.conf) is a sample reverse proxy. Set `cookie_secure: true` when the browser uses HTTPS.
- `X-Forwarded-For` is trusted only when the connection itself is from loopback.
- Session cookies are `wikiwright_<wiki id>`, `HttpOnly`, `SameSite=Lax`, and `Secure` when `cookie_secure` is set. The path is the wiki prefix.
- `secret` in the config signs nothing. Changing it does not log anyone out. Session tokens are random and stored server-side.
- Login failures are limited to 8 per 15 minutes per IP.
- Passwords are stored as argon2id PHC strings. The minimum length for a password set in the browser is 8 characters.
- A private wiki redirects anonymous HTML readers to `Special:UserLogin` and refuses anonymous API reads. Images are included.
- Uploaded SVG is rejected unless `svg` is listed in `upload_types`.
- The `totp` command and the preferences page print a new authenticator secret once. Do not record it in a shell history you share.

## Install on a server

```bash
sudo useradd --system --home /var/lib/wikiwright --create-home wikiwright
sudo install -d -o wikiwright -g wikiwright /etc/wikiwright /var/lib/wikiwright
sudo install -m 755 wikiwright /usr/local/bin/wikiwright
sudo cp deploy/wikiwright.example.yaml /etc/wikiwright/wikiwright.yaml
sudo cp deploy/wikiwright.service /etc/systemd/system/wikiwright.service
sudo systemctl enable --now wikiwright
```

Edit `/etc/wikiwright/wikiwright.yaml` before starting: paths, `secret`, `cookie_secure`, and the wiki list. Then:

```bash
docker build -t wikiwright .
docker run --rm -p 43123:43123 \
  -v /etc/wikiwright/wikiwright.yaml:/etc/wikiwright/wikiwright.yaml:ro \
  -v /var/lib/wikiwright:/var/lib/wikiwright \
  wikiwright
```

## Database files

`CREATE TABLE IF NOT EXISTS` creates a new wiki. Opening an existing Wikiwright database adds `sessions.grants` if that column is missing and creates a `settings` table if it is missing. It does not rewrite pages. Accounts, revisions, and file rows stay as they are. The accent color is one row in `settings`. Cookie names changed from earlier development builds, so browsers log in again.

## Tests

```bash
go test ./...
```

GitHub Actions runs the same suite on Go 1.26. The sample dump in `testdata/sample-export.xml` is synthetic.
