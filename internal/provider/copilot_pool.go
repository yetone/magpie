package provider

// Several Copilot accounts, each with its own plan's models (applemacssma
// on X: "每个ghcp账号有的模型不一样"). Copilot's /models answers each
// GitHub account with what its plan has: a Pro+ account lists models a Pro
// or Student one doesn't. The copilot provider's list was the first
// account's alone, fetched with its sign-in, so a model only another
// account had was nowhere to pick, and a model only the first had was
// sent to the others too, which answer it 400 model_not_supported. Each
// account's list is now kept of its own (accountModels), the provider's
// is what they have together, and the gateway sends a model only to the
// accounts whose list has it (Account.Lists), as Codex's ChatGPT accounts
// are served (codexPoolModels).

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilotSavedListAge is how old another account's list is before it is
// asked for again, and copilotSavedAtOnce how many such lists one fetch of
// the first account's asks. An account with no list yet is asked at once.
// A plan's models change seldom, and N /models calls from one address for
// accounts no editor uses is a pattern GitHub's own clients never make.
const (
	copilotSavedListAge = 24 * time.Hour
	copilotSavedAtOnce  = 1
	copilotSavedRetry   = time.Hour
)

// copilotSavedAsked is when each other account's list was last asked for
// and not saved, so one that keeps failing waits copilotSavedRetry; an ask
// in flight is held here too, so two fetches don't both make it.
var copilotSavedAsked = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// copilotSavedNow is copilotFetchSaved's clock; a var so tests can move it.
var copilotSavedNow = time.Now

// copilotPoolModels is ms, the list of the Copilot account in use first,
// user's, kept as that account's own, with the other accounts on's lists
// fetched as they are due (copilotFetchSaved) and joined to it.
func copilotPoolModels(ctx context.Context, user string, ms []catalog.Model) []catalog.Model {
	_ = catalog.SaveLive(accountModels("copilot", user), copilotBase, ms)
	copilotFetchSaved(ctx, user)
	return poolModels("copilot", ms)
}

// copilotFetchSaved asks for the models of the Copilot accounts on behind
// first, each with its own sign-in and through its own proxy. One that
// can't be asked now keeps what it listed last.
func copilotFetchSaved(ctx context.Context, first string) {
	now := copilotSavedNow()
	type due struct {
		c  copilotLoginApp
		at time.Time
	}
	var fresh, stale []due
	copilotSavedAsked.Lock()
	for _, c := range copilotLogins(copilotConfigDir()) {
		if c.Active || !c.On || strings.EqualFold(c.User, first) || c.app.Token == "" {
			continue
		}
		asked := copilotSavedAsked.m[strings.ToLower(c.User)]
		_, at, ok := catalog.Live(accountModels("copilot", c.User))
		if !ok {
			if now.Sub(asked) >= copilotSavedRetry {
				fresh = append(fresh, due{c, at})
			}
			continue
		}
		if asked.After(at) {
			at = asked
		}
		if now.Sub(at) >= copilotSavedListAge {
			stale = append(stale, due{c, at})
		}
	}
	sort.SliceStable(stale, func(i, j int) bool { return stale[i].at.Before(stale[j].at) })
	ask := append(fresh, stale[:min(len(stale), copilotSavedAtOnce)]...)
	for _, d := range ask {
		copilotSavedAsked.m[strings.ToLower(d.c.User)] = now
	}
	copilotSavedAsked.Unlock()
	for _, d := range ask {
		ms, err := copilotModels(ViaLogin(ctx, "copilot", d.c.User), d.c.app)
		if err != nil {
			continue
		}
		if catalog.SaveLive(accountModels("copilot", d.c.User), copilotBase, ms) == nil {
			// the list saved says when it was asked; only a failed ask
			// is held here, so a list gone (a new HOME, its file
			// removed) is asked for again at once
			copilotSavedAsked.Lock()
			if copilotSavedAsked.m[strings.ToLower(d.c.User)].Equal(now) {
				delete(copilotSavedAsked.m, strings.ToLower(d.c.User))
			}
			copilotSavedAsked.Unlock()
		}
	}
}

// copilotOthersList are the models the Copilot accounts on other than
// user have in their own lists, less those Copilot refused them: a model
// Copilot refused user stays the provider's while another serves it.
func copilotOthersList(user string) map[string]bool {
	out := map[string]bool{}
	for _, c := range copilotLogins(copilotConfigDir()) {
		if !c.On || strings.EqualFold(c.User, user) {
			continue
		}
		live, _, ok := catalog.Live(accountModels("copilot", c.User))
		if !ok {
			continue
		}
		for _, m := range live {
			if !copilotRefuses(c.app.Token, m.ID) {
				out[m.ID] = true
			}
		}
	}
	return out
}
