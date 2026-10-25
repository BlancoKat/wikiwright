package web

import (
	"crypto/subtle"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BlancoKat/wikiwright/internal/auth"
	"github.com/BlancoKat/wikiwright/internal/store"
	"github.com/BlancoKat/wikiwright/internal/title"
)

const (
	sessionTTL   = 14 * 24 * time.Hour
	challengeTTL = 10 * time.Minute
	maxFailures  = 8
	failWindow   = 15 * time.Minute
)

func (s *Site) cookieName() string {
	return "wikiwright_" + cookieToken(s.Cfg.ID)
}

func (s *Site) loginCookieName() string {
	return "wikiwright_login_" + cookieToken(s.Cfg.ID)
}

func cookieToken(id string) string {
	b := []byte(id)
	for i, c := range b {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			b[i] = '_'
		}
	}
	out := string(b)
	if out == "" {
		return "wiki"
	}
	return out
}

func (s *Site) cookiePath() string {
	if s.Cfg.PathPrefix == "" {
		return "/"
	}
	return s.Cfg.PathPrefix
}

func (s *Site) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     s.cookiePath(),
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Secure,
	})
}

func (s *Site) clearCookie(w http.ResponseWriter, name string) {
	s.setCookie(w, name, "", -1)
}

func (s *Site) current(r *http.Request) store.Session {
	c, err := r.Cookie(s.cookieName())
	if err != nil || c.Value == "" {
		return store.Session{}
	}
	sess, err := s.Store.Session(c.Value)
	if err != nil || sess.Token == "" {
		return store.Session{}
	}
	return sess
}

func (s *Site) loggedIn(r *http.Request) bool {
	sess := s.current(r)
	return sess.User.ID != 0 && !sess.NeedsTOTP
}

func tokensEqual(a, b string) bool {
	if a == "" || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Site) sessionView(r *http.Request) store.Session {
	sess := s.current(r)
	if sess.NeedsTOTP {
		return store.Session{}
	}
	return sess
}

func (s *Site) handleLogin(w http.ResponseWriter, r *http.Request) {
	sess := s.current(r)
	if r.Method == http.MethodPost {
		if !s.loginCSRF(r) {
			s.renderLogin(w, r, sess, "The login form expired. Reload the page and try again.", http.StatusBadRequest)
			return
		}
		ip := clientIP(r)
		n, err := s.Store.LoginFailures(ip, time.Now().Add(-failWindow))
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not check the login limit.")
			return
		}
		if n >= maxFailures {
			s.renderLogin(w, r, sess, "Too many failed logins from this address. Try again in 15 minutes.", http.StatusTooManyRequests)
			return
		}
		if sess.NeedsTOTP && sess.User.ID != 0 {
			code := strings.TrimSpace(r.FormValue("totp"))
			if auth.TOTP(sess.User.TOTPSecret, code, time.Now()) {
				exp := time.Now().Add(sessionTTL)
				if err := s.Store.FinishChallenge(sess.Token, exp); err != nil {
					s.fail(w, r, http.StatusInternalServerError, "Could not finish login.")
					return
				}
				_ = s.Store.RecordLogin(ip, sess.User.Name, true)
				s.setCookie(w, s.cookieName(), sess.Token, int(sessionTTL.Seconds()))
				http.Redirect(w, r, s.returnURL(r.FormValue("returnto")), http.StatusSeeOther)
				return
			}
			_ = s.Store.RecordLogin(ip, sess.User.Name, false)
			s.renderLogin(w, r, sess, "That authentication code is not valid.", http.StatusUnauthorized)
			return
		}
		name := strings.TrimSpace(r.FormValue("username"))
		pass := r.FormValue("password")
		user, err := s.Store.UserByName(name)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not look up that account.")
			return
		}
		if user.ID == 0 || !auth.Verify(pass, user.PasswordHash) {
			_ = s.Store.RecordLogin(ip, name, false)
			s.renderLogin(w, r, store.Session{}, "Incorrect username or password.", http.StatusUnauthorized)
			return
		}
		token, err := auth.RandomToken(32)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not start a session.")
			return
		}
		csrf, err := auth.RandomToken(32)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not start a session.")
			return
		}
		if user.TOTPEnabled {
			exp := time.Now().Add(challengeTTL)
			if err := s.Store.CreateChallenge(user.ID, token, csrf, exp); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not start a session.")
				return
			}
			s.setCookie(w, s.cookieName(), token, int(challengeTTL.Seconds()))
			sess = store.Session{Token: token, UserID: user.ID, CSRF: csrf, NeedsTOTP: true, User: user}
			s.renderLogin(w, r, sess, "", http.StatusOK)
			return
		}
		exp := time.Now().Add(sessionTTL)
		if err := s.Store.CreateSession(user.ID, token, csrf, exp); err != nil {
			s.fail(w, r, http.StatusInternalServerError, "Could not start a session.")
			return
		}
		_ = s.Store.RecordLogin(ip, user.Name, true)
		s.setCookie(w, s.cookieName(), token, int(sessionTTL.Seconds()))
		http.Redirect(w, r, s.returnURL(r.FormValue("returnto")), http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.renderLogin(w, r, sess, "", http.StatusOK)
}

func (s *Site) loginCSRF(r *http.Request) bool {
	c, err := r.Cookie(s.loginCookieName())
	if err != nil {
		return false
	}
	return tokensEqual(c.Value, r.FormValue("login_token"))
}

func (s *Site) renderLogin(w http.ResponseWriter, r *http.Request, sess store.Session, errMsg string, status int) {
	c, err := r.Cookie(s.loginCookieName())
	token := ""
	if err == nil {
		token = c.Value
	}
	if token == "" {
		if tok, err := auth.RandomToken(16); err == nil {
			token = tok
			s.setCookie(w, s.loginCookieName(), tok, 30*60)
		}
	}
	ret := r.FormValue("returnto")
	if ret == "" {
		ret = r.URL.Query().Get("returnto")
	}
	var b strings.Builder
	if s.Cfg.Access == "private" {
		b.WriteString(`<p>This wiki is private. Log in to read it.</p>`)
	}
	if errMsg != "" {
		b.WriteString(`<div class="wikiwright-error">` + esc(errMsg) + `</div>`)
	}
	b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:UserLogin")) + `">`)
	b.WriteString(`<input type="hidden" name="login_token" value="` + esc(token) + `">`)
	b.WriteString(`<input type="hidden" name="returnto" value="` + esc(ret) + `">`)
	if sess.NeedsTOTP && sess.User.ID != 0 {
		b.WriteString(`<p>Enter the 6-digit code from your authenticator for <strong>` + esc(sess.User.Name) + `</strong>.</p>`)
		b.WriteString(`<label>Authentication code <input type="text" name="totp" inputmode="numeric" autocomplete="one-time-code" autofocus></label>`)
		b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Continue</button></div>`)
	} else {
		b.WriteString(`<label>Username <input type="text" name="username" autocomplete="username" autofocus></label>`)
		b.WriteString(`<label>Password <input type="password" name="password" autocomplete="current-password"></label>`)
		b.WriteString(`<div class="wikiwright-form-actions"><button type="submit">Log in</button></div>`)
	}
	b.WriteString(`</form>`)
	s.writePage(w, r, pageView{
		Status:    status,
		Heading:   "Log in",
		Action:    "view",
		DBKey:     "Special:UserLogin",
		Namespace: title.NSSpecial,
		Body:      template.HTML(b.String()),
		Session:   store.Session{},
	})
}

func (s *Site) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := s.current(r)
	token := r.URL.Query().Get("token")
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		token = r.FormValue("token")
	}
	if sess.Token != "" && !tokensEqual(sess.CSRF, token) {
		s.fail(w, r, http.StatusForbidden, "The logout link did not match this session. Use the Log out link in the account menu.")
		return
	}
	if sess.Token != "" {
		_ = s.Store.DeleteSession(sess.Token)
	}
	s.clearCookie(w, s.cookieName())
	http.Redirect(w, r, s.pageURL(s.Cfg.MainPage), http.StatusSeeOther)
}

func (s *Site) handlePreferences(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionView(r)
	if sess.User.ID == 0 {
		http.Redirect(w, r, s.pageURL("Special:UserLogin")+"&returnto="+url.QueryEscape("Special:Preferences"), http.StatusFound)
		return
	}
	msg := template.HTML("")
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, http.StatusBadRequest, "Could not read the form.")
			return
		}
		if !tokensEqual(sess.CSRF, r.FormValue("csrf")) {
			s.fail(w, r, http.StatusForbidden, "The form token did not match. Reload and try again.")
			return
		}
		switch {
		case r.FormValue("wpSavePassword") != "":
			msg = s.changePassword(sess, r)
		case r.FormValue("wpEnableTOTP") != "":
			secret, err := auth.NewTOTPSecret()
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not generate a secret.")
				return
			}
			if err := s.Store.SetPendingTOTP(sess.Token, secret); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not store the secret.")
				return
			}
			sess.PendingTOTP = secret
			msg = `<div class="wikiwright-notice">Add this secret to your authenticator, then enter a code to confirm.</div>`
		case r.FormValue("wpConfirmTOTP") != "":
			if sess.PendingTOTP == "" || !auth.TOTP(sess.PendingTOTP, strings.TrimSpace(r.FormValue("totp")), time.Now()) {
				msg = `<div class="wikiwright-error">That code did not match the new secret.</div>`
			} else if err := s.Store.SetTOTP(sess.User.ID, sess.PendingTOTP, true); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not enable two-factor authentication.")
				return
			} else {
				_ = s.Store.SetPendingTOTP(sess.Token, "")
				sess.PendingTOTP = ""
				sess.User.TOTPEnabled = true
				sess.User.TOTPSecret = ""
				msg = `<div class="wikiwright-notice">Two-factor authentication is on.</div>`
			}
		case r.FormValue("wpDisableTOTP") != "":
			user, err := s.Store.UserByID(sess.User.ID)
			if err != nil || !auth.TOTP(user.TOTPSecret, strings.TrimSpace(r.FormValue("totp")), time.Now()) {
				msg = `<div class="wikiwright-error">Enter a current authentication code to turn two-factor off.</div>`
			} else if err := s.Store.SetTOTP(user.ID, "", false); err != nil {
				s.fail(w, r, http.StatusInternalServerError, "Could not disable two-factor authentication.")
				return
			} else {
				sess.User.TOTPEnabled = false
				msg = `<div class="wikiwright-notice">Two-factor authentication is off.</div>`
			}
		}
		if fresh, err := s.Store.UserByID(sess.User.ID); err == nil {
			sess.User = fresh
		}
		if fresh, err := s.Store.Session(sess.Token); err == nil && fresh.Token != "" {
			sess.PendingTOTP = fresh.PendingTOTP
			sess.CSRF = fresh.CSRF
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writePage(w, r, pageView{
		Heading:   "Preferences",
		Action:    "view",
		DBKey:     "Special:Preferences",
		Namespace: title.NSSpecial,
		Notices:   msg,
		Body:      s.preferencesForm(sess),
		Session:   sess,
	})
}

func (s *Site) changePassword(sess store.Session, r *http.Request) template.HTML {
	cur := r.FormValue("current")
	next := r.FormValue("newpass")
	again := r.FormValue("again")
	if !auth.Verify(cur, sess.User.PasswordHash) {
		return `<div class="wikiwright-error">The current password is not correct.</div>`
	}
	if len(next) < 8 {
		return `<div class="wikiwright-error">Use at least 8 characters for the new password.</div>`
	}
	if next != again {
		return `<div class="wikiwright-error">The new passwords do not match.</div>`
	}
	hash, err := auth.Hash(next, auth.ProductionParams)
	if err != nil {
		return `<div class="wikiwright-error">Could not store the new password.</div>`
	}
	if err := s.Store.SetPassword(sess.User.ID, hash); err != nil {
		return `<div class="wikiwright-error">Could not store the new password.</div>`
	}
	return `<div class="wikiwright-notice">Password updated.</div>`
}

func (s *Site) preferencesForm(sess store.Session) template.HTML {
	var b strings.Builder
	b.WriteString(`<p>Signed in as <strong>` + esc(sess.User.Name) + `</strong>. <a href="` + esc(s.pageURL("Special:BotPasswords")) + `">Bot passwords</a>`)
	if sess.User.IsAdmin {
		b.WriteString(` · <a href="` + esc(s.pageURL("Special:CreateAccount")) + `">Create account</a>`)
	}
	b.WriteString(`</p>`)
	b.WriteString(`<h2>Password</h2>`)
	b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:Preferences")) + `">`)
	b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
	b.WriteString(`<label>Current password <input type="password" name="current" autocomplete="current-password"></label>`)
	b.WriteString(`<label>New password <input type="password" name="newpass" autocomplete="new-password"></label>`)
	b.WriteString(`<label>Repeat new password <input type="password" name="again" autocomplete="new-password"></label>`)
	b.WriteString(`<div class="wikiwright-form-actions"><button type="submit" name="wpSavePassword" value="1">Change password</button></div></form>`)
	b.WriteString(`<h2>Two-factor authentication</h2>`)
	if sess.User.TOTPEnabled {
		b.WriteString(`<p>An authenticator code is required at login.</p>`)
		b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:Preferences")) + `">`)
		b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
		b.WriteString(`<label>Current code <input type="text" name="totp" inputmode="numeric" autocomplete="one-time-code"></label>`)
		b.WriteString(`<div class="wikiwright-form-actions"><button type="submit" name="wpDisableTOTP" value="1">Turn off two-factor</button></div></form>`)
	} else {
		b.WriteString(`<form method="post" action="` + esc(s.pageURL("Special:Preferences")) + `">`)
		b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
		b.WriteString(`<div class="wikiwright-form-actions"><button type="submit" name="wpEnableTOTP" value="1">Set up authenticator</button></div></form>`)
	}
	if sess.PendingTOTP != "" {
		otp := auth.OTPAuthURL(s.Cfg.Name, sess.User.Name, sess.PendingTOTP)
		b.WriteString(`<p>Secret: <code>` + esc(sess.PendingTOTP) + `</code></p>`)
		b.WriteString(`<p>Or add this URL in an app that accepts otpauth links:</p><p><code>` + esc(otp) + `</code></p>`)
		b.WriteString(`<form class="wikiwright-login" method="post" action="` + esc(s.pageURL("Special:Preferences")) + `">`)
		b.WriteString(`<input type="hidden" name="csrf" value="` + esc(sess.CSRF) + `">`)
		b.WriteString(`<label>Code from the app <input type="text" name="totp" inputmode="numeric" autocomplete="one-time-code"></label>`)
		b.WriteString(`<div class="wikiwright-form-actions"><button type="submit" name="wpConfirmTOTP" value="1">Confirm and enable</button></div></form>`)
	}
	return template.HTML(b.String())
}
