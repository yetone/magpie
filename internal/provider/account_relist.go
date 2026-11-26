package provider

// A signed-in account's model list is fetched once by FetchNew, when magpie
// has none for it yet, and after that only when Refresh is pressed. A model
// the vendor adds to the plan later never reached a magpie left running, nor
// one restarted, as the list it had was not new: OpenAI served GPT-6.1 Terra
// to ChatGPT accounts on 10-26, and Codex routed through magpie still listed
// what the ChatGPT backend had on 10-12 (cliff_26651 on Discord). The magpie
// serving the gateway now asks each account's vendor again once the list it
// has is accountListAge old, as KeepPluginsListed does for a plugin's.
//
// OpenRouter's decision models are a list of the same kind: magpie fetches
// it for the provider (decideListDue), the user picks nothing in it, and
// it was fetched once. A model OpenRouter added later (baseten/elephant-1,
// 10-21) was missing from a list fetched before, so a classifier picked by
// hand as baseten/elephant-1:free was taken for a chat model and refused
// 400 "is a decisions model … Use the /api/alpha/decisions endpoint
// instead" (coeo91 on Discord, 11-26). The same loop fetches that list
// again once it is decideListAge old.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/probelog"
)

// accountRelistEvery is how often the gateway's magpie looks for an
// account's list that is due; accountRelistFirst is how long after it
// starts it first does (FetchNew lists the new accounts meanwhile);
// accountListAge is how old a list is before it is asked for again.
var (
	accountRelistEvery = time.Hour
	accountRelistFirst = 2 * time.Minute
	accountListAge     = 45 * time.Minute
	decideListAge      = 6 * time.Hour
)

// KeepAccountsListed fetches each signed-in account's list again once it
// is accountListAge old, in the magpie serving the gateway.
func KeepAccountsListed(ctx context.Context) {
	t := time.NewTimer(accountRelistFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		relistAccounts(ctx, time.Now())
		relistDecisions(ctx, time.Now())
		t.Reset(accountRelistEvery)
	}
}

// relistAccounts fetches the list of each account whose list is older than
// accountListAge at now, and answers the ids it asked. An account with no
// list yet is FetchNew's; a plugin's is listed by KeepPluginsListed; one
// switched off isn't asked. A fetch that fails keeps the list there was
// (the fetch saves only what it got), and is tried at the next look.
func relistAccounts(ctx context.Context, now time.Time) []string {
	var asked []string
	for _, p := range All() {
		if p.Off || p.Account == nil || p.Account.fetch == nil || p.IsPlugin() || !p.Ready() {
			continue
		}
		at, ok := p.Fetched()
		if !ok || now.Sub(at) < accountListAge {
			continue
		}
		if ctx.Err() != nil {
			return asked
		}
		asked = append(asked, p.ID)
		fctx, cancel := context.WithTimeout(ctx, time.Minute)
		ms, err := p.Fetch(fctx)
		cancel()
		if err != nil {
			log.Printf("%s: fetching its list again failed: %s (the last list stays)", p.ID, err)
			continue
		}
		log.Printf("%s: fetched its list again, %d models", p.ID, len(ms))
	}
	return asked
}

// relistDecisions fetches OpenRouter's list of decision models again for
// each provider at OpenRouter whose list of them is older than
// decideListAge at now, and answers the ids it asked. One not fetched yet
// is FetchNew's (decideListDue); one switched off isn't asked; a fetch that
// fails keeps the list there was, and is tried at the next look.
func relistDecisions(ctx context.Context, now time.Time) []string {
	var asked []string
	for _, p := range All() {
		if !p.On() || !p.Decides() {
			continue
		}
		id := p.ID
		switch {
		case p.DecideOnly() && HostOf(strings.TrimSpace(p.ModelsURL)) == "openrouter.ai":
		case openRouterDecisions(p.Decide) == "":
			continue
		case !p.DecideOnly():
			id = decisionsID(p.ID)
		}
		_, at, ok := catalog.Live(id)
		if !ok || now.Sub(at) < decideListAge {
			continue
		}
		if ctx.Err() != nil {
			return asked
		}
		asked = append(asked, p.ID)
		fctx, cancel := context.WithTimeout(ctx, time.Minute)
		ms, err := p.fetchDecide(p.ownRequests(p.Via(fctx), probelog.KindList))
		cancel()
		if err != nil {
			log.Printf("%s: fetching its decision models again failed: %s (the last list stays)", p.ID, err)
			continue
		}
		log.Printf("%s: fetched its decision models again, %d models", p.ID, len(ms))
	}
	return asked
}
