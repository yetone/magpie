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
// own group's, ordered by its own routing and rules. A group can't be in
// itself, however deep: SaveGroup refuses the loop, and one written into
// providers.json by hand is cut where it closes.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// GroupPrefix starts a group's id in the catalog: "group/<id>".
const GroupPrefix = "group/"

// GroupIDOf is the group id a ref names ("group/<id>"), with spaces and the
// [1m] mark Claude Code puts on a 1M window taken off, as an agent's own
// settings carry it. ok is false for a ref that isn't a group's.
func GroupIDOf(ref string) (string, bool) {
	return strings.CutPrefix(strings.TrimSuffix(strings.TrimSpace(ref), "[1m]"), GroupPrefix)
}

// Affinities are how long a conversation stays with the key or account that
// answered it: "" auto, as long as what the vendor cached of it is worth
// keeping; for the whole session; within a turn only, free to move when
// the user speaks again; or never.
var Affinities = []string{"", AffinitySession, AffinityTurn, AffinityOff}

const (
	AffinitySession = "session"
	AffinityTurn    = "turn"
	AffinityOff     = "off"
)

// EffortAuto is a group whose classifier picks each turn's reasoning
// effort (Group.Effort).
const EffortAuto = "auto"

// Ruled reports whether the group decides anything as a user's turn
// begins: a rule to put a member first, or the turn's effort.
func (g Group) Ruled() bool { return len(g.Rules) > 0 || g.Effort == EffortAuto }

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

// Group is a routing group.
// ContextSmallest is a group's Context when agents are told the window of
// its smallest member.
const ContextSmallest = -1

type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"` // "provider/model[:effort]" or "group/<id>", in order (see MemberEffort)
	// Match are patterns its models are found by as well (#766): a glob
	// over the "provider/model" id with * in it ("openrouter/*:free"), or
	// a regular expression after "re:". Kept apart from Members, so the
	// group follows the catalog: read, it has the models they match now
	// after those it names (see withMatches).
	Match []string `json:"match,omitempty"`
	// Matched are the members the patterns match now, as Members has them
	// after its own. Derived each time the group is read, never stored.
	Matched  []string `json:"matched,omitempty"`
	Routing  string   `json:"routing,omitempty"`  // as Provider.Routing, over all the members' keys and accounts; or Manual
	Affinity string   `json:"affinity,omitempty"` // as Provider.Affinity
	// Sink is Provider.Sink over the group's members' accounts and keys
	// together: one rate limited with quota left goes behind every other
	// member's. Not for "rotate" or Manual.
	Sink bool `json:"sink,omitempty"`
	// FirstToken is how many seconds a member's streamed reply may take to
	// its first content — text, reasoning or a tool call — before the next
	// member is asked instead, nothing of it having reached the agent; 0
	// waits as long as it takes. The last one left is always waited for,
	// and the slow one doesn't rest.
	FirstToken int `json:"firstToken,omitempty"`
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
	// Family is a tag the group goes by in which agents are shown it
	// (settings' Visible), with its id.
	Family string `json:"family,omitempty"`
	// Auto is set on a group magpie found: one model served by several
	// providers. It is derived, never stored.
	Auto bool `json:"auto,omitempty"`
	// Hidden is stored for a found group the user removed.
	Hidden bool `json:"hidden,omitempty"`
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
	Via      []Group
	Provider Provider
	Model    string // what the vendor is asked for
	// Effort is the reasoning the member is fixed at ("provider/model:low"),
	// asked of the model whatever the agent or the group's classifier
	// asked; "" follows the group.
	Effort string
	// Fast is set on a member the group sends in its vendor's fast mode
	// (Group.Fast), where its model has one.
	Fast bool
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
	return Member{ID: m.Path[depth], Path: m.Path[depth:], Via: m.Via[depth:], Provider: m.Provider, Model: m.Model, Effort: m.Effort, Fast: m.Fast}
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

func groupsIn(entries []Entry) []Group {
	f := heldOf("file", load)
	var out []Group
	hidden := map[string]bool{}
	for _, g := range f.Groups {
		if g.Hidden {
			hidden[g.ID] = true
			continue
		}
		out = append(out, withMatches(entries, g))
	}
	if f.NoAutoGroups {
		return orderedGroups(out, f.GroupOrder)
	}
	for _, g := range autoGroups(entries, heldSettings().ModelSameAs) {
		if slices.ContainsFunc(out, func(o Group) bool { return o.ID == g.ID }) {
			continue // the user changed it: theirs now
		}
		g.Hidden = hidden[g.ID]
		out = append(out, g)
	}
	return orderedGroups(out, f.GroupOrder)
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

var groupDots = regexp.MustCompile(`\.{2,}`)

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
	return sameModel(model)
}

// AutoStandIn is the model a request for a group magpie found goes to while
// such groups are off (SetAutoGroups): its model, from the first provider
// that serves it, as "provider/model". An agent set to the group, or a
// session begun on it, keeps working, on one provider. ok is false for any
// other id, a group the user has of that id, or while found groups are on.
func AutoStandIn(id string) (string, bool) {
	gid, ok := GroupIDOf(id)
	if !ok || !strings.HasPrefix(gid, "auto-") || AutoGroupsOn() {
		return "", false
	}
	entries := providerEntries()
	if _, ok := groupOf(groupsIn(entries), gid); ok {
		return "", false
	}
	for _, e := range entries {
		if AutoGroupOf(e.Provider.ID, e.Model) == gid {
			return e.ID, true
		}
	}
	return "", false
}

// autoGroups are the models more than one ready provider serves under the
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
		out = append(out, g)
	}
	return out
}

// sameModel is a model's name as vendors agree on it: lowercase, without
// the vendor's own prefix ("anthropic/claude-sonnet-5" is claude-sonnet-5,
// "accounts/fireworks/models/…" too), with a version's dot as Anthropic
// writes it ("claude-opus-5.5" is claude-opus-5-5, and so is Fireworks'
// "p" for the dot, "deepseek-v4p1"), "_" as "-", and without the snapshot
// date some add: "claude-opus-5-5-20260801", Vertex's "…@20260801", and
// Volcengine Ark's six digits ("deepseek-v4-1-flash-260910" is
// deepseek-v4-1-flash, #583). Anything else stays: a variant after ":"
// (":batch"), -flash, -thinking, and a four-digit release (qwen's -2507,
// kimi-k2-0905) that is a model of its own.
func sameModel(id string) string {
	k := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "_", "-")
	if i := strings.LastIndex(k, "/"); i >= 0 {
		k = k[i+1:]
	}
	b := []byte(k)
	for i := 1; i+1 < len(b); i++ {
		if (b[i] == '.' || b[i] == 'p') && isDigit(b[i-1]) && isDigit(b[i+1]) {
			b[i] = '-'
		}
	}
	k = string(b)
	if i := strings.LastIndexAny(k, "-@"); i > 0 && snapshotDate(k[i+1:]) {
		k = k[:i]
	}
	return k
}

// snapshotDate is whether s is the date a vendor dates a snapshot of a
// model by: YYYYMMDD from 2000 on, or Ark's YYMMDD from 2023 on, with a
// month and a day that are one.
func snapshotDate(s string) bool {
	if strings.Trim(s, "0123456789") != "" {
		return false
	}
	switch {
	case len(s) == 8 && strings.HasPrefix(s, "20"):
		return true
	case len(s) == 6:
		yy, mm, dd := atoi2(s[0:2]), atoi2(s[2:4]), atoi2(s[4:6])
		return yy >= 23 && yy <= 39 && mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31
	}
	return false
}

func atoi2(s string) int { return int(s[0]-'0')*10 + int(s[1]-'0') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// FindGroup looks a group up by its catalog id ("group/<id>") and resolves
// its members; one not ready now is left out.
func FindGroup(id string) (Group, []Member, bool) {
	return GroupFinder()(id)
}

// GroupFor is the group a model's id without a provider in it names, as
// "group/<id>": the group of that id, else the group of that model however
// a vendor spells it ("grok-4.7" is the group grok-4-7 or auto-grok-4-7).
// A request for the model is the group's then, as it would be for the
// group's own id; ok is false when no group has it. An id with a provider
// in it ("a/m") names that provider's model, never a group.
func GroupFor(id string) (string, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	all := Groups()
	k := Slug(sameModel(id))
	for _, gid := range []string{strings.ToLower(id), k, "auto-" + k} {
		if g, ok := groupOf(all, gid); ok {
			return GroupPrefix + g.ID, true
		}
	}
	return "", false
}

// GroupFinder is FindGroup for looking up many: every provider's models
// are read once, when the first group is looked up, not again for each.
func GroupFinder() func(id string) (Group, []Member, bool) {
	var (
		entries []Entry
		all     []Group
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
			all = groupsIn(entries)
		}
		if g, ok := groupOf(all, gid); ok && !g.Disabled {
			return g, membersIn(entries, all, g), true
		}
		return Group{}, nil, false
	}
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

// membersIn are a group's models, ready now, in its order: a group in it
// gives its own there, as deep as they go. A model met again is left
// where it was first — the same model at another effort is another
// member — and a group that would be in itself is cut there. A manual
// group has only the member picked (see Manual), however deep.
func membersIn(entries []Entry, all []Group, g Group) []Member {
	var out []Member
	seen := map[string]bool{}
	var walk func(g Group, path []string, via []Group, in []string)
	walk = func(g Group, path []string, via []Group, in []string) {
		for _, id := range g.routes() {
			at := append(slices.Clone(path), id)
			if gid, ok := strings.CutPrefix(id, GroupPrefix); ok {
				sub, ok := groupOf(all, gid)
				if !ok || sub.Disabled || slices.Contains(in, gid) || len(via) >= maxNest {
					continue // gone, switched off, a loop, or deeper than anyone nests
				}
				walk(sub, at, append(slices.Clone(via), sub.Live()), append(slices.Clone(in), gid))
				continue
			}
			model, effort := memberEffortIn(entries, id)
			p, m, ok := resolveIn(entries, model)
			key := WithMemberEffort(p.ID+"/"+m, effort)
			if !ok || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Member{ID: at[0], Path: at, Via: via, Provider: p, Model: m, Effort: effort, Fast: g.IsFast(id)})
		}
	}
	walk(g, nil, nil, []string{g.ID})
	return out
}

// groupEntries are the catalog's groups: each with a member ready, named
// as the user named it, answering for its first member when an agent asks
// what the model can do, and reasoning when any member does — a member
// that doesn't think doesn't take reasoning from the others, as a
// text-only member doesn't take images (#756) — offering the reasoning
// levels the members have in common, as before, but for those fixed at
// an effort of their own, which take whatever the agent asks, and those
// whose levels nothing magpie reads knows (levelsUnknown), which are
// sent it as asked, and those that think with no levels to pick from,
// which are sent it as asked up to high (the gateway's fitFor). With
// every member fixed, the group offers the levels they are fixed at, so
// that an agent still asks it to reason. A group that names its own
// levels (Group.Levels) offers those, and so does a group in it for its
// models.
func groupEntries(entries []Entry) []Entry {
	var out []Entry
	all := groupsIn(entries)
	for _, g := range all {
		if g.Hidden || g.Disabled {
			continue
		}
		ms := membersIn(entries, all, g)
		if len(ms) == 0 {
			continue
		}
		e := Entry{ID: GroupPrefix + g.ID, Model: ms[0].Model, Name: g.Name, Provider: ms[0].Provider, Group: g.ID, Named: !g.Auto}
		var fixed []string // the efforts members are fixed at
		levelled := false  // a member that follows the agent's effort was met
		// Codex's ultra (max, with Codex handing parts of the task to agents
		// of its own) is a level of OpenAI's models that offer it (a ChatGPT
		// account's, or one served elsewhere: OffersUltra); a group with one
		// of them in it, every member at max, offers it too, and the
		// gateway sends max to the members that have no ultra
		ultra := false
		chatgpt := false // a ChatGPT account answers for a member
		smallest := 0    // the smallest window a member is known to have
		for i, m := range ms {
			chatgpt = chatgpt || m.Provider.Account != nil && m.Provider.Account.Agent == "codex"
			if !slices.ContainsFunc(ms[:i], func(o Member) bool { return o.Provider.ID == m.Provider.ID }) {
				e.Icons = append(e.Icons, m.Provider.Icon) // each provider once, "" for one without
			}
			var efforts []string
			images, thinks, ctx, output := false, false, 0, 0
			var imageInput *bool
			unknown := false
			for _, x := range entries {
				if x.Provider.ID == m.Provider.ID && x.Model == m.Model {
					efforts, images, thinks, ctx, output, imageInput = x.Efforts, x.Images, x.Reasoning, x.Context, x.Output, x.ImageInput
					unknown = levelsUnknown(x)
				}
			}
			// it reasons when a member does: the gateway sends the
			// effort to the members that take it, and a member known not
			// to think is sent none of it, as a text-only member is sent
			// no image (#756's rule for images, applied to reasoning).
			// One that doesn't think is no reason to tell the agent the
			// group can't. Which levels the group offers is left as it
			// was: a member known to take none still leaves the group
			// none (#597), and the group is then Levelless, a shape
			// magpie serves
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
			// the window agents are told is the largest a member has:
			// a conversation too long for one member goes on to a member
			// with room for it (#700, withRoom in the gateway), so the
			// agent compacts only when none has. The shortest member's,
			// as it was, had Pi compact a group of 1M DeepSeeks at 200k
			// for the one free model among them that holds 200k (#712)
			if ctx > e.Context {
				e.Context = ctx
			}
			if ctx > 0 && (smallest == 0 || ctx < smallest) {
				smallest = ctx
			}
			if i == 0 {
				e.ImageInput = imageInput
			} else {
				e.ImageInput = anyImageInput(e.ImageInput, imageInput)
			}
			// a group in the group that names its own levels offers them for
			// its models (the outermost that does)
			if i := slices.IndexFunc(m.Via, func(v Group) bool { return len(v.Levels) > 0 }); i >= 0 {
				efforts = m.Via[i].Levels
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
		e.Reasoning = e.Reasoning || len(e.Efforts) > 0
		if e.ImageInput != nil && !*e.ImageInput {
			e.Images = false
		}
		ruledEntry(&e, g.Live(), ms, entries)
		if g.Context > 0 {
			e.Context = g.Context
		} else if g.Context == ContextSmallest && smallest > 0 {
			e.Context = smallest
		}
		e.Family = g.Family
		out = append(out, e)
	}
	return out
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
	return !e.Reasoning && len(e.Efforts) == 0 && !levelsUnknown(e)
}

// SaveGroup adds or replaces a group of the user's. Changing one magpie
// found makes it the user's.
func SaveGroup(g Group) error {
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
	g.FirstToken = max(g.FirstToken, 0)
	if g.Context < 0 {
		g.Context = ContextSmallest
	}
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
	for i, m := range g.Members {
		if model, effort := memberEffortIn(entries, m); effort != "" && strings.HasPrefix(model, GroupPrefix) {
			return fmt.Errorf("%s is a group: its models reason as it says, so it takes no effort of its own (:%s)", model, effort)
		}
		g.Members[i] = cleanMember(entries, m)
	}
	own := cleanList(g.Members[:named])
	g.Members = append(slices.Clone(own), slices.DeleteFunc(cleanList(g.Members[named:]), func(m string) bool { return slices.Contains(own, m) })...)
	var off []string
	for _, m := range cleanList(g.Off) {
		if m = cleanMember(entries, m); slices.Contains(g.Members, m) && !slices.Contains(off, m) {
			off = append(off, m)
		}
	}
	g.Off = off
	if g.Routing != Manual && len(g.Members) > 0 && len(g.Off) == len(g.Members) {
		return fmt.Errorf("every model in %s is switched off: switch one on, or it has nothing to send to", g.Name)
	}
	for i := range g.Rules {
		g.Rules[i].Use = cleanMember(entries, strings.TrimSpace(g.Rules[i].Use))
	}
	if err := groupsInGroup(g, groupsIn(entries)); err != nil {
		return err
	}
	for _, m := range g.Members {
		if IsDecider(m) {
			return fmt.Errorf("%s decides a group's model and effort; it holds no conversation, so it can only be the group's classifier", m)
		}
	}
	if g.Routing != Ordered && g.Routing != Rotate && g.Routing != LeastUsed && g.Routing != Pace && g.Routing != Manual {
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
	g.Auto, g.Hidden = false, false
	g.Members = own // what the patterns match is found again as it is read
	g.Disabled = false
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
// of it in none of them is used by nothing.
func MemberGroups() map[string][]string {
	entries := providerEntries()
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
	if slices.ContainsFunc(autoGroups(providerEntries(), settings.Load().ModelSameAs), func(g Group) bool { return g.ID == id }) {
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
	if g, ok := groupOf(Groups(), gid); ok && g.Disabled {
		return g, true
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
	found := slices.ContainsFunc(autoGroups(providerEntries(), settings.Load().ModelSameAs), func(o Group) bool { return o.ID == from })
	g.ID, g.Auto, g.Hidden = to, false, false
	f.Groups = slices.DeleteFunc(f.Groups, func(o Group) bool { return o.ID == from })
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
		for j, r := range f.Groups[i].Rules {
			if r.Use == old {
				f.Groups[i].Rules[j].Use = now
			}
		}
		if f.Groups[i].Pick == old {
			f.Groups[i].Pick = now
		}
		for j, m := range f.Groups[i].Off {
			if m == old {
				f.Groups[i].Off[j] = now
			}
		}
		if f.Groups[i].Classifier == old {
			f.Groups[i].Classifier = now
		}
	}
	return store(f)
}
