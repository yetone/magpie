package provider

// A group's rules: which member a request goes to first, by what can be
// seen in the request itself — how long it is, whether it carries an
// image, how hard the agent asked the model to think, which agent sent it.
// Those are never guessed: the same request is always routed the same way,
// and the trace tells which rule did it. A rule may also hold only at some
// hours of the day (a vendor's peak-price hours, say), on magpie's clock. The one exception is a rule with
// an intent (what the user asks for, in the user's words): as a turn
// begins, a model the group names (its Classifier) is asked which of the
// intents the user's message is, once, and the trace tells what it said.
//
// A rule decides when a user's turn begins; the agent's requests within
// that turn — tool results sent back — stay with what it decided, so a
// turn is never moved to another model halfway through (the gateway keeps
// that; see gateway/rules.go). The member a rule names goes first; the
// group's other members stay behind it for failover.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Efforts are the reasoning levels a rule can ask for, lowest first.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Rule sends the requests it matches to one of its group's members first.
// Every condition set must hold; a rule sets at least one.
type Rule struct {
	Use string `json:"use"` // the member, as the group names it: "provider/model"
	// Tokens: the request is at least this long, in tokens — the larger of
	// an estimate from its size and what the vendor counted for the
	// conversation's last request.
	Tokens int `json:"tokens,omitempty"`
	// Images: the request carries an image, in this turn or before.
	Images bool `json:"images,omitempty"`
	// Effort: the agent asked for reasoning at least this level (one of
	// Efforts), or "on" for any reasoning at all.
	Effort string `json:"effort,omitempty"`
	// Agents: the request is from one of these agents (usage.AgentOf ids).
	Agents []string `json:"agents,omitempty"`
	// Intent: the user's message at the turn's start is of this kind, as
	// the group's Classifier judges it — "writing or fixing tests",
	// "a quick question". When the classifier can't say, it doesn't match.
	Intent string `json:"intent,omitempty"`
	// Compact: the agent is compacting its conversation — asking a model to
	// summarize it so it can go on in less room (Claude Code's /compact,
	// Codex's, OpenCode's, Pi's…) — which a cheaper, faster model can do
	// in place of the one the conversation is on.
	Compact bool `json:"compact,omitempty"`
	// Time: the turn begins within these hours of the day, local time — a
	// vendor's peak-price hours sent to another, say.
	Time *TimeWindow `json:"time,omitempty"`
}

// TimeWindow is hours of the day, local time: From until To, "HH:MM" each,
// past midnight when To comes before From (22:00–08:00), the whole day
// when they are the same. Days, when set, are the days it holds on ("mon"
// … "sun"); a window past midnight is of the day it begins on, so Friday's
// 22:00–08:00 takes Saturday's early hours too.
type TimeWindow struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Days []string `json:"days,omitempty"`
}

// Weekdays are the days a TimeWindow names, as time.Weekday counts them.
var Weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// dayOrder is the week as a window shows it, Monday first.
var dayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// clockMinutes reads "HH:MM" (or "H:MM") as minutes past midnight.
func clockMinutes(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(h) < 1 || len(h) > 2 || len(m) != 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || h[0] == '+' || m[0] == '+' {
		return 0, false
	}
	return hh*60 + mm, true
}

// ParseDay reads a day of the week, "mon" or "Monday", as Weekdays has it.
func ParseDay(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 3 {
		return "", false
	}
	for i, d := range Weekdays {
		if strings.HasPrefix(strings.ToLower(time.Weekday(i).String()), s) {
			return d, true
		}
	}
	return "", false
}

// clean checks a window and puts it the one way: "09:00", days known,
// each once in the week's order, none when it is every day.
func (w *TimeWindow) clean() error {
	from, ok := clockMinutes(w.From)
	if !ok {
		return fmt.Errorf("the time %q is not HH:MM (09:00)", w.From)
	}
	to, ok := clockMinutes(w.To)
	if !ok {
		return fmt.Errorf("the time %q is not HH:MM (18:00)", w.To)
	}
	w.From, w.To = fmt.Sprintf("%02d:%02d", from/60, from%60), fmt.Sprintf("%02d:%02d", to/60, to%60)
	var days []string
	for _, d := range w.Days {
		v, ok := ParseDay(d)
		if !ok {
			return fmt.Errorf("%q is not a day of the week (mon … sun)", d)
		}
		days = append(days, v)
	}
	w.Days = nil
	for _, d := range dayOrder {
		if slices.Contains(days, d) {
			w.Days = append(w.Days, d)
		}
	}
	if len(w.Days) == len(dayOrder) {
		w.Days = nil
	}
	return nil
}

// Holds reports whether at is within the window, on the machine's clock.
func (w TimeWindow) Holds(at time.Time) bool {
	from, ok1 := clockMinutes(w.From)
	to, ok2 := clockMinutes(w.To)
	if at.IsZero() || !ok1 || !ok2 {
		return false
	}
	at = at.Local()
	m, day := at.Hour()*60+at.Minute(), at.Weekday()
	switch {
	case from == to: // the whole day
	case from < to:
		if m < from || m >= to {
			return false
		}
	default: // past midnight
		if m >= to && m < from {
			return false
		}
		if m < to {
			day = (day + 6) % 7 // the window began the day before
		}
	}
	return len(w.Days) == 0 || slices.Contains(w.Days, Weekdays[day])
}

// DaysText is the window's days for a list: "Mon–Fri", "Sat,Sun",
// "Mon,Wed–Fri"; "" for every day.
func (w TimeWindow) DaysText() string {
	name := func(d string) string { return strings.ToUpper(d[:1]) + d[1:] }
	var out []string
	for i := 0; i < len(dayOrder); {
		if !slices.Contains(w.Days, dayOrder[i]) {
			i++
			continue
		}
		j := i
		for j+1 < len(dayOrder) && slices.Contains(w.Days, dayOrder[j+1]) {
			j++
		}
		switch j - i {
		case 0:
			out = append(out, name(dayOrder[i]))
		case 1:
			out = append(out, name(dayOrder[i]), name(dayOrder[j]))
		default:
			out = append(out, name(dayOrder[i])+"–"+name(dayOrder[j]))
		}
		i = j + 1
	}
	return strings.Join(out, ",")
}

// Text is the window as a rule's conditions show it: "09:00–18:00 Mon–Fri".
func (w TimeWindow) Text() string {
	s := w.From + "–" + w.To
	if w.From == w.To {
		s = "all day"
	}
	if d := w.DaysText(); d != "" {
		s += " " + d
	}
	return s
}

// MaxIntent is how long an intent may be, in characters.
const MaxIntent = 200

// Conditions says what a rule matches, for a list or a trace.
func (r Rule) Conditions() []string {
	var out []string
	if r.Tokens > 0 {
		out = append(out, fmt.Sprintf("tokens ≥ %d", r.Tokens))
	}
	if r.Images {
		out = append(out, "images")
	}
	switch r.Effort {
	case "":
	case "on":
		out = append(out, "reasoning")
	default:
		out = append(out, "effort ≥ "+r.Effort)
	}
	if len(r.Agents) > 0 {
		out = append(out, "agent "+strings.Join(r.Agents, "|"))
	}
	if r.Intent != "" {
		out = append(out, fmt.Sprintf("intent %q", r.Intent))
	}
	if r.Compact {
		out = append(out, "compacting")
	}
	if r.Time != nil {
		out = append(out, "time "+r.Time.Text())
	}
	return out
}

func cleanRules(rules []Rule, members []string) ([]Rule, error) {
	var out []Rule
	for i, r := range rules {
		r.Use = strings.TrimSpace(r.Use)
		r.Effort = strings.ToLower(strings.TrimSpace(r.Effort))
		r.Agents = cleanList(r.Agents)
		r.Intent = strings.Join(strings.Fields(r.Intent), " ")
		for j := range r.Agents {
			r.Agents[j] = strings.ToLower(r.Agents[j])
		}
		n := i + 1
		if r.Time != nil {
			w := *r.Time
			if err := w.clean(); err != nil {
				return nil, fmt.Errorf("rule %d: %w", n, err)
			}
			if w.From == w.To && len(w.Days) == 0 {
				return nil, fmt.Errorf("rule %d: its hours are the whole day, every day: give days or other hours", n)
			}
			r.Time = &w
		}
		switch {
		case r.Use == "":
			return nil, fmt.Errorf("rule %d: which model it sends to is missing", n)
		case !slices.Contains(members, r.Use):
			return nil, fmt.Errorf("rule %d: %s is not in the group", n, r.Use)
		case r.Tokens < 0:
			return nil, fmt.Errorf("rule %d: tokens can't be negative", n)
		case r.Effort != "" && r.Effort != "on" && !slices.Contains(Efforts, r.Effort):
			return nil, fmt.Errorf("rule %d: effort is on or one of %s, not %q", n, strings.Join(Efforts, ", "), r.Effort)
		case len([]rune(r.Intent)) > MaxIntent:
			return nil, fmt.Errorf("rule %d: an intent is at most %d characters", n, MaxIntent)
		case len(r.Conditions()) == 0:
			return nil, fmt.Errorf("rule %d: it needs a condition (tokens, images, effort, agents, intent, compacting or time)", n)
		}
		if len(r.Agents) == 0 {
			r.Agents = nil
		}
		out = append(out, r)
	}
	return out, nil
}

// RuleRequest is what a rule looks at in a request.
type RuleRequest struct {
	Tokens   int
	Images   bool
	Thinking bool   // the agent asked for reasoning
	Effort   string // at what level, when it said
	Agent    string
	// Intent is the one of the rules' intents the classifier said the
	// user's message is; "" when none, or it wasn't asked.
	Intent string
	// Compact: the request is the agent compacting its conversation.
	Compact bool
	// At is when the request came, for a rule's hours; a zero time is in
	// none.
	At time.Time
}

// Matches reports whether the request is one the rule is for.
func (r Rule) Matches(q RuleRequest) bool {
	if r.Intent != "" && !strings.EqualFold(r.Intent, q.Intent) {
		return false
	}
	return r.MatchesBesidesIntent(q)
}

// MatchesBesidesIntent is Matches but for the rule's intent: whether the
// classifier's answer is all it waits on.
func (r Rule) MatchesBesidesIntent(q RuleRequest) bool {
	if r.Tokens > 0 && q.Tokens < r.Tokens {
		return false
	}
	if r.Images && !q.Images {
		return false
	}
	if r.Compact && !q.Compact {
		return false
	}
	if r.Time != nil && !r.Time.Holds(q.At) {
		return false
	}
	switch r.Effort {
	case "":
	case "on":
		if !q.Thinking && q.Effort == "" {
			return false
		}
	default:
		if slices.Index(Efforts, q.Effort) < slices.Index(Efforts, r.Effort) {
			return false
		}
	}
	if len(r.Agents) > 0 && !slices.Contains(r.Agents, strings.ToLower(q.Agent)) {
		return false
	}
	return len(r.Conditions()) > 0
}

// MatchRule is the first of rules the request matches, -1 for none.
func MatchRule(rules []Rule, q RuleRequest) int {
	return slices.IndexFunc(rules, func(r Rule) bool { return r.Matches(q) })
}

// ThenUses are the members the rules after the n-th (from 1) that also
// match q send to, each once and in the rules' order, leaving out use: who
// a request a rule sent to use fails over to before the group's others (#112).
func ThenUses(rules []Rule, q RuleRequest, n int, use string) []string {
	var out []string
	for i := n; i < len(rules); i++ {
		if u := rules[i].Use; u != use && !slices.Contains(out, u) && rules[i].Matches(q) {
			out = append(out, u)
		}
	}
	return out
}

// Intents are the intents the classifier is to choose among for q: those
// of the rules that match it but for their intent, up to the first that
// matches outright (a rule after it could never be the first to match).
// None when no rule before that one has an intent — the classifier then
// isn't asked. Each is given once, as the first rule has it.
func Intents(rules []Rule, q RuleRequest) []string {
	var out []string
	for _, r := range rules {
		if !r.MatchesBesidesIntent(q) {
			continue
		}
		if r.Intent == "" {
			break
		}
		if !slices.ContainsFunc(out, func(s string) bool { return strings.EqualFold(s, r.Intent) }) {
			out = append(out, r.Intent)
		}
	}
	return out
}

// ruledEntry tells agents what a group with rules can do: images, when a
// rule of images alone sends every request with one to a member that takes
// them (and any rule before it, which may take such a request first, sends
// to one that does too); and a longer context, when a rule sends every
// request past a length the other members can all take to one with more
// room. Without rules the group can do what all its members can.
func ruledEntry(e *Entry, g Group, ms []Member, entries []Entry) {
	// of is what a member takes: a group in the group takes what every
	// one of its models does
	of := func(id string) (Entry, bool) {
		var out Entry
		found := false
		for _, m := range ms {
			if m.ID != id {
				continue
			}
			for _, x := range entries {
				if x.Provider.ID != m.Provider.ID || x.Model != m.Model {
					continue
				}
				if !found {
					out, found = x, true
					break
				}
				if x.Context > 0 && (out.Context == 0 || x.Context < out.Context) {
					out.Context = x.Context
				}
				out.Images = out.Images && x.Images
				out.ImageInput = sharedImageInput(out.ImageInput, x.ImageInput)
				break
			}
		}
		return out, found
	}
	sees := func(x Entry) bool { return x.Images && (x.ImageInput == nil || *x.ImageInput) }
	for i, r := range g.Rules {
		x, ok := of(r.Use)
		if !ok {
			continue
		}
		if r.Images && r.Tokens == 0 && r.Effort == "" && len(r.Agents) == 0 && r.Intent == "" && !r.Compact && r.Time == nil && sees(x) && !e.Images &&
			!slices.ContainsFunc(g.Rules[:i], func(b Rule) bool { y, ok := of(b.Use); return !ok || !sees(y) }) {
			e.Images, e.ImageInput = true, x.ImageInput
		}
		if r.Tokens == 0 || r.Images || r.Effort != "" || len(r.Agents) > 0 || r.Intent != "" || r.Compact || r.Time != nil || x.Context <= e.Context {
			continue // only a rule of length alone takes every long request
		}
		// every request up to the rule's length must fit whoever may get
		// it, and a longer one may still be taken by a rule before
		fits := x.Context
		for _, m := range ms {
			if m.ID == r.Use {
				continue
			}
			if y, ok := of(m.ID); ok && y.Context > 0 && y.Context < r.Tokens {
				fits = 0 // a request shorter than the rule's length may not fit it
			}
		}
		for _, before := range g.Rules[:i] {
			// one not ready now leaves what it matches to the group's order
			if y, ok := of(before.Use); !ok {
				fits = 0
			} else if y.Context < fits {
				fits = y.Context
			}
		}
		if fits > e.Context {
			e.Context = fits
		}
	}
}
