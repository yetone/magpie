package provider

// Routing groups: several models, from one provider or many, that an agent
// picks as one. The gateway serves a group's id like any model's and
// routes each request over every member's keys or accounts together — a
// subscription whose allowance renews soonest first across providers, one
// resting after a failure last — rather than over one provider's.
//
// The user makes groups, in the Routing view. magpie also finds some on
// its own: a model more than one provider serves under the same name is a
// group of those, derived each time and never stored until the user
// changes one. Models of different names are only ever grouped by the
// user — in a group of their own, or by saying in a provider's Names &
// levels that one is the same as another (settings' ModelSameAs): nothing
// here guesses which models are alike beyond how vendors spell one id.
//
// A group's member may be another group ("group/<id>"): to the group it is
// one member, which a rule can put first like a model; its models are its
// own group's, ordered by its own routing, rules and Sink. A group can't
// be in itself, however deep: SaveGroup refuses the loop, and one written
// into providers.json by hand is cut where it closes.

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/settings"
)

// GroupPrefix starts a group's id in the catalog: "group/<id>".
const GroupPrefix = "group/"

// GroupIDOf is the group id a ref names ("group/<id>", or a found group's
// "auto/<model>": CanonicalRef), with spaces and the [1m] mark Claude Code
// puts on a 1M window taken off, as an agent's own settings carry it. ok is
// false for a ref that isn't a group's.
func GroupIDOf(ref string) (string, bool) {
	return strings.CutPrefix(CanonicalRef(strings.TrimSuffix(strings.TrimSpace(ref), "[1m]")), GroupPrefix)
}

// Affinities are how long a conversation stays with the key or account that
// answered it: "" auto, as long as what the vendor cached of it is worth
// keeping; for the whole session; within a turn only, free to move when
// the user speaks again; never; or strictly: for the whole session, never
// moved to another key or account however it answers (#1797).
var Affinities = []string{"", AffinitySession, AffinityTurn, AffinityOff, AffinityStrict}

const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
	// AffinityStrict keeps a conversation on the key or account that
	// answered it whatever happens: resting, all but used up or past the
	// usage cap set on it, it is still the one asked, and its error goes
	// to the agent rather than the conversation to another. A usage cap
	// still keeps new conversations off it.
	AffinityStrict = "strict"
)

// EffortAuto is a group whose classifier picks each turn's reasoning
// effort (Group.Effort).
const EffortAuto = "auto"

// Ruled reports whether the group decides anything as a user's turn
// begins: a rule to put a member first, or the turn's effort. A pause
// rule decides nothing then (PausedOut is looked at on every request).
func (g Group) Ruled() bool {
	return slices.ContainsFunc(g.Rules, func(r Rule) bool { return !r.Pause }) || g.Effort == EffortAuto
}

// Manual is a group's routing when the user picks which member it uses,
// as CC Switch has one provider on at a time (#317): every request goes to
// the member picked (Group.Pick), over its own keys or accounts; the
// others, and the rules, wait until another is picked or the group is
// routed otherwise. The pick is changed on the group's card, in a click.
const Manual = "manual"

// Picked is the member a manual group sends to: the one the user picked
// while it is in the group, else its first.
func (g Group) Picked() string {
	if slices.Contains(g.Members, g.Pick) {
		return g.Pick
	}
	if len(g.Members) > 0 {
		return g.Members[0]
	}
	return ""
}

// routes are the members requests to the group may go to now: a manual
// group's pick alone, else every one not switched off.
func (g Group) routes() []string {
	if g.Routing == Manual {
		if p := g.Picked(); p != "" {
			return []string{p}
		}
		return nil
	}
	return slices.DeleteFunc(slices.Clone(g.Members), g.IsOff)
}

// IsOff is whether the group's member id is switched off.
func (g Group) IsOff(id string) bool { return slices.Contains(g.Off, id) }

// Live is the group as the gateway routes it: a manual group's rules
// wait (the member picked is all it sends to), though they are kept.
func (g Group) Live() Group {
	if g.Routing == Manual {
		g.Rules = nil
	}
	return g
}

// Within is g, a group in the group in, as in routes it: one left on the
// default (Smart) is routed as in is, as a provider left on the default
// is weighed with the group it is in (ownRouting, #3847, mycf_711) —
// Free first on the outer group reaches the seats of a group in it that
// says nothing of its own. One that sets a routing keeps it (#576), and
// so does every one in a group In order, By weight or Manual: those try
// each member whole, by its own routing, as they do a provider's keys.
func (g Group) Within(in Group) Group {
	if g.Routing == "" && pools(in.Routing) {
		g.Routing = in.Routing
	}
	return g
}

// pools: a group routed so weighs its members' keys and accounts as one
// pool, a provider left on the default among them.
func pools(routing string) bool {
	return routing != "" && routing != Ordered && routing != Weighted && routing != Manual
}

// PatienceOff, PatienceDefault and PatienceLongest are a group's
// Patience: none, the seconds a group waits that hasn't set it, and the
// most it may.
const (
	PatienceOff     = -1
	PatienceDefault = 60
	PatienceLongest = 240
)

// Waits is how long a request to the group may be kept for its members to
// come back (Patience), 0 for not at all. A group that sets none follows
// the default for every group (settings' GroupPatience, jiezhengj on
// Discord), PatienceDefault while that is unset too.
func (g Group) Waits() time.Duration {
	n := g.Patience
	if n == 0 {
		n = settings.Shared().GroupPatience
	}
	switch {
	case n < 0:
		return 0
	case n == 0:
		return PatienceDefault * time.Second
	}
	return time.Duration(min(n, PatienceLongest)) * time.Second
}

// FirstTokenWait is a group's FirstToken when the user picked Wait for it
// itself: every member waited for, whatever the default for every group.
// 0 is a group that sets nothing, which follows that default.
const FirstTokenWait = -1

// FirstTokenOr is the seconds the group lets a member take to its first
// content: its own FirstToken, else the default for every group
// (settings' GroupFirstToken); 0 or less waits as long as it takes.
func (g Group) FirstTokenOr() int {
	if g.FirstToken != 0 {
		return g.FirstToken
	}
	return settings.Shared().GroupFirstToken
}

// Sinks is whether one of the group's accounts or keys rate limited with
// quota left goes to the back (Sink): the group's own word, Sink or
// NoSink, else the default for every group (settings' GroupSink). Whether
// its routing can sink at all is the gateway's to say.
func (g Group) Sinks() bool {
	return g.Sink || !g.NoSink && settings.Shared().GroupSink
}

// ContextSmallest is a group's Context when agents are told the window of
// its smallest member.
const ContextSmallest = -1

// Group is a routing group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Note is the user's own word on the group (ConatusMoon2222 on X:
	// which key it is for, why it is ordered so): one line, shown under
	// its name on the Routing page and by magpie group, never sent to an
	// agent or a vendor. Empty shows nothing (CleanNote).
	Note    string   `json:"note,omitempty"`
	Members []string `json:"members"` // "provider/model[:effort]" or "group/<id>", in order (see MemberEffort)
	// Former are the ids it had before a rename (RenameGroup), newest
	// last: a request still asking group/<old> is routed to it, until a
	// group takes that id for its own (xinvxueyuan on Discord).
	Former []string `json:"former,omitempty"`
	// Match are patterns its models are found by as well (#766): a glob
	// over the "provider/model" id with * in it ("openrouter/*:free"), or
	// a regular expression after "re:". Kept apart from Members, so the
	// group follows the catalog: read, it has the models they match now
	// after those it names (see withMatches).
	Match []string `json:"match,omitempty"`
	// Matched are the members the patterns match now, as Members has them
	// after its own. Derived each time the group is read, never stored.
	Matched []string `json:"matched,omitempty"`
	// Placed is the order the user put the members in, those the
	// patterns match among those it names (usrname1833 on Discord), as
	// Members spells them. Read, the members follow it: a model matched
	// since comes after them in the catalog's order, and one no longer
	// matched drops out (placedIn). Empty when it is the order the group
	// has unasked, its own members first, then the matched. Saved, it
	// places the matched; the named go in Members' order (placedOver).
	Placed   []string `json:"placed,omitempty"`
	Routing  string   `json:"routing,omitempty"`  // as Provider.Routing, over all the members' keys and accounts; or Manual
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Sink is Provider.Sink over the group's members' accounts and keys
	// together: one rate limited with quota left goes behind every other
	// member's. Not for "rotate" or Manual.
	Sink bool `json:"sink,omitempty"`
	// NoSink is the user's Keeps its place for the group itself: none
	// sinks, whatever the default for every group says (Sinks). Neither
	// set, the group follows that default.
	NoSink bool `json:"noSink,omitempty"`
	// FirstToken is how many seconds a member's streamed reply may take to
	// its first content — text, reasoning or a tool call — before the next
	// member is asked instead, nothing of it having reached the agent;
	// FirstTokenWait waits as long as it takes, and 0 follows the default
	// for every group (FirstTokenOr). The last one left is always waited
	// for, and the slow one doesn't rest.
	FirstToken int `json:"firstToken,omitempty"`
	// Patience is how many seconds more a request may be kept, once every
	// member has failed and nothing of a reply has been sent, for the
	// members that failed in a way that passes —
	// busy, overloaded, rate limited for a moment — to be asked again
	// after a pause, in turn, until one answers (#1418). 0 follows the
	// default for every group, PatienceDefault while that is unset (Waits);
	// PatienceOff gives the agent the last one's error once each has been
	// asked.
	Patience int `json:"patience,omitempty"`
	// Off are the members switched off: kept where they are in the
	// order, with their rules, but sent nothing until switched on again,
	// so trying a group without one doesn't mean taking it out.
	Off []string `json:"off,omitempty"`
	// Pick is the member a Manual group sends every request to, as the
	// user picked it on the group's card; "" is its first. It is kept
	// while the group routes otherwise, for when it is manual again.
	Pick string `json:"pick,omitempty"`
	// Rules send the requests they match to one member first, in order:
	// the first that matches decides (see Rule).
	Rules []Rule `json:"rules,omitempty"`
	// Classifier is the model ("provider/model") asked which of the rules'
	// intents a user's message is, or a group ("group/<id>", never this
	// one) whose models are asked in turn, failing over as for any request.
	// Rules with an intent need one; a small, fast model without reasoning
	// does.
	Classifier string `json:"classifier,omitempty"`
	// IntentSure is how confident a decision model (Jev, from TypeSafe's
	// decision API) must be of an intent of this group's for it to count:
	// from 0, its likeliest intent taken however unsure it is ("none of
	// these" never counting), to 1, certain. nil asks 0.4, as groups have
	// always asked (#5101).
	IntentSure *float64 `json:"intentSure,omitempty"`
	// Effort "auto" has the classifier — Jev, from TypeSafe's decision API,
	// or any model, asked in words — judge how hard each turn is to think
	// about as it begins, and the turn asks its model for that much
	// reasoning, where the agent asked for some (see EffortAuto).
	Effort string `json:"effort,omitempty"`
	// Context is how long a request the user says the group takes, in
	// tokens: agents are told it rather than its largest member's.
	// ContextSmallest tells them its smallest member's, whichever that is
	// as the members change (Mikan on Discord), so the agent compacts
	// before any member would turn the conversation away.
	Context int `json:"context,omitempty"`
	// Compact is where Codex and Claude Code compact a conversation on the
	// group, in tokens (NovaStudiosss on X): over what its members' own
	// thresholds (settings.ModelCompacts) and Long conversations say, so a
	// group carries both its window (Context) and its compaction and no
	// provider needs either set for it. 0 leaves it to those.
	Compact int `json:"compact,omitempty"`
	// Levels are the reasoning levels agents are offered for the group,
	// lowest first, when the user names them (#295): rather than those
	// every member has, so that a member with few doesn't take the rest
	// from the others. A member without the level a request asks for is
	// sent the one it has nearest, as ever. Empty offers the members'
	// shared levels.
	Levels []string `json:"levels,omitempty"`
	// Fast are the members (as Members spells them) sent in their
	// vendor's fast mode, where the model has one (see CanFast). Kept
	// beside Members, not in their ids, so a version before it still
	// routes to them, only not fast.
	Fast []string `json:"fast,omitempty"`
	// Ultra are the members of Fast sent in the vendor's faster mode
	// still (Codex's Ultra Fast, kdajenks on X), where the account
	// offers it and priority where not. Always a part of Fast.
	Ultra []string `json:"ultra,omitempty"`
	// Weights are the members' shares (as Members spells them) when the
	// group routes by weight (Weighted, #1910): one weighing 3 takes three
	// requests for every one a member weighing 1 takes. One not in it
	// weighs 1. Kept while the group routes otherwise, for when it is by
	// weight again.
	Weights map[string]int `json:"weights,omitempty"`
	// EffortMaps are, for the members (as Members spells them) that
	// follow the group's effort, the level each sends in place of the one
	// asked for ("high" → "xhigh"): see groupeffortmap.go. A level not in
	// a member's map is sent as asked.
	EffortMaps map[string]map[string]string `json:"effortMaps,omitempty"`
	// Family is a tag the group goes by in which agents are shown it
	// (settings' Visible), with its id.
	Family string `json:"family,omitempty"`
	// Unlists keeps the models the group names, or its patterns match,
	// out of the lists agents and the gateway's model list show (#1783):
	// they are offered through the group alone, as a provider kept for
	// routing groups offers its own (Provider.Unlisted), and a request
	// naming one is still served. A group in it keeps its own, as it
	// says. Not while the group is switched off.
	Unlists bool `json:"unlists,omitempty"`
	// Accounts hold the group to some accounts or keys of the providers
	// they name (duanckham on X: a personal and a company GPT account,
	// each group's tasks on its own), as a gateway key's account list
	// does (#905) and in its form, "<provider>/<account stable id or key
	// fingerprint>": a request through the group is sent only on those
	// of a provider it names, a provider it names none of on every
	// account it has. Empty, every account of every member serves.
	Accounts []string `json:"accounts,omitempty"`
	// Keys bind the group to some gateway keys (access.Key.ID; Xerxes754
	// on X: a group for one or two keys alone). Set, a request with
	// another key may neither use the group nor see it in a model list,
	// whatever models that key may use; a key issued under one of them
	// may, as it may what its issuer may. A request with no key (this
	// computer's own) is held by no key and passes. Empty, every key the
	// group is given passes, as before. Saved, nil keeps the group's
	// (SaveGroup), so a caller that doesn't know of them leaves them be;
	// empty clears them.
	Keys []string `json:"keys,omitempty"`
	// Redact is whether a request through the group is masked (1dwsdqwd2
	// on X: some groups masked, some not): "" follows each member's
	// provider ("Send requests unmasked") and Settings, RedactOn masks it
	// as Settings say whichever provider, RedactOff sends it as written.
	// It is the user's alone: Settings with masking off mask nothing, a
	// shared group carries none, and in a group inside a group any RedactOn
	// among them wins over RedactOff. Saved, "" keeps the group's
	// (SaveGroup) and RedactFollow clears it, so a caller that doesn't
	// know of it leaves it be.
	Redact string `json:"redact,omitempty"`
	// Auto is set on a group magpie found: one model served by several
	// providers. It is derived, never stored.
	Auto bool `json:"auto,omitempty"`
	// Spelled is how a vendor spells the model of a group magpie found
	// (auto-…), which agents are given it by: auto/<Spelled> (Ref). It is
	// derived from the members each time the groups are read, never stored.
	Spelled string `json:"-"`
	// Hidden is stored for a found group the user removed.
	Hidden bool `json:"hidden,omitempty"`
	// Seen is stored for a found group the user changed (an auto-… id):
	// the models, as "provider/model", magpie had found in it by the
	// user's last save, and those the user named. A provider that serves
	// the model since joins the group as it is read (joined), as it joins
	// one magpie finds; one seen and not a member is one the user took
	// out, and stays out. Empty, the group was changed before magpie
	// kept this, and gets no member it doesn't name (Kxsg79bEYFP4 on X).
	Seen []string `json:"seen,omitempty"`
	// Disabled is a group of the user's switched off (PAMI on Discord):
	// kept on the Routing page as it is, but not offered to agents, a
	// request to it turned away with a word of why, and skipped where
	// another group has it in it, until switched on again (SwitchGroup).
	// Only SwitchGroup changes it: a save keeps it as it was.
	Disabled bool `json:"disabled,omitempty"`
}

// Member is one of a group's models as it resolves now.
type Member struct {
	ID string // the group's member it is: the model, or the group in the group it is of
	// Path is the members from the group's own down to the model: [ID] for
	// a model the group names itself, ["group/fast", "a/m"] for one of
	// its group fast, and so on down.
	Path []string
	// Via are the groups in the group it is of, the outermost first: the
	// group each of Path's members but the last names.
	Via []Group
	// Redact is what the groups it is reached through say of masking, the
	// group itself and each in Via (Group.Redact): RedactOn if any says so,
	// else RedactOff if any does, else "".
	Redact   string
	Provider Provider
	Model    string // what the vendor is asked for
	// Effort is the reasoning the member is fixed at ("provider/model:low"),
	// asked of the model whatever the agent or the group's classifier
	// asked; "" follows the group.
	Effort string
	// Levels are the reasoning levels the member follows the agent among
	// ("provider/model:low+high"), lowest first: the agent's effort is
	// sent at the nearest of them. nil for a member fixed at one (Effort)
	// or following the group.
	Levels []string
	// EffortMap is, for a member following the group's effort, the level
	// it sends in place of the one asked for (Group.EffortMaps); nil
	// sends it as asked.
	EffortMap map[string]string
	// Fast is set on a member the group sends in its vendor's fast mode
	// (Group.Fast), where its model has one.
	Fast bool
	// Ultra is set on a Fast member the group sends in the faster mode.
	Ultra bool
	// Account is the one account or key of Provider the member is sent
	// on, "<provider>/<stable id>", as Group.Accounts has them
	// ("provider/model#<id>", groupaccount.go); "" for every one the
	// groups it is in allow.
	Account string
	// At is where the walk down the group met it, counting every model of
	// every group in it, the same model's later places too: a group's own
	// models are met in its own order.
	At int
	// Also are the other places further down the walk that have the same
	// model at the same effort, which the group asks once, where it was
	// met first: a rule naming a later group in the group still has it,
	// in that group's own order (#2361).
	Also []Member
}

// In is the member as the group's member id has it: itself, where it was
// met further down the walk (Also), or, for a model, where a group in the
// group ahead of it has it too (#625). ok is false when id isn't one of
// its places.
func (m Member) In(id string) (Member, bool) {
	if m.ID == id {
		return m, true
	}
	for _, o := range m.Also {
		if o.ID == id {
			return o, true
		}
	}
	if !strings.HasPrefix(id, GroupPrefix) && len(m.Path) > 1 && m.Path[len(m.Path)-1] == id {
		return m, true
	}
	return Member{}, false
}

// Homed is the member as the place under the members path names (the
// group's member ids from the top) has it, its others in Also; ok is
// false when none of its places is there.
func (m Member) Homed(path []string) (Member, bool) {
	under := func(o Member) bool { return len(o.Path) > len(path) && slices.Equal(o.Path[:len(path)], path) }
	if under(m) {
		return m, true
	}
	for i, o := range m.Also {
		if under(o) {
			o.Also = append(append([]Member{m.only()}, m.Also[:i]...), m.Also[i+1:]...)
			return o, true
		}
	}
	return Member{}, false
}

// only is the member without its other places.
func (m Member) only() Member {
	m.Also = nil
	return m
}

// Groups are the ids of the groups in the group the model is of, the
// outermost first.
func (m Member) Groups() []string {
	out := make([]string, len(m.Via))
	for i, g := range m.Via {
		out[i] = g.ID
	}
	return out
}

// Below is the member as the group at depth (0 the group itself, 1 the
// group in it Path[0] names, …) has it.
func (m Member) Below(depth int) Member {
	out := Member{ID: m.Path[depth], Path: m.Path[depth:], Via: m.Via[depth:], Redact: m.Redact, Provider: m.Provider, Model: m.Model, Effort: m.Effort, Levels: m.Levels, EffortMap: m.EffortMap, Fast: m.Fast, Ultra: m.Ultra, Account: m.Account, At: m.At}
	// its other places in the same group at depth
	for _, o := range m.Also {
		if len(o.Path) > depth && slices.Equal(o.Path[:depth], m.Path[:depth]) {
			out.Also = append(out.Also, o.Below(depth))
		}
	}
	return out
}

// maxNest is how deep groups in groups may go.
const maxNest = 8

// Groups lists the user's groups, then those magpie found, hidden ones
// too (marked so), in the order the user put them in (SetGroupOrder) where
// they did: the Routing page, the gateway's model list, the CLI and the
// TUI all list them so.
func Groups() []Group {
	return groupsIn(providerEntries())
}

// groupsIn is the groups among entries (Groups), built once while a
// request holds the catalog: a request looked its group up by building
// every group again, matching each pattern and finding the automatic
// groups over every model (FindGroup, then the groups' entries). Each
// caller gets a copy of its own to change.
func groupsIn(entries []Entry) []Group {
	return cloneGroups(heldFrom("groups", entries, buildGroupsIn))
}

// sharedGroups is Groups for a caller that only reads them: no copy, so
// nothing it is given may be changed. PublicRef, asked for every model a
// page lists, copied every group and its rules each time (115 MB of a live
// magpie's 2.6 GB in a minute and a half).
func sharedGroups() []Group {
	return heldFrom("groups", providerEntries(), buildGroupsIn)
}

// sharedGroupsIn is groupsIn without the copy, for a caller that only
// reads them: a group found in it is copied alone (cloneGroup). A routing
// group's lookup (FindGroup, GroupFinder) copied every group and its
// rules for the one it wanted, each time: 4 MB of the window's 15-second
// drift poll, and once per request on a group.
func sharedGroupsIn(entries []Entry) []Group {
	return heldFrom("groups", entries, buildGroupsIn)
}

// cloneGroup is g with nothing shared with it (cloneGroups).
func cloneGroup(g Group) Group { return cloneGroups([]Group{g})[0] }

// GroupsBound says some routing group is bound to gateway keys
// (Group.Keys), read from the groups held for the catalog, no copy made:
// a model list for a key no group binds needs no other look.
func GroupsBound() bool {
	return slices.ContainsFunc(sharedGroups(), func(g Group) bool { return len(g.Keys) > 0 })
}

// cloneGroups is gs with nothing shared with it: no slice, map or pointer
// of a group or its rules.
func cloneGroups(gs []Group) []Group {
	if gs == nil {
		return nil
	}
	out := make([]Group, len(gs))
	for i, g := range gs {
		g.Members, g.Match, g.Matched, g.Placed = slices.Clone(g.Members), slices.Clone(g.Match), slices.Clone(g.Matched), slices.Clone(g.Placed)
		g.Off, g.Levels, g.Fast, g.Ultra = slices.Clone(g.Off), slices.Clone(g.Levels), slices.Clone(g.Fast), slices.Clone(g.Ultra)
		g.Accounts, g.Seen, g.Keys = slices.Clone(g.Accounts), slices.Clone(g.Seen), slices.Clone(g.Keys)
		g.Former = slices.Clone(g.Former)
		g.Weights = maps.Clone(g.Weights)
		g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string { return m })
		if g.IntentSure != nil {
			v := *g.IntentSure
			g.IntentSure = &v
		}
		if g.Rules != nil {
			rs := make([]Rule, len(g.Rules))
			for j, r := range g.Rules {
				r.Agents, r.Folders, r.ExceptFolders, r.More = slices.Clone(r.Agents), slices.Clone(r.Folders), slices.Clone(r.ExceptFolders), slices.Clone(r.More)
				r.Where = slices.Clone(r.Where)
				if r.Time != nil {
					t := *r.Time
					t.Days = slices.Clone(t.Days)
					r.Time = &t
				}
				rs[j] = r
			}
			g.Rules = rs
		}
		out[i] = g
	}
	return out
}

// groupsBuilt counts buildGroupsIn, for the test that a held request
// builds the groups once.
var groupsBuilt atomic.Int64

func buildGroupsIn(entries []Entry) []Group {
	groupsBuilt.Add(1)
	f := heldOf("file", load)
	var out []Group
	hidden := map[string]bool{}
	var have map[string]bool // the providers magpie has, switched off or not
	var found []Group        // the groups magpie finds, for a changed one to follow
	if !f.NoAutoGroups {
		found = foundGroups(entries)
	}
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		if strings.HasPrefix(g.ID, "auto-") {
			if have == nil {
				have = map[string]bool{}
				for _, p := range All() {
					have[p.ID] = true
				}
			}
			g = joined(withoutRemoved(g, have), found, entries)
		}
		out = append(out, withMatches(entries, g.own()))
	}
	if !served(f.Groups) { // plugins/groups is off: the user's own aren't served
		out = slices.DeleteFunc(out, func(g Group) bool { return !g.Auto })
	}
	if !f.NoAutoGroups {
		for _, g := range found {
			if slices.ContainsFunc(out, func(o Group) bool { return o.ID == g.ID }) {
				continue // the user changed it: theirs now
			}
			g.Hidden = hidden[g.ID]
			out = append(out, g)
		}
	}
	same := heldSettings().ModelSameAs
	for i := range out {
		out[i].Spelled = spelledIn(out[i], same)
	}
	return orderedGroups(out, f.GroupOrder)
}

// withoutRemoved is a found group the user changed (an "auto-…" id kept
// in providers.json) without the members of providers magpie no longer
// has (have): a provider removed leaves the group as it leaves one magpie
// finds, where they had stayed for the user to take out by hand (Discord).
// It is how the group is read, not stored, so a provider back under its
// id (an account shown again) is back in it. A member the user picked a
// manual group's requests to, or a rule sends to, stays: taken out, the
// group would send elsewhere unasked, or not save. So does every member
// when none would be left. A provider switched off keeps its members: the
// gateway skips them (membersIn) and they are sent to again once it is on.
func withoutRemoved(g Group, have map[string]bool) Group {
	gone := func(m string) bool {
		if strings.HasPrefix(m, GroupPrefix) || IsPattern(m) {
			return false
		}
		pid, _, ok := strings.Cut(m, "/")
		if !ok || have[pid] || (g.Routing == Manual && m == g.Picked()) {
			return false
		}
		return !slices.ContainsFunc(g.Rules, func(r Rule) bool { return r.Names(m) })
	}
	if !slices.ContainsFunc(g.Members, gone) {
		return g
	}
	kept := slices.DeleteFunc(slices.Clone(g.Members), gone)
	if len(kept) == 0 && len(g.Match) == 0 {
		return g
	}
	g.Members = kept
	g.Off = slices.DeleteFunc(slices.Clone(g.Off), gone)
	g.Fast = slices.DeleteFunc(slices.Clone(g.Fast), gone)
	g.Ultra = slices.DeleteFunc(slices.Clone(g.Ultra), gone)
	g.Weights = renamedWeights(g.Weights, func(m string) string {
		if gone(m) {
			return ""
		}
		return m
	})
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string {
		if gone(m) {
			return ""
		}
		return m
	})
	return g
}

// joined is a found group the user changed (an "auto-…" id kept in
// providers.json) with the providers that serve its model since the
// user's last save: found's members it neither names nor has seen
// (Group.Seen), after its own. A third provider with the model, a
// plugin's among them, joins it as it joins one magpie finds
// (Kxsg79bEYFP4 on X: changed, the group had stayed at the two it was
// found with). One the user took out was seen, and stays out; so does
// every provider of a group changed before Seen was kept, as nothing
// says which of its models the user took out. It is how the group is
// read, not stored: a save names the joined, and sees them.
func joined(g Group, found []Group, entries []Entry) Group {
	if len(g.Seen) == 0 {
		return g
	}
	i := slices.IndexFunc(found, func(o Group) bool { return o.ID == g.ID })
	if i < 0 {
		return g
	}
	names := make(map[string]bool, len(g.Members))
	for _, m := range g.Members {
		base, _ := memberEffortIn(entries, m)
		names[base] = true
	}
	var add []string
	for _, m := range found[i].Members {
		if !names[m] && !slices.Contains(g.Seen, m) {
			add = append(add, m)
		}
	}
	if len(add) == 0 {
		return g
	}
	g.Members = slices.Concat(g.Members, add)
	return g
}

// seenIn is the Seen a found group the user changed is saved with: what
// it had seen (was), the models magpie finds in it now, and those the
// user names (own), each "provider/model" once.
func seenIn(id string, was, own []string, entries []Entry) []string {
	seen := slices.Clone(was)
	see := func(m string) {
		if m != "" && !strings.HasPrefix(m, GroupPrefix) && !IsPattern(m) && !slices.Contains(seen, m) {
			seen = append(seen, m)
		}
	}
	for _, g := range foundGroups(entries) {
		if g.ID == id {
			for _, m := range g.Members {
				see(m)
			}
		}
	}
	for _, m := range own {
		base, _ := memberEffortIn(entries, m)
		see(base)
	}
	return seen
}

// own is the group with lists of its own. f.Groups is the providers.json
// every look-up shares while a request holds the catalog (heldOf), so a
// caller changing its group in place — the TUI deleting a rule, SaveGroup
// cleaning one — would change it for every reader in that while, and keep
// the change there when the save failed.
func (g Group) own() Group {
	g.Members = slices.Clone(g.Members)
	g.Match = slices.Clone(g.Match)
	g.Matched = slices.Clone(g.Matched)
	g.Placed = slices.Clone(g.Placed)
	g.Off = slices.Clone(g.Off)
	g.Levels = slices.Clone(g.Levels)
	g.Fast = slices.Clone(g.Fast)
	g.Ultra = slices.Clone(g.Ultra)
	g.Accounts = slices.Clone(g.Accounts)
	g.Keys = slices.Clone(g.Keys)
	g.Seen = slices.Clone(g.Seen)
	g.Former = slices.Clone(g.Former)
	g.Weights = maps.Clone(g.Weights)
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string { return m })
	g.Rules = slices.Clone(g.Rules)
	for i, r := range g.Rules {
		g.Rules[i].Agents = slices.Clone(r.Agents)
		g.Rules[i].Folders = slices.Clone(r.Folders)
		g.Rules[i].ExceptFolders = slices.Clone(r.ExceptFolders)
		g.Rules[i].More = slices.Clone(r.More)
		g.Rules[i].Where = slices.Clone(r.Where)
		if r.Time != nil {
			w := *r.Time
			w.Days = slices.Clone(w.Days)
			g.Rules[i].Time = &w
		}
	}
	return g
}

// AutoGroupsOn reports whether magpie finds groups on its own: a model
// more than one provider serves is a group of them (autoGroups). It is on
// until the user turns it off.
func AutoGroupsOn() bool { return !load().NoAutoGroups }

// SetAutoGroups turns the groups magpie finds on its own on or off, all of
// them at once (蓝猫 on Discord: they could only be removed one by one).
// Off, none is listed or served; the groups the user made or changed, one
// found included, stay, and so do the records of those removed, so that
// on again brings back the found groups as they were. A found group one
// of the user's has in it, or classifies with, keeps them on until it is
// taken out, as DeleteGroup keeps it: the user's group would lose it
// unsaid.
func SetAutoGroups(on bool) error {
	f, err := read()
	if err != nil {
		return err
	}
	if f.NoAutoGroups == !on {
		return nil
	}
	if !on {
		all := groupsIn(providerEntries())
		found := func(ref string) bool {
			gid, ok := strings.CutPrefix(ref, GroupPrefix)
			return ok && slices.ContainsFunc(all, func(o Group) bool { return o.ID == gid && o.Auto && !o.Hidden })
		}
		var held []string
		for _, g := range all {
			if g.Auto || g.Hidden {
				continue
			}
			for _, m := range g.Members {
				if found(m) {
					held = append(held, fmt.Sprintf("%s is in %s", strings.TrimPrefix(m, GroupPrefix), g.Name))
				}
			}
			if found(g.Classifier) {
				held = append(held, fmt.Sprintf("%s is %s's classifier", strings.TrimPrefix(g.Classifier, GroupPrefix), g.Name))
			}
		}
		if len(held) > 0 {
			return fmt.Errorf("%s: take it out, or change it to make it yours, first", strings.Join(held, ", "))
		}
	}
	f.NoAutoGroups = !on
	return store(f)
}

// AutoGroupID is the id of the group magpie finds for a model, however a
// vendor spells it: "auto-claude-opus-5-5" for claude-opus-5.5.
func AutoGroupID(model string) string { return "auto-" + Slug(sameModel(model)) }

var groupDots = lazyre.New(`\.{2,}`)

// GroupSlug derives a group's own id from a name as Slug does, the dots
// kept: "GPT 6.1 Sol" → "gpt-6.1-sol", so an agent sending group/<id> sends
// the model's name as harnesses that price or tune by it read it (#968). A
// run of dots is one, and none starts or ends it. The ids magpie finds
// (auto-…) stay Slug's, so no id saved before changes.
func GroupSlug(name string) string {
	s := strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.'
	}), "-")
	return strings.Trim(groupDots.ReplaceAllString(s, "."), "-.")
}

// AutoGroupOf is the id of the group magpie finds for a provider's model:
// AutoGroupID of the model the user said it is the same as (settings'
// ModelSameAs), else of its own id.
func AutoGroupOf(pid, model string) string {
	return "auto-" + Slug(mergeKey(pid, model, settings.Load().ModelSameAs))
}

// MergeName is the name the groups magpie finds merge a model by, when
// the user said nothing of it: its id as vendors agree on it (sameModel).
func MergeName(model string) string { return sameModel(model) }

// mergeKey is what the groups magpie finds merge pid's model by: the
// model the user said it is the same as (same, by "<provider>/<model>"),
// spelt as vendors agree on it, else its own id so spelt.
func mergeKey(pid, model string, same map[string]string) string {
	if v := same[pid+"/"+model]; v != "" {
		return sameModel(v)
	}
	if v := editionTwins[pid+"/"+model]; v != "" {
		return sameModel(v)
	}
	return sameModel(model)
}

// editionTwins are the models a vendor's international edition lists
// under another id than its China edition does, which are the same model:
// the found groups merge each with its twin, as they merge Qoder's and
// Trae's two editions by their one id (Kts6M2fwWN43967 on X: WorkBuddy and
// WorkBuddy AI stayed two rows). The user's own "same as" (ModelSameAs)
// is read first. WorkBuddy AI lists deepseek-v4.1-flash under its paid
// id, -sg, which the product config names as the free id's paid id
// (allowPaidSwitch), so a signed-in account may list that one alone.
var editionTwins = map[string]string{
	"workbuddy-ai/deepseek-v4.1-flash-sg": "deepseek-v4.1-flash",
}

// AutoStandIn is the model a request for a group magpie found goes to while
// such groups are off (SetAutoGroups), or once the providers the user keeps
// out of them (SetAutoGroupSkip) took it away: its model, from the first
// provider that serves it, one not kept out first, as "provider/model". An
// agent set to the group, or a session begun on it, keeps working, on one
// provider. ok is false for any other id, a group the user has of that id,
// one they removed, and while found groups are on, one there is or that
// isn't gone for a provider kept out.
func AutoStandIn(id string) (string, bool) { return AutoStandInFor("", id) }

// AutoStandInFor is AutoStandIn for a request of agent's: the agent's own
// subscription first when it serves the model (freefcw on X: off, Codex's
// gpt-6-astra went to Devin, the first provider in the order), then the
// first provider as AutoStandIn picks.
func AutoStandInFor(agent, id string) (string, bool) {
	gid, ok := GroupIDOf(id)
	if !ok || !strings.HasPrefix(gid, "auto-") {
		return "", false
	}
	f := load()
	entries := providerEntries()
	if _, ok := groupOf(groupsIn(entries), gid); ok {
		return "", false
	}
	if !f.NoAutoGroups {
		if len(f.AutoGroupSkip) == 0 || slices.ContainsFunc(f.Groups, func(g Group) bool { return g.ID == gid }) {
			return "", false // not gone for skipping: removed, or the user's
		}
		all := autoGroups(foundFrom(entries, heldOffEntries()), heldSettings().ModelSameAs)
		if !slices.ContainsFunc(all, func(g Group) bool { return g.ID == gid }) {
			return "", false
		}
	}
	var first, kept string
	for _, e := range entries {
		if AutoGroupOf(e.Provider.ID, e.Model) != gid {
			continue
		}
		if ownAgent(*e.Provider, agent) {
			return e.ID, true
		}
		if kept == "" && !slices.Contains(f.AutoGroupSkip, e.Provider.ID) {
			kept = e.ID
		}
		if first == "" {
			first = e.ID
		}
	}
	if kept != "" {
		return kept, true
	}
	return first, first != ""
}

// foundGroups are the groups magpie finds (autoGroups) among entries and
// the models of the providers switched off: one switched off for a while
// stays in the group, a member the gateway skips (membersIn), rather than
// taking a group of two away with every agent, rule and group set to it.
func foundGroups(entries []Entry) []Group {
	return autoGroups(unskipped(foundFrom(entries, heldOffEntries()), heldOf("file", load).AutoGroupSkip), heldSettings().ModelSameAs)
}

// unskipped is entries without the models of the providers skip names:
// those the user keeps out of found groups (AutoGroupSkip).
func unskipped(entries []Entry, skip []string) []Entry {
	if len(skip) == 0 {
		return entries
	}
	return slices.DeleteFunc(slices.Clone(entries), func(e Entry) bool { return slices.Contains(skip, e.Provider.ID) })
}

// AutoGroupSkip are the providers the user keeps out of the groups magpie
// finds (SetAutoGroupSkip).
func AutoGroupSkip() []string { return slices.Clone(load().AutoGroupSkip) }

// SetAutoGroupSkip keeps the providers ids names out of the groups magpie
// finds, and every other back in: none of their models is in a found
// group, so a model one other provider serves is no group of them. The
// groups the user made or changed, a found one included, keep them: they
// are the user's. A request still naming a found group skipping took away
// goes to its model from one provider (AutoStandIn).
func SetAutoGroupSkip(ids []string) error {
	f, err := read()
	if err != nil {
		return err
	}
	var skip []string
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && !slices.Contains(skip, id) {
			skip = append(skip, id)
		}
	}
	if slices.Equal(skip, f.AutoGroupSkip) {
		return nil
	}
	f.AutoGroupSkip = skip
	return store(f)
}

// foundFrom is entries with idle's models it lacks, each provider's in
// the Providers order, so a provider on again is back in its place.
func foundFrom(entries, idle []Entry) []Entry {
	have := make(map[string]bool, len(entries))
	for _, e := range entries {
		have[e.ID] = true
	}
	idle = slices.DeleteFunc(slices.Clone(idle), func(e Entry) bool { return have[e.ID] })
	if len(idle) == 0 {
		return entries
	}
	at := map[string]int{}
	for i, p := range All() {
		at[p.ID] = i
	}
	pos := func(e Entry) int {
		if i, ok := at[e.Provider.ID]; ok {
			return i
		}
		return len(at)
	}
	out := append(slices.Clone(entries), idle...)
	slices.SortStableFunc(out, func(a, b Entry) int { return cmp.Compare(pos(a), pos(b)) })
	return out
}

// autoGroups are the models more than one provider serves under the
// same name — however each vendor spells it (see sameModel), or as the
// user said one is the same as another (same: settings' ModelSameAs) — in
// the order the providers were added.
func autoGroups(entries []Entry, same map[string]string) []Group {
	var order []string
	by := map[string][]Entry{}
	for _, e := range entries {
		k := mergeKey(e.Provider.ID, e.Model, same)
		if !slices.ContainsFunc(by[k], func(o Entry) bool { return o.Provider.ID == e.Provider.ID }) {
			if by[k] == nil {
				order = append(order, k)
			}
			by[k] = append(by[k], e)
		}
	}
	var out []Group
	for _, k := range order {
		es := by[k]
		if len(es) < 2 {
			continue
		}
		// the model's own name: one a user gave it is that provider's alone
		own := func(e Entry) string {
			if e.Default != "" {
				return e.Default
			}
			return e.Name
		}
		g := Group{ID: "auto-" + Slug(k), Name: own(es[0]), Auto: true}
		for _, e := range es {
			g.Members = append(g.Members, e.ID)
			if g.Name == es[0].Model && own(e) != e.Model {
				g.Name = own(e) // a vendor that names it, over one that only lists its id
			}
		}
		// the name goes with the id agents are given (auto/<Spelled>): the
		// one models.dev gives that spelling, else, where no vendor names
		// it, that spelling itself, not the first provider's (XYenon on
		// Discord: glm-5-3-flash beside auto/glm-5.3-flash). A group the
		// user changed is stored with their name, never this one.
		g.Spelled = spelledIn(g, same)
		if n := catalog.NameOf(g.Spelled); g.Spelled != "" && n != "" {
			g.Name = n
		} else if g.Spelled != "" && g.Name == es[0].Model {
			g.Name = g.Spelled
		}
		out = append(out, g)
	}
	return out
}

// sameModel is a model's name as vendors agree on it: catalog.SameModel,
// which says what it keeps and what it folds.
func sameModel(id string) string { return catalog.SameModel(id) }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// FindGroup looks a group up by its catalog id ("group/<id>") and resolves
// its members; one not ready now is left out.
func FindGroup(id string) (Group, []Member, bool) {
	return GroupFinder()(id)
}

// GroupHasNative says whether the group id has a member found now that
// speaks api natively, kept across requests while the files stay the same
// (keptOf, a minute at most). Pi's Drift asks it of the model an agent is
// set to on every /api/drift poll, and finding the members was ~3.4 MB a
// poll with real accounts, four of five bytes the window allocated a minute.
func GroupHasNative(id string, api Protocol) bool {
	return keptOf("group native "+string(api)+" "+id, func() bool {
		_, ms, ok := GroupFinder()(id)
		if !ok {
			return false
		}
		for _, m := range ms {
			if m.Provider.Native(m.Model) == api {
				return true
			}
		}
		return false
	})
}

// FallbackGroup is the routing group a provider's fallback entry names
// ("group/<id>", 01huadalang on Discord), as the gateway routes it (Live),
// with its members ready now. ok is false for an entry naming a model,
// and for a group removed or switched off: such an entry is passed over.
func FallbackGroup(id string) (Group, []Member, bool) {
	if _, ok := GroupIDOf(id); !ok {
		return Group{}, nil, false
	}
	g, ms, ok := FindGroup(id)
	return g.Live(), ms, ok
}

// FallbackModels are the models p's fallbacks reach, in order: an entry
// naming a model, that model as routing reaches it (ResolveRoute); one
// naming a routing group, the group's members. An entry that reaches
// nothing is left out, as the gateway passes it over, and a model met
// again is listed where it was met first.
func FallbackModels(p Provider) []Member {
	var out []Member
	seen := map[string]bool{}
	add := func(m Member) {
		if k := m.Provider.ID + "/" + m.Model; !seen[k] {
			seen[k] = true
			out = append(out, m)
		}
	}
	for _, id := range p.Fallback {
		if _, ms, ok := FallbackGroup(id); ok {
			for _, m := range ms {
				add(m)
			}
			continue
		}
		if fp, fm, ok := ResolveRoute(id); ok {
			add(Member{ID: id, Path: []string{id}, Provider: fp, Model: fm})
		}
	}
	return out
}

// GroupsAfter are the routing groups the user built that have id
// ("provider/model") in them as a member and another member to go on to,
// as "group/<id>", in the user's order; one magpie found (Auto), hidden
// or switched off is none. A request for the model that its own accounts
// can't take (every one held at its usage cap, or Codex's own sign-in out
// of its allowance) goes on to these when nothing set as its fallback
// takes it (adorable_piglet on Discord, v0.1.3354): the Codex app asks
// Codex's own model, never the group built around it. A request that
// named the provider itself ("claude/…") is not sent on to these
// (gateway's namedProvider, Anotiaww on X).
func GroupsAfter(id string) []string {
	var out []string
	for _, gid := range MemberGroups()[id] {
		g, ms, ok := FallbackGroup(gid)
		if ok && !g.Auto && slices.ContainsFunc(ms, func(m Member) bool { return m.Provider.ID+"/"+m.Model != id }) {
			out = append(out, gid)
		}
	}
	return out
}

// GroupFor is the group a model's id without a provider in it names, as
// "group/<id>": the group of that id, else the group of that model however
// a vendor spells it ("grok-4.7" is the group grok-4-7 or auto-grok-4-7).
// A request for the model is the group's then, as it would be for the
// group's own id; failing those, the group whose name it is (groupNamed);
// ok is false when no group has it. An id with a provider
// in it ("a/m") names that provider's model, never a group.
func GroupFor(id string) (string, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return groupForIn(Groups(), id)
}

// groupForIn is GroupFor over the groups all, read once for many ids.
func groupForIn(all []Group, id string) (string, bool) {
	k := Slug(sameModel(id))
	for _, gid := range []string{strings.ToLower(id), k, "auto-" + k} {
		if g, ok := groupOf(all, gid); ok {
			return GroupPrefix + g.ID, true
		}
	}
	return groupNamed(all, id)
}

// groupNamed is the group whose name, not its id, the request gave: a
// client the user typed the group's name into (ZCode's own model field)
// asks for it so, and a group renamed keeps its id (MOMO on Discord: "DS
// Flash" was 404 once group set name= gave it that name). The name is
// matched as the id is, by its slug; only one group may have it, and a
// model a provider serves by that id is the provider's, not the group's.
func groupNamed(all []Group, id string) (string, bool) {
	k := Slug(sameModel(id))
	if k == "" {
		return "", false
	}
	var hit *Group
	for i, g := range all {
		if g.Hidden || strings.TrimSpace(g.Name) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(g.Name), id) || Slug(sameModel(g.Name)) == k {
			if hit != nil {
				return "", false // two groups go by it: neither is meant
			}
			hit = &all[i]
		}
	}
	if hit == nil {
		return "", false
	}
	for _, e := range providerEntries() {
		if e.Model == id || e.ID == id {
			return "", false
		}
	}
	return GroupPrefix + hit.ID, true
}

// GroupFinder is FindGroup for looking up many: every provider's models
// are read once, when the first group is looked up, not again for each.
func GroupFinder() func(id string) (Group, []Member, bool) {
	var (
		entries []Entry
		all     []Group
		find    entryOf
		read    bool
	)
	return func(id string) (Group, []Member, bool) {
		// the mark Claude Code puts on a 1M window rides on the group's
		// id, as it does on a model's: GroupFor takes it off for the
		// gateway, and a ref the gateway routes is a group here too
		gid, ok := GroupIDOf(id)
		if !ok {
			return Group{}, nil, false
		}
		if !read {
			entries, read = providerEntries(), true
			all, find = sharedGroupsIn(entries), heldFrom("entries by", entries, entriesBy)
		}
		if g, ok := routedGroupOf(all, gid); ok && !g.Disabled {
			groupsFound.Add(1)
			g = cloneGroup(g)
			return g, membersFound(entries, find, all, g, disabledNow(), nil), true
		}
		return Group{}, nil, false
	}
}

// groupsFound counts the groups GroupFinder found members for, for a test
// that counts them.
var groupsFound atomic.Int64

// GroupsFound is how many times a group's members were found, for another
// package's test that counts them.
func GroupsFound() int64 { return groupsFound.Load() }

// FindDrawingGroup is FindGroup for a drawing (the gateway's images API):
// a member that is one of its provider's image models, as draws says, is
// the group's even where the provider's picks leave it out (unpicked).
// Picks are of chat models, and an image model is offered to a group
// beside them (jzw690 on X), so a group of image models draws on its
// members without any of them being a model agents are offered.
func FindDrawingGroup(id string, draws func(p Provider, model string) bool) (Group, []Member, bool) {
	return FindGroupAlso(id, draws)
}

// FindGroupAlso is FindGroup with a member also says is the group's kept
// where its provider's picks leave it out: picks are of chat models, so a
// group of models made for another API (images, embeddings, speech,
// video) keeps those of a provider with picks only so (FindDrawingGroup,
// the gateway's media groups).
func FindGroupAlso(id string, also func(p Provider, model string) bool) (Group, []Member, bool) {
	gid, ok := GroupIDOf(id)
	if !ok {
		return Group{}, nil, false
	}
	entries := providerEntries()
	all := sharedGroupsIn(entries)
	if g, ok := routedGroupOf(all, gid); ok && !g.Disabled {
		g = cloneGroup(g)
		return g, membersFound(entries, heldFrom("entries by", entries, entriesBy), all, g, disabledNow(), also), true
	}
	return Group{}, nil, false
}

// groupOf is the group of an id among all, unless it was removed.
func groupOf(all []Group, id string) (Group, bool) {
	for _, g := range all {
		if g.ID == id && !g.Hidden {
			return g, true
		}
	}
	return Group{}, false
}

// routedGroupOf is the group a request for group/<id> goes to: the group
// of that id, or else the one renamed from it (Former), so an agent or a
// script still on the old id keeps working after a rename. A group of
// that id, switched off too, holds the id; one magpie found that is
// removed (Hidden, as a rename leaves a found group's old id) does not.
func routedGroupOf(all []Group, id string) (Group, bool) {
	if g, ok := groupOf(all, id); ok {
		return g, true
	}
	for _, g := range all {
		if !g.Hidden && slices.Contains(g.Former, id) {
			return g, true
		}
	}
	return Group{}, false
}

// dropFormer takes id out of every group's Former: a group took it for
// its own, and group/<id> is that group's now.
func dropFormer(gs []Group, id string) {
	for i := range gs {
		if slices.Contains(gs[i].Former, id) {
			gs[i].Former = slices.DeleteFunc(slices.Clone(gs[i].Former), func(x string) bool { return x == id })
		}
	}
}

// membersIn are a group's models, ready now, in its order: a group in it
// gives its own there, as deep as they go. A model met again is left
// where it was first — the same model at another effort is another
// member — and a group that would be in itself is cut there. A manual
// group has only the member picked (see Manual), however deep.
func membersIn(entries []Entry, all []Group, g Group) []Member {
	return membersOff(entries, all, g, disabledNow())
}

// membersOff is membersIn less the models off reports.
func membersOff(entries []Entry, all []Group, g Group, off func(pid, model string) bool) []Member {
	return membersFound(entries, entriesBy(entries), all, g, off, nil)
}

// membersFound is membersOff with entries' index made already: GroupFinder
// looks up every group in one list, and indexing it again for each was
// most of what the Providers page spent on groups. A model also says is
// the group's whatever p's picks are (FindDrawingGroup); nil for none.
func membersFound(entries []Entry, find entryOf, all []Group, g Group, off func(pid, model string) bool, also func(p Provider, model string) bool) []Member {
	// made at the group's own count: a Member is large, and growing out
	// by append was 9 MB of what forty polls of the Providers page
	// allocated with real accounts. A group in it can add more.
	n := len(g.Members)
	if g.Routing == Manual {
		n = 1
	}
	out := make([]Member, 0, n)
	seen := make(map[string]int, n) // where in out
	at := 0
	var walk func(g Group, path []string, via []Group, in []string, redact string)
	walk = func(g Group, path []string, via []Group, in []string, redact string) {
		redact = RedactWith(redact, g.Redact)
		for _, id := range g.routes() {
			if gid, ok := strings.CutPrefix(id, GroupPrefix); ok {
				sub, ok := groupOf(all, gid)
				if !ok || sub.Disabled || slices.Contains(in, gid) || len(via) >= maxNest {
					continue // gone, switched off, a loop, or deeper than anyone nests
				}
				// its own copy: all may be the groups held (sharedGroupsIn),
				// and a member's Via and EffortMap are its caller's
				sub = cloneGroup(sub).Within(g)
				walk(sub, append(slices.Clone(path), id), append(slices.Clone(via), sub.Live()), append(slices.Clone(in), gid), redact)
				continue
			}
			model, effort := memberEffortIn(entries, id)
			p, m, ok := resolveIn(entries, model)
			if !ok || unpicked(find, p, m) && (also == nil || !also(p, m)) {
				continue // none serves it, or the user unchecked it (#3911)
			}
			// on one account of its own (EastonXvii on X): the same model
			// on another account is another member, asked in its own place
			_, acct := MemberAccount(id)
			if acct != "" {
				acct = p.ID + "/" + acct
			}
			key := WithMemberAccount(WithMemberEffort(p.ID+"/"+m, effort), acct)
			here := append(slices.Clone(path), id)
			fixed, levels := MemberLevels(effort)
			mem := Member{ID: here[0], Path: here, Via: via, Redact: redact, Provider: p, Model: m, Effort: fixed, Levels: levels, Fast: g.IsFast(id), Ultra: g.IsUltra(id), Account: acct, At: at}
			if fixed == "" && levels == nil {
				mem.EffortMap = g.MemberEffortMap(id)
			}
			at++
			if i, ok := seen[key]; ok {
				// asked once, where it was met first; a rule naming this
				// group still finds it here (#2361)
				out[i].Also = append(out[i].Also, mem)
				continue
			}
			if off(p.ID, m) {
				continue // the user turned it off (#1867): a group never reaches it
			}
			seen[key] = len(out)
			out = append(out, mem)
		}
	}
	walk(g, nil, nil, []string{g.ID}, "")
	if len(out) == 0 {
		return nil // none, as before it was made at its size
	}
	return out
}

// unpicked reports whether p's model m is one the user unchecked in p's
// model list since a group took it (timonwong, #3911): p has picks and
// its served models (entries, as find has them) lack m. resolveIn takes
// any "provider/model" of a provider that is on, so that a request can
// name a model its list lacks; a group's member is the user's pick of a
// model they now said not to serve, and the Routing page strikes it.
// A provider with no picks still routes a model its list lacks (#2642),
// and a decision model, never in the catalog, is still a decision
// group's.
func unpicked(find entryOf, p Provider, m string) bool {
	return len(p.Models) > 0 && find(p.ID, m) < 0 && !p.DecidesModel(m)
}

// ResolveRoute is Resolve for a model a route names, a routing group's
// member or a provider's fallback, as routing reaches it: not one the
// user unchecked (unpicked). A request naming the model itself goes by
// Resolve, which still takes it.
func ResolveRoute(id string) (Provider, string, bool) {
	entries := providerEntries()
	p, m, ok := resolveIn(entries, ThroughSelf(strings.TrimSuffix(strings.TrimSpace(id), "[1m]")))
	// one pass for the one model, not a map of every entry (entriesBy):
	// a request asks it for each fallback it may go to
	find := func(pid, model string) int {
		for i := range entries {
			if entries[i].Provider.ID == pid && entries[i].Model == model {
				return i
			}
		}
		return -1
	}
	if !ok || unpicked(find, p, m) {
		return Provider{}, "", false
	}
	return p, m, true
}

// Unpicked reports whether id, "provider/model", names a model the user
// has since unchecked in that provider's list (unpicked), and which
// provider: Resolve still takes it, so an agent left on it goes on asking
// the vendor for the old model, while the user picked the provider's
// others (JNDliuliuM on X: Codex CLI still on it after a restart).
func Unpicked(id string) (pid string, ok bool) {
	entries := providerEntries()
	p, m, found := resolveIn(entries, ThroughSelf(strings.TrimSuffix(strings.TrimSpace(id), "[1m]")))
	if !found {
		return "", false
	}
	find := func(pid, model string) int {
		for i := range entries {
			if entries[i].Provider.ID == pid && entries[i].Model == model {
				return i
			}
		}
		return -1
	}
	if !unpicked(find, p, m) {
		return "", false
	}
	return p.ID, true
}

// groupEntries are the catalog's groups: each with a member ready, named
// as the user named it, answering for its first member when an agent asks
// what the model can do, and reasoning when any member does — a member
// that doesn't think doesn't take reasoning from the others, as a
// text-only member doesn't take images (#756) — offering the reasoning
// levels the members have in common, as before, but for those fixed at
// an effort of their own, which take whatever the agent asks, and those
// whose levels nothing magpie reads knows (levelsUnknown), which are
// sent it as asked, those that think with no levels to pick from,
// which are sent it as asked up to high (the gateway's fitFor), and those
// known not to think, which are sent no reasoning ask (Quiet). With
// every member fixed, the group offers the levels they are fixed at, so
// that an agent still asks it to reason. A group that names its own
// levels (Group.Levels) offers those, and so does a group in it for its
// models. Held while a request holds the catalog (heldFrom).
func groupEntries(entries []Entry) []Entry {
	return heldFrom("group entries", entries, buildGroupEntries)
}

// entryOf is where in entries the last entry of provider pid's model is,
// -1 when none is.
type entryOf func(pid, model string) int

// entriesBy is entryOf for entries, looked up in a map built once rather
// than by a pass over every entry for each member of each group.
func entriesBy(entries []Entry) entryOf {
	at := make(map[[2]string]int, len(entries))
	for i := range entries {
		at[[2]string{entries[i].Provider.ID, entries[i].Model}] = i
	}
	return func(pid, model string) int {
		if i, ok := at[[2]string{pid, model}]; ok {
			return i
		}
		return -1
	}
}

// groupSharedKimi gives a group what the providers' lists say of a model
// (AlwaysThinks, DynamicTools, DefaultEffort) when every member's entry
// says the same: a group of Kimi Code's models keeps always_thinking and
// dynamically_loaded_tools in Kimi Code (jiabubu on Discord: one Kimi
// model put in a group to list it first lost both). One member without it,
// or one magpie has no entry for, and the group has none: Kimi Code would
// send a member that can't the request shaped for one that can
func groupSharedKimi(e *Entry, ms []Member, entries []Entry, find entryOf) {
	for i, m := range ms {
		j := find(m.Provider.ID, m.Model)
		if j < 0 {
			e.AlwaysThinks, e.DynamicTools, e.DefaultEffort = false, false, ""
			return
		}
		x := &entries[j]
		if i == 0 {
			e.AlwaysThinks, e.DynamicTools, e.DefaultEffort = x.AlwaysThinks, x.DynamicTools, x.DefaultEffort
			continue
		}
		e.AlwaysThinks = e.AlwaysThinks && x.AlwaysThinks
		e.DynamicTools = e.DynamicTools && x.DynamicTools
		if e.DefaultEffort != x.DefaultEffort {
			e.DefaultEffort = ""
		}
	}
}

func buildGroupEntries(entries []Entry) []Entry {
	var out []Entry
	all := groupsIn(entries)
	find := entriesBy(entries)
	for _, g := range all {
		if g.Hidden || g.Disabled {
			continue
		}
		ms := membersIn(entries, all, g)
		if len(ms) == 0 || DecisionGroup(ms) {
			continue // a decision group answers System One, no agent's conversation
		}
		e := Entry{ID: g.Ref(), Model: ms[0].Model, Name: g.Name, Provider: &ms[0].Provider, Group: g.ID, Named: !g.Auto}
		var fixed []string // the efforts members are fixed at
		levelled := false  // a member that follows the agent's effort was met
		// Codex's ultra (max, with Codex handing parts of the task to agents
		// of its own) is a level of OpenAI's models that offer it (a ChatGPT
		// account's, or one served elsewhere: OffersUltra); a group with one
		// of them in it, every member at max, offers it too, and the
		// gateway sends max to the members that have no ultra
		ultra := false
		chatgpt := false // a ChatGPT account answers for a member
		for i, m := range ms {
			chatgpt = chatgpt || m.Provider.Account != nil && m.Provider.Account.Agent == "codex"
			if !slices.ContainsFunc(ms[:i], func(o Member) bool { return o.Provider.ID == m.Provider.ID }) {
				e.Icons = append(e.Icons, m.Provider.Icon) // each provider once, "" for one without
			}
			var efforts []string
			images, thinks, ctx, output := false, false, 0, 0
			var imageInput *bool
			// the kinds of input and output the member's source listed
			// (video, audio and pdf beside image, #1595)
			var inputs, outputs []string
			unknown, reasonless, typed := false, false, false
			if j := find(m.Provider.ID, m.Model); j >= 0 {
				x := &entries[j]
				efforts, images, thinks, ctx, output, imageInput = x.Efforts, x.Images, x.Reasoning, x.Context, x.Output, x.ImageInput
				inputs, outputs = x.Inputs, x.Outputs
				typed = x.WindowSet
				unknown, reasonless = levelsUnknown(*x), x.Reasonless
			}
			// it reasons when a member does: the gateway sends the
			// effort to the members that take it, and a member known not
			// to think is sent none of it, as a text-only member is sent
			// no image (#756's rule for images, applied to reasoning).
			// One that doesn't think is no reason to tell the agent the
			// group can't, nor does it take the others' levels away
			// (below)
			e.Reasoning = e.Reasoning || thinks
			// the reply agents are told is the longest a member gives: the
			// gateway asks each member for no more than its own
			// (withMaxOutput), so the members that write long replies aren't
			// cut at the shortest one's (ARNO on Discord: a group's
			// maxTokens was its smallest member's, and agents' replies were
			// "truncated before completion")
			if output > e.Output {
				e.Output = output
			}
			// it takes images when a member does: the gateway sends a
			// request with an image to the members that see it, and the
			// rest are given it described or without it (#756: an auto
			// group of DeepSeek V4.1 Flash with a text-only member in it
			// turned every image away, though its other members see)
			e.Images = e.Images || images
			// and every other kind a member takes or gives, a video among
			// them: the gateway sends a request with a video only to the
			// members that take one (videoInput in the gateway), as it does
			// an image. Left nil, Modalities fell back to text and image,
			// and a group of MiniMax M3.1 Flash, which takes video, was
			// listed as not taking it (#2898, #6135)
			e.Inputs = unionKinds(e.Inputs, inputs)
			e.Outputs = unionKinds(e.Outputs, outputs)
			// the window agents are told is the largest a member has:
			// a conversation too long for one member goes on to a member
			// with room for it (#700, withRoom in the gateway), so the
			// agent compacts only when none has. The shortest member's,
			// as it was, had Pi compact a group of 1M DeepSeeks at 200k
			// for the one free model among them that holds 200k (#712).
			// It is one the user typed when they typed that member's, as
			// smallestWindow says it for a group following its smallest
			// (Osal on Discord: an Ultra group of a ChatGPT account's
			// GPT-6 Astra, set to its 872K on the codex provider, was
			// handed to Codex as magpie's default 272K, ~258K at 95%)
			if ctx > e.Context {
				e.Context, e.WindowSet = ctx, typed
			} else if ctx > 0 && ctx == e.Context {
				e.WindowSet = e.WindowSet || typed
			}
			if i == 0 {
				e.ImageInput = imageInput
			} else {
				e.ImageInput = anyImageInput(e.ImageInput, imageInput)
			}
			// a member that sends other levels than the ones asked
			// (EffortMaps) takes the levels its map is asked for too: the
			// gateway sends them at the level they map to
			if len(m.EffortMap) > 0 && !reasonless && !unknown && len(efforts) > 0 {
				efforts = mappedLevels(efforts, m.EffortMap)
			}
			// a member the user gave several levels of its own offers them
			// (Garydesu on X: one model at a few efforts, the agent picking);
			// a group in the group that names its own levels offers them for
			// its models (the outermost that does)
			if len(m.Levels) > 0 && !reasonless {
				efforts = m.Levels
			} else if i := slices.IndexFunc(m.Via, func(v Group) bool { return len(v.Levels) > 0 }); i >= 0 {
				efforts = m.Via[i].Levels
			} else if reasonless {
				// the user said it takes no reasoning ask: the gateway sends
				// it none, whatever the agent asks, so it doesn't take the
				// others' levels away (yuexian7 on Discord: a group of
				// Opus and a model that answers reasoning_effort with 422)
				continue
			} else if m.Effort != "" {
				if !slices.Contains(fixed, m.Effort) {
					fixed = append(fixed, m.Effort)
				}
				continue
			} else if unknown {
				// nothing magpie reads says which levels it takes, or that
				// it takes none: the gateway sends it the effort asked as
				// it is (fitEffort), so it doesn't take the others' away —
				// a Token Plan's deepseek-v4-pro-202606 beside a TokenHub
				// deepseek-v4-pro left the group none, and Pi only off
				// (#597)
				continue
			} else if len(efforts) == 0 && thinks {
				// it thinks with no levels to pick from (a thinking switch
				// alone, or levels its list doesn't give): the gateway
				// sends it the effort asked, no more than high (fitFor),
				// so it doesn't take the others' away — an auto group of
				// deepseek-v4.1-flash with a Volcengine endpoint's in it
				// had none, and dsh offered no thinking for it (ARNO on
				// Discord: routing group 在接入 harness 的时候，没有设置
				// reasoning_efforts)
				continue
			} else if len(efforts) == 0 {
				// it is known not to think (Quiet): the gateway sends it no
				// reasoning ask (quiet.go, #950), so it doesn't take the
				// others' levels away either. It left the group none, and a
				// group that reasons with none to pick from is a model Codex
				// offers no effort control for, at its own medium (#2127:
				// a 高性能 group's sessions and subagents ran at medium)
				continue
			}
			ultra = ultra || slices.Contains(efforts, "ultra")
			if !levelled {
				e.Efforts, levelled = efforts, true
				continue
			}
			e.Efforts = slices.DeleteFunc(slices.Clone(e.Efforts), func(v string) bool { return !slices.Contains(efforts, v) })
		}
		if !levelled {
			e.Efforts = fixedLevels(fixed)
		}
		e.Shared = e.Efforts
		if len(g.Levels) > 0 {
			e.Efforts = slices.Clone(g.Levels)
		}
		if ultra && slices.Contains(e.Efforts, "max") && !slices.Contains(e.Efforts, "ultra") {
			e.Efforts = append(e.Efforts, "ultra")
		}
		// V2 for Ultra only when no member is a ChatGPT account's: a lead
		// it answers seals its subagents' tasks (#141)
		e.AgentsV2 = !chatgpt && slices.Contains(e.Efforts, "ultra")
		groupSharedKimi(&e, ms, entries, find)
		e.Reasoning = e.Reasoning || len(e.Efforts) > 0
		if e.ImageInput != nil && !*e.ImageInput {
			e.Images = false
		}
		largest := e.Context
		ruledEntry(&e, g.Live(), ms, entries)
		if e.Context != largest {
			// lifted by a rule of length to what its members fit, which
			// is no member's typed window
			e.WindowSet = false
		}
		if g.Context > 0 {
			e.Context, e.WindowSet = g.Context, true
		} else if g.Context == ContextSmallest {
			// only for a group that follows its smallest member: run for
			// every group, it was another pass over the catalog for each
			// member of each (8f4d091c6)
			if smallest, typed := smallestWindowBy(entries, find, ms); smallest > 0 {
				e.Context, e.WindowSet = smallest, typed
			}
		}
		e.Family = g.Family
		out = append(out, e)
	}
	return out
}

// unionKinds is every kind a or b lists, in the order given; nil when
// neither lists any, which is not an empty list: Modalities falls back to
// text and image on nil (an entry whose source said nothing), and reads a
// list as it is.
func unionKinds(a, b []string) []string {
	if a == nil && b == nil {
		return nil
	}
	out := slices.Clone(a)
	if out == nil {
		out = []string{}
	}
	for _, k := range b {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// smallestWindow is the smallest window a member of ms is known to have
// (0 when none is), and whether the user typed it, on the member's
// provider for its model or all of its models. A group told its
// smallest member's window is then told one the user typed (#2224: a
// group following its smallest member, a 500K the user set on it, was
// handed to Claude Desktop and Codex as magpie's default 272K, which a
// group set to 500K itself wasn't). A tie counts as typed: the window is
// the user's either way.
func smallestWindow(entries []Entry, ms []Member) (n int, typed bool) {
	return smallestWindowBy(entries, entriesBy(entries), ms)
}

// largestWindow is the window a group that names no size of its own is
// told, its largest member's, and whether the user typed it, as
// buildGroupEntries says it (Entry.WindowSet).
func largestWindow(entries []Entry, g Group) (typed bool) {
	for _, e := range groupEntries(entries) {
		if e.Group == g.ID {
			return e.WindowSet
		}
	}
	return false
}

func smallestWindowBy(entries []Entry, find entryOf, ms []Member) (n int, typed bool) {
	for _, m := range ms {
		ctx, set := 0, false
		if j := find(m.Provider.ID, m.Model); j >= 0 {
			ctx, set = entries[j].Context, entries[j].WindowSet
		}
		switch {
		case ctx <= 0:
		case n == 0 || ctx < n:
			n, typed = ctx, set
		case ctx == n:
			typed = typed || set
		}
	}
	return n, typed
}

// levelsUnknown reports whether nothing magpie reads says which reasoning
// levels the model of x takes, or that it takes none: it has none in the
// catalog, no account's list gives it, its vendor and its maker don't list
// it, and models.dev lists no model of its id at all.
func levelsUnknown(x Entry) bool {
	if len(x.Efforts) > 0 || x.Provider.Account != nil && x.Provider.Account.models != nil {
		return false
	}
	if _, ok := catalog.ListedBy(x.Provider.Catalogs(), x.Model); ok {
		return false
	}
	if _, ok := catalog.ListedBy(makerCatalogs(), x.Model); ok {
		return false
	}
	return !catalog.Knows(x.Model)
}

// Quiet reports whether the model is known not to think: magpie has a
// word on it — its vendor's or its maker's list, or its provider's own
// account list — and that word says no reasoning. A model nothing speaks
// for isn't quiet: the gateway sends it the effort the agent asked for,
// as #597 leaves it. Nor is one that takes levels — a model that thinks,
// or one the user gave levels of their own, which the gateway fits an
// effort to. A group's member that is quiet is sent no reasoning ask,
// which some vendors turn away with a 400 on a model that can't think,
// but for one the user fixed at an effort (Group's member syntax, #189):
// the gateway leaves that alone, the user's word over magpie's.
func (e Entry) Quiet() bool {
	return e.Reasonless || !e.Reasoning && len(e.Efforts) == 0 && !levelsUnknown(e)
}

// NoteLongest is how many characters a group's note keeps.
const NoteLongest = 200

// CleanNote is a group's note as stored: one line, its spaces and line
// breaks run together, at most NoteLongest characters.
func CleanNote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > NoteLongest {
		s = strings.TrimSpace(string(r[:NoteLongest]))
	}
	return s
}

// SaveGroup adds or replaces a group of the user's. Changing one magpie
// found makes it the user's.
func SaveGroup(g Group) error {
	g = g.own() // cleaned below in place: the caller's lists stay as they were
	g.canonicalRefs()
	g.ID = strings.ToLower(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == "" {
		g.ID = GroupSlug(g.Name)
	}
	if g.ID == "" || g.ID != GroupSlug(g.ID) {
		return fmt.Errorf("a group's id must be lowercase letters, digits, dots and dashes, not %q", g.ID)
	}
	if g.Name == "" {
		g.Name = g.ID
	}
	g.Note = CleanNote(g.Note)
	g.FirstToken = max(g.FirstToken, FirstTokenWait)
	if g.Sink {
		g.NoSink = false
	}
	if g.Patience < 0 {
		g.Patience = PatienceOff
	}
	g.Patience = min(g.Patience, PatienceLongest)
	if g.Context < 0 {
		g.Context = ContextSmallest
	}
	g.Compact = max(g.Compact, 0)
	// the members it names and its patterns, without what the patterns
	// matched when it was read: those are found again below, and stored
	// never
	members, match := ownMembers(g)
	g.Members, g.Matched = cleanList(members), nil
	var err error
	if g.Match, err = CleanPatterns(match); err != nil {
		return err
	}
	if len(g.Members) == 0 && len(g.Match) == 0 {
		return errors.New("a group needs a model in it, or a pattern its models match")
	}
	f, err := read()
	if err != nil {
		return err
	}
	entries := providerEntries()
	// checked with what its patterns match now, so that a rule, a pick, a
	// fast or an off member may name one of those
	named := len(g.Members)
	g.Members = append(g.Members, matchesIn(entries, g)...)
	if err := cleanFast(entries, &g); err != nil {
		return err
	}
	if err := checkMemberAccounts(g.Members, wasMembers(f.Groups, g.ID)); err != nil {
		return err
	}
	for i, m := range g.Members {
		if model, effort := memberEffortIn(entries, m); effort != "" && IsGroupRef(model) {
			return fmt.Errorf("%s is a group: its models reason as it says, so it takes no effort of its own (:%s)", model, effort)
		}
		g.Members[i] = cleanMember(entries, m)
	}
	own := cleanList(g.Members[:named])
	g.Members = append(slices.Clone(own), slices.DeleteFunc(cleanList(g.Members[named:]), func(m string) bool { return slices.Contains(own, m) })...)
	// the order the user put them in, a pattern's members among them: as
	// the caller sends it (the Routing page's editor), or as it was. The
	// models named go in the order the caller names them, so one added
	// to a group as read (models+=, the TUI) goes after those named
	before := g.Placed
	if len(before) == 0 {
		if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
			before = f.Groups[i].Placed
		}
	}
	key := func(m string) string {
		id, _ := memberFastIn(entries, strings.TrimSpace(m))
		return cleanMember(entries, id)
	}
	if g.Placed = placedOver(g.Members, mapEach(before, key), mapEach(members, key)); g.Placed != nil {
		own = slices.DeleteFunc(slices.Clone(g.Placed), func(m string) bool { return !slices.Contains(own, m) })
	}
	var off []string
	for _, m := range cleanList(g.Off) {
		if m = cleanMember(entries, m); slices.Contains(g.Members, m) && !slices.Contains(off, m) {
			off = append(off, m)
		}
	}
	g.Off = off
	if err := cleanWeights(entries, &g); err != nil {
		return err
	}
	if err := cleanEffortMaps(entries, &g); err != nil {
		return err
	}
	var was, keysWas, formerWas []string
	var redactWas string
	if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
		was, keysWas, formerWas, redactWas = f.Groups[i].Accounts, f.Groups[i].Keys, f.Groups[i].Former, f.Groups[i].Redact
	}
	switch g.Redact {
	case "":
		g.Redact = redactWas // a caller that doesn't know of it
	case RedactOn, RedactOff:
	default:
		g.Redact = "" // RedactFollow, or a word it doesn't know
	}
	if g.Former == nil {
		g.Former = slices.Clone(formerWas) // a caller that doesn't know of them
	}
	g.Former = slices.DeleteFunc(cleanList(g.Former), func(id string) bool { return id == g.ID })
	if g.Keys == nil {
		g.Keys = slices.Clone(keysWas) // a caller that doesn't know of them
	} else if g.Keys, err = cleanGroupKeys(g.Keys); err != nil {
		return err
	}
	if g.Accounts, err = cleanGroupAccounts(g.Accounts, was); err != nil {
		return err
	}
	if g.Routing != Manual && len(g.Members) > 0 && len(g.Off) == len(g.Members) {
		return fmt.Errorf("every model in %s is switched off: switch one on, or it has nothing to send to", g.Name)
	}
	for i := range g.Rules {
		g.Rules[i].MapUses(func(u string) string { return cleanMember(entries, strings.TrimSpace(u)) })
	}
	if err := groupsInGroup(g, groupsIn(entries)); err != nil {
		return err
	}
	// decision models make a group of their own, asked at /v1/systemone,
	// never one with models that hold conversations (decide_group.go)
	if err := cleanDecisionGroup(&g, groupsIn(entries)); err != nil {
		return err
	}
	if g.Routing != Ordered && g.Routing != Rotate && g.Routing != LeastUsed && g.Routing != Pace && g.Routing != Weighted && g.Routing != FreeFirst && g.Routing != Fastest && g.Routing != Cheapest && g.Routing != Drain && g.Routing != FreeExpiring && g.Routing != Manual && !PluginRouting(g.Routing) {
		g.Routing = ""
	}
	if !slices.Contains(Affinities, g.Affinity) {
		g.Affinity = ""
	}
	if g.Pick = strings.TrimPrefix(strings.TrimSpace(g.Pick), "magpie/"); g.Pick != "" {
		g.Pick = cleanMember(entries, g.Pick)
	}
	if !slices.Contains(g.Members, g.Pick) {
		g.Pick = "" // taken out of the group: its first, when manual
	}
	if g.Routing == Manual && g.Pick == "" && len(g.Members) > 0 {
		g.Pick = g.Members[0]
	}
	rules, err := cleanRules(g.Rules, g.Members)
	if err != nil {
		return err
	}
	g.Rules = rules
	if g.Levels, err = CleanLevels(g.Levels); err != nil {
		return err
	}
	g.Classifier = strings.TrimPrefix(strings.TrimSpace(g.Classifier), "magpie/")
	g.Effort = strings.ToLower(strings.TrimSpace(g.Effort))
	intents := slices.ContainsFunc(g.Rules, func(r Rule) bool { return r.Intent != "" })
	switch {
	case g.Effort != "" && g.Effort != EffortAuto:
		return fmt.Errorf("a group's effort is %q or left to the agent, not %q", EffortAuto, g.Effort)
	case g.Classifier == GroupPrefix+g.ID:
		return fmt.Errorf("%s can't be its own classifier: asking it would ask it again", g.Name)
	case intents && g.Classifier == "":
		return errors.New("a rule with an intent needs the group's classifier: the model that tells which intent a message is")
	case g.Effort == EffortAuto && g.Classifier == "":
		return errors.New("effort picked per turn needs the group's classifier: the model that rates how hard a turn is")
	case !intents && g.Effort == "":
		g.Classifier = "" // nothing to ask it
	}
	if gid, ok := strings.CutPrefix(g.Classifier, GroupPrefix); ok {
		if c, ok := groupOf(groupsIn(providerEntries()), gid); !ok {
			return fmt.Errorf("magpie has no group %q to classify with", gid)
		} else if c.Disabled {
			return fmt.Errorf("%s is switched off: switch it on to classify with it", c.Name)
		}
	} else if g.Classifier != "" {
		if _, _, ok := Resolve(g.Classifier); !ok {
			return fmt.Errorf("magpie knows no model %q to classify with", g.Classifier)
		}
	}
	if g.IntentSure != nil && !(*g.IntentSure >= 0 && *g.IntentSure <= 1) { // NaN too
		return fmt.Errorf("a group's intent sure is from 0 to 1, not %g", *g.IntentSure)
	}
	g.Auto, g.Hidden = false, false
	g.Members = own // what the patterns match is found again as it is read
	// a found group changed keeps following its model: what magpie had
	// found in it is seen, so a model taken out stays out and one a new
	// provider serves joins it (joined)
	g.Seen = nil
	if strings.HasPrefix(g.ID, "auto-") {
		var was []string
		if i := slices.IndexFunc(f.Groups, func(x Group) bool { return x.ID == g.ID }); i >= 0 {
			was = f.Groups[i].Seen
		}
		g.Seen = seenIn(g.ID, was, own, entries)
	}
	g.Disabled = false
	dropFormer(f.Groups, g.ID)
	for i := range f.Groups {
		if f.Groups[i].ID == g.ID {
			g.Disabled = f.Groups[i].Disabled && !f.Groups[i].Hidden
			f.Groups[i] = g
			return store(f)
		}
	}
	f.Groups = append(f.Groups, g)
	return store(f)
}

// cleanGroupAccounts is a group's account list as kept: each entry
// "<provider>/<account>", resolved as a gateway key's are (ResolveAccount)
// — an account by its stable id or the name it is shown by, a key by its
// fingerprint — trimmed, each once, in the order given. One the group
// held already and its provider has no more (an account signed out, a key
// rotated) is kept as it was, matching nothing, so the group stays held
// rather than let off to every account; a new one must be one magpie has.
// cleanGroupKeys is a group's Keys trimmed, each once: nil for none.
func cleanGroupKeys(in []string) ([]string, error) {
	var out []string
	for _, k := range in {
		if k = strings.TrimSpace(k); k == "" || slices.Contains(out, k) {
			continue
		}
		if len(k) > 200 {
			return nil, errors.New("a group's gateway key is its id, at most 200 characters")
		}
		out = append(out, k)
	}
	if len(out) > 500 {
		return nil, errors.New("a group can be bound to at most 500 gateway keys")
	}
	return out, nil
}

func cleanGroupAccounts(in, was []string) ([]string, error) {
	var out []string
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		pid, ref, ok := strings.Cut(a, "/")
		if !ok || pid == "" || ref == "" {
			return nil, errors.New("a group's account is <provider>/<account> or <provider>/<key id>, like codex/me@example.com: " + a)
		}
		if len(a) > 300 {
			return nil, errors.New("a group's account is at most 300 characters")
		}
		kept, err := ResolveAccount(pid, ref)
		if err != nil {
			if !slices.ContainsFunc(was, func(w string) bool { return strings.EqualFold(strings.TrimSpace(w), a) }) {
				return nil, err
			}
			kept = a
		}
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, kept) }) {
			out = append(out, kept)
		}
	}
	if len(out) > 500 {
		return nil, errors.New("a group can list at most 500 accounts")
	}
	return out, nil
}

// groupsInGroup checks the groups a group has in it: each one magpie has,
// and none that has the group in it, however deep — the group would be
// in itself, and a request to it would go round for ever.
func groupsInGroup(g Group, all []Group) error {
	for _, id := range g.Members {
		gid, ok := strings.CutPrefix(id, GroupPrefix)
		if !ok {
			continue
		}
		if gid == g.ID {
			return fmt.Errorf("%s can't be in itself", g.Name)
		}
		sub, ok := groupOf(all, gid)
		if !ok {
			return fmt.Errorf("magpie has no group %q to put in %s", gid, g.Name)
		}
		if way := wayTo(all, sub, g.ID, nil); way != nil {
			return fmt.Errorf("%s can't be in %s: %s is in it already (%s), so %s would be in itself",
				sub.Name, g.Name, g.Name, strings.Join(append(append([]string{sub.ID}, way...), g.ID), " ⊃ "), g.Name)
		}
		if d := depthOf(all, sub, nil); d+1 > maxNest {
			return fmt.Errorf("%s has groups in it %d deep; a group in a group goes at most %d deep", sub.Name, d, maxNest)
		}
	}
	return nil
}

// wayTo is the groups from g down to the group id, when g has it in it
// however deep (its first step first, the id itself left out); nil when
// it hasn't.
func wayTo(all []Group, g Group, id string, in []string) []string {
	for _, m := range g.Members {
		gid, ok := strings.CutPrefix(m, GroupPrefix)
		if !ok || slices.Contains(in, gid) {
			continue
		}
		if gid == id {
			return []string{}
		}
		if sub, ok := groupOf(all, gid); ok {
			if way := wayTo(all, sub, id, append(slices.Clone(in), g.ID)); way != nil {
				return append([]string{gid}, way...)
			}
		}
	}
	return nil
}

// depthOf is how deep the groups in g go: 0 when it has none.
func depthOf(all []Group, g Group, in []string) int {
	d := 0
	for _, m := range g.Members {
		gid, ok := strings.CutPrefix(m, GroupPrefix)
		if !ok || slices.Contains(in, gid) {
			continue
		}
		if sub, ok := groupOf(all, gid); ok {
			d = max(d, 1+depthOf(all, sub, append(slices.Clone(in), g.ID)))
		}
	}
	return d
}

// GroupsWith are the groups that have the group id in them as a member.
func GroupsWith(id string) []Group {
	var out []Group
	for _, g := range groupsIn(providerEntries()) {
		if !g.Hidden && slices.Contains(g.Members, GroupPrefix+id) {
			out = append(out, g)
		}
	}
	return out
}

// MemberGroups are the routing groups each model is in, as "group/<id>",
// by the model's "provider/model" id; a member fixed at an effort counts as
// its model, and a group removed is none. A provider kept for routing
// groups (Provider.Unlisted) reaches agents through these alone: a model
// of it in none of them is used by nothing. Held while a request holds the
// catalog, so it is shared, only for reading: the providers' page asked
// for it once for each provider, 11.6 MB of what twenty polls allocated.
func MemberGroups() map[string][]string {
	return heldFrom("member-groups", providerEntries(), memberGroupsIn)
}

// memberGroupsBuilt counts MemberGroups built, for a test.
var memberGroupsBuilt atomic.Int64

func memberGroupsIn(entries []Entry) map[string][]string {
	memberGroupsBuilt.Add(1)
	out := map[string][]string{}
	for _, g := range groupsIn(entries) {
		if g.Hidden {
			continue
		}
		for _, id := range g.Members {
			if strings.HasPrefix(id, GroupPrefix) {
				continue
			}
			m, _ := memberEffortIn(entries, id)
			if !slices.Contains(out[m], GroupPrefix+g.ID) {
				out[m] = append(out[m], GroupPrefix+g.ID)
			}
		}
	}
	return out
}

// DeleteGroup removes a group of the user's; one magpie found is hidden,
// to come back with ShowGroup. A group another has in it, or classifies
// with, stays until it is taken out of that one.
func DeleteGroup(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	if in := GroupsWith(id); len(in) > 0 {
		var names []string
		for _, g := range in {
			names = append(names, g.Name)
		}
		return fmt.Errorf("%s is in %s: take it out first", id, strings.Join(names, ", "))
	}
	for _, g := range groupsIn(providerEntries()) {
		if !g.Hidden && g.Classifier == GroupPrefix+id {
			return fmt.Errorf("%s is %s's classifier: choose another first", id, g.Name)
		}
	}
	found := false
	f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool {
		if g.ID == id {
			found = true
			return true
		}
		return false
	})
	if slices.ContainsFunc(foundGroups(providerEntries()), func(g Group) bool { return g.ID == id }) {
		f.Groups = append(f.Groups, Group{ID: id, Hidden: true})
		found = true
	}
	if !found {
		return fmt.Errorf("no group %q", id)
	}
	return store(f)
}

// DeleteGroups removes several groups at once, as DeleteGroup does each
// (lc on Discord: they could only be removed one at a time). It is all or
// none: refused, before any is removed, when one isn't there or a group
// left holds one as a member or classifier; one held only by another of
// them goes after it.
func DeleteGroups(ids []string) error {
	in := map[string]bool{}
	var left []string
	for _, id := range ids {
		if !in[id] {
			in[id] = true
			left = append(left, id)
		}
	}
	all := groupsIn(providerEntries())
	for _, id := range left {
		if !slices.ContainsFunc(all, func(g Group) bool { return g.ID == id && !g.Hidden }) {
			return fmt.Errorf("no group %q", id)
		}
	}
	removedFound := slices.ContainsFunc(all, func(g Group) bool { return g.Auto && !g.Hidden && in[g.ID] })
	for _, g := range all {
		if g.Hidden || in[g.ID] {
			continue
		}
		for _, id := range left {
			if slices.Contains(g.Members, GroupPrefix+id) {
				return fmt.Errorf("%s is in %s: take it out first", id, g.Name)
			}
			if g.Classifier == GroupPrefix+id {
				return fmt.Errorf("%s is %s's classifier: choose another first", id, g.Name)
			}
		}
	}
	for len(left) > 0 {
		var next []string
		var last error
		for _, id := range left {
			if err := DeleteGroup(id); err != nil {
				next, last = append(next, id), err
			}
		}
		if len(next) == len(left) {
			return last
		}
		left = next
	}
	// Removing every group magpie found says the user wants none: a model
	// a provider serves later, or one it lists after a refresh, must not
	// bring a new one back (xwang_sh on X). The removed ones stay recorded;
	// found groups on again (SetAutoGroups) brings them as they were.
	if removedFound && AutoGroupsOn() && !slices.ContainsFunc(groupsIn(providerEntries()), func(g Group) bool { return g.Auto && !g.Hidden }) {
		return SetAutoGroups(false)
	}
	return nil
}

// SwitchGroup switches a group of the user's on or off (Group.Disabled).
// A group magpie found is the catalog's, so it is removed rather than
// switched off; and one that classifies for another stays on, or that
// group's rules would have no one to ask.
func SwitchGroup(id string, on bool) error {
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id && !g.Hidden })
	if i < 0 {
		if g, ok := groupOf(groupsIn(providerEntries()), id); ok && g.Auto {
			return fmt.Errorf("%s was found by magpie: remove it, or change it to make it yours, to switch it off", g.Name)
		}
		return fmt.Errorf("magpie has no group %q", id)
	}
	if f.Groups[i].Disabled == !on {
		return nil
	}
	if !on {
		for _, o := range f.Groups {
			if !o.Hidden && !o.Disabled && o.ID != id && o.Classifier == GroupPrefix+id {
				return fmt.Errorf("%s classifies %s's requests: pick another classifier there first", f.Groups[i].Name, o.Name)
			}
		}
	}
	f.Groups[i].Disabled = !on
	return store(f)
}

// PickGroup sends every request to a Manual group of the user's to its
// member pick from the next request on (#317), as its card's radios do,
// from wherever the user switches it: the tray panel (#2489) too. Nothing
// else of the group changes, and only a Manual group can be switched so.
func PickGroup(id, pick string) error {
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Groups, func(g Group) bool { return g.ID == id && !g.Hidden })
	if i < 0 {
		return fmt.Errorf("magpie has no group %q", id)
	}
	g := &f.Groups[i]
	if g.Routing != Manual {
		return fmt.Errorf("%s doesn't route by hand: its card picks a model only in Manual routing", g.Name)
	}
	if !slices.Contains(g.Members, pick) {
		return fmt.Errorf("%s is not in %s", pick, g.Name)
	}
	if g.Pick == pick {
		return nil
	}
	g.Pick = pick
	return store(f)
}

// DisabledGroup is the group of the user's a model id names ("group/<id>",
// or a model's as GroupFor takes it) when it is switched off.
func DisabledGroup(id string) (Group, bool) {
	gid, ok := GroupIDOf(id)
	if !ok {
		if gid, ok = GroupFor(id); !ok {
			return Group{}, false
		}
		gid = strings.TrimPrefix(gid, GroupPrefix)
	}
	if g, ok := groupOf(sharedGroups(), gid); ok && g.Disabled {
		return cloneGroups([]Group{g})[0], true
	}
	return Group{}, false
}

// RemovedGroups are the found groups the user removed, whether or not
// magpie finds them now: each is a record in providers.json ({"id", "hidden":
// true}) that keeps it removed when two providers serve its model again.
func RemovedGroups() []string {
	var out []string
	for _, g := range load().Groups {
		if g.Hidden {
			out = append(out, g.ID)
		}
	}
	return out
}

// ShowGroup brings back a group magpie found that the user had removed.
func ShowGroup(id string) error {
	f, err := read()
	if err != nil {
		return err
	}
	f.Groups = slices.DeleteFunc(f.Groups, func(g Group) bool { return g.ID == id && g.Hidden })
	return store(f)
}

// RenameGroup gives a group another id, the one agents pick it by
// (group/<id>). A group magpie found becomes the user's under the new id,
// the found one kept removed so it doesn't come back beside it. The
// groups that have it in them, and their rules, name it by the new id.
func RenameGroup(from, to string) error {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	if to == "" || to != GroupSlug(to) {
		return fmt.Errorf("a group's id must be lowercase letters, digits, dots and dashes, not %q", to)
	}
	if to == from {
		return nil
	}
	f, err := read()
	if err != nil {
		return err
	}
	all := groupsIn(providerEntries())
	g, ok := groupOf(all, from)
	if !ok {
		return fmt.Errorf("no group %q", from)
	}
	if slices.ContainsFunc(all, func(o Group) bool { return o.ID == to }) ||
		slices.ContainsFunc(f.Groups, func(o Group) bool { return o.ID == to }) {
		return fmt.Errorf("there is a group %q already", to)
	}
	found := slices.ContainsFunc(foundGroups(providerEntries()), func(o Group) bool { return o.ID == from })
	g.ID, g.Auto, g.Hidden = to, false, false
	g.Former = append(slices.DeleteFunc(slices.Clone(g.Former), func(id string) bool { return id == from || id == to }), from)
	f.Groups = slices.DeleteFunc(f.Groups, func(o Group) bool { return o.ID == from })
	dropFormer(f.Groups, to)
	if found {
		f.Groups = append(f.Groups, Group{ID: from, Hidden: true})
	}
	f.Groups = append(f.Groups, g)
	// renamed, it keeps its place in the order the user put the groups in
	if i := slices.Index(f.GroupOrder, from); i >= 0 {
		f.GroupOrder = slices.Clone(f.GroupOrder)
		f.GroupOrder[i] = to
	}
	old, now := GroupPrefix+from, GroupPrefix+to
	for i := range f.Groups {
		for j, m := range f.Groups[i].Members {
			if m == old {
				f.Groups[i].Members[j] = now
			}
		}
		for j, m := range f.Groups[i].Placed {
			if m == old {
				f.Groups[i].Placed[j] = now
			}
		}
		for j := range f.Groups[i].Rules {
			f.Groups[i].Rules[j].MapUses(func(u string) string {
				if u == old {
					return now
				}
				return u
			})
		}
		if f.Groups[i].Pick == old {
			f.Groups[i].Pick = now
		}
		for j, m := range f.Groups[i].Off {
			if m == old {
				f.Groups[i].Off[j] = now
			}
		}
		f.Groups[i].Weights = renamedWeights(f.Groups[i].Weights, func(m string) string {
			if m == old {
				return now
			}
			return m
		})
		f.Groups[i].EffortMaps = renamedEffortMaps(f.Groups[i].EffortMaps, func(m string) string {
			if m == old {
				return now
			}
			return m
		})
		if f.Groups[i].Classifier == old {
			f.Groups[i].Classifier = now
		}
	}
	if err := store(f); err != nil {
		return err
	}
	return renameGroupPicks(old, now)
}

// renameGroupPicks moves what each agent's list says of a group to its new
// id, as renameModelPrefs does a provider's models: picked for an agent
// shown only its picks, taken out, put in an order, sent fast. Left under
// the old id, a group picked for Pi was off in Pi's list after its rename
// (#2289, eachann1024), the id it was picked by no longer in the catalog.
func renameGroupPicks(old, now string) error {
	s := settings.Load()
	moved := false
	// the hidden and the picked are kept sorted (sortedIDs); an order is
	// the user's own
	for i, m := range []map[string][]string{s.HiddenModels, s.PickedModels, s.OrderedModels, s.FastPicks} {
		for agent, ids := range m {
			if !slices.Contains(ids, old) {
				continue
			}
			var out []string
			for _, id := range ids {
				if id == old {
					id = now
				}
				if !slices.Contains(out, id) {
					out = append(out, id)
				}
			}
			if i < 2 {
				out = sortedIDs(out)
			}
			m[agent], moved = out, true
		}
	}
	if !moved {
		return nil
	}
	if err := settings.Save(s); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

// A Group's Redact: what a request through it is masked by.
const (
	RedactOn     = "on"     // masked as Settings say, whatever its provider says
	RedactOff    = "off"    // sent as written
	RedactFollow = "follow" // saved: clears it, the providers' own say
)

// RedactWith is what two say of masking together: on wins over off, and
// either over nothing.
func RedactWith(a, b string) string {
	if a == RedactOn || b == RedactOn {
		return RedactOn
	}
	if a == RedactOff || b == RedactOff {
		return RedactOff
	}
	return ""
}

// Masking is what the groups the member is reached through say of
// masking, all its places counted (Also, the same model met again in
// another group): RedactOn if any says so, RedactOff only if all do, else
// "". The model's provider is the same in each place.
func (m Member) Masking() string {
	out := m.Redact
	for _, o := range m.Also {
		switch o := o.Masking(); {
		case o == RedactOn || out == RedactOn:
			out = RedactOn
		case o != out:
			out = ""
		}
	}
	return out
}

// Unmasked says a request to the member goes as written: its groups say
// so (Masking), or none says otherwise and its provider is set to send
// unmasked (Provider.SkipsRedaction).
func (m Member) Unmasked() bool { return UnmaskedBy(m.Masking(), m.Provider) }

// UnmaskedBy says a request to p goes as written under what groups say of
// masking (Group.Redact, Member.Masking): RedactOn never, RedactOff
// always, else as p is set.
func UnmaskedBy(redact string, p Provider) bool {
	switch redact {
	case RedactOn:
		return false
	case RedactOff:
		return true
	}
	return p.SkipsRedaction()
}
