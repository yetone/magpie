package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// A plugin's provider is signed in to as many times as the user likes, as
// a built-in subscription is: plugin-auth.json keeps each account's
// sign-in (under the provider's id, then id#slot), logins.json which is
// first and which are on, as it does for the built-ins, the account's key
// in its Home.

// pluginAgent is what logins.json lists a plugin provider's accounts as.
func pluginAgent(pp plugin.Provider) string { return "plugin:" + pp.ID }

// publicPlugin is whether every way to sign in to the plugin's provider is
// keyless, its credential one the plugin gives whatever is typed: OpenCode
// Zen Free's shared "public". Such an account is nobody's, so with
// subscription accounts off (#1535, discovery.go) it is still listed and
// served. Before, Zen Free signed in all the same with them off, the
// Plugins page said so, and magpie listed it nowhere (lileo0668 on
// Discord: "signed in, but magpie can't list it").
func publicPlugin(pp plugin.Provider) bool {
	if len(pp.Methods) == 0 {
		return false
	}
	for _, m := range pp.Methods {
		if m.Type != "api" || !m.Keyless {
			return false
		}
	}
	return true
}

// publicLogins are a public plugin's accounts while subscription accounts
// are off, read from the plugin alone: logins.json and plugin-auth.json
// are left unread then (discovery.go), so the first is in use and every
// one is on.
func publicLogins(pp plugin.Provider) []pluginLogin {
	agent, labels := pluginAgent(pp), pluginLabels(pp, nil)
	var out []pluginLogin
	for _, a := range pp.Accounts {
		saved := savedLogin{Agent: agent, User: labels[a.Key], Home: a.Key, On: true}
		out = append(out, pluginLogin{sideLogin{Login{Agent: agent, User: saved.User, On: true}, saved}, a})
	}
	if len(out) > 0 {
		out[0].Active = true
	}
	return out
}

// publicAccount is whether p is a public plugin's provider (publicPlugin),
// served while subscription accounts are off.
func (p Provider) publicAccount() bool {
	return p.IsPlugin() && publicPlugin(*p.Account.plugin)
}

// pluginLabels names each account: by named (the agent's own, as the
// agent is signed in now), else its id, else the end of its key, else what
// it signed in with; a name two share gets the account's slot. The slot
// goes on after the own name, so the own account never reads as another
// signed in to the same one (dev on Discord: ZCode's, its id "ZCode",
// named as one magpie signed in, and Remove on that one took the own).
func pluginLabels(pp plugin.Provider, named map[string]string) map[string]string {
	out := map[string]string{}
	seen := map[string]int{}
	for _, a := range pp.Accounts {
		l := named[a.Key]
		if l == "" {
			l = a.AccountID
		}
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

// pluginNames is pluginLabels as the accounts list names them, the
// agent's own as the agent is signed in now.
func pluginNames(pp plugin.Provider) map[string]string {
	_, named := pluginOwnNames(pp)
	return pluginLabels(pp, named)
}

// pluginOwnNames tells the provider's accounts that are the agent's own,
// and names them as the agent is signed in now, where that can be read.
func pluginOwnNames(pp plugin.Provider) (own map[string]bool, named map[string]string) {
	own, named = map[string]bool{}, map[string]string{}
	listed := map[string]bool{}
	for _, a := range pp.Accounts {
		listed[a.Key] = true
	}
	for k, a := range plugin.Auths(pp.ID) {
		if listed[k] && pluginOwn(pp.ID, a) {
			own[k] = true
		}
	}
	if len(own) > 0 {
		if u := pluginOwnUser(pp.ID); u != "" {
			for k := range own {
				named[k] = u
			}
		}
	}
	return own, named
}

type pluginLogin struct {
	sideLogin
	acct plugin.Account
}

// cliMarked says a sign-in carries the "cli" mark of the agent's own
// sign-in: true as a mover wrote it before, "true" as the Devin plugin
// writes it since 0.1.24 (plugins #396).
func cliMarked(md map[string]any) bool {
	return md["cli"] == true || md["cli"] == "true"
}

// pluginOwn says a plugin sign-in is the agent's own (its CLI's, its
// app's), which the plugin reads where the agent keeps it. The movers mark
// it so (migrate_side.go, migrate_kiro.go, migrate_workbuddy.go,
// migrate_zcode.go), as the plugins' own "CLI's sign-in" ways do.
func pluginOwn(id string, auth map[string]any) bool {
	md, _ := auth["metadata"].(map[string]any)
	switch id {
	case "devin", CommandCodePlanID:
		return cliMarked(md)
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

// pluginLoginLocks counts the times pluginLogins took loginsMu (a test
// reads it).
var pluginLoginLocks atomic.Int64

// pluginLogins are the provider's accounts, the first in use first. What
// plugin-auth.json keeps is the truth: logins.json follows it. The agent's
// own account is named as the agent is signed in now; removed in magpie,
// it is only hidden, and shows again once the agent signs in anew.
func pluginLogins(pp plugin.Provider) []pluginLogin {
	if settings.AccountDiscoveryOff() {
		if publicPlugin(pp) {
			return publicLogins(pp)
		}
		return nil
	}
	agent := pluginAgent(pp)
	byKey := map[string]plugin.Account{}
	for _, a := range pp.Accounts {
		byKey[a.Key] = a
	}
	own, named := pluginOwnNames(pp)
	mark := ""
	if len(own) > 0 {
		mark = ownMark(pp.ID)
	}
	labels := pluginLabels(pp, named)
	// looked at as kept, and copied only at the first change: nearly
	// every list changes nothing, and a page's state lists each plugin's
	settle := func(ls []savedLogin) ([]savedLogin, bool) {
		changed := false
		have := map[string]bool{}
		var keep []savedLogin
		for i, l := range ls {
			drop, edit := false, false
			if l.Agent == agent {
				if _, ok := byKey[l.Home]; !ok || have[l.Home] {
					drop = true // signed out, or listed twice
				} else {
					have[l.Home] = true
					renamed := l.User != labels[l.Home]
					if renamed {
						l.User, edit = labels[l.Home], true
					}
					// the agent's own, hidden, shows again once the agent signs
					// in anew: told by its sign-in's mark, else by another account
					if l.Hidden != "" && own[l.Home] && (mark != "" && mark != l.Hidden || mark == "" && l.Hidden == hiddenNoMark && renamed) {
						l.Hidden, edit = "", true
					}
				}
			}
			if (drop || edit) && !changed {
				keep, changed = slices.Clone(ls[:i:i]), true
			}
			if changed && !drop {
				keep = append(keep, l)
			}
		}
		if changed {
			ls = keep
		}
		for _, a := range pp.Accounts {
			if !have[a.Key] {
				if !changed {
					ls, changed = slices.Clone(ls), true
				}
				ls = append(ls, savedLogin{Agent: agent, User: labels[a.Key], Home: a.Key, On: true, Seen: time.Now().UTC().Truncate(time.Second)})
			}
		}
		return ls, changed
	}
	// what nothing changes takes no lock: taken, it opens logins.json's
	// lock file, across magpies, and /api/providers lists every plugin
	// (a fifth of its CPU). A change is made from a read under it.
	if _, changed := settle(sharedLogins()); changed {
		pluginLoginLocks.Add(1)
		loginsMu.Lock()
		if ls, changed := settle(sharedLogins()); changed {
			_ = writeLogins(ls)
		}
		loginsMu.Unlock()
	}
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
	isNext := listedMatch(agent, next, ls)
	err := editListedLogin(agent, user, ls, func(saved []savedLogin, j int) ([]savedLogin, error) {
		n := -1
		if next != "" {
			n = pickSaved(saved, isNext)
		}
		for k := range saved {
			if saved[k].Agent == agent && (first || k == j) {
				saved[k].First = k == n
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

// pluginAlsoOn is the provider's accounts in use behind the first;
// withOff, those turned off too, marked off.
func pluginAlsoOn(pp plugin.Provider, withOff bool) []Provider {
	var out []Provider
	for _, l := range pluginLogins(pp) {
		if !l.Active && (l.On || withOff) {
			out = append(out, offIf(pluginProvider(pp, l), !l.On))
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

// PluginAccountUser is who the plugin id's account at key is (an email, a
// login), as the Routing trace and the request log name it: "" when no
// account is kept there.
func PluginAccountUser(id, key string) string {
	for _, l := range readLogins() {
		if l.Agent == "plugin:"+id && l.Home == key {
			return l.User
		}
	}
	return ""
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
	notePluginLapseOf(pluginAgent(pp), key, status, began, func(user string) string { return lapsedText(pp, user) })
}

// notePluginLapseOf is notePluginLapseSince by the account's agent, so a
// mark can be cleared without the plugin's provider at hand.
func notePluginLapseOf(agent, key string, status int, began uint64, text func(user string) string) {
	refused := status == http.StatusUnauthorized
	if !refused && (status < 200 || status > 299) {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	said := agent + "/" + key
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
		if l.Agent == agent && l.Home == key {
			want := ""
			if refused {
				want = text(l.User)
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
// the built-in's did (#320). The account's kept usage reading goes too: it
// was read under the old sign-in, and for a minute it would answer the
// next reading with the old sign-in's "expired" without asking the plugin,
// and so without marking or clearing anything. The mark goes by the
// account alone: the providers' list is empty while another magpie's
// change to plugins.json is read again, and a sign-in finished then left
// the account saying "sign in again" (#2748).
func clearPluginLapse(saved plugin.Saved) {
	notePluginLapseOf("plugin:"+saved.Provider, saved.Account, http.StatusOK, toldNow, nil)
	for _, pp := range plugin.Cached() {
		if pp.ID == saved.Provider {
			if user := pluginNames(pp)[saved.Account]; user != "" {
				StaleAllowance(pluginAgent(pp), user)
			}
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
