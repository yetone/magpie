package provider

// A plugin's models are listed by its models hook when magpie starts, a
// sign-in or the plugins change, or Refresh is pressed. A magpie left
// running listed them no more, so a free model OpenCode Zen dropped stayed
// listed and a new one never came (Jeremy.Zhou on Discord, about
// opencode-zen-free). The magpie serving the gateway now asks again every
// hour, and a model a provider's list had and its new one doesn't leaves
// the user's picks too, as Refetch does for a built-in's. A list that
// fell back (the vendor's couldn't be read) is the one told before
// (plugin.keepListed): nothing is dropped for it.
//
// A pick that leaves is set aside (Delisted), not forgotten, and comes back
// when a list has it again: Our Free Model's list is short for a while
// after its service starts and while a free pool is down, and each magpie
// update left its user to pick every model again (多喝热水 on Discord).

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// relistEvery is how often the gateway's magpie lists the plugins'
// models again; relistFirst is how long after it starts it first does;
// relistSoon is how soon it asks again after a list left out a pick, as
// a plugin's service just started may not have listed everything yet.
var (
	relistEvery = time.Hour
	relistFirst = time.Minute
	relistSoon  = 5 * time.Minute
)

// relistPlugins asks the plugins for their providers and models again,
// and keeps the answer (plugin.Providers); nil with no plugin installed or
// no Bun to run one. Tests replace it.
var relistPlugins = func(ctx context.Context) ([]plugin.Provider, error) {
	if len(plugin.Load().Plugins) == 0 || !plugin.Running() && !plugin.HasBun() {
		return nil, nil
	}
	return plugin.Providers(ctx)
}

// KeepPluginsListed lists the plugins' models again every relistEvery,
// in the magpie serving the gateway, and drops the picks of models gone
// from them.
func KeepPluginsListed(ctx context.Context) {
	// what was listed before this magpie asked: on disk from the last run
	before := plugin.Cached()
	t := time.NewTimer(relistFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if len(before) == 0 {
			before = plugin.Cached()
		}
		next := relistEvery
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		now, err := relistPlugins(c)
		cancel()
		if err != nil {
			log.Printf("listing the plugins' models again: %s (the last lists stay)", err)
		} else if now != nil {
			for id, gone := range dropGonePluginPicks(before, now) {
				log.Printf("%s: %v left its plugin's list, and its picks until it lists them again", id, gone)
				next = relistSoon
			}
			before = keptListed(before, now)
		}
		t.Reset(next)
	}
}

// pluginListedIDs is the ids each plugin provider lists, by magpie's id for
// it, those that fell back left out: their list wasn't read. A model the
// user lists as another id is there under that one, as a pick is.
func pluginListedIDs(ps []plugin.Provider) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range ps {
		if p.FellBack || len(p.Models) == 0 {
			continue
		}
		ids := make(map[string]bool, len(p.Models))
		for _, m := range p.Models {
			ids[m.ID] = true
		}
		// picks are of the ids the user lists them by (SetModelID)
		out[PluginID(p.ID)] = modelIDsOf(PluginID(p.ID)).ids(ids)
	}
	return out
}

// dropGonePluginPicks takes out of each plugin provider's picks the
// models before listed and now doesn't, and answers them by provider. A
// pick neither listed was typed in by hand, and stays.
func dropGonePluginPicks(before, now []plugin.Provider) map[string][]string {
	was, is := pluginListedIDs(before), pluginListedIDs(now)
	out := map[string][]string{}
	// by what is listed now: not listed now, or fell back, nothing is known
	// gone; listed now and not before, a pick set aside may come back
	for id, n := range is {
		dropped, err := dropGonePicks(id, was[id], n)
		if err != nil {
			log.Println(id + ": " + err.Error())
		}
		if len(dropped) > 0 {
			out[id] = dropped
		}
	}
	return out
}

// keptListed is what the next relist compares with: now's lists, and for
// a provider whose list fell back or went empty this time the one before,
// so a model dropped once its vendor answers again still leaves the picks.
func keptListed(before, now []plugin.Provider) []plugin.Provider {
	out := make([]plugin.Provider, 0, len(now))
	for _, p := range now {
		if p.FellBack || len(p.Models) == 0 {
			if i := slices.IndexFunc(before, func(b plugin.Provider) bool { return b.ID == p.ID }); i >= 0 {
				p = before[i]
			}
		}
		out = append(out, p)
	}
	return out
}
