package provider

// A group's rules: which member a request goes to first, by what can be
// seen in the request itself — how long it is, whether it carries an
// image, how hard the agent asked the model to think, which agent sent it.
// Those are never guessed: the same request is always routed the same way,
// and the trace tells which rule did it. A rule may also hold only at some
// hours of the day (a vendor's peak-price hours, say), in the time zone
// they were set in (magpie's clock when they name none). The one exception is a rule with
// an intent (what the user asks for, in the user's words): as a turn
// begins, a model the group names (its Classifier) is asked which of the
// intents the user's message is, once, and the trace tells what it said.
//
// A rule decides when a user's turn begins; the agent's requests within
// that turn — tool results sent back — stay with what it decided, so a
// turn is never moved to another model halfway through (the gateway keeps
// that; see gateway/rules.go). The member a rule names goes first, then
// the others it names after it (Rule.More), in order; the group's other
// members stay behind them for failover.
//
// A pause rule (Rule.Pause) sends nothing first: while it holds, the
// member it names is out of the group altogether — not first, not a
// failover, not where a conversation was — as a member switched off is
// (Group.Off), but only at its hours, on its days, for its agents or in its
// folders (or outside them: #1606, an account's quota kept for the long
// task in one project). It is
// looked at on every request, not as a turn begins: a paused model is
// paid nothing in its hours, so a turn on it moves when they begin.

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Efforts are the reasoning levels a rule can ask for, lowest first.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Rule sends the requests it matches to one of its group's members first,
// and then to the others it names, in order. Every condition set must
// hold; a rule sets at least one.
type Rule struct {
	Use string `json:"use"` // the member, as the group names it: "provider/model"
	// More: the members the rule sends to after Use, in order, each tried
	// when the ones before it can't take the request (monkeyin_92 on X: one
	// rule for several models, not a rule each). They go before the
	// members the matching rules after it name, and the group's others.
	// Use stays the first, so a magpie from before More still sends the
	// rule's requests to it, and has the others behind it as group members.
	// A pause names one model and has none.
	More []string `json:"more,omitempty"`
	// UseEffort: the reasoning the member is asked for on the requests the
	// rule sends it (one of Efforts), in place of what the agent asked —
	// an intent picks the model and its effort as a pair (#2633). Like a
	// member fixed at an effort, it is fitted to the model's own levels,
	// and a model whose levels aren't known is asked as the agent asked.
	// It is an output, as Use is; Effort below is a condition.
	UseEffort string `json:"useEffort,omitempty"`
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
	// Time: the turn begins within these hours of the day, in the window's
	// zone (TimeWindow.Zone) — a vendor's peak-price hours sent to another,
	// say.
	Time *TimeWindow `json:"time,omitempty"`
	// Folders: the request's working folder, as the agent states it in
	// the request, is one of these or inside one — a project, or every
	// project under a folder (Cario0x on X). "~/work", "/Users/me/x",
	// "C:\work"; WSL's /mnt/c/work is C:\work. When the agent doesn't
	// say its folder, it doesn't match.
	Folders []string `json:"folders,omitempty"`
	// ExceptFolders: the request's working folder is none of these nor
	// inside one, as Folders reads them. A request whose agent doesn't say
	// its folder is not known to be in one, so it holds for it: a pause
	// "except in ~/big-task" keeps the model for requests known to come
	// from there (#1606), never for one that may come from elsewhere.
	ExceptFolders []string `json:"exceptFolders,omitempty"`
	// Where: conditions as a field, a way to compare and a value
	// (rulecond.go) — "effort is not off", "tokens < 20000", "agent is
	// not codex", "subagent", "message contains …" — for what the fields
	// above can't say. A magpie from before them doesn't read them, and a
	// rule of them alone has no condition there, so it matches nothing.
	Where []Cond `json:"where,omitempty"`
	// Pause: rather than sending to Use first, the rule leaves Use out of
	// the group while it holds (John on Discord: a model paused in a
	// vendor's peak hours). Only Time, Agents, Folders and ExceptFolders
	// (and Where's agent conditions) can be its conditions: the request's
	// clock, its agent and the folder it says, never what it asks for — a pause keeps the model out of the
	// request whatever it is.
	Pause bool `json:"pause,omitempty"`
}

// Uses are the members the rule sends to, in order: Use, then More.
func (r Rule) Uses() []string {
	if r.Use == "" {
		return slices.Clone(r.More)
	}
	return append([]string{r.Use}, r.More...)
}

// SetUses has the rule send to uses, in order, each once and none empty:
// the first is Use, the rest More.
func (r *Rule) SetUses(uses []string) {
	var out []string
	for _, u := range uses {
		if u != "" && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	r.Use, r.More = "", nil
	if len(out) > 0 {
		r.Use = out[0]
	}
	if len(out) > 1 {
		r.More = out[1:]
	}
}

// MapUses has each member the rule sends to go by what f makes of it:
// a member renamed, cleaned or taken out ("").
func (r *Rule) MapUses(f func(string) string) {
	uses := r.Uses()
	for i, u := range uses {
		uses[i] = f(u)
	}
	r.SetUses(uses)
}

// Without is the rule with m no longer among the members it sends to;
// false when it names no other, and goes with m.
func (r Rule) Without(m string) (Rule, bool) {
	r.MapUses(func(u string) string {
		if u == m {
			return ""
		}
		return u
	})
	return r, r.Use != ""
}

// Names reports whether the rule sends to m.
func (r Rule) Names(m string) bool { return r.Use == m || slices.Contains(r.More, m) }

// TimeWindow is hours of the day: From until To, "HH:MM" each, past
// midnight when To comes before From (22:00–08:00), the whole day when
// they are the same. Days, when set, are the days it holds on ("mon" …
// "sun"); a window past midnight is of the day it begins on, so Friday's
// 22:00–08:00 takes Saturday's early hours too.
type TimeWindow struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Days []string `json:"days,omitempty"`
	// Zone: the IANA time zone the hours and days were set in
	// ("Asia/Shanghai"): the browser's on the Routing page, the terminal's
	// for magpie group rule. magpie on a server in UTC keeps a Beijing
	// user's 09:00–18:00 at Beijing's 09:00 (gaubeebangeel on X). A window
	// with none — every one set before zones were kept — is on this
	// computer's clock, as it was.
	Zone string `json:"zone,omitempty"`
}

// zones are the zones windows were read in, each loaded once: Holds is
// asked on every request a pause rule may hold for.
var zones sync.Map // name → *time.Location

// zoneOf is the window's zone: time.Local when it names none; false when
// it names one this magpie doesn't know.
func zoneOf(name string) (*time.Location, bool) {
	if name == "" {
		return time.Local, true
	}
	if l, ok := zones.Load(name); ok {
		return l.(*time.Location), true
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, false // not kept: a file's text can be anything
	}
	zones.Store(name, l)
	return l, true
}

// ValidZone reports whether name is a time zone a window can be in: an
// IANA name this magpie knows, or "" for this computer's clock.
func ValidZone(name string) bool {
	_, ok := zoneOf(name)
	return ok
}

// LocalZone is the IANA name of this computer's time zone, as a window
// set here records it: $TZ when it names one, else what /etc/localtime
// links to (macOS, Linux). "" when neither says (Windows, a TZ of "UTC+8"
// that names no zone): the window is then on the clock of whichever
// computer runs the gateway, as before.
func LocalZone() string {
	if tz, ok := os.LookupEnv("TZ"); ok {
		tz = strings.TrimPrefix(tz, ":")
		if tz != "" && tz != "Local" && ValidZone(tz) {
			return tz
		}
		return ""
	}
	if n := time.Local.String(); n != "Local" && n != "" && ValidZone(n) {
		return n
	}
	if dst, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(dst, "zoneinfo/"); ok && name != "" && ValidZone(name) {
			return name
		}
	}
	return ""
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
	w.Zone = strings.TrimSpace(w.Zone)
	if !ValidZone(w.Zone) {
		return fmt.Errorf("the time zone %q is not one magpie knows: an IANA name such as Asia/Shanghai or America/New_York", w.Zone)
	}
	return nil
}

// Holds reports whether at is within the window, in its zone. A zone
// this magpie doesn't know (a file edited by hand; saving refuses one)
// is an unknown clock, and the window doesn't hold: the rule is as if it
// weren't there, rather than at hours that may be a day's half off.
func (w TimeWindow) Holds(at time.Time) bool {
	from, ok1 := clockMinutes(w.From)
	to, ok2 := clockMinutes(w.To)
	loc, ok3 := zoneOf(w.Zone)
	if at.IsZero() || !ok1 || !ok2 || !ok3 {
		return false
	}
	at = at.In(loc)
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

// Text is the window as a rule's conditions show it: "09:00–18:00 Mon–Fri",
// its zone after it when it has one: "09:00–18:00 (Asia/Shanghai)".
func (w TimeWindow) Text() string {
	s := w.From + "–" + w.To
	if w.From == w.To {
		s = "all day"
	}
	if d := w.DaysText(); d != "" {
		s += " " + d
	}
	if w.Zone != "" {
		s += " (" + w.Zone + ")"
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
	if len(r.Folders) > 0 {
		out = append(out, "folder "+strings.Join(r.Folders, "|"))
	}
	if len(r.ExceptFolders) > 0 {
		out = append(out, "not folder "+strings.Join(r.ExceptFolders, "|"))
	}
	for _, c := range r.Where {
		out = append(out, c.Text())
	}
	return out
}

func cleanRules(rules []Rule, members []string) ([]Rule, error) {
	var out []Rule
	for i, r := range rules {
		r.MapUses(strings.TrimSpace)
		r.Effort = strings.ToLower(strings.TrimSpace(r.Effort))
		r.UseEffort = strings.ToLower(strings.TrimSpace(r.UseEffort))
		r.Agents = cleanList(r.Agents)
		r.Intent = strings.Join(strings.Fields(r.Intent), " ")
		for j := range r.Agents {
			r.Agents[j] = strings.ToLower(r.Agents[j])
		}
		n := i + 1
		var err error
		if r.Folders, err = cleanFolders(r.Folders); err != nil {
			return nil, fmt.Errorf("rule %d: %w", n, err)
		}
		if r.ExceptFolders, err = cleanFolders(r.ExceptFolders); err != nil {
			return nil, fmt.Errorf("rule %d: %w", n, err)
		}
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
		if len(r.Where) == 0 {
			r.Where = nil
		} else {
			r.Where = slices.Clone(r.Where)
		}
		for j := range r.Where {
			if err := r.Where[j].clean(); err != nil {
				return nil, fmt.Errorf("rule %d: %w", n, err)
			}
			if r.Pause && r.Where[j].Field != "agent" {
				return nil, fmt.Errorf("rule %d: a pause holds by hours, days, agents or folders only", n)
			}
		}
		switch {
		case r.Use == "":
			return nil, fmt.Errorf("rule %d: which model it sends to is missing", n)
		case slices.ContainsFunc(r.Uses(), func(u string) bool { return !slices.Contains(members, u) }):
			u := r.Uses()[slices.IndexFunc(r.Uses(), func(u string) bool { return !slices.Contains(members, u) })]
			return nil, fmt.Errorf("rule %d: %s is not in the group", n, u)
		case r.Pause && len(r.More) > 0:
			return nil, fmt.Errorf("rule %d: a pause names one model: give each its own rule", n)
		case r.Tokens < 0:
			return nil, fmt.Errorf("rule %d: tokens can't be negative", n)
		case r.Effort != "" && r.Effort != "on" && !slices.Contains(Efforts, r.Effort):
			return nil, fmt.Errorf("rule %d: effort is on or one of %s, not %q", n, strings.Join(Efforts, ", "), r.Effort)
		case r.UseEffort != "" && !slices.Contains(Efforts, r.UseEffort):
			return nil, fmt.Errorf("rule %d: the reasoning it asks its model for is one of %s, not %q", n, strings.Join(Efforts, ", "), r.UseEffort)
		case r.Pause && r.UseEffort != "":
			return nil, fmt.Errorf("rule %d: a pause sends nothing, so it asks for no reasoning", n)
		case len([]rune(r.Intent)) > MaxIntent:
			return nil, fmt.Errorf("rule %d: an intent is at most %d characters", n, MaxIntent)
		case len(r.Conditions()) == 0:
			return nil, fmt.Errorf("rule %d: it needs a condition (tokens, images, effort, agents, subagent, message, intent, compacting, time or folders)", n)
		case r.Pause && (r.Tokens > 0 || r.Images || r.Effort != "" || r.Intent != "" || r.Compact):
			return nil, fmt.Errorf("rule %d: a pause holds by hours, days, agents or folders only", n)
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
	// Folder is the working folder the agent says the request is from;
	// "" when it doesn't say, which is in no rule's folders.
	Folder string
	// Off: the agent turned reasoning off (effort none, thinking
	// disabled), which a rule's effort condition reads as off.
	Off bool
	// Subagent: the request is from a subagent the agent started, as the
	// agent tells it (Claude Code's and Codex's do); false when it can't
	// be told.
	Subagent bool
	// Message is the user's own words at the turn's start, read only when
	// a rule has a message condition (RulesReadMessage).
	Message string
}

// RulesReadMessage reports whether a rule of rules looks at the user's
// message, which is then read for it.
func RulesReadMessage(rules []Rule) bool {
	return slices.ContainsFunc(rules, func(r Rule) bool {
		return slices.ContainsFunc(r.Where, func(c Cond) bool { return c.Field == "message" })
	})
}

// Matches reports whether the request is one the rule sends to its member
// first; a pause rule sends none (see Pauses).
func (r Rule) Matches(q RuleRequest) bool {
	if r.Intent != "" && !strings.EqualFold(r.Intent, q.Intent) {
		return false
	}
	return r.MatchesBesidesIntent(q)
}

// MatchesBesidesIntent is Matches but for the rule's intent: whether the
// classifier's answer is all it waits on.
func (r Rule) MatchesBesidesIntent(q RuleRequest) bool {
	if r.Pause {
		return false
	}
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
	if !r.inFolders(q.Folder) {
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
	for _, c := range r.Where {
		if !c.Holds(q) {
			return false
		}
	}
	return len(r.Conditions()) > 0
}

// MatchRule is the first of rules the request matches, -1 for none.
func MatchRule(rules []Rule, q RuleRequest) int {
	return slices.IndexFunc(rules, func(r Rule) bool { return r.Matches(q) })
}

// ThenUses are the members the rules after the n-th (from 1) that also
// match q send to, each once and in the rules' order (a rule's own in its
// order), leaving out those of uses: who a request a rule sent to uses
// fails over to before the group's others (#112).
func ThenUses(rules []Rule, q RuleRequest, n int, uses []string) []string {
	var out []string
	for i := n; i < len(rules); i++ {
		if !rules[i].Matches(q) {
			continue
		}
		for _, u := range rules[i].Uses() {
			if !slices.Contains(uses, u) && !slices.Contains(out, u) {
				out = append(out, u)
			}
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
// room. Without rules the group can do what all its members can — but for
// its context, which groupEntries already takes from its largest member
// (#712), so a rule of length never lifts it there.
func ruledEntry(e *Entry, g Group, ms []Member, entries []Entry) {
	// of is what a member takes: a group in the group takes what every
	// one of its models does
	of := func(id string) (Entry, bool) {
		var out Entry
		found := false
		for _, m := range ms {
			if m.ID != id && !slices.ContainsFunc(m.Also, func(o Member) bool { return o.ID == id }) {
				continue // one of a later group in the group too (#2361)
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
	// ofRule is what every member a rule sends to takes: a request it
	// matches may go to any of them
	ofRule := func(r Rule) (Entry, bool) {
		var out Entry
		for i, id := range r.Uses() {
			x, ok := of(id)
			if !ok {
				return Entry{}, false
			}
			if i == 0 {
				out = x
				continue
			}
			if x.Context > 0 && (out.Context == 0 || x.Context < out.Context) {
				out.Context = x.Context
			}
			out.Images = out.Images && x.Images
			out.ImageInput = sharedImageInput(out.ImageInput, x.ImageInput)
		}
		return out, r.Use != ""
	}
	sees := func(x Entry) bool { return x.Images && (x.ImageInput == nil || *x.ImageInput) }
	for i, r := range g.Rules {
		x, ok := ofRule(r)
		if !ok || r.Pause {
			continue
		}
		if r.Images && r.Tokens == 0 && r.Effort == "" && len(r.Agents) == 0 && len(r.Where) == 0 && r.Intent == "" && !r.Compact && r.Time == nil && !r.ByFolder() && sees(x) && !e.Images &&
			!slices.ContainsFunc(g.Rules[:i], func(b Rule) bool { y, ok := ofRule(b); return !b.Pause && (!ok || !sees(y)) }) {
			e.Images, e.ImageInput = true, x.ImageInput
		}
		if r.Tokens == 0 || r.Images || r.Effort != "" || len(r.Agents) > 0 || len(r.Where) > 0 || r.Intent != "" || r.Compact || r.Time != nil || r.ByFolder() || x.Context <= e.Context {
			continue // only a rule of length alone takes every long request
		}
		// every request up to the rule's length must fit whoever may get
		// it, and a longer one may still be taken by a rule before
		fits := x.Context
		for _, m := range ms {
			if r.Names(m.ID) {
				continue
			}
			if y, ok := of(m.ID); ok && y.Context > 0 && y.Context < r.Tokens {
				fits = 0 // a request shorter than the rule's length may not fit it
			}
		}
		for _, before := range g.Rules[:i] {
			// one not ready now leaves what it matches to the group's order
			if before.Pause {
				continue
			}
			if y, ok := ofRule(before); !ok {
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

// Pauses reports whether a pause rule holds for a request from agent at
// the time given, working in folder ("" when it didn't say).
func (r Rule) Pauses(agent, folder string, at time.Time) bool {
	if !r.Pause || r.Time == nil && len(r.Agents) == 0 && len(r.Where) == 0 && !r.ByFolder() {
		return false
	}
	if r.Time != nil && !r.Time.Holds(at) {
		return false
	}
	if !r.inFolders(folder) {
		return false
	}
	for _, c := range r.Where {
		if c.Field != "agent" || !c.Holds(RuleRequest{Agent: agent}) {
			return false // a pause's conditions are its agents only
		}
	}
	return len(r.Agents) == 0 || slices.Contains(r.Agents, strings.ToLower(agent))
}

// ByFolder reports whether the rule holds by the folder the agent works
// in, in some or outside them: the request's folder is then read for it.
func (r Rule) ByFolder() bool { return len(r.Folders) > 0 || len(r.ExceptFolders) > 0 }

// inFolders reports whether folder ("" unknown) is as the rule's folders
// ask: in one of Folders, when it has some (an unknown folder is in none),
// and in none of ExceptFolders (an unknown folder is not known to be in
// one, so it is outside them).
func (r Rule) inFolders(folder string) bool {
	under := func(root string) bool { return FolderUnder(folder, root) }
	if len(r.Folders) > 0 && !slices.ContainsFunc(r.Folders, under) {
		return false
	}
	return !slices.ContainsFunc(r.ExceptFolders, under)
}

// PausesByFolder reports whether a pause rule of g, or of a group in it on
// the way to one of ms, holds by folder: the request's folder is then
// read before the pauses are looked at.
func PausesByFolder(g Group, ms []Member) bool {
	byFolder := func(x Group) bool {
		return slices.ContainsFunc(x.Rules, func(r Rule) bool { return r.Pause && r.ByFolder() })
	}
	return byFolder(g) || slices.ContainsFunc(ms, func(m Member) bool { return slices.ContainsFunc(m.Via, byFolder) })
}

// Paused is a model a pause rule left out of a request's group.
type Paused struct {
	Member string   `json:"member"`          // "provider/model[:effort]"
	Group  string   `json:"group,omitempty"` // the group in the group whose rule it is; "" the group's own
	Rule   int      `json:"rule"`            // the rule, from 1
	Use    string   `json:"use"`             // the member the rule names: the model, or a group in the group
	When   []string `json:"when"`
	// Folder: the folder the agent said it works in, when the rule holds
	// by folder (ByFolder); "" when it said none, which is outside every
	// folder of a rule's ExceptFolders.
	Folder   string `json:"folder,omitempty"`
	ByFolder bool   `json:"byFolder,omitempty"`
}

// PausedOut leaves out of ms the models a pause rule holds for now, for a
// request from agent working in folder ("" when it didn't say, or wasn't
// read: see PausesByFolder): the group's own, or those of a group in it on
// the way to them. A rule naming a group in the group pauses every model
// of it.
func PausedOut(g Group, ms []Member, agent, folder string, at time.Time) (kept []Member, paused []Paused) {
	if !slices.ContainsFunc(g.Rules, func(r Rule) bool { return r.Pause }) &&
		!slices.ContainsFunc(ms, func(m Member) bool {
			return slices.ContainsFunc(m.Via, func(v Group) bool { return slices.ContainsFunc(v.Rules, func(r Rule) bool { return r.Pause }) })
		}) {
		return ms, nil
	}
	for _, m := range ms {
		var hit *Paused
		for d := 0; d <= len(m.Via) && hit == nil; d++ {
			by, in := g, ""
			if d > 0 {
				by, in = m.Via[d-1], m.Via[d-1].ID
			}
			for i, r := range by.Rules {
				if d < len(m.Path) && pathNames(m.Path[d:], r.Use) && r.Pauses(agent, folder, at) {
					hit = &Paused{Member: m.Spelled(), Group: in, Rule: i + 1, Use: r.Use, When: r.Conditions(), ByFolder: r.ByFolder()}
					if r.ByFolder() {
						hit.Folder = folder
					}
					break
				}
			}
		}
		if hit != nil {
			paused = append(paused, *hit)
			continue
		}
		kept = append(kept, m)
	}
	return kept, paused
}

// pathNames reports whether a member's way down from a group (its id
// there first, its model last) is what that group's rule names: the
// member itself, or the model when a group in the group, ahead of it, has
// it too (#625).
func pathNames(path []string, id string) bool {
	return path[0] == id || !strings.HasPrefix(id, GroupPrefix) && len(path) > 1 && path[len(path)-1] == id
}
