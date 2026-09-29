package provider

// Several Copilot subscriptions. Copilot's editors and its CLI keep one
// GitHub account, which magpie only ever reads. Each further account is
// signed in by magpie itself, with GitHub's device code under the Copilot
// editors' own OAuth app, and its GitHub token kept in logins.json; the
// editors' own account is remembered there too, with nothing of its token,
// so it can stand behind another or be turned off.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// copilotConfigDir is where the Copilot editors keep their sign-in.
func copilotConfigDir() string {
	if cfg := os.Getenv("XDG_CONFIG_HOME"); cfg != "" {
		return cfg
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// copilotSaved is the sign-in of an account magpie keeps.
func copilotSaved(l savedLogin) (copilotApp, bool) {
	var app copilotApp
	if l.own() || json.Unmarshal(l.Auth, &app) != nil || app.Token == "" {
		return copilotApp{}, false
	}
	app.User = l.User
	return app, true
}

type copilotLoginApp struct {
	Login
	app copilotApp
}

// copilotLogins lists the Copilot accounts, the first in use first, then
// the rest as they were added.
func copilotLogins(cfg string) []copilotLoginApp {
	own, _ := copilotLogin(cfg)
	ownUser := copilotOwnUser(cfg)
	var out []copilotLoginApp
	for _, l := range sideLogins("copilot", ownUser, func(l savedLogin) bool {
		_, ok := copilotSaved(l)
		return ok
	}) {
		app := own
		if !l.saved.own() {
			app, _ = copilotSaved(l.saved)
		}
		app.User = l.User
		out = append(out, copilotLoginApp{l.Login, app})
	}
	return out
}

// copilotOwnUser is who the editors' or CLI's Copilot sign-in is, "" for none.
func copilotOwnUser(cfg string) string {
	own, ok := copilotLogin(cfg)
	if !ok {
		return ""
	}
	return firstNonEmpty(own.User, "GitHub")
}

func copilotSide() []sideLogin {
	var out []sideLogin
	for _, c := range copilotLogins(copilotConfigDir()) {
		out = append(out, sideLogin{Login: c.Login})
	}
	return out
}

func copilotLoginList() []Login { return loginsOf(copilotSide()) }

func switchCopilotLogin(user string) error {
	return switchSideLogin("copilot", user, copilotSide())
}

func setCopilotLoginOn(user string, on bool) error {
	return setSideLoginOn("copilot", user, on, copilotSide())
}

func forgetCopilotLogin(user string) error {
	return forgetSideLogin("copilot", user, copilotSide(), nil)
}

// addCopilotLogin keeps an account magpie just signed in.
func addCopilotLogin(user, plan, token string) error {
	auth, _ := json.Marshal(map[string]string{"oauth_token": token})
	return addSideLogin(savedLogin{Agent: "copilot", User: user, Plan: plan, Auth: auth}, copilotOwnUser(copilotConfigDir()), func(savedLogin) {})
}

// copilotAccount is the Copilot account in use first.
func copilotAccount(cfg string) (Provider, bool) {
	ls := copilotLogins(cfg)
	if len(ls) == 0 {
		return Provider{}, false
	}
	return copilotProvider(ls[0].app, ls[0].Plan), true
}

// copilotAlsoOn is the Copilot accounts in use behind the first.
func copilotAlsoOn() []Provider {
	var out []Provider
	for _, c := range copilotLogins(copilotConfigDir()) {
		if !c.Active && c.On {
			out = append(out, copilotProvider(c.app, c.Plan))
		}
	}
	return out
}

// copilotPlans names Copilot's plans as GitHub sells them.
var copilotPlans = map[string]string{
	"free": "Free", "individual": "Pro", "individual_pro": "Pro+",
	"business": "Business", "enterprise": "Enterprise",
}

// copilotUser is who a GitHub token belongs to and which Copilot plan it
// has; no plan, no Copilot.
func copilotUser(ctx context.Context, token string) (user, plan string, err error) {
	get := func(u string, v any) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if res.StatusCode != 200 {
			return res.StatusCode, errors.New(APIError(b, res.Status))
		}
		return 200, json.Unmarshal(b, v)
	}
	var gh struct {
		Login string `json:"login"`
	}
	if _, err := get(GitHubUserURL, &gh); err != nil || gh.Login == "" {
		return "", "", errors.New("GitHub didn't say whose account this is")
	}
	var cp struct {
		Plan string `json:"copilot_plan"`
	}
	if code, err := get(CopilotUserURL, &cp); err != nil {
		if code == 401 || code == 403 || code == 404 {
			return gh.Login, "", errors.New(gh.Login + " has no Copilot subscription")
		}
		return gh.Login, "", errors.New("Copilot: " + err.Error())
	}
	plan = copilotPlans[strings.ToLower(cp.Plan)]
	if plan == "" && cp.Plan != "" {
		plan = strings.ToUpper(cp.Plan[:1]) + cp.Plan[1:]
	}
	return gh.Login, plan, nil
}

// GitHubUserURL says whose a GitHub token is; a var so tests can point it
// elsewhere.
var GitHubUserURL = "https://api.github.com/user"
