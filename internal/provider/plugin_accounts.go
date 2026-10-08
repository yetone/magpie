package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's provider is signed in to as many times as the user likes, as
// a built-in subscription is: plugin-auth.json keeps each account's
// sign-in (under the provider's id, then id#slot), logins.json which is
// first and which are on, as it does for the built-ins, the account's key
// in its Home.

// pluginAgent is what logins.json lists a plugin provider's accounts as.
func pluginAgent(pp plugin.Provider) string { return "plugin:" + pp.ID }

// pluginLabels names each account: its id, else the end of its key, else
// what it signed in with; a name two share gets the account's slot.
func pluginLabels(pp plugin.Provider) map[string]string {
	out := map[string]string{}
	seen := map[string]int{}
	for _, a := range pp.Accounts {
		l := a.AccountID
		if l == "" && a.Hint != "" {
			l = "API key …" + a.Hint
		}
		if l == "" {
			l = map[string]string{"api": "API key", "oauth": "Signed in"}[a.Type]
		}
		if l == "" {
			l = "Signed in"
		}
		out[a.Key] = l
		seen[strings.ToLower(l)]++
	}
	for _, a := range pp.Accounts {
		if seen[strings.ToLower(out[a.Key])] > 1 {
			if _, slot, ok := strings.Cut(a.Key, "#"); ok {
				out[a.Key] += " (" + slot + ")"
			}
		}
	}
	return out
}

type pluginLogin struct {
	sideLogin
	acct plugin.Account
}

// pluginOwn says a plugin sign-in is the agent's own (its CLI's, its
// app's), which the plugin reads where the agent keeps it. The movers mark
// it so (migrate_side.go, migrate_kiro.go, migrate_workbuddy.go,
// migrate_zcode.go), as the plugins' own "CLI's sign-in" ways do.
func pluginOwn(id string, auth map[string]any) bool {
	md, _ := auth["metadata"].(map[string]any)
	switch id {
	case "devin", CommandCodePlanID:
		return md["cli"] == true
	case "grok":
		return samePath(str(auth["refresh"]), GrokHome())
	case "cursor":
		return str(auth["refresh"]) == cursorCLIMark
	case "kiro", "workbuddy", WorkBuddyAIID:
		return str(auth["source"]) != ""
	case "zcode":
		var s struct{ Source string }
		_ = json.Unmarshal([]byte(str(auth["refresh"])), &s)
		return s.Source == "zcode"
	}
	return false
}

// pluginOwnUser is who the agent itself is signed in to now, "" when it
// can't be told: the own account is named after it, as the built-in's
// was, so the name follows a switch made in the agent.
var pluginOwnUser = func(id string) string {
	switch id {
	case "devin":
		if u, _, ok := devinIdentity(); ok {
			return u
		}
	case "grok":
		if c, ok := readGrokCredential(GrokHome()); ok {
			return c.Email
		}
	case CommandCodePlanID:
		if u, _, ok := cmdOwn(); ok {
			return u
		}
	case "cursor":
		if CursorExecutable() != "" && !cursorSignedOut() {
			if u, _, ok := cursorIdentity(); ok {
				return u
			}
		}
	case "kiro":
		return kiroOwnUser()
	case "zcode":
		if u, _, ok := zcodeOwn(); ok {
			return u
		}
	case "workbuddy", WorkBuddyAIID:
		if u, _, ok := wbOwn(wbSiteOf(id)); ok {
			return u
		}
	}
	return ""
}

// pluginLogins are the provider's accounts, the first in use first. What
// plugin-auth.json keeps is the truth: logins.json follows it. The agent's
// own account is named as the agent is signed in now; removed in magpie,
// it is only hidden, and shows again once the agent signs in anew.
func pluginLogins(pp plugin.Provider) []pluginLogin {
	agent := pluginAgent(pp)
	labels := pluginLabels(pp)
	byKey := map[string]plugin.Account{}
	for _, a := range pp.Accounts {
		byKey[a.Key] = a
	}
	own := map[string]bool{}
	for k, a := range plugin.Auths(pp.ID) {
		if _, ok := byKey[k]; ok && pluginOwn(pp.ID, a) {
			own[k] = true
		}
	}
	mark := ""
	if len(own) > 0 {
		if u := pluginOwnUser(pp.ID); u != "" {
			for k := range own {
				labels[k] = u
			}
		}
		mark = ownMark(pp.ID)
	}
	loginsMu.Lock()
	ls := readLogins()
	changed := false
	have := map[string]bool{}
	keep := ls[:0]
	for _, l := range ls {
		if l.Agent == agent {
			if _, ok := byKey[l.Home]; !ok || have[l.Home] {
				changed = true // signed out, or listed twice
				continue
			}
			have[l.Home] = true
			renamed := l.User != labels[l.Home]
			if renamed {
				l.User, changed = labels[l.Home], true
			}
			// the agent's own, hidden, shows again once the agent signs
			// in anew: told by its sign-in's mark, else by another account
			if l.Hidden != "" && own[l.Home] && (mark != "" && mark != l.Hidden || mark == "" && l.Hidden == hiddenNoMark && renamed) {
				l.Hidden, changed = "", true
			}
		}
		keep = append(keep, l)
	}
	ls = keep
	for _, a := range pp.Accounts {
		if !have[a.Key] {
			ls = append(ls, savedLogin{Agent: agent, User: labels[a.Key], Home: a.Key, On: true, Seen: time.Now().UTC().Truncate(time.Second)})
			changed = true
		}
	}
	if changed {
		_ = writeLogins(ls)
	}
	loginsMu.Unlock()
	var out []pluginLogin
	for _, l := range sideLogins(agent, "", func(l savedLogin) bool { _, ok := byKey[l.Home]; return ok && l.Hidden == "" }) {
		l.Lapsed = l.saved.Lapsed // a refused sign-in shows on the account
		l.Own = own[l.saved.Home]
		out = append(out, pluginLogin{l, byKey[l.saved.Home]})
	}
	return out
}

func pluginSide(pp plugin.Provider) []sideLogin {
	var out []sideLogin
	for _, l := range pluginLogins(pp) {
		out = append(out, l.sideLogin)
	}
	return out
}

// pluginOfAgent is the plugin provider the accounts page names by its
// magpie id.
func pluginOfAgent(agent string) (plugin.Provider, bool) {
	if strings.HasPrefix(agent, "plugin:") {
		id := strings.TrimPrefix(agent, "plugin:")
		for _, pp := range plugin.Cached() {
			if pp.ID == id {
				return pp, true
			}
		}
		return plugin.Provider{}, false
	}
	return PluginOf(agent)
}

func pluginLoginList(pp plugin.Provider) []Login { return loginsOf(pluginSide(pp)) }

func switchPluginLogin(pp plugin.Provider, user string) error {
	return switchSideLogin(pluginAgent(pp), user, pluginSide(pp))
}

func setPluginLoginOn(pp plugin.Provider, user string, on bool) error {
	return setSideLoginOn(pluginAgent(pp), user, on, pluginSide(pp))
}

// forgetPluginLogin signs the account out: its sign-in is magpie's own.
// The agent's own is only hidden, as the built-in hid it: the agent stays
// signed in, and the account shows again once it signs in anew. Qoder's
// first goes as the built-in's did, the next put first.
func forgetPluginLogin(pp plugin.Provider, user string) error {
	agent := pluginAgent(pp)
	ls := pluginSide(pp)
	i := slices.IndexFunc(ls, func(l sideLogin) bool { return strings.EqualFold(l.User, user) })
	if i < 0 {
		return fmt.Errorf("no %s account %q", pp.Name, user)
	}
	own, first := ls[i].Own, ls[i].Active
	if !own && !(first && firstGoes(pp.ID)) {
		var err error
		ferr := forgetSideLogin(agent, user, ls, func(l savedLogin) { err = signOutPlugin(pp, l.Home) })
		if ferr != nil {
			return ferr
		}
		return err
	}
	next := ""
	if first {
		for _, l := range ls {
			if !strings.EqualFold(l.User, user) {
				next = l.User
				break
			}
		}
	}
	key := ls[i].saved.Home
	err := editSideLogin(agent, user, func(saved []savedLogin, j int) ([]savedLogin, error) {
		for k := range saved {
			if saved[k].Agent == agent && (first || k == j) {
				saved[k].First = next != "" && strings.EqualFold(saved[k].User, next)
			}
		}
		if own {
			saved[j].Hidden = firstNonEmpty(ownMark(pp.ID), hiddenNoMark)
			return saved, nil
		}
		return append(saved[:j], saved[j+1:]...), nil
	})
	if err != nil || own {
		return err
	}
	return signOutPlugin(pp, key)
}

// firstGoes says the account in use first can be removed, the next taking
// its place, as the built-in let it (Qoder's).
func firstGoes(id string) bool { return id == "qoder" || id == QoderCNID }

func signOutPlugin(pp plugin.Provider, key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return plugin.SignOut(ctx, pp.ID, key)
}

// pluginAlsoOn is the provider's accounts in use behind the first.
func pluginAlsoOn(pp plugin.Provider) []Provider {
	var out []Provider
	for _, l := range pluginLogins(pp) {
		if !l.Active && l.On {
			out = append(out, pluginProvider(pp, l))
		}
	}
	return out
}

// keepPluginPlan keeps the plan an account's allowance told on its row,
// for the accounts list to show it as a built-in's.
func keepPluginPlan(pp plugin.Provider, key, plan string) {
	if plan == "" {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i, l := range ls {
		if l.Agent == pluginAgent(pp) && l.Home == key {
			if l.Plan != plan {
				ls[i].Plan = plan
				_ = writeLogins(ls)
			}
			return
		}
	}
}

// notePluginLapse marks the account at key lapsed when its vendor refused
// a request (401), as a built-in's refused sign-in is marked, and clears
// the mark once one goes through.
func notePluginLapse(pp plugin.Provider, key string, status int) {
	notePluginLapseSince(pp, key, status, toldNow)
}

// What plugin accounts' sign-ins were told of (a request's answer, a usage
// reading, a sign-in) is counted, under loginsMu: pluginSignInTold
// counts it all, and pluginSignInSaid has the count each account was last
// told of at. A count, not a time: Windows' clock moves in ticks of up to
// 15.6ms, and a reading begun just after an answer would read as begun
// with it.
var (
	pluginSignInTold uint64
	pluginSignInSaid = map[string]uint64{}
)

// toldNow is the began of what is told now, not read from earlier.
const toldNow = ^uint64(0)

// pluginSignInAt is the count a usage reading beginning now goes by.
func pluginSignInAt() uint64 {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	return pluginSignInTold
}

// notePluginLapseSince is notePluginLapse for what a usage reading begun
// at began (pluginSignInAt) found, toldNow for what is told now: what was
// told of the sign-in since began stands over it. A reading begun before
// a request's 401 that ended clean took the mark off an account the
// vendor had just refused.
func notePluginLapseSince(pp plugin.Provider, key string, status int, began uint64) {
	refused := status == http.StatusUnauthorized
	if !refused && (status < 200 || status > 299) {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	said := pluginAgent(pp) + "/" + key
	if pluginSignInSaid[said] > began {
		return
	}
	if began == toldNow {
		pluginSignInTold++
		began = pluginSignInTold
	}
	pluginSignInSaid[said] = began
	ls := readLogins()
	for i, l := range ls {
		if l.Agent == pluginAgent(pp) && l.Home == key {
			want := ""
			if refused {
				want = lapsedText(pp, l.User)
			}
			if l.Lapsed != want {
				ls[i].Lapsed = want
				_ = writeLogins(ls)
			}
			return
		}
	}
}

// lapsedText is what a plugin account the vendor refused says.
func lapsedText(pp plugin.Provider, user string) string {
	name, _ := pluginCard(pp)
	return user + "'s " + name + " sign-in has expired — sign in again"
}

// clearPluginLapse takes the mark off an account signed in again, and
// brings back the agent's own account removed in magpie, as signing in to
// the built-in's did (#320).
func clearPluginLapse(saved plugin.Saved) {
	for _, pp := range plugin.Cached() {
		if pp.ID == saved.Provider {
			notePluginLapse(pp, saved.Account, http.StatusOK)
		}
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i, l := range ls {
		if l.Agent == "plugin:"+saved.Provider && l.Home == saved.Account && l.Hidden != "" {
			ls[i].Hidden = ""
			_ = writeLogins(ls)
			return
		}
	}
}
