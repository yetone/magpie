package gateway

// A group's rules (provider.Rule) put one member first for the requests
// they match. A rule is looked at when a user's turn begins, and what it
// decided holds for the rest of the turn: the agent's tool results go back
// to the model that asked for them, however long the conversation grows
// meanwhile.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// RuleHit is what the trace tells of a group's rules for a request.
type RuleHit struct {
	N    int      `json:"n"`             // the rule, from 1; 0 when none matched
	Use  string   `json:"use,omitempty"` // the member it puts first
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
	// Unready: the member the rule names has nobody to take it now, and
	// the group routes the request as it would without the rule.
	Unready bool `json:"unready,omitempty"`
	// Classified: the group's classifier was asked which intent the
	// turn's first message is
	Classified *Classified `json:"classified,omitempty"`
}

// Classified is what the classifier was asked as a turn began, and said.
type Classified struct {
	By      string   `json:"by"`               // the classifier model
	Intents []string `json:"intents"`          // what it chose among
	Intent  string   `json:"intent,omitempty"` // what it said the message is; "" for none
	Cached  bool     `json:"cached,omitempty"` // said before, for the same message
	Ms      int      `json:"ms,omitempty"`     // how long asking it took
	Error   string   `json:"error,omitempty"`  // why it couldn't say: no intent matches then
}

type turnRule struct {
	turn  int
	use   string // "" when no rule matched
	n     int
	at    time.Time
	input int // the tokens the vendor counted the conversation's last request as
	// intent is what the classifier said the turn's first message is
	intent string
}

var turnRules = struct {
	sync.Mutex
	m map[string]turnRule // group|conversation → what its turn's rule was
}{m: map[string]turnRule{}}

// ruleKey is the conversation a group's rules keep a turn's decision for:
// the agent's session and the conversation's first words (firstWords).
func ruleKey(g provider.Group, in http.Header, req *Request) string {
	return provider.GroupPrefix + g.ID + "|" + conversationID(in, nil) + "|" + firstWords(req)
}

// firstWords tells an agent's subagents from it: they share its session
// but not what they were asked. Only the words are taken — not how the
// agent marked them for caching — as they stay the same for the
// conversation's whole life.
func firstWords(req *Request) string {
	h := sha256.New()
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
// sends it to one with more room. It returns nil for a group without
// rules.
func ruleFor(key string, g provider.Group, ms []provider.Member, req *Request, agent string, ask classifier) *RuleHit {
	if len(g.Rules) == 0 || req == nil {
		return nil
	}
	turn, within := turnIn(req)
	q := provider.RuleRequest{Tokens: estimate(req), Thinking: req.Thinking, Effort: req.Effort, Agent: agent}
	for _, m := range req.Messages {
		if slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == Image }) {
			q.Images = true
			break
		}
	}
	now := time.Now()
	turnRules.Lock()
	tr, had := turnRules.m[key]
	turnRules.Unlock()
	if had && now.Sub(tr.at) > stickKeep {
		had = false
	}
	if had && tr.input > q.Tokens {
		q.Tokens = tr.input // what the vendor counted last, a floor: the conversation only grew since
	}
	hit := &RuleHit{Turn: turn, Tokens: q.Tokens, Images: q.Images, Effort: q.Effort}
	if hit.Effort == "" && q.Thinking {
		hit.Effort = "on"
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
			hit.Held, hit.N, hit.Use, hit.When = true, tr.n, tr.use, g.Rules[tr.n-1].Conditions()
		default:
			hit.Waits = true
		}
		if grown, ok := outgrown(g, ctx, hit, q); ok {
			hit = grown
			turnRules.m[key] = turnRule{turn: turn, use: hit.Use, n: hit.N, at: now, input: tr.input, intent: q.Intent}
			return hit
		}
		if had {
			if cur, ok := turnRules.m[key]; ok {
				cur.at = now
				turnRules.m[key] = cur
			}
		}
		return hit
	}
	// a new turn: the classifier is asked only when a rule that could be
	// the first to match waits on its intent
	if intents := provider.Intents(g.Rules, q); len(intents) > 0 {
		c := &Classified{By: g.Classifier, Intents: intents}
		text := userText(req)
		switch {
		case g.Classifier == "":
			c.Error = "the group has no classifier"
		case ask == nil:
			c.Error = "nothing to ask the classifier with"
		case text == "":
			c.Error = "the message has no words to classify"
		default:
			t0 := time.Now()
			intent, cached, err := classify(ask, g.Classifier, intents, text)
			c.Intent, c.Cached, c.Ms = intent, cached, int(time.Since(t0).Milliseconds())
			if err != nil {
				c.Error = err.Error()
			}
		}
		q.Intent = c.Intent
		hit.Classified = c
	}
	if i := provider.MatchRule(g.Rules, q); i >= 0 {
		hit.N, hit.Use, hit.When = i+1, g.Rules[i].Use, g.Rules[i].Conditions()
	}
	turnRules.Lock()
	defer turnRules.Unlock()
	input := tr.input
	if cur, ok := turnRules.m[key]; ok {
		input = cur.input // answered while the classifier was asked
	}
	turnRules.m[key] = turnRule{turn: turn, use: hit.Use, n: hit.N, at: now, input: input, intent: q.Intent}
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
	out.N, out.Use, out.When = i+1, g.Rules[i].Use, g.Rules[i].Conditions()
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
				if c, ok := out[m.ID]; !ok || e.Context > 0 && (c == 0 || e.Context < c) {
					out[m.ID] = e.Context
				}
				break
			}
		}
	}
	return out
}

// ruleAnswered keeps what the vendor counted a conversation's request as,
// for its next turn's rules to go by.
func ruleAnswered(key string, u Usage) {
	n := u.Input + u.CacheRead + u.CacheWrite
	if n <= 0 {
		return
	}
	turnRules.Lock()
	defer turnRules.Unlock()
	if tr, ok := turnRules.m[key]; ok {
		tr.input = n
		turnRules.m[key] = tr
	}
}

// nestedRules looks at the rules of each group in the group that the one
// going first is of, outermost first: a group's rules pick among its own
// members as the group's do, keeping what they decided for the turn by
// their own key (at, the group's, and the group's way down). It gives
// what each decided and their keys.
func (s *Server) nestedRules(at string, req *Request, agent string, ms []provider.Member, cands []candidate, pl planned, aff *Affinity) ([]NestedRule, []string, []candidate, planned) {
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
		if len(sub.Rules) == 0 {
			continue
		}
		var subMs []provider.Member
		for _, m := range ms {
			if len(m.Path) > depth+1 && m.Path[depth] == lead.Path[depth] {
				subMs = append(subMs, m.Below(depth+1))
			}
		}
		h := ruleFor(at, sub, subMs, req, agent, s.askClassifier)
		keys = append(keys, at)
		out = append(out, NestedRule{Group: sub.ID, Name: sub.Name, Rule: h})
		if h == nil || h.Use == "" || h.Held && aff != nil && aff.Kept {
			continue
		}
		was := cands[0]
		ok := false
		if ruled := ruleMembers(h, subMs); len(ruled) > 0 {
			cands, pl, ok = ruleFirst(ruled, cands, pl)
		}
		h.Unready = !ok
		if ok && aff != nil && aff.Kept && cands[0].rest != was.rest {
			aff.Kept, aff.Why = false, "rule"
		}
	}
	return out, keys, cands, pl
}

// ruleMembers are the models of the member a rule names, among those
// ready now: the model, or a group in the group's.
func ruleMembers(hit *RuleHit, ms []provider.Member) []provider.Member {
	if hit == nil || hit.Use == "" {
		return nil
	}
	var out []provider.Member
	for _, m := range ms {
		if m.ID == hit.Use {
			out = append(out, m)
		}
	}
	return out
}

// ruleFirst puts first the member's keys or accounts that aren't resting,
// in the order they had; everyone else follows as they were, to fail over
// to. It reports false when the member has none ready.
func ruleFirst(ms []provider.Member, cs []candidate, pl planned) ([]candidate, planned, bool) {
	var first, rest []candidate
	var wFirst, wRest []Weighed
	for i, c := range cs {
		if slices.ContainsFunc(ms, func(m provider.Member) bool { return ofMember(c, m) }) && pl.order[i].Rest == nil {
			first, wFirst = append(first, c), append(wFirst, pl.order[i])
		} else {
			rest, wRest = append(rest, c), append(wRest, pl.order[i])
		}
	}
	if len(first) == 0 {
		return cs, pl, false
	}
	pl.order = append(wFirst, wRest...)
	return append(first, rest...), pl, true
}

// ofMember reports whether a candidate is one of the member's keys or
// accounts: its rest is the provider's id, or that with the key ("#") or
// the account ("@") after it.
func ofMember(c candidate, m provider.Member) bool {
	id := m.Provider.ID
	return c.model == m.Model && (c.rest == id || strings.HasPrefix(c.rest, id+"#") || strings.HasPrefix(c.rest, id+"@"))
}

// membersImageInput is whether a group request may carry images: the
// member a rule put first decides, or else every member must take them
// (as the group's catalog entry says without rules).
func membersImageInput(ms []provider.Member, ruled []provider.Member) *bool {
	if len(ruled) > 0 {
		ms = ruled
	}
	var out *bool
	cat := provider.Catalog()
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
		out = sharedImageInput(out, in)
	}
	return out
}

// sharedImageInput is provider's: an explicit text-only answer wins, and
// an unknown one stays unknown.
func sharedImageInput(a, b *bool) *bool {
	if a != nil && !*a {
		return a
	}
	if b != nil && !*b {
		return b
	}
	if a == nil || b == nil {
		return nil
	}
	return a
}
