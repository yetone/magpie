package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/settings"
)

// Rename gives a provider another id, the one its models are picked by
// (<id>/<model>). What magpie keeps that names it by id follows: the
// routing groups it is in and their rules and classifiers, the other
// providers' fallbacks, which agents are shown it, and its StepFun sign-in. The old id stays
// with it (Was), so an agent still running on a config that names it the
// old way reaches it, and its usage so far is counted with it; the agents'
// own files are the agent package's to rewrite (agent.RenameProvider).
// A signed-in account keeps the id of its agent.
func Rename(from, to string) error {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	if to == "" || to != Slug(to) {
		return fmt.Errorf("a provider's id must be lowercase letters, digits and dashes, not %q", to)
	}
	if to == from {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == from })
	// one of the user's own saved on a subscription's id before that was one
	// (a "WorkBuddy" key before v0.1.261) can be moved off it; the
	// subscription itself can't
	custom := i >= 0 && hasEndpoint(f.Providers[i])
	_, signedIn := find(Accounts(), from)
	sub := subscriptionID(from)
	if (signedIn || sub) && !custom {
		return fmt.Errorf("%s is a subscription: its id is its agent's", from)
	}
	switch {
	case to == "magpie":
		return errors.New(`"magpie" is what agents call the gateway itself; pick another id`)
	case to == strings.TrimSuffix(GroupPrefix, "/"):
		return errors.New(`"group" starts the ids of routing groups; pick another id`)
	case to == autoID:
		return errAutoID
	case subscriptionID(to):
		return fmt.Errorf("%q is the id of the %s subscription; pick another", to, to)
	}
	if i < 0 {
		return fmt.Errorf("no provider %q", from)
	}
	if slices.ContainsFunc(f.Providers, func(p Provider) bool { return p.ID == to }) {
		return fmt.Errorf("there is a provider %q already", to)
	}
	// an id another provider once had is this one's now; the calls
	// recorded under it until now stay that one's
	giveID(f.Providers, i, to, time.Now())
	p := &f.Providers[i]
	p.Was = slices.DeleteFunc(p.Was, func(w string) bool { return w == to })
	p.ID = to
	// the subscription's id is the subscription's: an old ref to it isn't
	// taken for this one
	if !sub && !slices.Contains(p.Was, from) {
		p.Was = append(p.Was, from)
	}
	mapModelRefs(&f, func(m string) string { return renamedRef(m, from, to) })
	for j, id := range f.Order {
		if id == from {
			f.Order[j] = to
		}
	}
	// kept out of found groups under its new id too
	for j, id := range f.AutoGroupSkip {
		if id == from {
			f.AutoGroupSkip[j] = to
		}
	}
	// a deleted provider merged into it stays merged (MergeDeleted)
	for j := range f.Deleted {
		if f.Deleted[j].Into == from {
			f.Deleted[j].Into = to
		}
	}
	// the vendor's list last fetched goes with it
	os.Rename(catalog.LivePath(from), catalog.LivePath(to))
	os.Rename(catalog.LivePath(decisionsID(from)), catalog.LivePath(decisionsID(to)))
	if err := store(f); err != nil {
		return err
	}
	// so is its StepFun sign-in, kept by provider id
	if err := renameStepFun(from, to); err != nil {
		return err
	}
	s := settings.Load()
	changed := renameModelPrefs(&s, from, to)
	for a, names := range s.Visible {
		for k, n := range names {
			if strings.EqualFold(n, from) {
				s.Visible[a][k], changed = to, true
			}
		}
	}
	if changed {
		return settings.Save(s)
	}
	return nil
}

// mapModelRefs gives every model ref the file keeps for routing — the
// providers' fallbacks, and the routing groups' members, rules, fast, seen and
// placed members, those turned off, weights, classifiers and picks — as
// to has it: what a provider's rename (Rename) and a model given another
// id at its provider (SetModelID) both rewrite.
func mapModelRefs(f *file, to func(string) string) {
	for j := range f.Providers {
		for k, m := range f.Providers[j].Fallback {
			f.Providers[j].Fallback[k] = to(m)
		}
	}
	for j := range f.Groups {
		g := &f.Groups[j]
		for k, m := range g.Members {
			g.Members[k] = to(m)
		}
		for k := range g.Rules {
			g.Rules[k].MapUses(to)
		}
		for k, m := range g.Fast {
			g.Fast[k] = to(m)
		}
		for k, m := range g.Ultra {
			g.Ultra[k] = to(m)
		}
		for k, m := range g.Placed {
			g.Placed[k] = to(m)
		}
		for k, m := range g.Seen {
			g.Seen[k] = to(m)
		}
		for k, m := range g.Off {
			g.Off[k] = to(m)
		}
		g.Weights = renamedWeights(g.Weights, to)
		g.EffortMaps = renamedEffortMaps(g.EffortMaps, to)
		g.Classifier = to(g.Classifier)
		g.Pick = to(g.Pick)
	}
}

// renamedRef is a model id ("provider/model", "magpie/provider/model")
// with the provider from named to instead.
func renamedRef(ref, from, to string) string {
	pre, rest := "", ref
	if r, ok := strings.CutPrefix(ref, "magpie/"); ok {
		pre, rest = "magpie/", r
	}
	if m, ok := strings.CutPrefix(rest, from+"/"); ok {
		return pre + to + "/" + m
	}
	return ref
}

// RenamedRef is ref with the provider it names by an id the provider had
// named by the id it has now.
func RenamedRef(ref string) string {
	for old, now := range Renamed() {
		if r := renamedRef(ref, old, now); r != ref {
			return r
		}
	}
	return ref
}

// Renamed maps the ids providers had to the ones they have now. An id
// that a provider or a subscription has now is that one's, and is not
// in it.
func Renamed() map[string]string { return UsageRenames().now }

// Gave is an id a provider had and gave up to another (Provider.Gave).
type Gave struct {
	ID    string    `json:"id"`
	Until time.Time `json:"until"`
}

// Deleted is a provider the user deleted (Delete): the calls recorded
// under its id, and under the ids it had (Was, Gave), until it was
// deleted are its own, under its id while no other has taken it and
// under UsageID once one has, never a provider's added later under the
// same id. cybercaius on Discord: a provider deleted and
// another added under its id showed as one on the Usage page.
type Deleted struct {
	ID   string    `json:"id"`
	Name string    `json:"name,omitempty"`
	Icon string    `json:"icon,omitempty"`
	At   time.Time `json:"at"`
	Was  []string  `json:"was,omitempty"`
	Gave []Gave    `json:"gave,omitempty"`
	// Into is the provider the user merged its calls into on the Usage
	// page (MergeDeleted): they are listed and counted as that one's,
	// priced as they were (Renames.Own). Only this record is merged, so
	// a provider added under its id later keeps its own calls. A provider
	// deleted under Into after MergedAt ends the merge.
	Into     string    `json:"into,omitempty"`
	MergedAt time.Time `json:"mergedAt,omitzero"`
	// Label is the name the user gave it on the Usage page (NameDeleted),
	// shown instead of Name.
	Label string `json:"label,omitempty"`
}

// Shown is the name the Usage page lists it by: the user's Label, its
// own name, or its id.
func (d Deleted) Shown() string {
	for _, n := range []string{d.Label, d.Name} {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return d.ID
}

// merged is the provider its calls go to now, "" when it is not merged:
// a provider deleted under Into since the merge ends it, as there is no
// one to show them under (its own calls are shown as deleted again).
func (d Deleted) merged(all []Deleted) string {
	if d.Into == "" || slices.ContainsFunc(all, func(l Deleted) bool { return l.ID == d.Into && l.At.After(d.MergedAt) }) {
		return ""
	}
	return d.Into
}

// UsageID is the id its calls are counted under: its id and when it was
// deleted, which no provider can take ("~" is never in one, Slug).
func (d Deleted) UsageID() string { return d.ID + "~" + d.At.UTC().Format("20060102T150405") }

// Renames say which provider a call recorded under a provider id was
// made to, as its id is now: a provider renamed takes the calls recorded
// under the ids it had (Was), and an id another provider took since
// (Gave) is that one's only from then on (cybercaius on Discord: a
// renamed provider's usage was shown under its old id and its new one).
type Renames struct {
	now  map[string]string
	gave map[string][]gaveTo
	// hosts are the providers by the host each is on, for magpie's own
	// requests to a provider being added, which has no id yet (ProbeOf)
	hosts map[string]string
	// sites are the providers by the site (SiteOf) of each host they are
	// on, "" for a site two are on: a model list asked at a provider's
	// bare domain before it was saved on its api. subdomain (OnSite)
	sites map[string]string
	// gone are the deleted providers by UsageID
	gone map[string]Deleted
	// into are the deleted providers merged into another (Deleted.Into),
	// the id their calls go to otherwise to the provider's; merged is
	// the other way, each provider's merged ones
	into   map[string]string
	merged map[string][]string
}

// Same is whether o is r itself, the one read UsageRenames hands back
// while providers.json is unchanged: the same maps, not equal ones. A
// reader that keeps what it made from r asks it to know r is unchanged.
func (r Renames) Same(o Renames) bool {
	at := func(m any) uintptr { return reflect.ValueOf(m).Pointer() }
	return at(r.now) == at(o.now) && at(r.gave) == at(o.gave) && at(r.hosts) == at(o.hosts) &&
		at(r.sites) == at(o.sites) && at(r.gone) == at(o.gone) && at(r.into) == at(o.into) &&
		at(r.merged) == at(o.merged)
}

type gaveTo struct {
	Until time.Time `json:"until"`
	To    string    `json:"to"`
}

// Of is the id the provider a call recorded under id at the time at has
// now; id itself when it was not renamed. A zero at is before every
// hand-over. A deleted provider the user merged into another is that
// one (Deleted.Into); Own is the id without the merge, which the call is
// priced by.
func (r Renames) Of(id string, at time.Time) string {
	shown, _ := r.Both(id, at)
	return shown
}

// Both is Of, and Own beside it: the provider a call is shown under and
// the one it is priced as. They differ only for a deleted provider merged
// into another, whose calls cost what they did before the merge.
func (r Renames) Both(id string, at time.Time) (shown, own string) {
	own = r.Own(id, at)
	if to, ok := r.into[own]; ok {
		return to, own
	}
	return own, own
}

// Own is Of without merges: the provider the call was made to, by the id
// it has now, a deleted one by its own (UsageID). What it cost is worked
// out under it, and what decides by who answered (the gateway's
// loggedAnswers, window holds) goes by it.
func (r Renames) Own(id string, at time.Time) string {
	for _, g := range r.gave[id] {
		if at.Before(g.Until) {
			return g.To
		}
	}
	if to, ok := r.now[id]; ok {
		return to
	}
	return id
}

// ProbeOf is Of for one of magpie's own requests (probelog): one sent
// while a provider was being added, its Detect or Test before it had an
// id, is recorded under its host, and is the provider's on that host now.
// yyddbds on Discord: 大灰狼, on dahuilang.best, was listed twice in the
// Requests page's providers, once as dahuilang.best for the model lists
// asked while adding it. A host two providers are on, or none, stays as
// it is.
func (r Renames) ProbeOf(id string, at time.Time) string {
	if to := r.hosts[strings.ToLower(id)]; to != "" {
		return to
	}
	return r.Of(id, at)
}

// OnHost is whether a provider saved now is on host, one or more.
func (r Renames) OnHost(host string) bool {
	_, ok := r.hosts[strings.ToLower(host)]
	return ok
}

// OnSite is the provider saved now on a host of host's site (SiteOf),
// for a probe recorded under a host no provider is on: CyberCaius on
// Discord, Leee's model list was asked at leee.baby before it was saved
// on another host of that site, and the Usage page listed leee.baby
// beside it. on is whether any provider is on that site; id is "" when
// two are, or host has no site.
func (r Renames) OnSite(host string) (id string, on bool) {
	site := SiteOf(host)
	if site == "" {
		return "", false
	}
	id, on = r.sites[site]
	return id, on
}

// SiteOf is the registrable domain of host (a URL's host, with or without
// its port), lowercased: api.leee.baby:443 is leee.baby. A host on a
// public suffix's own names (a.vercel.app is vercel.app's tenant a), an
// address or a name with no suffix (localhost) has none, "".
func SiteOf(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i+1:], "]") && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" || strings.ContainsAny(h, ":[]") || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return ""
	}
	site, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return ""
	}
	return site
}

// Gone is the deleted provider whose calls are counted under id: its
// own id while no provider has it, or its UsageID.
func (r Renames) Gone(id string) (Deleted, bool) {
	d, ok := r.gone[id]
	return d, ok
}

// Merged are the deleted providers merged into id, by the ids Gone
// knows them by, oldest deleted first.
func (r Renames) Merged(id string) []string { return r.merged[id] }

// Timed is whether the id's calls go to more than one provider by when
// they were made, so Of needs each call's time.
func (r Renames) Timed(id string) bool { return len(r.gave[id]) > 0 }

// Each is every id a call recorded under id can go to (Of), by when it
// was made.
func (r Renames) Each(id string) []string {
	out := []string{r.Of(id, time.Time{})}
	for _, g := range r.gave[id] {
		out = append(out, g.To)
	}
	out = append(out, r.Of(id, time.Now().Add(100*365*24*time.Hour)))
	slices.Sort(out)
	return slices.Compact(out)
}

// RenamesOf are renames of ids to the ones they are now, none given away
// (for a test).
func RenamesOf(now map[string]string) Renames { return Renames{now: now} }

// Empty is whether no id is renamed.
func (r Renames) Empty() bool { return len(r.now) == 0 && len(r.gave) == 0 && len(r.into) == 0 }

// Key is r as text, for a cache of what was worked out with it.
func (r Renames) Key() string {
	b, _ := json.Marshal([]any{r.now, r.gave, r.into})
	return string(b)
}

// lastRenames are the renames last read, kept for when providers.json
// can't be read: a failed read is not "nothing renamed", and the usage of
// every renamed provider would be shown split until it read again.
var lastRenames atomic.Pointer[Renames]

// UsageRenames are the renames of providers.json as it is now, worked
// out once each time the file changes and shared, so read only: each
// profile's in-use check on every state poll parsed the whole file again
// for them, 5 MB of forty polls with real accounts.
func UsageRenames() Renames {
	if r, err := filememo.Read("provider renames", Path(), func(b []byte) (Renames, error) {
		f, err := parseProviders(b)
		if err != nil {
			return Renames{}, err
		}
		return renamesOf(f.Providers, f.Deleted), nil
	}); err == nil {
		lastRenames.Store(&r)
		return r
	}
	// missing, or a file that doesn't decode: read's own way, its last
	// good generation
	f, err := read()
	if err != nil {
		if r := lastRenames.Load(); r != nil {
			return *r
		}
		return Renames{now: map[string]string{}}
	}
	r := renamesOf(f.Providers, f.Deleted)
	lastRenames.Store(&r)
	return r
}

func renamesOf(ps []Provider, deleted []Deleted) Renames {
	r := Renames{now: map[string]string{}}
	held := map[string]bool{}
	for _, p := range ps {
		held[p.ID] = true
	}
	give := func(id string, until time.Time, to string) {
		if r.gave == nil {
			r.gave = map[string][]gaveTo{}
		}
		r.gave[id] = append(r.gave[id], gaveTo{until, to})
	}
	for i, d := range deleted {
		// its calls stay under its id until a provider is added under it,
		// deleted since or there now
		to := d.ID
		if held[d.ID] || slices.ContainsFunc(deleted[i+1:], func(l Deleted) bool { return l.ID == d.ID || slices.Contains(l.Was, d.ID) }) {
			to = d.UsageID()
		}
		if r.gone == nil {
			r.gone = map[string]Deleted{}
		}
		r.gone[to] = d
		if into := d.merged(deleted); into != "" {
			if r.into == nil {
				r.into, r.merged = map[string]string{}, map[string][]string{}
			}
			r.into[to] = into
			r.merged[into] = append(r.merged[into], to)
		}
		if to != d.ID {
			give(d.ID, d.At, to)
		}
		for _, w := range d.Was {
			// the subscription's id is the subscription's (Rename)
			if !subscriptionID(w) {
				give(w, d.At, to)
			}
		}
		for _, g := range d.Gave {
			give(g.ID, g.Until, to)
		}
	}
	for _, p := range ps {
		for _, w := range p.Was {
			// the subscription's id is the subscription's (Rename)
			if !held[w] && !subscriptionID(w) {
				r.now[w] = p.ID
			}
		}
		for _, g := range p.Gave {
			give(g.ID, g.Until, p.ID)
		}
	}
	for _, gs := range r.gave {
		slices.SortStableFunc(gs, func(a, b gaveTo) int { return a.Until.Compare(b.Until) })
	}
	for _, p := range ps {
		if p.Hidden {
			continue
		}
		for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide} {
			h := HostOf(u)
			// a provider's id (one stored by hand can have a dot, b.ai)
			// is that provider's, never a host's
			if h == "" || held[h] {
				continue
			}
			if r.hosts == nil {
				r.hosts = map[string]string{}
			}
			if to, ok := r.hosts[h]; ok && to != p.ID {
				r.hosts[h] = "" // two providers on it: which one is unknown
				continue
			}
			r.hosts[h] = p.ID
		}
	}
	for h, id := range r.hosts {
		site := SiteOf(h)
		if site == "" {
			continue
		}
		if r.sites == nil {
			r.sites = map[string]string{}
		}
		if to, ok := r.sites[site]; ok && to != id {
			id = "" // two providers on it, or a host two are on
		}
		r.sites[site] = id
	}
	return r
}

// giveID moves id out of the Was of every provider of ps but ps[taker]
// (-1 for one not among them) into its Gave at now: from now on id is
// another's, and what was recorded under it before stays the one's that
// had it.
func giveID(ps []Provider, taker int, id string, now time.Time) {
	for j := range ps {
		if j == taker || !slices.Contains(ps[j].Was, id) {
			continue
		}
		ps[j].Was = slices.DeleteFunc(ps[j].Was, func(w string) bool { return w == id })
		ps[j].Gave = append(ps[j].Gave, Gave{ID: id, Until: now})
	}
}

// OnAccountIDs are the user's own providers saved on a subscription's id,
// which hide that subscription once it is signed in (All lists the one
// saved): made before the id was a subscription's, as a WorkBuddy key was
// before WorkBuddy's plan was one. MoveOffAccountIDs moves them.
func OnAccountIDs() []string {
	var out []string
	for _, p := range load().Providers {
		if slices.Contains(accountIDs, p.ID) && hasEndpoint(p) {
			out = append(out, p.ID)
		}
	}
	return out
}

// FreeID is id, or id-2, id-3… whichever no provider or subscription has.
func FreeID(id string) string { return freeID(id) }

// hasEndpoint: a provider of the user's, not a subscription's model picks.
// Vertex AI's has none of the four: it is asked at its project's address.
func hasEndpoint(p Provider) bool {
	return p.Chat != "" || p.Responses != "" || p.Anthropic != "" || p.Gemini != "" || p.Decide != "" || p.IsVertex()
}

// deletedAt is the index in f.Deleted of the deleted provider whose calls
// the Usage page lists under id (Renames.Gone).
func deletedAt(f *file, id string) (int, error) {
	d, ok := renamesOf(f.Providers, f.Deleted).gone[id]
	if !ok {
		return -1, fmt.Errorf("no deleted provider %q", id)
	}
	i := slices.IndexFunc(f.Deleted, func(l Deleted) bool { return l.ID == d.ID && l.At.Equal(d.At) })
	if i < 0 {
		return -1, fmt.Errorf("no deleted provider %q", id)
	}
	return i, nil
}

// MergeDeleted has the calls of the deleted provider the Usage page lists
// under id listed and counted as into's, a provider there is now, or, with
// into "", as its own again (CyberCaius on Discord, who had renamed a
// provider onto a deleted one's id to see their calls together). Nothing
// in the usage log is rewritten: the merge is kept on its Deleted record
// and read through Renames, its calls priced as they were. A provider
// added later under the deleted one's id is not merged.
func MergeDeleted(id, into string) error {
	into = strings.ToLower(strings.TrimSpace(into))
	f, err := read()
	if err != nil {
		return err
	}
	i, err := deletedAt(&f, id)
	if err != nil {
		return err
	}
	d := &f.Deleted[i]
	if into == "" {
		d.Into, d.MergedAt = "", time.Time{}
		return store(f)
	}
	if !slices.ContainsFunc(All(), func(p Provider) bool { return p.ID == into }) {
		return fmt.Errorf("no provider %q to merge into", into)
	}
	d.Into, d.MergedAt = into, time.Now()
	return store(f)
}

// NameDeleted names the deleted provider the Usage page lists under id
// as the user wants it shown there; "" shows it by its own name again.
func NameDeleted(id, name string) error {
	f, err := read()
	if err != nil {
		return err
	}
	i, err := deletedAt(&f, id)
	if err != nil {
		return err
	}
	f.Deleted[i].Label = strings.TrimSpace(name)
	if f.Deleted[i].Label == f.Deleted[i].Name {
		f.Deleted[i].Label = ""
	}
	return store(f)
}
