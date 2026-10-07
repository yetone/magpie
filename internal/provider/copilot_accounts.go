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

	"github.com/yetone/magpie/internal/appdir"
)

// copilotConfigDir is where the Copilot editors keep their sign-in.
func copilotConfigDir() string {
	if cfg := appdir.Getenv("XDG_CONFIG_HOME"); cfg != "" {
		return cfg
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// copilotName is the name a Copilot account is listed and addressed by
// (switching, forgetting, its usage, routing): its GitHub login on
// github.com, login@<name>.ghe.com on an enterprise's host (#1220). A
// GitHub login holds no "@", so the host reads apart, and one login's
// accounts on two enterprises, or on an enterprise and github.com, are as
// many accounts, not one taking the place of the next.
func copilotName(login, host string) string {
	if host == "" {
		return login
	}
	return login + "@" + host
}

// copilotSavedName is a saved account's name as it is listed now: one
// signed in at an enterprise before its name carried the host (#1220)
// gets the host, the rest keep theirs.
func copilotSavedName(l savedLogin) string {
	var app copilotApp
	if l.own() || json.Unmarshal(l.Auth, &app) != nil || app.Host == "" {
		return l.User
	}
	if h, err := CopilotHost(app.Host); err != nil || h != app.Host || strings.HasSuffix(strings.ToLower(l.User), "@"+h) {
		return l.User
	}
	return copilotName(l.User, app.Host)
}

// copilotSaved is the sign-in of an account magpie keeps.
func copilotSaved(l savedLogin) (copilotApp, bool) {
	var app copilotApp
	if l.own() || json.Unmarshal(l.Auth, &app) != nil || app.Token == "" {
		return copilotApp{}, false
	}
	// only github.com or an enterprise's <name>.ghe.com gets the token
	if h, err := CopilotHost(app.Host); err != nil || h != app.Host {
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
	return copilotName(firstNonEmpty(own.User, "GitHub"), own.Host)
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

// addCopilotLogin keeps an account magpie just signed in as login, on
// host ("" for github.com), under its name there (copilotName).
func addCopilotLogin(login, plan, token, host string) error {
	a := map[string]string{"oauth_token": token}
	if host != "" {
		a["host"] = host
	}
	auth, _ := json.Marshal(a)
	return addSideLogin(savedLogin{Agent: "copilot", User: copilotName(login, host), Plan: plan, Auth: auth}, copilotOwnUser(copilotConfigDir()), func(savedLogin) {})
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

// copilotPlans names Copilot's plans as GitHub sells them, as GitHub's own
// clients name each copilot_plan (VS Code's chatEntitlementService,
// CopilotForXcode's planDisplayName): individual_pro is Pro+ and
// individual_max is Max. individual_edu is the student plan, which the
// free_educational_quota SKU also names.
var copilotPlans = map[string]string{
	"free": "Free", "individual": "Pro", "individual_pro": "Pro+",
	"individual_max": "Max", "individual_edu": "Education",
	"business": "Business", "enterprise": "Enterprise",
}

// copilotUser is who a GitHub token belongs to and which Copilot plan it
// has; no plan, no Copilot.
func copilotUser(ctx context.Context, token, host string) (user, plan string, err error) {
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
	if _, err := get(gitHubUserURL(host), &gh); err != nil || gh.Login == "" {
		return "", "", errors.New("GitHub didn't say whose account this is")
	}
	var cp copilotEntitlement
	if code, err := get(copilotUserURL(host), &cp); err != nil {
		if code == 401 || code == 403 || code == 404 {
			return gh.Login, "", errors.New(gh.Login + " has no Copilot subscription")
		}
		return gh.Login, "", errors.New("Copilot: " + err.Error())
	}
	plan = cp.label()
	return gh.Login, plan, nil
}

// GitHubUserURL says whose a GitHub token is; a var so tests can point it
// elsewhere.
var GitHubUserURL = "https://api.github.com/user"
