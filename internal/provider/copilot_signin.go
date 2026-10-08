package provider

// Signing in to a Copilot account from magpie: GitHub's device flow, with
// the OAuth app Copilot's editors sign in with. magpie shows the code, the
// user types it at github.com/login/device, and the token GitHub then
// hands over is kept for magpie alone (see copilot_accounts.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// copilotClientID is the OAuth app of Copilot's editors.
const copilotClientID = "Iv1.b507a08c87ecfe98"

// GitHub's device flow; vars so tests can point them elsewhere.
var (
	gitHubDeviceURL = "https://github.com/login/device/code"
	gitHubTokenURL  = "https://github.com/login/oauth/access_token"
)

func gitHubForm(ctx context.Context, u string, form url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return errors.New("GitHub: " + APIError(b, res.Status))
	}
	return json.Unmarshal(b, v)
}

// startCopilotSignIn signs in on github.com, or on the enterprise's
// <name>.ghe.com the sign-in names (#723).
func startCopilotSignIn(s *signInFlow) error {
	host, err := CopilotHost(s.site)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	var dc struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := gitHubForm(ctx, copilotDeviceURL(host), url.Values{"client_id": {copilotClientID}, "scope": {"read:user"}}, &dc); err != nil {
		cancel()
		return err
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		cancel()
		return errors.New("GitHub gave no device code")
	}
	s.st.URL, s.st.Code = dc.URI, dc.UserCode
	if s.st.URL == "" {
		s.st.URL = copilotDevicePage(host)
	}
	s.stop = cancel
	interval := time.Duration(max(dc.Interval, 1)) * time.Second
	go func() {
		fail := func(msg string) { s.finish(SignInState{State: "failed", Error: msg}) }
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			var tok struct {
				Token string `json:"access_token"`
				Error string `json:"error"`
				Desc  string `json:"error_description"`
			}
			err := gitHubForm(ctx, copilotOAuthURL(host), url.Values{"client_id": {copilotClientID}, "device_code": {dc.DeviceCode},
				"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &tok)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				continue // a hiccup: ask again
			case tok.Error == "authorization_pending":
				continue
			case tok.Error == "slow_down":
				interval += 5 * time.Second
				continue
			case tok.Error == "expired_token":
				fail("the code expired; start again")
				return
			case tok.Error == "access_denied":
				fail("the sign-in was declined on GitHub")
				return
			case tok.Error != "" || tok.Token == "":
				fail("GitHub: " + strings.TrimSpace(tok.Error+" "+tok.Desc))
				return
			}
			user, plan, err := copilotUser(ctx, tok.Token, host)
			if err != nil {
				fail(err.Error())
				return
			}
			if err := addCopilotLogin(user, plan, tok.Token, host); err != nil {
				fail(err.Error())
				return
			}
			// the one Copilot's editors are signed in to, if it is this
			using := false
			if own, ok := copilotLogin(copilotConfigDir()); ok && strings.EqualFold(own.User, user) && own.Host == host {
				using = true
			}
			// named as the account list names it, so the window finds it
			s.finish(SignInState{State: "done", User: CopilotAccountName(user, host), Plan: plan, Using: using})
			return
		}
	}()
	return nil
}
