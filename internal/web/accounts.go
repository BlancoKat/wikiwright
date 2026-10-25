package web

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/store"
)

var botNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

func (s *Site) handleBotPasswords(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	if sess.User.ID == 0 {
		http.Redirect(w, r, s.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape("Special:BotPasswords"), http.StatusFound)
		return
	}
	notice := ""
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "Could not read the form.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		appID := strings.TrimSpace(r.FormValue("wpBotName"))
		if !botNameRE.MatchString(appID) {
			notice = `<div class="wikiwright-error">Use a bot name of letters, digits, and ._- , up to 32 characters, starting with a letter or digit.</div>`
		} else {
			var err error
			notice, err = s.botPasswordAction(sess, r, appID)
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not update bot passwords.")
				return
			}
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := s.Store.BotPasswords(sess.User.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not list bot passwords.")
		return
	}
	var b strings.Builder
	b.WriteString(notice)
	b.WriteString(`<p>A bot password signs a script in as <code>` + esc(sess.User.Name) + `@name</code>. The password is shown once. Wikiwright stores only a hash. Edits are recorded under ` + esc(sess.User.Name) + `.</p>`)
	if len(rows) == 0 {
		b.WriteString(`<p>No bot passwords yet.</p>`)
	} else {
		b.WriteString(`<table class="wikitable"><thead><tr><th>Login</th><th>Grants</th><th>Created</th><th></th></tr></thead><tbody>`)
		for _, row := range rows {
			b.WriteString(`<tr><td><code>` + esc(sess.User.Name+"@"+row.AppID) + `</code></td><td>` + esc(row.Grants) + `</td><td>` + esc(s.fmtTime(row.CreatedAt)) + `</td><td>`)
			b.WriteString(s.botMiniForm(sess, "reset", row.AppID, "Reset"))
			b.WriteString(` `)
			b.WriteString(s.botMiniForm(sess, "delete", row.AppID, "Delete"))
			b.WriteString(`</td></tr>`)
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`<h2>Create a bot password</h2>`)
	b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:BotPasswords")) + `">`)
	b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	b.WriteString(`<input type="hidden" name="wpAction" value="create">`)
	b.WriteString(`<label>Bot name <input type="text" name="wpBotName" required></label>`)
	b.WriteString(`<label><input type="checkbox" name="wpGrantEdit" value="1"> Edit existing pages</label>`)
	b.WriteString(`<label><input type="checkbox" name="wpGrantCreate" value="1"> Create pages</label>`)
	b.WriteString(`<label><input type="checkbox" name="wpGrantUpload" value="1"> Upload files</label>`)
	b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Create</button></div></form>`)
	s.writeSpecial(w, r, "Bot passwords", b.String())
}

func (s *Site) botMiniForm(sess store.Session, action, appID, label string) string {
	return `<form method="post" action="` + esc(s.pageURL("Special:BotPasswords")) + `" style="display:inline">` +
		`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">` +
		`<input type="hidden" name="wpAction" value="` + esc(action) + `">` +
		`<input type="hidden" name="wpBotName" value="` + esc(appID) + `">` +
		`<button type="submit">` + esc(label) + `</button></form>`
}

func (s *Site) botPasswordAction(sess store.Session, r *http.Request, appID string) (string, error) {
	switch r.FormValue("wpAction") {
	case "create":
		_, _, ok, err := s.Store.BotPasswordHash(sess.User.ID, appID)
		if err != nil {
			return "", err
		}
		if ok {
			return `<div class="wikiwright-error">A bot password named ` + esc(appID) + ` already exists. Reset it to replace the password.</div>`, nil
		}
		grants := grantsFromForm(r)
		if grants == "" {
			return `<div class="wikiwright-error">Choose at least one grant.</div>`, nil
		}
		return s.storeNewBotPassword(sess, appID, grants)
	case "reset":
		_, grants, ok, err := s.Store.BotPasswordHash(sess.User.ID, appID)
		if err != nil {
			return "", err
		}
		if !ok {
			return `<div class="wikiwright-error">That bot password does not exist.</div>`, nil
		}
		if r.Form.Has("wpGrantEdit") || r.Form.Has("wpGrantCreate") || r.Form.Has("wpGrantUpload") {
			grants = grantsFromForm(r)
			if grants == "" {
				return `<div class="wikiwright-error">Choose at least one grant.</div>`, nil
			}
		}
		return s.storeNewBotPassword(sess, appID, grants)
	case "delete":
		if err := s.Store.DeleteBotPassword(sess.User.ID, appID); err != nil {
			return "", err
		}
		return `<div class="wikiwright-notice">Deleted bot password ` + esc(appID) + `.</div>`, nil
	default:
		return `<div class="wikiwright-error">Unknown action.</div>`, nil
	}
}

func (s *Site) storeNewBotPassword(sess store.Session, appID, grants string) (string, error) {
	secret, err := newBotSecret()
	if err != nil {
		return "", err
	}
	hash, err := auth.Hash(secret, auth.ProductionParams)
	if err != nil {
		return "", err
	}
	if err := s.Store.UpsertBotPassword(sess.User.ID, appID, hash, grants); err != nil {
		return "", err
	}
	login := sess.User.Name + "@" + appID
	return `<div class="wikiwright-notice" id="botpassword-result">` +
		`<p>Bot username: <code id="wpBotLogin">` + esc(login) + `</code></p>` +
		`<p>Password: <code id="wpBotPassword">` + esc(secret) + `</code></p>` +
		`<p>Copy the password now. It is not shown again. Grants: ` + esc(grants) + `.</p>` +
		`</div>`, nil
}

func grantsFromForm(r *http.Request) string {
	var g []string
	if r.FormValue("wpGrantCreate") != "" {
		g = append(g, "create")
	}
	if r.FormValue("wpGrantEdit") != "" {
		g = append(g, "edit")
	}
	if r.FormValue("wpGrantUpload") != "" {
		g = append(g, "upload")
	}
	return strings.Join(g, ",")
}

func newBotSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Site) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	if sess.User.ID == 0 || !sess.User.IsAdmin {
		s.fail(w, r, http.StatusForbidden, "Only an administrator can create an account.")
		return
	}
	notice := ""
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "Could not read the form.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		name := strings.TrimSpace(r.FormValue("wpName"))
		pass := r.FormValue("wpPassword")
		again := r.FormValue("wpPasswordAgain")
		switch {
		case !validAccountName(name):
			notice = `<div class="wikiwright-error">Usernames use letters, digits, spaces, and underscores, and cannot contain @.</div>`
		case len(pass) < 8:
			notice = `<div class="wikiwright-error">Use at least 8 characters for the password.</div>`
		case pass != again:
			notice = `<div class="wikiwright-error">The passwords do not match.</div>`
		default:
			hash, err := auth.Hash(pass, auth.ProductionParams)
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not store the password.")
				return
			}
			admin := r.FormValue("wpAdmin") != ""
			if err := s.Store.CreateUser(name, hash, admin); err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "unique") {
					notice = `<div class="wikiwright-error">That username is already in use.</div>`
				} else {
					s.fail(w, r, http.StatusInternalServerError, "Could not create the account.")
					return
				}
			} else {
				notice = `<div class="wikiwright-notice">Created account ` + esc(name) + `.</div>`
			}
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var b strings.Builder
	b.WriteString(notice)
	b.WriteString(`<p>New accounts can log in and, if you grant it, create bot passwords. There is no public registration.</p>`)
	b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:CreateAccount")) + `">`)
	b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	b.WriteString(`<label>Username <input type="text" name="wpName" autocomplete="off" required></label>`)
	b.WriteString(`<label>Password <input type="password" name="wpPassword" autocomplete="new-password" required></label>`)
	b.WriteString(`<label>Repeat password <input type="password" name="wpPasswordAgain" autocomplete="new-password" required></label>`)
	b.WriteString(`<label><input type="checkbox" name="wpAdmin" value="1"> Administrator</label>`)
	b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Create account</button></div></form>`)
	s.writeSpecial(w, r, "Create account", b.String())
}

func validAccountName(name string) bool {
	if name == "" || len(name) > 80 || strings.Contains(name, "@") {
		return false
	}
	for _, r := range name {
		switch {
		case r == ' ' || r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
