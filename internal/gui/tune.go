package gui

// The Context tab's advice: for each agent, the settings that would have
// cost this user the fewest tokens over the period, worked out by
// replaying their own calls (internal/tune) against what the agent's
// config has now, and putting one in, or back, on their say.

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/tune"
)

type tuneAdvice struct {
	Knob    string            `json:"knob"`
	Model   string            `json:"model,omitempty"`
	Setting agent.TuneSetting `json:"setting"`
	Compact *tune.Compaction  `json:"compact,omitempty"`
	TTL     *tune.TTL         `json:"ttl,omitempty"`
	// Own is the model whose own Compact at (its provider's, or one set
	// on it) is where Codex runs now, over Long conversations: putting a
	// window in Long conversations doesn't move it (hoobnn on Discord: a
	// card said "In use" for 170K beside "Now 430K")
	Own *tuneOwn `json:"own,omitempty"`
	// Apart are the models of the range with a Compact at of their own
	// beside others Long conversations moves: a Use moves those others,
	// and these keep compacting where they did (hoobnn on Discord, 11-01)
	Apart []tuneOwn `json:"apart,omitempty"`
	// Async is Codex's background compaction at the advice, its foreground
	// limit kept as the backstop; nil where it can't help
	Async *tuneAsync `json:"async,omitempty"`
	// Few: a setting tune put in, or Claude Code's cache as it is here,
	// kept on the card with nothing to advise (too few calls in the
	// range), so its Undo, or its way back to 5 minutes, is still there
	Few bool `json:"few,omitempty"`
}

// tuneAsync is the advice as Codex's background compaction takes it: a
// share of the model's window (Percent), about Value tokens of Window.
// Held are the Codexes here too old for the share, or of no known version.
type tuneAsync struct {
	Percent int                 `json:"percent"`
	Value   int                 `json:"value"`
	Window  int                 `json:"window"`
	Setting agent.TuneSetting   `json:"setting"`
	Held    []agent.CodexReader `json:"held,omitempty"`
}

// codexAsyncAt is the advice r as a share of the window Codex's
// background compaction starts at: the fewest tokens (a compaction in the
// background keeps no one waiting, so the wait the advice weighs doesn't
// count), rounded down, so it starts no later than that. ok is false
// where it would start no sooner than the foreground does, at the lesser
// of the limit now and 90% of the window (codex-rs ModelInfo
// auto_compact_token_limit), and where the window isn't known.
func codexAsyncAt(r *tune.Compaction) (percent, value int, ok bool) {
	if r == nil || r.Window <= 0 {
		return 0, 0, false
	}
	v := r.Best
	if r.Cheapest > 0 && r.Facts.Took > 0 {
		v = r.Cheapest
	}
	percent = max(1, min(100, v*100/r.Window))
	value = r.Window * percent / 100
	fg := r.Window * 9 / 10
	if r.Current > 0 {
		fg = min(fg, r.Current)
	}
	return percent, value, value < fg
}

type tuneOwn struct {
	Model string `json:"model"`
	Value int    `json:"value"`
}

type tuneAgent struct {
	Agent  string       `json:"agent"`
	Calls  int          `json:"calls"`
	Advice []tuneAdvice `json:"advice"`
}

type tuneState struct {
	Days   int         `json:"days"`
	Agents []tuneAgent `json:"agents"`
}

// tuneOf works the advice out of the calls the agents' files hold.
func tuneOf(cs []sessions.Call, days int, home string) tuneState {
	by := map[string][]tune.Call{}
	for _, c := range cs {
		if c.Agent == "claude" || c.Agent == "codex" {
			by[c.Agent] = append(by[c.Agent], tune.FromCalls([]sessions.Call{c})...)
		}
	}
	return adviseOf(by, days, home)
}

// adviseOf is tuneOf from each agent's calls made tune's already.
func adviseOf(by map[string][]tune.Call, days int, home string) tuneState {
	out := tuneState{Days: days, Agents: []tuneAgent{}}
	weigh := pricedWeights(settings.Load())
	for _, name := range []string{"claude", "codex"} {
		calls := by[name]
		kept := agent.TuneKept(home, name)
		if name == "claude" {
			// the cache this computer's Claude Code keeps, with or without
			// calls read here, its way back to 5 minutes with it
			here := agent.TuneCacheHere(home)
			for _, k := range kept {
				if !slices.ContainsFunc(here, func(o agent.TuneKnob) bool { return o.Knob == k.Knob && o.Model == k.Model }) {
					here = append(here, k)
				}
			}
			kept = here
		}
		if len(calls) == 0 && len(kept) == 0 {
			continue
		}
		ta := tuneAgent{Agent: name, Calls: len(calls), Advice: []tuneAdvice{}}
		switch name {
		case "claude":
			models := map[string][]tune.Call{}
			var main, side []tune.Call
			for _, c := range calls {
				models[c.Model] = append(models[c.Model], c)
				if c.Sub {
					side = append(side, c)
				} else {
					main = append(main, c)
				}
			}
			for m, mc := range models {
				s, err := agent.TuneRead(home, name, agent.TuneCompact, m)
				if err != nil {
					continue
				}
				// a model asked for with [1m], or seen past 200K, has the
				// whole 1M; another the 200K Claude Code's own models start with
				window := 200000
				for _, c := range mc {
					if c.Long || c.Prompt > window {
						window = 1000000
						break
					}
				}
				cur, _ := strconv.Atoi(s.Value)
				o := tune.CompactOpts{Min: 100000, Max: 1000000, Step: 10000, Reserve: 33000, Window: window, Current: cur, Weigh: weigh}
				if r := tune.Compact(mc, o); r != nil {
					ta.Advice = append(ta.Advice, tuneAdvice{Knob: agent.TuneCompact, Model: m, Setting: s, Compact: r})
				}
			}
			sort.SliceStable(ta.Advice, func(i, j int) bool {
				return ta.Advice[i].Compact.Facts.Calls > ta.Advice[j].Compact.Facts.Calls
			})
			for _, k := range []struct {
				knob  string
				calls []tune.Call
			}{{agent.TuneCache, main}, {agent.TuneSubCache, side}} {
				s, err := agent.TuneRead(home, name, k.knob, "")
				if err != nil {
					continue
				}
				if r := tune.CacheTTL(k.calls, s.Value, weigh); r != nil {
					ta.Advice = append(ta.Advice, tuneAdvice{Knob: k.knob, Setting: s, TTL: r})
				}
			}
		case "codex":
			s, err := agent.TuneRead(home, name, agent.TuneCompact, "")
			if err != nil {
				continue
			}
			st := settings.Load().ForAgent("codex")
			window, told, own, apart := codexWindow(calls, st, provider.Catalog(), provider.GroupFinder())
			// Long conversations' value is the setting; where Codex runs
			// now is that as magpie tells it for these models (their
			// windows, a provider's own Compact at)
			cur, _ := strconv.Atoi(s.Value)
			if cur == 0 || s.Source == agent.TuneLong {
				cur = told
			}
			// a value the user chose is never past the last one offered
			o := tune.CompactOpts{Min: 50000, Max: max(1000000, window, cur, st.CompactAt), Step: 10000, Window: window, Current: cur, Weigh: weigh}
			if r := tune.Compact(calls, o); r != nil {
				ad := tuneAdvice{Knob: agent.TuneCompact, Setting: s, Compact: r}
				if own != "" && s.Source == agent.TuneLong && r.Current == told {
					ad.Own = &tuneOwn{Model: own, Value: told}
				}
				if s.Source == agent.TuneLong {
					ad.Apart = apart
				}
				if pct, v, ok := codexAsyncAt(r); ok {
					as, _ := agent.TuneRead(home, name, agent.TuneAsync, "")
					ad.Async = &tuneAsync{Percent: pct, Value: v, Window: r.Window, Setting: as, Held: agent.CodexAsyncHeldHere(home)}
				}
				ta.Advice = append(ta.Advice, ad)
			}
		}
		// what tune put in stays on the card, Undo with it, when the range
		// has too few calls to advise on it (linkaww on Discord), and so
		// does Claude Code's cache as it is here
		for _, k := range kept {
			if !slices.ContainsFunc(ta.Advice, func(a tuneAdvice) bool { return a.Knob == k.Knob && a.Model == k.Model }) {
				ta.Advice = append(ta.Advice, tuneAdvice{Knob: k.Knob, Model: k.Model, Setting: k.Setting, Few: true})
			}
		}
		out.Agents = append(out.Agents, ta)
	}
	return out
}

// pricedWeights weighs a model's calls at what magpie bills them where the
// agent asked for a model magpie wrote into its config (provider/model, as
// Claude Code is given it, dashed): the price of the provider it resolves
// to, as the usage pages count it, so a Claude model on Copilot has its
// hour at the 5-minute write Copilot bills and one on Anthropic at 2× input
// (#3435). By its name alone a provider/model id was weighed as no Claude
// model, its hour costing what a 5-minute write does. A model the agent
// named itself never went through magpie and is weighed at its maker's list
// (tune.WeightsFor), as is one magpie has no cache price for.
func pricedWeights(st settings.Settings) tune.Weigh {
	memo := map[string]tune.Weights{}
	return func(model string) tune.Weights {
		if w, ok := memo[model]; ok {
			return w
		}
		w := tune.WeightsFor(model)
		if strings.Contains(model, "/") {
			if p, m, ok := provider.Resolve(provider.ClaudeRead(model)); ok {
				if pr, ok := provider.EffectivePriceIn(st, p.ID, m); ok && pr.Input > 0 && pr.Output > 0 && pr.CacheRead > 0 && pr.CacheWrite > 0 {
					w = tune.Weights{Read: pr.CacheRead / pr.Input, Write: pr.CacheWrite / pr.Input,
						Write1h: pr.OneHour() / pr.Input, Out: pr.Output / pr.Input}
				}
			}
		}
		memo[model] = w
		return w
	}
}

// codexWindow is the window Codex runs the calls' models to, and where
// magpie has it compact them when its config.toml says nothing (told: the
// largest of the models Long conversations moves, 0 when magpie tells it
// none). A model outside the catalog, or one whose window magpie doesn't
// know, leaves told to the others: it once made told 0 for the whole
// range, so "Now" was guessed from the prompts instead, and the same
// setting read 380K over 7 days and 430K today (hoobnn on Discord).
//
// A model with a Compact at of its own (provider.CompactSetIn, on it or
// its provider) compacts there whatever Long conversations says, so it
// isn't in told while another model is: over 7 days such a model's 430K
// was "Now", a Use moved every other model to the advice, and the card
// still read 430K (hoobnn, 11-01). Those models are apart, each with
// where it compacts. Only when every model of the range has one is told
// the largest of theirs, and own the model it comes from: no Use moves
// it. A window the user typed is not one of these: it gives way to a size
// tune puts in Long conversations (CompactFor's rule).
//
// A model of magpie's catalog has the window it is listed with, one the
// user typed or the vendor's, and is told the threshold the user set on
// it, else Settings › Long conversations (provider.CompactFor, as
// codexcat writes it). One whose window magpie doesn't know caps nothing
// (window 0): the largest prompt it had been sent was taken for its
// window, and a 1M model set to compact at 750K was offered no more than
// 412K (zhemu on Discord). Codex's own models, outside magpie's catalog,
// run to 272K or the largest prompt seen, as before.
func codexWindow(calls []tune.Call, st settings.Settings, entries []provider.Entry, find func(string) (provider.Group, []provider.Member, bool)) (window, told int, own string, apart []tuneOwn) {
	byID := make(map[string]provider.Entry, len(entries))
	for _, e := range entries {
		byID[strings.ToLower(e.ID)] = e
	}
	peak := map[string]int{}
	for _, c := range calls {
		peak[c.Model] = max(peak[c.Model], c.Prompt)
	}
	unknown, moved, held := false, false, 0
	for m, p := range peak {
		e, ok := byID[strings.TrimPrefix(m, "magpie/")]
		switch {
		case !ok:
			window = max(window, 272000, p)
		case e.Context <= 0:
			unknown = true
		default:
			window = max(window, e.Context, p)
			if n := provider.CompactSetIn(st, e.ID, find); n > 0 {
				at := min(e.Context, n)
				apart = append(apart, tuneOwn{Model: e.ID, Value: at})
				held = max(held, at)
				continue
			}
			at := st.Working(e.Context)
			if n := provider.CompactFor(st, e, find); n > 0 {
				at = min(e.Context, n)
			}
			told, moved = max(told, at), true
		}
	}
	// the same models, in the same order, in every read
	slices.SortFunc(apart, func(a, b tuneOwn) int {
		if a.Value != b.Value {
			return b.Value - a.Value
		}
		return strings.Compare(a.Model, b.Model)
	})
	if !moved && len(apart) > 0 {
		told, own, apart = held, apart[0].Model, nil
	}
	if unknown {
		window = 0
	}
	return window, told, own, apart
}

func tuneRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tune", func(rw http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days <= 0 || days > 30 {
			days = 7
		}
		writeJSON(rw, tuneFor(days))
	})
	type change struct {
		Agent, Knob, Model, Value string
	}
	mux.HandleFunc("POST /api/tune/apply", func(rw http.ResponseWriter, r *http.Request) {
		var in change
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := agent.TuneApply("", in.Agent, in.Knob, in.Model, in.Value); err != nil {
			fail(rw, err)
			return
		}
		tuneForget()
		s, _ := agent.TuneRead("", in.Agent, in.Knob, in.Model)
		writeJSON(rw, s)
	})
	// Claude Code's prompt cache picked in its settings: "5m", "1h", or ""
	// for Claude Code's own default (agent.ClaudeCacheSet)
	mux.HandleFunc("POST /api/settings/claude-cache", func(rw http.ResponseWriter, r *http.Request) {
		var in struct{ Knob, Value string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := agent.ClaudeCacheSet("", in.Knob, in.Value); err != nil {
			fail(rw, err)
			return
		}
		tuneForget()
		writeJSON(rw, settingsState())
	})
	mux.HandleFunc("POST /api/tune/undo", func(rw http.ResponseWriter, r *http.Request) {
		var in change
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if err := agent.TuneUndo("", in.Agent, in.Knob, in.Model); err != nil {
			fail(rw, err)
			return
		}
		tuneForget()
		s, _ := agent.TuneRead("", in.Agent, in.Knob, in.Model)
		writeJSON(rw, s)
	})
}

// tuneKept is what tune has read of the Claude Code and Codex files: each
// file's calls made tune's, by the size and time it had. The Context tab
// asks for the advice at every refresh, 5s by default, and each ask read
// every session file of the range whole into sessions.Call (300k calls
// were ~500 MB at once, a second of CPU each time): a small server went
// down with the tab open (Yayoi_no_yume on X). Now a file is read again
// only when it changed, one at a time, and what is kept of a call is
// tune's ~100 bytes.
var tuneKept = struct {
	sync.Mutex
	files map[string]tuneFile
}{files: map[string]tuneFile{}}

type tuneFile struct {
	size  int64
	mod   time.Time
	agent string
	calls []tune.Call
	// msgs: each of its calls' message, hashed, the failed ones too; at
	// is where calls[i]'s own is
	msgs []uint64
	at   []int
}

// tuneAnswer is the advice last given. Working it out goes over every
// call of the range, a week's 300k of them half a second of CPU and a few
// hundred MB at once, too much for each refresh of the Usage page (5s): it
// stands for ctxAgain, and a setting tune changes lets it go at once.
var tuneAnswer struct {
	sync.Mutex
	days int
	at   time.Time
	out  tuneState
}

// tuneFor is the advice over the last days.
func tuneFor(days int) tuneState {
	tuneAnswer.Lock()
	defer tuneAnswer.Unlock()
	if tuneAnswer.days == days && time.Since(tuneAnswer.at) < ctxAgain {
		return tuneAnswer.out
	}
	out := adviseOf(tuneCallsSince(time.Now().AddDate(0, 0, -days)), days, "")
	tuneAnswer.days, tuneAnswer.at, tuneAnswer.out = days, time.Now(), out
	return out
}

func tuneForget() {
	tuneAnswer.Lock()
	tuneAnswer.at = time.Time{}
	tuneAnswer.Unlock()
}

// tuneRead, in tests, is told each file tune reads.
var tuneRead func(path string)

func msgHash(s string) uint64 {
	if s == "" {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64() | 1
}

// tuneCallsSince is each agent's calls since, as sessions.Calls has them:
// a message a resumed Claude Code session copied is the earlier file's.
func tuneCallsSince(since time.Time) map[string][]tune.Call {
	var srcs []sessions.CallSource
	for _, s := range sessions.CallSources() {
		if (s.Agent == "claude" || s.Agent == "codex") && !s.Modified.Before(since) {
			srcs = append(srcs, s)
		}
	}
	// earlier files own messages copied into a resumed Claude session
	sort.Slice(srcs, func(i, j int) bool {
		if !srcs[i].Modified.Equal(srcs[j].Modified) {
			return srcs[i].Modified.Before(srcs[j].Modified)
		}
		return srcs[i].Path < srcs[j].Path
	})
	tuneKept.Lock()
	defer tuneKept.Unlock()
	on := make(map[string]bool, len(srcs))
	for _, s := range srcs {
		on[s.Path] = true
		if f, ok := tuneKept.files[s.Path]; ok && f.size == s.Size && f.mod.Equal(s.Modified) && f.agent == s.Agent {
			continue
		}
		if tuneRead != nil {
			tuneRead(s.Path)
		}
		f := tuneFile{size: s.Size, mod: s.Modified, agent: s.Agent}
		cs := sessions.ReadCallSource(s)
		f.msgs = make([]uint64, len(cs))
		for i, c := range cs {
			f.msgs[i] = msgHash(c.Msg)
			if tc := tune.FromCalls(cs[i : i+1]); len(tc) == 1 {
				f.calls = append(f.calls, tc[0])
				f.at = append(f.at, i)
			}
		}
		tuneKept.files[s.Path] = f
	}
	for p := range tuneKept.files {
		if !on[p] {
			delete(tuneKept.files, p)
		}
	}
	by := map[string][]tune.Call{}
	seen := map[uint64]bool{}
	for _, s := range srcs {
		f := tuneKept.files[s.Path]
		for i, c := range f.calls {
			if m := f.msgs[f.at[i]]; m != 0 && seen[m] || c.Time.Before(since) {
				continue
			}
			by[f.agent] = append(by[f.agent], c)
		}
		for _, m := range f.msgs {
			if m != 0 {
				seen[m] = true
			}
		}
	}
	for _, cs := range by {
		slices.SortStableFunc(cs, func(a, b tune.Call) int { return b.Time.Compare(a.Time) })
	}
	return by
}

// claudeCacheJSON is one of Claude Code's prompt-cache lifetimes: its
// knob (agent.TuneCache, agent.TuneSubCache), the value it runs at ("" for
// Claude Code's own default), and the variable that sets it, when one does.
type claudeCacheJSON struct {
	Knob   string `json:"knob"`
	Value  string `json:"value,omitempty"`
	Locked string `json:"locked,omitempty"`
}

func claudeCacheState() []claudeCacheJSON {
	var out []claudeCacheJSON
	for _, knob := range []string{agent.TuneCache, agent.TuneSubCache} {
		s, err := agent.TuneRead("", "claude", knob, "")
		if err != nil {
			continue
		}
		out = append(out, claudeCacheJSON{Knob: knob, Value: s.Value, Locked: s.Locked})
	}
	return out
}
