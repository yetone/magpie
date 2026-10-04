package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// ForgetAccount signs a subscription's accounts out of magpie for good, the
// way past a removal (#694: a removed Qoder came back with its old account
// each time it was added back, and nothing took it away). A plugin's
// sign-ins leave plugin-auth.json (the plugin host's signOut when one runs,
// else under plugin-auth.json.lock) and logins.json, and a moved built-in's
// backup of them goes too, so moving back doesn't bring them either; a
// built-in's saved accounts go as each account's Remove takes them. An
// agent's own sign-in, which magpie only reads, is hidden as Remove hides
// it. Signing in again starts afresh.
func ForgetAccount(id string) error {
	p, ok := find(Accounts(), strings.ToLower(strings.TrimSpace(id)))
	if !ok || p.Account == nil {
		return fmt.Errorf("no signed-in account %q", id)
	}
	if p.IsPlugin() {
		pp, ok := PluginOf(p.ID)
		if !ok {
			pp = *p.Account.plugin
		}
		return forgetPluginAccounts(pp)
	}
	return forgetLogins(p.Account.Agent)
}

// forgetLogins forgets each of a built-in's accounts, the one in use first
// last, as the accounts list removes them one by one.
func forgetLogins(agent string) error {
	ls := Logins(agent)
	slices.SortStableFunc(ls, func(a, b Login) int {
		switch {
		case a.Active == b.Active:
			return 0
		case a.Active:
			return 1
		}
		return -1
	})
	var errs []error
	for _, l := range ls {
		if err := ForgetLogin(agent, l.User); err != nil {
			errs = append(errs, err)
		}
	}
	ForgetAccounts()
	return errors.Join(errs...)
}

// forgetPluginAccounts signs every account of the plugin's provider out:
// the ones magpie signed in to through it from plugin-auth.json, and the
// agent's own (a CLI's sign-in the plugin reads) hidden.
func forgetPluginAccounts(pp plugin.Provider) error {
	agent := pluginAgent(pp)
	var errs []error
	keys, users := map[string]bool{}, map[string]bool{}
	var owners []string
	for _, l := range pluginLogins(pp) {
		if l.Own {
			owners = append(owners, l.User)
			continue
		}
		keys[l.acct.Key], users[strings.ToLower(l.User)] = true, true
	}
	own := len(owners) > 0
	// the agent's own sign-in first, while the list still has every
	// account: hidden, as each account's Remove hides it
	for _, u := range owners {
		if err := forgetPluginLogin(pp, u); err != nil {
			errs = append(errs, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !own {
		// every sign-in kept under the provider's id, listed or not
		if err := plugin.SignOut(ctx, pp.ID, ""); err != nil {
			errs = append(errs, err)
		}
	} else {
		for k := range keys {
			if err := plugin.SignOut(ctx, pp.ID, k); err != nil {
				errs = append(errs, err)
			}
		}
	}
	loginsMu.Lock()
	ls := readLogins()
	n := len(ls)
	ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
		return l.Agent == agent && (!own || keys[l.Home]) && !l.own()
	})
	if len(ls) != n {
		if err := writeLogins(ls); err != nil {
			errs = append(errs, err)
		}
	}
	loginsMu.Unlock()
	if Moved(pp.ID) {
		// moving back would bring the accounts forgotten back from the
		// backup the move set aside
		mv := movers[pp.ID]
		err := setMigration(pp.ID, func(m *Migration) {
			m.Accounts = slices.DeleteFunc(m.Accounts, func(a movedAccount) bool { return !a.Own && (!own || keys[a.Key]) })
			m.Backup = slices.DeleteFunc(m.Backup, func(b savedLogin) bool {
				return mv != nil && slices.Contains(mv.agents, b.Agent) && !b.own() && (!own || users[strings.ToLower(b.User)])
			})
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	ForgetAccounts()
	return errors.Join(errs...)
}
