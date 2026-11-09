package provider

// A provider of the user's own can keep its model list up to date by itself
// and take the models it newly lists by rule (#2135: relays of 10–70
// models, run headless under `magpie serve`, whose new models never reached
// the list until Fetch models was clicked, and were then picked one by one).
//
//   - RefreshEvery: the gateway fetches the list again once it is that old
//     (KeepListsFresh), exactly as Fetch models or `magpie provider refresh`
//     does (Refetch), so a model the vendor drops leaves the picks as it does
//     there.
//   - AutoInclude: patterns over a model's id. A model a fetch lists that
//     the list before it didn't, and that a pattern matches, joins the
//     picks (takeMatching); setting the patterns takes the matches of the
//     list as it is now (SetAutoInclude). The rules only ever add: what is
//     served now stays served, and a model the user unticks afterwards
//     stays unticked, as it isn't new to the next fetch.
//
// Both are the user's own settings, kept by every save that doesn't set
// them (the window's editor among them), and only a provider of the user's
// own has them: an account's list is fetched for it (FetchNew, then
// KeepAccountsListed), and a plugin's by the plugin (KeepPluginsListed).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/catalog"
)

// MinRefreshEvery is the shortest RefreshEvery: a vendor's list changes
// over days, and asking it each minute only spends its rate limit.
const MinRefreshEvery = 10 * time.Minute

// ParseRefreshEvery reads a RefreshEvery as typed: a Go duration ("30m",
// "6h") or a number of days ("1d"); "", "off", "never" or "0" for none.
func ParseRefreshEvery(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "off", "never", "no", "none", "0", "-":
		return 0, nil
	}
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a time magpie can read: write it as 30m, 6h or 1d", s)
		}
		d = time.Duration(days * float64(24*time.Hour))
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("%q is not a time magpie can read: write it as 30m, 6h or 1d", s)
		}
	}
	if d < MinRefreshEvery {
		return 0, fmt.Errorf("a list is fetched at most every %s, not every %s", MinRefreshEvery, d)
	}
	return d, nil
}

// RemoteMagpieEvery is how often, as RefreshEvery writes it, the gateway
// fetches a Remote magpie's list when its user set no RefreshEvery. Its list is the other magpie's
// models as they are now: a model that comes there later (a ZCode gift
// plan's GLM-5.3-Trial, listed once its plugin read the gift; a provider,
// plugin or routing group added there) reached this magpie only when
// Fetch models was pressed in its editor, and a trial a day long could
// pass unseen (6igsteven on Discord, 10-24). It is a list on the other
// computer's magpie, read from its memory, so asking it often costs
// nothing a vendor sees. "off" (magpie provider refresh-every <id> off)
// asks it only when told, as before.
const RemoteMagpieEvery = "10m"

// RefreshSchedule is the schedule the provider's list is fetched on, as
// written: its user's, or a Remote magpie's default (RemoteMagpieEvery)
// when its user set none; "" for only when asked.
func (p Provider) RefreshSchedule() string {
	if !p.ownList() {
		return ""
	}
	if strings.TrimSpace(p.RefreshEvery) == "" && p.IsRemoteMagpie() {
		return RemoteMagpieEvery
	}
	return p.RefreshEvery
}

// RefreshInterval is how often the gateway fetches the provider's list by
// itself: 0 for only when asked, and for one RefreshEvery can't be read.
// A Remote magpie with none set is fetched every RemoteMagpieEvery.
func (p Provider) RefreshInterval() time.Duration {
	if !p.ownList() {
		return 0
	}
	d, err := ParseRefreshEvery(p.RefreshSchedule())
	if err != nil {
		return 0
	}
	return d
}

// ownList reports whether the provider's list is the user's own provider's
// to keep: not an account's, a plugin's or a decision-only provider's.
func (p Provider) ownList() bool {
	return p.Account == nil && !p.IsPlugin() && !p.DecideOnly()
}

// errNotOwnList is why an account's or a plugin's provider takes neither
// setting.
func errNotOwnList(p Provider) error {
	return fmt.Errorf("%s's list is fetched for it by magpie (or by its plugin): a schedule and rules are for a provider of your own", p.Name)
}

// SetRefreshEvery sets how often the gateway fetches the provider's list by
// itself, as ParseRefreshEvery reads it; "off" for only when asked.
func SetRefreshEvery(id, every string) error {
	d, err := ParseRefreshEvery(every)
	if err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if !p.ownList() {
		return errNotOwnList(*p)
	}
	p.RefreshEvery = ""
	if d > 0 {
		p.RefreshEvery = strings.ToLower(strings.TrimSpace(every))
	} else if p.IsRemoteMagpie() {
		// kept as said: none set is RemoteMagpieEvery for a Remote magpie
		p.RefreshEvery = "off"
	}
	return Save(*p)
}

// CleanAutoInclude are AutoInclude's patterns as they are kept: trimmed,
// each once, every one one magpie can read. A pattern is a glob with * in
// it (any run of characters, in any case: "gpt-*", "*claude*") or a
// regular expression after "re:" that matches the whole id.
func CleanAutoInclude(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || slices.Contains(out, p) {
			continue
		}
		if !IsPattern(p) {
			return nil, fmt.Errorf("%q is a model's id, not a rule: a rule has a * in it, or starts with %s (magpie provider models <id> +%s picks that one)", p, RegexpPrefix, p)
		}
		if _, err := compilePattern(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// SetAutoInclude sets the provider's rules, and takes the models its list
// has now that they match into its picks; none clears them. It answers the
// models taken.
func SetAutoInclude(id string, rules []string) ([]string, error) {
	rules, err := CleanAutoInclude(rules)
	if err != nil {
		return nil, err
	}
	p, err := Find(id)
	if err != nil {
		return nil, err
	}
	if !p.ownList() {
		return nil, errNotOwnList(*p)
	}
	p.AutoInclude = rules
	if err := Save(*p); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	return takeMatching(id, func(string) bool { return true })
}

// takeMatching adds to the saved provider's picks the models it lists that
// isNew says to look at and one of its AutoInclude rules matches, and
// answers them. The picks start from what it serves now (Exposed: the picks
// with what withNewlyListed adds, or the vendor's list it serves with none
// picked), so nothing served is taken away; with nothing to add nothing is
// written.
func takeMatching(id string, isNew func(string) bool) ([]string, error) {
	p, err := Find(id)
	if err != nil {
		return nil, err
	}
	var res []matcher
	for _, r := range p.AutoInclude {
		if x, err := compilePattern(r); err == nil {
			res = append(res, x)
		}
	}
	if len(res) == 0 {
		return nil, nil
	}
	var served []string
	for _, m := range p.Exposed() {
		served = append(served, m.ID)
	}
	avail := p.Available()
	var taken []string
	for _, m := range avail {
		if !isNew(m.ID) || slices.Contains(served, m.ID) || slices.Contains(taken, m.ID) {
			continue
		}
		if slices.ContainsFunc(res, func(x matcher) bool { return x.MatchString(m.ID) }) {
			taken = append(taken, m.ID)
		}
	}
	if len(taken) == 0 {
		return nil, nil
	}
	f, err := read()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(f.Providers, func(q Provider) bool { return q.ID == id })
	if i < 0 {
		return nil, nil
	}
	q := &f.Providers[i]
	// new ones first, as the vendor's list and withNewlyListed have them
	q.Models = append(slices.Clone(taken), served...)
	q.NoModels, q.BestInFamily = false, false
	// picked from the list as it is now: one it lists later and no rule
	// matches stays out, unless the picks hold every model
	q.PickedFrom = nil
	for _, m := range avail {
		q.PickedFrom = append(q.PickedFrom, m.ID)
	}
	if err := store(f); err != nil {
		return nil, err
	}
	return taken, nil
}

// listsTried is when KeepListsFresh last asked each provider for its list,
// so that one which fails is asked again after listRetry, not each minute.
var listsTried = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// listRetry is how long a scheduled fetch that failed waits to be tried
// again (less when the provider's RefreshEvery is shorter).
var listRetry = 15 * time.Minute

// listsEvery is how often KeepListsFresh looks for a list that is due.
var listsEvery = time.Minute

// KeepListsFresh fetches the list of each provider of the user's own that
// has a RefreshEvery once its list is that old, run by the magpie serving
// the gateway (`magpie serve`, the app).
func KeepListsFresh(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	var wait <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wait:
		}
		refreshDueLists(ctx, time.Now())
		wait = beat.Next(listsEvery)
	}
}

// refreshDueLists fetches each list due at now, one after another, and
// answers the ids it asked.
func refreshDueLists(ctx context.Context, now time.Time) []string {
	var asked []string
	// the user's own providers alone, as All lists them: only those keep a
	// list on a schedule (ownList), and All asks every agent for its
	// sign-in, the Claude keychain's `security` among them, each minute
	// of an idle magpie (#3185)
	f := load()
	own, _ := ownOf(f)
	for _, p := range ordered(own, f.Order) {
		every := p.RefreshInterval()
		// a list saved before lists kept the models made for other APIs
		// (embeddings, rerank, speech, transcriptions) is fetched again
		// once, schedule or none, so their routes and groups have them
		// without the user's Refresh (#5339); tried once a run, so one
		// that fails waits for its schedule, the next start or a Refresh
		listsTried.Lock()
		_, triedOnce := listsTried.m[p.ID]
		listsTried.Unlock()
		_, fetched := p.Fetched()
		once := !triedOnce && fetched && !catalog.LiveKinded(p.ID)
		// one that lists nothing (Preset.NoList) has no list to keep fresh
		if every == 0 && !once || p.Off || !p.lists() {
			continue
		}
		if at, ok := p.Fetched(); ok && !once && now.Sub(at) < every {
			continue
		}
		listsTried.Lock()
		tried, ok := listsTried.m[p.ID]
		retry := min(every, listRetry)
		if !once && ok && now.Sub(tried) < retry {
			listsTried.Unlock()
			continue
		}
		listsTried.m[p.ID] = now
		listsTried.Unlock()
		if ctx.Err() != nil {
			return asked
		}
		asked = append(asked, p.ID)
		fctx, cancel := context.WithTimeout(ctx, time.Minute)
		ms, dropped, taken, err := p.RefetchTaking(fctx)
		cancel()
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			again := "tried again in " + retry.String()
			if every == 0 {
				again = "tried again on the next start or Refresh"
			}
			log.Printf("%s: its scheduled list fetch failed: %s (%s)", p.ID, err, again)
		case err == nil:
			log.Printf("%s: fetched its list on schedule, %d models%s%s", p.ID, len(ms), listNote(" taken by rule: ", taken), listNote(" no longer listed: ", dropped))
		}
	}
	return asked
}

func listNote(head string, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ";" + head + strings.Join(ids, ", ")
}
