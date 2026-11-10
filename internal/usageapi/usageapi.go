// Package usageapi builds what magpie tells of its usage log: the Usage
// page's summary and its requests, a page at a time. The GUI's /api/usage
// and the gateway's /v1/magpie/usage answer the same JSON from here, so an
// app reading one reads the other.
package usageapi

import (
	"cmp"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Client is an agent as the usage JSON names it.
type Client struct{ ID, Name, Icon string }

// Clients are the agents magpie knows, for their names and logos: set by
// the GUI (internal/agent imports the gateway, which imports this). Unset,
// an agent is named by its id.
var Clients func() []Client

func clients() map[string]*Named {
	out := map[string]*Named{}
	if Clients == nil {
		return out
	}
	for _, c := range Clients() {
		out[c.ID] = &Named{ID: c.ID, Name: c.Name, Icon: c.Icon}
	}
	return out
}

// Tilde is p with the home folder written ~.
func Tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// Group is a usage.Group with what the UI needs to draw it.
type Group struct {
	usage.Group
	Name string `json:"name"`
	Sub  string `json:"sub,omitempty"`  // models: the provider's name
	Icon string `json:"icon,omitempty"` // a real logo, or "generic" for an unknown client
}

type Usage struct {
	usage.Summary
	Agents     []Group `json:"agents"`
	Models     []Group `json:"models"`
	Accounts   []Group `json:"accounts"`
	CallerKeys []Group `json:"callerKeys"`
	Path       string  `json:"path"`
}

// State is the Usage page's summary of a period: what /api/usage and the
// gateway's GET /v1/magpie/usage answer.
func State(p usage.Period) Usage { return stateOf(usage.Summarize(p)) }

// PageSessions is how many sessions Page lists: the Usage page draws none of
// them, and SessionCount says how many there are.
const PageSessions = 100

// Page is State with the top PageSessions sessions only, which is what the
// Usage page asks for: over a long history (Vantiboo1ean, ~750k requests)
// State sent the page every one of ~84k sessions (22 MB) and worked the
// summary out again for each refresh.
func Page(p usage.Period) Usage { return PageIn(p, nil) }

// PageIn is Page with the period's days and the chart's hours in zone, the
// reader's (ZoneOf); nil is this computer's.
func PageIn(p usage.Period, zone *time.Location) Usage {
	return stateOf(usage.SummarizeTopIn(p, PageSessions, zone))
}

// ZoneOf is the reader's time zone, which the Usage page names in ?tz= (its
// browser's IANA name, as Intl gives it) for its days and hours to be the
// reader's: magpie web may be read from another zone than the computer's
// (Outvi V on Discord). This computer's when it names none or one unknown
// here.
func ZoneOf(r *http.Request) *time.Location { return usage.Zone(r.URL.Query().Get("tz")) }

// FilterIn is FilterOf with the reader's zone, ZoneOf(r), which its Day and
// the period's days are read in.
func FilterIn(r *http.Request) usage.Filter {
	f := FilterOf(r.URL.Query())
	f.Zone = ZoneOf(r)
	return f
}

func stateOf(s usage.Summary) Usage {
	out := Usage{Summary: s, Agents: []Group{}, Models: []Group{}, Accounts: []Group{}, Path: Tilde(usage.Path())}
	agents := clients()
	for _, g := range s.Agents {
		ug := Group{Group: g, Name: g.ID, Icon: "generic"}
		if a := agents[g.ID]; a != nil {
			ug.Name, ug.Icon = a.Name, a.Icon
		}
		out.Agents = append(out.Agents, ug)
	}
	which := providerFaces(provider.All())
	for _, g := range s.Models {
		p := which(g.Provider)
		ug := Group{Group: g, Name: g.Model, Sub: p.Name, Icon: p.Icon}
		if g.Host != "" {
			ug.Sub += " · " + g.Host
		}
		out.Models = append(out.Models, ug)
	}
	// each subscription account's share, by the account that answered
	// (#557), named as the user named it now (provider.AccountShownOf,
	// plugins#310): the record keeps the vendor's name, which the user's
	// is kept by; Name "" is the calls whose record names none, the page
	// saying "account not recorded"
	for _, g := range s.Accounts {
		p := which(g.Provider)
		ug := Group{Group: g, Name: provider.AccountShownOf(g.Provider, g.Account), Sub: p.Name, Icon: p.Icon}
		out.Accounts = append(out.Accounts, ug)
	}
	out.CallerKeys = callerGroups(s)
	return out
}

// providerFaces names a provider id from the usage log as the Usage page
// shows it: by the provider set up under it (all), by the name magpie
// knows it by when none is (a Claude Code subscription signed out of, a
// preset's removed since: provider.KnownFace), and by the id itself only
// when neither knows it. A session file without route evidence is a local
// source, not a supplier.
func providerFaces(all []provider.Provider) func(id string) Named {
	set := map[string]provider.Provider{}
	for _, p := range all {
		set[p.ID] = p
	}
	renames := provider.UsageRenames()
	return func(id string) Named {
		a := Named{ID: id}
		if p, ok := set[id]; ok && p.Name != "" {
			a.Name, a.Icon = p.Name, p.Icon
		} else if d, ok := renames.Gone(id); ok {
			at := d.At
			a.Name, a.Icon, a.Deleted = d.Shown(), d.Icon, &at
		} else if id == usage.UnknownProvider {
			a.Name = "Local session"
		} else {
			a.Name, a.Icon = provider.KnownFace(id)
		}
		if a.Name == "" {
			a.Name = id
		}
		if a.Icon == "" {
			a.Icon = "generic"
		}
		// the deleted providers the user merged into it, so the page can
		// part them again
		for _, m := range renames.Merged(id) {
			if d, ok := renames.Gone(m); ok {
				a.Merged = append(a.Merged, Merged{ID: m, Name: d.Shown()})
			}
		}
		return a
	}
}

func callerGroups(s usage.Summary) []Group {
	keys := []Group{}
	current, _ := access.List()
	names := map[string]string{}
	for _, k := range current {
		names[k.ID] = k.Name
	}
	for _, g := range s.CallerKeys {
		n := names[g.CallerKeyID]
		if n == "" {
			n = g.CallerKeyName
		}
		if n == "" {
			n = g.CallerKeyID
		}
		keys = append(keys, Group{Group: g, Name: n, Icon: "generic"})
	}
	return keys
}

// inKeyOrder puts the Gateway key filter's keys in the order the user
// gave the keys on the Gateway page (alextjy on Discord), not by what each
// used: a key removed since, which the store no longer lists, goes after
// them, in the order it came. The Overview's list stays a ranking.
func inKeyOrder(groups []Group) []Group {
	current, err := access.List()
	if err != nil {
		return groups
	}
	place := make(map[string]int, len(current))
	for i, k := range current {
		place[k.ID] = i
	}
	at := func(g Group) int {
		if i, ok := place[g.CallerKeyID]; ok {
			return i
		}
		return len(current)
	}
	slices.SortStableFunc(groups, func(a, b Group) int { return at(a) - at(b) })
	return groups
}

// PeriodOf is the period a ?period= names: a preset, or days picked as
// "2026-10-01..2026-10-07" (#1492); 30 days for anything else.
func PeriodOf(s string) usage.Period {
	if p := usage.Period(s); p.Known() {
		return p
	}
	return usage.Month
}

// FilterOf is the filter a request's query names. A found routing group's
// model is taken as magpie keeps it (group/auto-…), as the menu offers it
// by its public auto/<model>, so either id picks the group's calls.
func FilterOf(q url.Values) usage.Filter {
	id, _ := strconv.ParseInt(q.Get("route"), 10, 64)
	return usage.Filter{Day: q.Get("day"), RouteID: id, Model: provider.CanonicalRef(q.Get("model")), Agent: q.Get("agent"), Provider: q.Get("provider"), Purpose: q.Get("purpose"), Account: q.Get("account"), CallerKey: q.Get("callerKey"), Failed: q.Get("failed") == "1", Retried: q.Get("retried") == "1", Query: q.Get("q"), Computer: q.Get("computer"), Through: viaOf(q.Get("via"))}
}

// viaOf is the source a request's ?via= names, "" for both.
func viaOf(s string) string {
	switch s {
	case usage.SourceGateway, usage.SourceSession, usage.SourceReported:
		return s
	}
	return ""
}

// Row is a usage.Row with the names the page shows it by.
type Row struct {
	usage.Row
	CallerKeyLabel string `json:"callerKeyLabel,omitempty"`
	AgentName      string `json:"agentName"`
	Icon           string `json:"icon"` // the agent's
	ProviderName   string `json:"providerName"`
	Access         string `json:"access,omitempty"` // known account/route type, independent of model maker
	PricingModel   string `json:"pricing_model,omitempty"`
	// ComputerName is the other computer a call was made on, shared through sync (#542)
	ComputerName string `json:"computerName,omitempty"`
	// Canceled: its agent canceled it before it was answered
	// (usage.Record.Canceled), which the page shows as no failure
	Canceled bool `json:"canceled,omitempty"`
}

type Ledger struct {
	CallerKeys []Group      `json:"callerKeys"`
	Period     usage.Period `json:"period"`
	Rows       []Row        `json:"rows"`
	Offset     int          `json:"offset"`
	Total      int          `json:"total"` // the rows the filter keeps, on every page
	// Series: the period by hour, day or week (Bucket), before the day filter
	Bucket string              `json:"bucket"`
	Series []usage.SeriesPoint `json:"series"`
	// By: the rows told apart by provider, agent, model and model at each
	// provider ("modelAt", named "model · provider"), the most tokens
	// first. The one by a dimension the filter has picked is of the rows
	// without that pick, so the others are still there to switch to.
	By      map[string][]Share `json:"by"`
	ChartBy map[string][]Share `json:"chartBy,omitempty"`
	Day     string             `json:"day,omitempty"`
	usage.Totals
	// Through and Direct: the rows the gateway served, and the rows read
	// from the agents' own session files, which it never saw. Totals is
	// the two together, so the page can show all three (#the usage report).
	Through usage.Totals `json:"through"`
	Direct  usage.Totals `json:"direct"`
	// Reported: the calls a client told magpie of through POST
	// /v1/magpie/usage/requests (#1874), which went straight to their vendor
	Reported usage.Totals `json:"reported"`
	// Agents, Providers and Purposes: those with calls in the period, for the filters
	Agents    []Named  `json:"agents"`
	Providers []Named  `json:"providers"`
	Purposes  []string `json:"purposes"`
	// Accounts: the subscription accounts that answered calls in the
	// period, for the Account filter (#557)
	Accounts []Account `json:"accounts"`
	// Computers: this one ("this", no name) and the others whose usage
	// sync brought (#542), for the filter; none while there are none
	Computers []Share `json:"computers,omitempty"`
}

// Share is a usage.Share with the name and logo the page shows it by.
type Share struct {
	usage.Share
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
}

// Account is a subscription account the Account filter offers: its
// name, as the record keeps it and the filter asks by, the name the user
// gave it in magpie when there is one (plugins#310), and the providers it
// answered for, by name.
type Account struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Providers []string `json:"providers"`
}

type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
	// Deleted is when the provider was deleted, for one whose calls are
	// kept apart from a provider's added under its id since
	Deleted *time.Time `json:"deleted,omitempty"`
	// Merged are the deleted providers whose calls the user merged into
	// this one (provider.MergeDeleted), by the ids they are kept apart by
	Merged []Merged `json:"merged,omitempty"`
}

// Merged is a deleted provider merged into another.
type Merged struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LedgerPage is one page of the ledger: limit rows (100 when none is
// given, 500 at most) from offset.
func LedgerPage(p usage.Period, f usage.Filter, offset, limit int) Ledger {
	l := usage.QueryPage(p, f, offset, limit)
	offset = max(0, min(offset, l.Total))
	page := l.Rows
	agents := clients()
	all := provider.All()
	which := providerFaces(all)
	access := map[string]string{}
	for _, pr := range all {
		if pr.Account != nil {
			access[pr.ID] = "subscription"
		} else if pr.Key != "" {
			access[pr.ID] = "api"
		}
	}
	who := func(id string) Named {
		if a := agents[id]; a != nil {
			return Named{ID: id, Name: a.Name, Icon: a.Icon}
		}
		return Named{ID: id, Name: id, Icon: "generic"}
	}
	out := Ledger{Period: p, Rows: make([]Row, 0, len(page)), Offset: offset, Total: l.Total, Totals: l.Sum, Agents: []Named{}, Providers: []Named{}, Accounts: []Account{}}
	out.Through, out.Direct, out.Reported = l.Through, l.Direct, l.Reported
	out.Bucket, out.Series = l.Bucket, l.Series
	out.Purposes = l.Purposes
	out.Day = f.Day
	out.CallerKeys = inKeyOrder(callerGroups(usage.Summary{CallerKeys: l.CallerKeys}))
	callerLabels := map[string]string{}
	for _, g := range out.CallerKeys {
		callerLabels[g.CallerKeyID] = g.Name
	}
	// a group magpie found is named as agents pick it, auto/<model>; the
	// ledger keeps group/auto-<slug> (provider.CanonicalRef)
	pub := provider.PublicRefs()
	for _, r := range page {
		a := who(r.Agent)
		lr := Row{Row: r, AgentName: a.Name, Icon: a.Icon, ProviderName: which(r.Provider).Name, Canceled: r.Canceled()}
		if pub != nil {
			lr.Model, lr.Requested = pub(r.Model), pub(r.Requested)
		}
		if r.Source != "log" && r.Source != usage.SourceReported {
			lr.Access = access[r.Provider]
		}
		if r.Priced && r.OwnCost == nil {
			if model := provider.PricedNameFor(r.Provider, r.Model); model != r.Model {
				lr.PricingModel = model
			}
		}
		lr.CallerKeyLabel = callerLabels[r.CallerKeyID]
		if r.Computer != "" {
			lr.ComputerName = l.Names[r.Computer]
			if lr.ComputerName == "" {
				lr.ComputerName = r.Computer
			}
		}
		out.Rows = append(out.Rows, lr)
	}
	for _, id := range l.Agents {
		out.Agents = append(out.Agents, who(id))
	}
	for _, id := range l.Providers {
		out.Providers = append(out.Providers, which(id))
	}
	// one choice per account, most used first, with the providers it
	// answered for: the same email may be a Codex and a Claude account
	at := map[string]int{}
	for _, g := range l.Accounts {
		name := which(g.Provider).Name
		own := provider.AccountLabelOf(g.Provider, g.Account)
		if i, ok := at[g.Account]; ok {
			a := &out.Accounts[i]
			if !slices.Contains(a.Providers, name) {
				a.Providers = append(a.Providers, name)
			}
			a.Name = cmp.Or(a.Name, own)
			continue
		}
		at[g.Account] = len(out.Accounts)
		out.Accounts = append(out.Accounts, Account{ID: g.Account, Name: own, Providers: []string{name}})
	}
	// what each is of: the rows of the filter, or, for the dimension the
	// filter has picked, of the rows without that pick
	sharesJSON := func(by map[string][]usage.Share) map[string][]Share {
		out := map[string][]Share{}
		for _, d := range usage.Dimensions {
			shares := []Share{}
			for _, s := range by[d] {
				ls := Share{Share: s, Name: s.ID}
				switch d {
				case "provider":
					a := which(s.ID)
					ls.Name, ls.Icon = a.Name, a.Icon
				case "agent":
					a := who(s.ID)
					ls.Name, ls.Icon = a.Name, a.Icon
				case "modelAt":
					// the model, and the provider it went to
					prov, model, _ := strings.Cut(s.ID, "/")
					a := which(prov)
					ls.Name, ls.Icon = model+" · "+a.Name, a.Icon
				case "callerKey":
					// the key's own name, as the key filter says it; the
					// GUI translates the name of the calls with none
					ls.Name, ls.Icon = callerLabels[s.ID], "generic"
					if s.ID == usage.NoCallerKey {
						ls.Name = "No gateway key"
					} else if ls.Name == "" {
						ls.Name = s.ID
					}
				}
				shares = append(shares, ls)
			}
			out[d] = shares
		}
		return out
	}
	out.By = sharesJSON(l.By)
	if l.ChartBy != nil {
		out.ChartBy = sharesJSON(l.ChartBy)
	}
	for _, s := range l.Computers {
		ls := Share{Share: s, Name: l.Names[s.ID]}
		if s.ID != usage.ThisComputer && ls.Name == "" {
			ls.Name = s.ID
		}
		out.Computers = append(out.Computers, ls)
	}
	return out
}

// OwnPage is l, a page filtered to the gateway key key's own calls, with
// its filter lists cut to what those calls had: they are drawn from every
// call in the period, and so would name the other keys, and the agents,
// providers and accounts only someone else's calls reached. The split by
// key is counted without the key's pick, so it is cut to the key's own
// share too, or it would tell a holder every other key's calls (#2521).
func OwnPage(l Ledger, key string) Ledger {
	l.CallerKeys = slices.DeleteFunc(l.CallerKeys, func(g Group) bool { return g.CallerKeyID != key })
	for _, by := range []map[string][]Share{l.By, l.ChartBy} {
		if by != nil {
			by["callerKey"] = slices.DeleteFunc(by["callerKey"], func(s Share) bool { return s.ID != key })
		}
	}
	had := func(d string) map[string]bool {
		m := map[string]bool{}
		for _, s := range l.By[d] {
			m[s.ID] = true
		}
		return m
	}
	agents, providers := had("agent"), had("provider")
	l.Agents = slices.DeleteFunc(l.Agents, func(a Named) bool { return !agents[a.ID] })
	l.Providers = slices.DeleteFunc(l.Providers, func(a Named) bool { return !providers[a.ID] })
	accounts := map[string]bool{}
	for _, r := range l.Rows {
		accounts[r.Account()] = true
	}
	l.Accounts = slices.DeleteFunc(l.Accounts, func(a Account) bool { return !accounts[a.ID] })
	return l
}
