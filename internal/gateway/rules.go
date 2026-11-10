package gateway

// A group's rules (provider.Rule) put one member first for the requests
// they match. A rule is looked at when a user's turn begins, and what it
// decided holds for the rest of the turn: the agent's tool results go back
// to the model that asked for them, however long the conversation grows
// meanwhile.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// ruleClock is the time a turn's rules look at (a rule's hours), and its
// decision is kept from; tests set it.
var ruleClock = time.Now

// RuleHit is what the trace tells of a group's rules for a request.
type RuleHit struct {
	N   int    `json:"n"`             // the rule, from 1; 0 when none matched
	Use string `json:"use,omitempty"` // the member it puts first
	// More: the members the rule names after Use (provider.Rule.More), in
	// order: they go next, before Then
	More []string `json:"more,omitempty"`
	When []string `json:"when,omitempty"`
	Held bool     `json:"held,omitempty"` // decided when the turn began
	// Waits: the turn began before magpie saw it (or its rule was changed
	// since), so no rule moves it; the next turn is looked at afresh
	Waits bool `json:"waits,omitempty"`
	// Grown: within the turn the conversation grew past what the model it
	// was on can take, so the rules were looked at again — the one time a
	// turn moves
	Grown  bool   `json:"grown,omitempty"`
	Turn   int    `json:"turn"`             // the user's turns so far
	Tokens int    `json:"tokens"`           // how long the request was taken to be
	Images bool   `json:"images,omitempty"` // it carries an image
	Effort string `json:"effort,omitempty"` // the reasoning asked for: a level, or "on"
	// Folder: the working folder the agent said, looked at when a rule
	// is by folder; "" when it said none
	Folder string `json:"folder,omitempty"`
	// Unready: the member the rule names has nobody to take it now, and
	// the group routes the request as it would without the rule.
	Unready bool `json:"unready,omitempty"`
	// Then: the members the rules after it that match too send to, which
	// the request fails over to, in turn, before the group's others (#112)
	Then []string `json:"then,omitempty"`
	// Instead: the one of Then that went first, Use having nobody ready
	Instead string `json:"instead,omitempty"`
	// Classified: the group's classifier was asked which intent the
	// turn's first message is
	Classified *Classified `json:"classified,omitempty"`
	// Pick: the reasoning the group's decision model picked for the turn,
	// which its requests ask their model for (provider.EffortAuto)
	Pick string `json:"pick,omitempty"`
	// Wanted: the reasoning the decision model picked for the turn when
	// the conversation kept a higher one (Pick) for the vendor's cache of
	// it (#617) — sent instead where the change goes as an effort update
	// that keeps the cache (takesEffortUpdates)
	Wanted string `json:"wanted,omitempty"`
	// Compact: the request is the agent compacting its conversation, looked
	// at on its own; Small, the members of rules it matched that take less
	// than it is long, passed over
	Compact bool     `json:"compact,omitempty"`
	Small   []string `json:"small,omitempty"`
	// Bare: the group has no rules, only its effort picked
	Bare bool `json:"bare,omitempty"`
	// Ended: the rule (from 1) that sent the conversation's last turn to
	// its member held by hours that are over now (MinglunZhu on X: a
	// cheaper provider at night, the usual one by day). No rule puts a
	// member first this turn, and the vendor's cache of the night's member
	// doesn't keep the conversation there (affine's "hours"): it goes
	// where the group routes it, as a new conversation would
	Ended int `json:"ended,omitempty"`
	// UseEffort: the reasoning the rule asks the member it names for
	// (provider.Rule.UseEffort, #2633), in place of the agent's
	UseEffort string `json:"useEffort,omitempty"`
	// members are the models of the member the rule names, as the group
	// whose rule it is has them (ruleOver): who UseEffort is for
	members []provider.Member
}

// Classified is what the classifier was asked as a turn began, and said.
type Classified struct {
	By      string   `json:"by"`               // the classifier model
	Intents []string `json:"intents"`          // what it chose among
	Intent  string   `json:"intent,omitempty"` // what it said the message is; "" for none
	// Said: the answer as it said it — the number a chat model answered
	// with (0 for none), or the choice a decision model named, its own
	// "none of these" included — kept when no intent came of it, so
	// "none" is told from an answer no rule could take (#4598)
	Said   string  `json:"said,omitempty"`
	After  string  `json:"after,omitempty"`  // what it said the turn before was, which it was told
	Effort string  `json:"effort,omitempty"` // the reasoning it said the turn wants
	Score  float64 `json:"score,omitempty"`  // how hard it took the turn to be, 0 (low) to 3 (xhigh)
	Sure   float64 `json:"sure,omitempty"`   // how confident it was of the intent, 0 to 1
	Cached bool    `json:"cached,omitempty"` // said before, for the same message
	Ms     int     `json:"ms,omitempty"`     // how long asking it took
	Error  string  `json:"error,omitempty"`  // why it couldn't say: no intent matches then, unless Fallback
	// Above: the reasoning a group above this one picked for the turn, so
	// this one's classifier wasn't asked for it (#4358): the turn reasons
	// at that, not as the agent asked
	Above string `json:"above,omitempty"`
	// Fallback: "previous" when it couldn't say and the turn keeps the
	// intent the turn before had (After), rather than none
	Fallback string `json:"fallback,omitempty"`

	// AfterEffort: the reasoning picked for the turn before, which it was told
	AfterEffort string `json:"afterEffort,omitempty"`
}

type turnRule struct {
	turn  int
	use   string // "" when no rule matched
	n     int
	at    time.Time
	input int // the tokens the vendor counted the conversation's last request as
	// intent is what the classifier said the turn's first message is
	intent string
	effort string // the reasoning it picked for the turn
	wanted string // what it would have picked, kept from (RuleHit.Wanted)
	// answered is when the vendor last answered the conversation: its
	// cache of it is read from then (ruleAnswered)
	answered time.Time
	size     int // how long magpie took the conversation's last request to be (estimate)
	// hour is what the vendor said it keeps of the conversation for an
	// hour (Anthropic's 1-hour cache writes), warm past cacheCold (#2302)
	hour hourCache
	// cold is how long the vendor that answered last keeps the
	// conversation cached unread (coldAfter); 0 is cacheCold
	cold time.Duration
}

var turnRules = struct {
	sync.Mutex
	m map[string]turnRule // group|conversation → what its turn's rule was
}{m: map[string]turnRule{}}

// ruleKey is the conversation a group's rules keep a turn's decision for:
// the agent's session and the conversation's first words (firstWords).
func ruleKey(g provider.Group, in http.Header, req *Request) string {
	return provider.GroupPrefix + g.ID + "|" + affinityID(in, nil) + "|" + firstWords(req)
}

// firstWords tells an agent's subagents from it: they share its session
// but not what they were asked. Only what the user said is taken (userSaid,
// the first user message that has any) — not how the agent marked it for
// caching, nor what it put in beside it (instruction files, reminders,
// Codex's context), which an agent such as ZCode changes as the
// conversation goes on — so it stays the same for the conversation's
// whole life. A conversation the user said nothing in yet goes by its
// first user message's text.
func firstWords(req *Request) string {
	h := sha256.New()
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		if said := userSaid(m); said != "" {
			h.Write([]byte(said))
			return hex.EncodeToString(h.Sum(nil)[:12])
		}
	}
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == Text {
				h.Write([]byte(p.Text))
			}
		}
		break
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// ruleFor is the rule a group request goes by: at a turn's start, the
// first of the group's rules it matches — asking the group's classifier
// first when a rule that may match has an intent; within the turn — the
// agent handing tool results back — what was decided when it began, unless
// the conversation has grown past what that model can take and a rule
// sends it to one with more room. A group whose decision model picks the
// effort (provider.EffortAuto) has it pick as the turn begins, when the
// agent asked for reasoning, and the turn keeps it. pick is the reasoning
// a group above this one already picked for the turn, when there is one:
// its answer is the one sent (#4358), so this group's classifier is not
// asked for the effort again — the intents, when its rules have them, are
// still this group's own routing. It returns nil for a group that decides
// nothing.
func ruleFor(key string, g provider.Group, ms []provider.Member, req *Request, agent string, ask classifier, pick string) *RuleHit {
	if !g.Ruled() || req == nil {
		return nil
	}
	turn, within := turnIn(req)
	now := ruleClock()
	size := estimate(req)
	q := provider.RuleRequest{Tokens: size, Thinking: req.Thinking, Effort: req.Effort, Off: req.ThinkOff, Agent: agent, At: now, Subagent: req.subagent}
	if provider.RulesRead(g.Rules, "message") {
		_, q.Message = lastSaid(req, len(req.Messages)) // the user's own words, as the turn began
	}
	if provider.RulesRead(g.Rules, "system") {
		q.System = req.systemTexts()
	}
	if provider.RulesRead(g.Rules, "last") {
		q.Last = lastUserTexts(req)
	}
	for _, m := range req.Messages {
		if slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == Image }) {
			q.Images = true
			break
		}
	}
	turnRules.Lock()
	tr, had := turnRules.m[key]
	turnRules.Unlock()
	if had && now.Sub(tr.at) > stickKeep {
		had = false
	}
	if had && tr.input > q.Tokens {
		q.Tokens = tr.input // what the vendor counted last, a floor: the conversation only grew since
	}
	if slices.ContainsFunc(g.Rules, provider.Rule.ByFolder) {
		q.Folder = workDir(req)
	}
	hit := &RuleHit{Turn: turn, Tokens: q.Tokens, Images: q.Images, Effort: q.Effort, Folder: q.Folder, Bare: len(g.Rules) == 0}
	defer func() {
		// whatever put a member first, the rules after it that match too
		// say who comes next
		if hit != nil && hit.Use != "" && hit.N >= 1 {
			hit.Then = provider.ThenUses(g.Rules, q, hit.N, hit.uses())
			if hit.N <= len(g.Rules) {
				hit.UseEffort = g.Rules[hit.N-1].UseEffort // and how hard it asks it to think
			}
		}
	}()
	if hit.Effort == "" && q.Thinking {
		hit.Effort = "on"
	}
	if slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Compact }) && compacting(req) {
		// the agent summarizing its conversation: the first rule it matches,
		// on its own — not the turn's decision, so the requests after it go
		// on as the turn would. The request is as long as the conversation:
		// a model known to take less would only refuse it, and is passed over.
		q.Compact, hit.Compact = true, true
		ctx := memberContexts(ms)
		for i, r := range g.Rules {
			if !r.Matches(q) {
				continue
			}
			// of the models the rule names, those that take it, in order
			var fit []string
			for _, u := range r.Uses() {
				if c := ctx[u]; c > 0 && c < q.Tokens {
					hit.Small = append(hit.Small, u)
					continue
				}
				fit = append(fit, u)
			}
			if len(fit) == 0 {
				continue
			}
			hit.N, hit.Use, hit.When = i+1, fit[0], r.Conditions()
			if len(fit) > 1 {
				hit.More = fit[1:]
			}
			break
		}
		return hit
	}
	if within {
		// the same turn: what was decided as it began, while the group
		// still has that rule; else nothing moves it
		ctx := memberContexts(ms) // before the lock: it reads the catalog
		if had {
			q.Intent = tr.intent // as the classifier said when the turn began
		}
		turnRules.Lock()
		defer turnRules.Unlock()
		switch {
		case !had || tr.turn != turn && turn > 0:
			hit.Waits = true
		case tr.use == "":
			hit.Held = true
		case tr.n >= 1 && tr.n <= len(g.Rules) && g.Rules[tr.n-1].Use == tr.use:
			hit.Held, hit.N, hit.Use, hit.More, hit.When = true, tr.n, tr.use, g.Rules[tr.n-1].More, g.Rules[tr.n-1].Conditions()
		default:
			hit.Waits = true
		}
		if !hit.Waits && hit.Effort != "" && !req.ThinkOff {
			hit.Pick, hit.Wanted = tr.effort, tr.wanted
		}
		if grown, ok := outgrown(g, ctx, hit, q); ok {
			hit = grown
			turnRules.m[key] = turnRule{turn: turn, use: hit.Use, n: hit.N, at: now, input: tr.input, intent: q.Intent, effort: tr.effort, wanted: tr.wanted, answered: tr.answered, size: size, hour: tr.hour}
			return hit
		}
		if had {
			if cur, ok := turnRules.m[key]; ok {
				cur.at, cur.size = now, size
				turnRules.m[key] = cur
			}
		}
		return hit
	}
	// a new turn: the classifier is asked only when a rule that could be
	// the first to match waits on its intent, or it picks the effort of a
	// turn the agent asked to reason in. Reasoning turned off ("none",
	// which Effort reads as low) is not asked to reason: the pick would
	// never be sent (withEffort leaves "none" as it is), only shown (#2247)
	intents := provider.Intents(g.Rules, q)
	effort := g.Effort == provider.EffortAuto && hit.Effort != "" && !req.ThinkOff
	above := ""
	if effort && pick != "" {
		// the group above picked this turn's reasoning (#4358): its answer
		// is the one sent, so asking here again is paid for and thrown
		// away (a group in it answered 27 of 28 turns on one reporter's
		// day, several seconds each, and the turn sent the group above's)
		effort, above = false, pick
		if len(intents) == 0 {
			hit.Classified = &Classified{By: g.Classifier, Above: pick}
		}
	}
	if len(intents) > 0 || effort {
		c := &Classified{By: g.Classifier, Intents: intents, Above: above}
		if had && slices.Contains(intents, tr.intent) {
			c.After = tr.intent // a message that only carries on is of its kind
		}
		prev := before{Intent: c.After}
		if had && effort {
			prev.Effort = tr.effort // and needs the reasoning it had
			c.AfterEffort = tr.effort
		}
		text := userText(req)
		// what was said before it, for a follow-up that says little of
		// its own (@HirotaNagai on X): from the same parsed, masked request
		prev.User, prev.Reply = turnBefore(req)
		switch {
		case g.Classifier == "":
			c.Error = "the group has no classifier"
		case ask == nil:
			c.Error = "nothing to ask the classifier with: this is a classifier's own call, or Claude Desktop's connection check"
		case text == "":
			c.Error = "the message has no words to classify"
		default:
			t0 := time.Now()
			v, cached, err := classify(ask, g.Classifier, intents, prev, effort, text)
			classifiedFor(g.ID, g.Classifier, err)
			v.Intent = intentOf(v, g) // the group's own sure of an intent (#5101)
			c.Intent, c.Said, c.Cached, c.Ms = v.Intent, v.Said, cached, int(time.Since(t0).Milliseconds())
			if effort {
				c.Effort, c.Score = v.Effort, v.Score
			}
			if len(intents) > 0 {
				c.Sure = v.Sure
			}
			if err != nil {
				c.Error = err.Error()
				if c.After != "" {
					// timed out, failed or resting: the intent the turn
					// before had, still one of this turn's, is a better
					// guess than none, which sends the turn to the
					// group's first member whatever it is (#1826)
					c.Intent, c.Fallback = c.After, "previous"
				}
			}
		}
		q.Intent = c.Intent
		hit.Classified, hit.Pick = c, c.Effort
		if effort && had && keepsEffort(tr, c.Effort, size, now) {
			hit.Pick, hit.Wanted = tr.effort, c.Effort
		}
	}
	if i := provider.MatchRule(g.Rules, q); i >= 0 {
		hit.N, hit.Use, hit.More, hit.When = i+1, g.Rules[i].Use, g.Rules[i].More, g.Rules[i].Conditions()
	}
	if had && hit.Use == "" && tr.use != "" && tr.n >= 1 && tr.n <= len(g.Rules) {
		if r := g.Rules[tr.n-1]; r.Use == tr.use && r.Time != nil && !r.Time.Holds(now) {
			hit.Ended = tr.n
		}
	}
	turnRules.Lock()
	defer turnRules.Unlock()
	input, answered, hour := tr.input, tr.answered, tr.hour
	if cur, ok := turnRules.m[key]; ok {
		input, answered, hour = cur.input, cur.answered, cur.hour // answered while the classifier was asked
	}
	turnRules.m[key] = turnRule{turn: turn, use: hit.Use, n: hit.N, at: now, input: input, intent: q.Intent, effort: hit.Pick, wanted: hit.Wanted, answered: answered, size: size, hour: hour}
	if len(turnRules.m) > 4096 {
		for k, tr := range turnRules.m {
			if now.Sub(tr.at) > stickKeep {
				delete(turnRules.m, k)
			}
		}
	}
	return hit
}

// outgrown is the rule a request within a turn moves by when it no longer
// fits the model the turn is on: what its rule sent it to, or, when no
// rule did, the least any member takes. It moves only to a model known to
// take more.
func outgrown(g provider.Group, ctx map[string]int, held *RuleHit, q provider.RuleRequest) (*RuleHit, bool) {
	limit := 0
	if held.Use != "" {
		limit = ctx[held.Use]
	} else {
		for _, c := range ctx {
			if c > 0 && (limit == 0 || c < limit) {
				limit = c
			}
		}
	}
	if limit == 0 || q.Tokens < limit*95/100 {
		return nil, false
	}
	i := provider.MatchRule(g.Rules, q)
	if i < 0 || g.Rules[i].Use == held.Use || ctx[g.Rules[i].Use] <= limit {
		return nil, false
	}
	out := *held
	out.Held, out.Waits, out.Grown = false, false, true
	out.N, out.Use, out.More, out.When = i+1, g.Rules[i].Use, g.Rules[i].More, g.Rules[i].Conditions()
	return &out, true
}

// memberContexts is the tokens each member takes, where known: a group in
// the group, the least any of its models takes.
func memberContexts(ms []provider.Member) map[string]int {
	out := map[string]int{}
	cat := provider.Served() // unlisted models still serve routing groups
	for _, m := range ms {
		for _, e := range cat {
			if e.Group == "" && e.Provider.ID == m.Provider.ID && e.Model == m.Model {
				// a later group in the group that has it too (#2361)
				for _, id := range append([]string{m.ID}, alsoIDs(m)...) {
					if c, ok := out[id]; !ok || e.Context > 0 && (c == 0 || e.Context < c) {
						out[id] = e.Context
					}
				}
				// a model the group names that a group in it has too (named)
				if n := len(m.Path); n > 1 {
					if _, ok := out[m.Path[n-1]]; !ok {
						out[m.Path[n-1]] = e.Context
					}
				}
				break
			}
		}
	}
	return out
}

// alsoIDs are the group's member ids of m's other places (Also).
func alsoIDs(m provider.Member) []string {
	var out []string
	for _, o := range m.Also {
		out = append(out, o.ID)
	}
	return out
}

// ruleAnswered keeps what the vendor counted a conversation's request as,
// for its next turn's rules to go by.
func ruleAnswered(key string, u Usage, cold time.Duration) {
	n := u.Input + u.CacheRead + u.CacheWrite
	if n <= 0 {
		return
	}
	turnRules.Lock()
	defer turnRules.Unlock()
	if tr, ok := turnRules.m[key]; ok {
		now := ruleClock()
		tr.input, tr.answered, tr.hour, tr.cold = n, now, tr.hour.after(u, now), cold
		turnRules.m[key] = tr
	}
}

// keepsEffort is whether a new turn of a conversation keeps the reasoning
// its turn before was asked for, rather than the lower one the decision
// model picked now (#617). The reasoning is part of what the vendor caches
// a conversation under: a turn asking for another reads none of it back —
// with group effort "auto", 40 of 42 changes between a Pi session's turns
// on gpt-6.1-sol read 0 cached tokens, against 4 of 834 without one. So
// while the cache is warm the effort only goes up — a hard turn still gets
// the reasoning it needs, and a conversation pays for at most one miss per
// level — and a lower pick waits for a turn the cache is lost to anyway:
// one after cacheCold without an answer (tr.cold at the ChatGPT backend,
// which keeps it longer; an hour, where the vendor said it
// keeps the conversation that long: tr.hour, #2302), or after the agent
// compacted the conversation (it is now under half as long as its last
// request).
func keepsEffort(tr turnRule, pick string, tokens int, now time.Time) bool {
	if tr.effort == "" || pick == "" || pick == tr.effort {
		return false
	}
	if slices.Index(effortRank, pick) > slices.Index(effortRank, tr.effort) {
		return false // up: the turn wants more than the conversation had
	}
	last := tr.at
	if tr.answered.After(last) {
		last = tr.answered
	}
	cold := tr.cold
	if cold <= 0 {
		cold = cacheCold
	}
	if now.Sub(last) > cold && !tr.hour.warm(now) {
		return false
	}
	if tr.size > 0 && tokens < tr.size/2 {
		return false
	}
	return true
}

// nestedRules looks at the rules of each group in the group that the one
// going first is of, outermost first: a group's rules pick among its own
// members as the group's do, keeping what they decided for the turn by
// their own key (at, the group's, and the group's way down), asking ask
// of the message as the group's own rules do. It gives what each decided
// and their keys.
func (s *Server) nestedRules(at string, req *Request, agent string, ask classifier, ms []provider.Member, cands []candidate, pl planned, aff *Affinity, picked string) ([]NestedRule, []string, []candidate, planned) {
	var out []NestedRule
	var keys []string
	for depth := 0; ; depth++ { // as deep as the one going first is
		i := slices.IndexFunc(ms, func(m provider.Member) bool { return ofMember(cands[0], m) })
		if i < 0 || len(ms[i].Via) <= depth {
			break
		}
		lead := ms[i]
		sub := lead.Via[depth]
		at += ">" + sub.ID
		if !sub.Ruled() {
			continue
		}
		var subMs []provider.Member
		for _, m := range ms {
			if len(m.Path) > depth+1 && m.Path[depth] == lead.Path[depth] {
				subMs = append(subMs, m.Below(depth+1))
			}
		}
		// the outermost pick stands (#4358): a group in the group asked
		// once one above it has picked only pays for an answer the turn
		// would throw away
		h := ruleFor(at, sub, subMs, req, agent, ask, picked)
		keys = append(keys, at)
		out = append(out, NestedRule{Group: sub.ID, Name: sub.Name, Rule: h})
		if h != nil && h.Pick != "" && picked == "" {
			picked = h.Pick
		}
		if h == nil || h.Use == "" {
			continue
		}
		cands, pl = ruleOver(h, subMs, cands, pl, aff)
		if led := ruleLed(h, subMs, cands); strings.HasPrefix(led, provider.GroupPrefix) {
			// further down, the way the group it chose has its models
			ms = homed(ms, append(slices.Clone(lead.Path[:depth+1]), led))
		}
	}
	return out, keys, cands, pl
}

// named reports whether m is the group's member id: the model, or one of
// the models of the group in the group it is. A model the group names
// itself that a group in it, ahead of it, names too is kept where it was
// met first (provider's membersIn), in that group, and a rule naming it
// is still its (#625); so is a model two groups in the group share, for a
// rule naming the later one (#2361).
func named(m provider.Member, id string) bool {
	_, ok := m.In(id)
	return ok
}

// membersNamed are the models of the group's member id among ms, each as
// that member has it and in its own order: a group in the group that
// shares models with one ahead of it has them in its order, not the
// other's (#2361).
func membersNamed(id string, ms []provider.Member) []provider.Member {
	var out []provider.Member
	for _, m := range ms {
		if o, ok := m.In(id); ok {
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b provider.Member) int { return a.At - b.At })
	return out
}

// uses are the members the rule names, in order: Use, then More.
func (h *RuleHit) uses() []string {
	if h == nil || h.Use == "" {
		return nil
	}
	return append([]string{h.Use}, h.More...)
}

// ruleMembers are the models of the members a rule names, among those
// ready now, in the rule's order: each a model, or a group in the group's.
// A model two of them have is the first's.
func ruleMembers(hit *RuleHit, ms []provider.Member) []provider.Member {
	var out []provider.Member
	for _, id := range hit.uses() {
		for _, m := range membersNamed(id, ms) {
			if !slices.ContainsFunc(out, func(o provider.Member) bool { return o.At == m.At }) {
				out = append(out, m)
			}
		}
	}
	return out
}

// ruleLed is the member of the rule's (Use or one of More) the one going
// first is of: Use when it is none of them.
func ruleLed(hit *RuleHit, ms []provider.Member, cs []candidate) string {
	if hit == nil || len(cs) == 0 {
		return ""
	}
	for _, id := range hit.uses() {
		if slices.ContainsFunc(membersNamed(id, ms), func(m provider.Member) bool { return ofMember(cs[0], m) }) {
			return id
		}
	}
	return hit.Use
}

// homeAt are the places (At) of the rule's models met first in the
// members it names, not shared with a group ahead of them: only those
// were planned in that member's own routing (#2407, #2361).
func homeAt(hit *RuleHit, ms []provider.Member) map[int]bool {
	out := map[int]bool{}
	uses := hit.uses()
	for _, m := range ms {
		if slices.Contains(uses, m.ID) {
			out[m.At] = true
		}
	}
	return out
}

// homed is ms with each model that has a place under path (the group's
// member ids from the top) as that place has it, in the walk's order:
// the groups in a group a rule chose decide among its models with their
// own rules, its own order and the way down it has them (#2361).
func homed(ms []provider.Member, path []string) []provider.Member {
	out := make([]provider.Member, len(ms))
	for i, m := range ms {
		out[i] = m
		if o, ok := m.Homed(path); ok {
			out[i] = o
		}
	}
	slices.SortStableFunc(out, func(a, b provider.Member) int { return a.At - b.At })
	return out
}

// modelRuled reports whether the one going first is a model the group's
// rule names itself: one a group in the group has too goes there, and that
// group's rules don't pick another over it (#625).
func modelRuled(hit *RuleHit, ms []provider.Member, cs []candidate) bool {
	if hit == nil || len(cs) == 0 {
		return false
	}
	led := ruleLed(hit, ms, cs)
	if led == "" || strings.HasPrefix(led, provider.GroupPrefix) {
		return false
	}
	return slices.ContainsFunc(membersNamed(led, ms), func(m provider.Member) bool { return ofMember(cs[0], m) })
}

// ruleOrder is who a rule's request goes to before the group's others:
// the members it names, in order, then those the rules after it that
// match too name.
func ruleOrder(hit *RuleHit, ms []provider.Member) []provider.Member {
	out := ruleMembers(hit, ms)
	if hit == nil || hit.Use == "" {
		return out
	}
	for _, id := range hit.Then {
		out = append(out, membersNamed(id, ms)...)
	}
	return out
}

// ruleFirst puts first the keys or accounts of the members in ms that
// aren't resting, a member's before the next's and each's in the order
// they had; everyone else follows as they were, to fail over to. own is
// how many of ms are the rule's own members, ahead of the rules after
// it's. lead is the one in ms whose goes first, -1 when none is ready.
func ruleFirst(ms []provider.Member, cs []candidate, pl planned, own int, home map[int]bool) (out []candidate, _ planned, lead int) {
	rank := func(i int) int {
		if pl.order[i].Rest != nil {
			return -1
		}
		at := slices.IndexFunc(ms, func(m provider.Member) bool { return ofMember(cs[i], m) })
		if at < 0 || at >= own {
			return at
		}
		// the group in the group the rule names is one member here: its
		// models rank as the group's block, in the order the group's own
		// routing planned them, not the group's listed order — ranked
		// apart, a rotate group in the group was pinned to its first
		// member on every request the rule put it first (#2407). A model
		// it shares with a group ahead of it was planned in that group's
		// order, so it keeps its rank in the named group's (#2361).
		if len(ms[at].Path) > 1 && home[ms[at].At] {
			if first := slices.IndexFunc(ms[:own], func(m provider.Member) bool { return m.ID == ms[at].ID && home[m.At] }); first >= 0 {
				return first
			}
		}
		return at
	}
	idx := make([]int, len(cs))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		ra, rb := rank(a), rank(b)
		switch {
		case ra == rb:
			return 0
		case ra < 0:
			return 1
		case rb < 0:
			return -1
		}
		return ra - rb
	})
	lead = rank(idx[0])
	if lead < 0 {
		return cs, pl, -1
	}
	order := make([]Weighed, len(cs))
	out = make([]candidate, len(cs))
	for j, i := range idx {
		out[j], order[j] = cs[i], pl.order[i]
	}
	pl.order = order
	return out, pl, lead
}

// ruleOver puts a rule's members first, over whoever answered the
// conversation last as a turn begins. Within the turn the rule held (#1946),
// the one the turn stays on stays first while it is of the rule's member
// that goes first — the one it names or, that resting, the one a rule after
// it does — and the rule's other members follow it, ahead of the group's
// others, so a failure fails over within the member the rule chose before
// going outside it. One kept outside it, which took over when it failed,
// isn't held there: the turn goes back to the rule's member once one of it
// is ready again, its rest over.
func ruleOver(hit *RuleHit, ms []provider.Member, cs []candidate, pl planned, aff *Affinity) (out []candidate, _ planned) {
	defer func() { homeCands(out, ruleMembers(hit, ms)) }()
	if hit.UseEffort != "" {
		hit.members = ruleMembers(hit, ms)
	}
	kept := aff != nil && aff.Kept
	if hit.Held && kept && len(cs) > 1 {
		ordered := ruleOrder(hit, ms)
		ids := append(hit.uses(), hit.Then...)
		place := func(i int) int { // which of the rule's members cs[i] is of: Use 0, then More's, then the Then's
			for _, m := range ordered {
				if !ofMember(cs[i], m) {
					continue
				}
				for j, id := range ids {
					if named(m, id) {
						return j
					}
				}
			}
			return -1
		}
		best := -1
		for i := range cs {
			if p := place(i); p >= 0 && pl.order[i].Rest == nil && (best < 0 || p < best) {
				best = p
			}
		}
		if p := place(0); best < 0 || p == best {
			// it stays: the rule's members after it, then the group's
			sub := pl
			sub.order = pl.order[1:]
			rest, sub, _ := ruleFirst(ordered, cs[1:], sub, len(ruleMembers(hit, ms)), homeAt(hit, ms))
			cs = append([]candidate{cs[0]}, rest...)
			pl.order = append([]Weighed{pl.order[0]}, sub.order...)
			if p >= 0 {
				// one of More is the rule's own: ready, though not Use
				hit.Unready = p > len(hit.More)
				if p > 0 {
					hit.Instead = ids[p]
				}
			}
			return cs, pl
		}
	}
	was := cs[0]
	cs, pl, ok := applyRule(hit, ms, cs, pl)
	if ok && kept && cs[0].rest != was.rest {
		aff.Kept, aff.Why = false, "rule"
	}
	return cs, pl
}

// ruleEffort is the reasoning a rule asks its request for (#2633), fitted
// to the levels c's model has: that of the outermost group's rule naming
// one of c's members, as the turn's pick is the outermost group's. A
// member the group fails over to is asked it too (#3664): the effort is
// the turn's decision, so a provider that turns the request away doesn't
// leave the member it went to instead at the agent's own level — and a
// rule that names c wins over one it only fails over from. A nested
// group's rule decides for that group's members only, so a member of the
// outer group outside it isn't asked its effort (ms are the outer
// group's, each with the groups it is reached through). A model whose
// levels aren't known is asked as it would be without the rule: magpie
// can't tell it thinks. "" for none.
// A rule after it that only fails over (Then) asks nothing of its own:
// its member is asked the first rule's, as every other member is.
func ruleEffort(c candidate, hit *RuleHit, nested []NestedRule, ms []provider.Member) string {
	hits := []*RuleHit{hit}
	for _, n := range nested {
		hits = append(hits, n.Rule)
	}
	within := func(group string) bool {
		return slices.ContainsFunc(ms, func(m provider.Member) bool { return ofMember(c, m) && slices.Contains(m.Groups(), group) })
	}
	asked := "" // the effort of the first rule with one, unless one names c
	for i, h := range hits {
		if h == nil || h.UseEffort == "" || i > 0 && !within(nested[i-1].Group) {
			continue
		}
		if asked == "" {
			asked = h.UseEffort
		}
		if slices.ContainsFunc(h.members, func(m provider.Member) bool { return ofMember(c, m) }) {
			asked = h.UseEffort
			break
		}
	}
	if asked == "" {
		return ""
	}
	levels := c.p.Efforts(c.model)
	if len(levels) == 0 {
		return ""
	}
	return fitLevel(asked, levels)
}

// homeCands sends the keys or accounts of the rule's members as the
// member the rule names has them: fast, and its first token's wait, where
// a group ahead of it has the same model too (#2361).
func homeCands(cs []candidate, ms []provider.Member) {
	for i := range cs {
		if j := slices.IndexFunc(ms, func(m provider.Member) bool { return ofMember(cs[i], m) }); j >= 0 {
			cs[i].fast, cs[i].firstToken = ms[j].Fast, innerFirstToken(ms[j])
		}
	}
}

// applyRule puts a rule's members first (ruleOrder) and says in the hit
// whether the one it names first leads, and who does when it doesn't; it
// reports whether any of them was ready.
func applyRule(hit *RuleHit, ms []provider.Member, cs []candidate, pl planned) ([]candidate, planned, bool) {
	ordered := ruleOrder(hit, ms)
	own := len(ruleMembers(hit, ms))
	lead := -1
	if len(ordered) > 0 && len(cs) > 0 {
		cs, pl, lead = ruleFirst(ordered, cs, pl, own, homeAt(hit, ms))
	}
	// ordered has the rule's own first (Use's, then More's), then those
	// of the rules after it
	hit.Unready = lead < 0 || lead >= own
	if lead >= 0 && !named(ordered[lead], hit.Use) {
		next := append(slices.Clone(hit.More), hit.Then...)
		hit.Instead = ordered[lead].ID
		if i := slices.IndexFunc(next, func(id string) bool { return named(ordered[lead], id) }); i >= 0 {
			hit.Instead = next[i]
		}
	}
	return cs, pl, lead >= 0
}

// ofMember reports whether a candidate is one of the member's keys or
// accounts: its rest is the provider's id, or that with the key ("#") or
// the account ("@") after it, for the member's model at the member's
// effort.
func ofMember(c candidate, m provider.Member) bool {
	id := m.Provider.ID
	return c.model == m.Model && c.effort == m.Effort && slices.Equal(c.levels, m.Levels) && (c.rest == id || strings.HasPrefix(c.rest, id+"#") || strings.HasPrefix(c.rest, id+"@"))
}

// membersImageInput is whether a group request may carry images: when a
// member takes them it may (as the group's catalog entry says), and a
// request with an image goes to the members that see it, the others
// given it described or left out (#756). A rule's member first changes
// none of that: one that can't see is passed over for an image.
func membersImageInput(ms []provider.Member) *bool {
	var out *bool
	cat := provider.Served() // an unlisted member counts as much as the others
	for i, m := range ms {
		var in *bool
		for _, e := range cat {
			if e.Group == "" && e.Provider.ID == m.Provider.ID && e.Model == m.Model {
				in = e.ImageInput
				break
			}
		}
		if i == 0 {
			out = in
			continue
		}
		out = anyImageInput(out, in)
	}
	return out
}

// anyImageInput is provider's: one member that takes images is enough,
// both text-only is text-only, and otherwise it is unknown.
func anyImageInput(a, b *bool) *bool {
	if a != nil && *a {
		return a
	}
	if b != nil && *b {
		return b
	}
	if a != nil && b != nil {
		return a
	}
	return nil
}

// pausedError tells an agent that every model of the group it asked for
// is paused now, by which rules and when.
func pausedError(asked string, paused []provider.Paused) string {
	var bits []string
	for _, p := range paused {
		by := fmt.Sprintf("rule %d", p.Rule)
		if p.Group != "" {
			by = fmt.Sprintf("rule %d of %s", p.Rule, p.Group)
		}
		bits = append(bits, fmt.Sprintf("%s (%s: %s)", p.Member, by, strings.Join(p.When, " · ")))
	}
	return fmt.Sprintf("every model of %s is paused now by its rules: %s; change the rules on Magpie's Routing page, or ask again when they end", asked, strings.Join(bits, ", "))
}
