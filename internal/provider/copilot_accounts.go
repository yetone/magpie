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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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

// CopilotAccountName is how a Copilot account is known in magpie: its
// GitHub login on github.com (host ""), "<login>@<name>.ghe.com" on an
// enterprise's (#1220). One login on two hosts is two accounts, each with
// its own token, plan and usage, so the name every list, switch, card and
// routing setting goes by carries the host. A GitHub login never has an @.
func CopilotAccountName(login, host string) string {
	if host == "" || login == "" {
		return login
	}
	return login + "@" + host
}

// copilotSavedName is a saved Copilot account's name as CopilotAccountName
// gives it. One magpie signed in on an enterprise's host before #1220 was
// saved under its login alone, its host only in its auth: read, it takes
// the host into its name, kept under it from its next write. The agent's
// own (no auth) and one on github.com are as they were.
func copilotSavedName(l savedLogin) string {
	if l.own() || strings.Contains(l.User, "@") {
		return l.User
	}
	var a struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(l.Auth, &a) != nil || a.Host == "" {
		return l.User
	}
	if h, err := CopilotHost(a.Host); err != nil || h != a.Host {
		return l.User // copilotSaved leaves it out anyway
	}
	return CopilotAccountName(l.User, a.Host)
}

// copilotRenamed says the agent's own account, remembered by its login
// alone before #1220, is the one now named with its enterprise's host:
// the same account, not a new sign-in.
func copilotRenamed(was, now string) bool {
	login, _, ok := strings.Cut(now, "@")
	return ok && !strings.Contains(was, "@") && strings.EqualFold(login, was)
}

// renameTo names l now, remembering the name it was read under (was) so
// its settings can follow it (renameSettled). Its stable id is pinned
// first: a stand-in made of the new name would be another account's.
func (l *savedLogin) renameTo(now string) {
	if l.ID == "" {
		l.ID = loginID(*l)
	}
	if l.was == "" {
		l.was = l.User
	}
	l.User = now
}

// renameFailedAt is when the settings of a renamed account last couldn't
// be moved; renames wait a while after it rather than write logins.json
// at every listing while providers.json can't be read or written.
var (
	renameMu       sync.Mutex
	renameFailedAt time.Time
)

func renameDue() bool {
	renameMu.Lock()
	defer renameMu.Unlock()
	return time.Since(renameFailedAt) > time.Minute
}

// renameSettled is ls as it is written: each account renamed on read
// (renameTo) under its new name once the per-account settings kept by
// its old one — proxy, models, cap, concurrency, in providers.json — are
// moved to the new one (moveAccountSettings), and under its old name
// still, its settings where they were, when they can't be: a
// providers.json that can't be read is not one without settings. Before
// #1220 a bare Copilot login named one account (another of that name
// wrote over it), so what it kept is that account's. One whose old name
// another account still bears leaves those settings to it.
func renameSettled(ls []savedLogin) []savedLogin {
	moves := map[string]string{}
	for _, l := range ls {
		if l.was == "" || slices.ContainsFunc(ls, func(o savedLogin) bool {
			return o.Agent == l.Agent && o.was == "" && accountKey(o.User) == accountKey(l.was)
		}) {
			continue
		}
		moves[l.was] = l.User
	}
	err := moveAccountSettings("copilot", moves)
	if err != nil {
		log.Printf("copilot accounts renamed with their host (#1220): their settings couldn't be moved (%v); kept under their old names for now", err)
		renameMu.Lock()
		renameFailedAt = time.Now()
		renameMu.Unlock()
	}
	disk := slices.Clone(ls)
	for i := range ls {
		if ls[i].was == "" {
			continue
		}
		if err != nil {
			disk[i].User = ls[i].was
			continue
		}
		ls[i].was, disk[i].was = "", ""
	}
	return disk
}

// moveAccountSettings moves the per-account settings of the subscription
// id from each old name in moves to its new one: a setting the new name
// has already stays, and the old one with it. providers.json is read for
// the edit, so one that can't be read stops it, and written only when a
// setting moved.
func moveAccountSettings(id string, moves map[string]string) error {
	if len(moves) == 0 {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	moved := false
	for i := range f.Providers {
		p := &f.Providers[i]
		if p.ID != id {
			continue
		}
		for was, now := range moves {
			from, to := accountKey(was), accountKey(now)
			moved = moveKey(p.AccountProxies, from, to) || moved
			moved = moveKey(p.AccountModels, from, to) || moved
			moved = moveKey(p.AccountCaps, from, to) || moved
			moved = moveKey(p.AccountWindowCaps, from, to) || moved
			moved = moveKey(p.AccountConcurrency, from, to) || moved
		}
	}
	if !moved {
		return nil
	}
	return store(f)
}

func moveKey[V any](m map[string]V, from, to string) bool {
	v, ok := m[from]
	if !ok || from == to {
		return false
	}
	if _, taken := m[to]; taken {
		return false
	}
	m[to] = v
	delete(m, from)
	return true
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
	return CopilotAccountName(firstNonEmpty(own.User, "GitHub"), own.Host)
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

// addCopilotLogin keeps an account magpie just signed in, on host ("" for
// github.com), under its login and host: a same-named account on another
// host is another account, neither written over nor dropped as the
// agent's own (#1220).
func addCopilotLogin(user, plan, token, host string) error {
	a := map[string]string{"oauth_token": token}
	if host != "" {
		a["host"] = host
	}
	auth, _ := json.Marshal(a)
	name := CopilotAccountName(user, host)
	own := copilotOwnUser(copilotConfigDir())
	// the editors' own sign-in of this account refused (#1238): this one
	// is kept, standing for it, rather than let go as the same account
	if strings.EqualFold(own, name) && copilotEditorRefused(copilotConfigDir(), user, host) {
		own = ""
	}
	return addSideLogin(savedLogin{Agent: "copilot", User: name, Plan: plan, Auth: auth}, own, func(savedLogin) {})
}

// copilotEditorRefused says GitHub refused the editors' sign-in of user on
// host: a stale token in apps.json, which a sign-in from magpie is not let
// go for (#1238).
func copilotEditorRefused(cfg, user, host string) bool {
	gh, ghe := copilotEditorLogins(cfg)
	for _, e := range []*copilotApp{gh, ghe} {
		if e != nil && copilotSameAccount(*e, copilotApp{User: user, Host: host}) && copilotRefusedToken(e.Token) {
			return true
		}
	}
	return false
}

// copilotProbeEditor asks GitHub whether it still takes the editors' sign-in
// of user on host, when magpie has not asked yet: a sign-in from magpie of
// the same account is kept only when it doesn't (addCopilotLogin).
func copilotProbeEditor(ctx context.Context, user, host string) {
	gh, ghe := copilotEditorLogins(copilotConfigDir())
	for _, e := range []*copilotApp{gh, ghe} {
		if e != nil && copilotSameAccount(*e, copilotApp{User: user, Host: host}) && !copilotRefusedToken(e.Token) {
			_, _ = copilotToken(ctx, *e)
		}
	}
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
