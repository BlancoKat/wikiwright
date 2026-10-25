package mwapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

func prepareClient(opt *Options) error {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.UserAgent == "" {
		opt.UserAgent = "Wikiwright/1.0 (https://github.com/BlancoKat/wikiwright; read-only)"
	}
	if opt.Client == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		opt.Client = &http.Client{Timeout: 60 * time.Second, Jar: jar}
		return nil
	}
	if opt.Client.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		opt.Client.Jar = jar
	}
	return nil
}

// login is the only non-read request. A username with @ is a bot password
// (action=login). Anything else is a main account (action=clientlogin).
func login(ctx context.Context, opt Options, api string) error {
	user := strings.TrimSpace(opt.Username)
	if user == "" {
		return nil
	}
	if opt.Password == "" {
		return fmt.Errorf("set WIKIWRIGHT_SOURCE_PASSWORD to log in; it is not accepted as a flag")
	}
	// Build the jar on this options value so the token request and the login
	// POST share it. Import has already done the same, and passes that client in.
	if err := prepareClient(&opt); err != nil {
		return err
	}
	token, err := loginToken(ctx, opt, api)
	if err != nil {
		return err
	}
	if strings.Contains(user, "@") {
		return botLogin(ctx, opt, api, user, token)
	}
	return clientLogin(ctx, opt, api, user, token)
}

func loginToken(ctx context.Context, opt Options, api string) (string, error) {
	q := url.Values{
		"action": {"query"}, "meta": {"tokens"}, "type": {"login"},
		"format": {"json"}, "formatversion": {"2"},
	}
	var resp struct {
		Query struct {
			Tokens struct {
				LoginToken string `json:"logintoken"`
			} `json:"tokens"`
		} `json:"query"`
	}
	if err := get(ctx, opt, api, q, &resp); err != nil {
		return "", err
	}
	if resp.Query.Tokens.LoginToken == "" {
		return "", fmt.Errorf("login token was empty")
	}
	return resp.Query.Tokens.LoginToken, nil
}

func botLogin(ctx context.Context, opt Options, api, user, token string) error {
	for attempt := 0; attempt < 2; attempt++ {
		form := url.Values{
			"action": {"login"}, "lgname": {user}, "lgpassword": {opt.Password},
			"lgtoken": {token}, "format": {"json"}, "formatversion": {"2"},
		}
		var resp struct {
			Login struct {
				Result string `json:"result"`
				Reason string `json:"reason"`
				Token  string `json:"token"`
			} `json:"login"`
		}
		if err := post(ctx, opt, api, form, &resp); err != nil {
			return err
		}
		switch resp.Login.Result {
		case "Success":
			opt.Logf("api: logged in")
			return nil
		case "NeedToken":
			if attempt == 1 || resp.Login.Token == "" {
				return fmt.Errorf("login failed: NeedToken")
			}
			token = resp.Login.Token
		default:
			why := strings.TrimSpace(resp.Login.Result + " " + resp.Login.Reason)
			return fmt.Errorf("login failed: %s", scrub(opt.Password, why))
		}
	}
	return fmt.Errorf("login failed")
}

func clientLogin(ctx context.Context, opt Options, api, user, token string) error {
	form := url.Values{
		"action": {"clientlogin"}, "username": {user}, "password": {opt.Password},
		"logintoken": {token}, "loginreturnurl": {"http://127.0.0.1/"},
		"format": {"json"}, "formatversion": {"2"},
	}
	var resp struct {
		ClientLogin struct {
			Status  string    `json:"status"`
			Message mwMessage `json:"message"`
		} `json:"clientlogin"`
	}
	if err := post(ctx, opt, api, form, &resp); err != nil {
		return err
	}
	switch resp.ClientLogin.Status {
	case "PASS":
		opt.Logf("api: logged in")
		return nil
	case "FAIL":
		why := strings.TrimSpace(string(resp.ClientLogin.Message))
		if why == "" {
			why = "FAIL"
		}
		return fmt.Errorf("login failed: %s", scrub(opt.Password, why))
	default:
		// UI, RESTART, or REDIRECT: captcha, two-factor, or another step.
		return fmt.Errorf("login needs another step (two-factor or a captcha); use a bot password instead")
	}
}

// post sends a login form. The body is the only place the password is written.
// Any other action is refused so a private import cannot edit the source wiki.
func post(ctx context.Context, opt Options, api string, form url.Values, dest any) error {
	if err := prepareClient(&opt); err != nil {
		return err
	}
	action := form.Get("action")
	if action != "login" && action != "clientlogin" {
		return fmt.Errorf("refusing non-read request action=%s", action)
	}
	u, err := url.Parse(api)
	if err != nil {
		return err
	}
	u.RawQuery = ""
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", opt.UserAgent)
	req.Header.Set("Accept", "application/json")
	res, err := opt.Client.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("login response could not be read")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("login status %s", res.Status)
	}
	var probe struct {
		Error *struct {
			Code string `json:"code"`
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("login response was not json")
	}
	if probe.Error != nil {
		return fmt.Errorf("login %s: %s", probe.Error.Code, scrub(opt.Password, probe.Error.Info))
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("login response was not json")
	}
	if d := opt.delay(); d > 0 {
		time.Sleep(d)
	}
	return nil
}

func scrub(secret, msg string) string {
	if secret == "" || msg == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "[redacted]")
}

// mwMessage accepts either a formatversion=2 string or a formatversion=1 object.
type mwMessage string

func (m *mwMessage) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*m = mwMessage(s)
		return nil
	}
	var obj struct {
		Star string `json:"*"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		*m = mwMessage(string(b))
		return nil
	}
	if obj.Star != "" {
		*m = mwMessage(obj.Star)
		return nil
	}
	*m = mwMessage(obj.Code)
	return nil
}
