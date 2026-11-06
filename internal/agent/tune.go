package agent

// The settings tune advises (internal/tune), as each agent's config has
// them, and putting one in on the user's say: Claude Code's auto-compact
// window for a model and its cache lifetimes, and where Codex and Claude
// Code compact. What was there before is stashed, so Undo puts it back; a
// variable that takes precedence over the setting is told of, never
// written over.
//
// Where Codex compacts, and Claude Code wherever magpie's own window is
// what it runs at, is one value: Long conversations (settings.CompactAt,
// FullContext), which Sync writes into each agent's config. Tune reads and
// sets that value, so the Usage page and the agents' settings never show
// two numbers (7eleven on Discord). Before, tune wrote Codex's
// model_auto_compact_token_limit beside it; tuneMigrateCodex moves such a
// value into Long conversations. An agent with a Long conversations of
// its own (settings.AgentCompacts) has tune read and set that one.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The settings tune knows.
const (
	TuneCompact  = "compact"            // when the agent compacts its conversation, in tokens
	TuneCache    = "cache_ttl"          // Claude Code: the main conversation's cache, 5m or 1h
	TuneSubCache = "subagent_cache_ttl" // Claude Code: its subagents', workflows' and helpers'
	// Codex (0.163 on): the share of a model's window past which it
	// compacts in the background while the turn goes on, 1–100; its
	// foreground limit stays where it is, as the backstop
	TuneAsync = "async_compact"
)

// TuneSetting is one of those settings as an agent has it now.
type TuneSetting struct {
	Value string `json:"value,omitempty"` // "" when nothing sets it
	// Where Value comes from: settings (the agent's own config file),
	// model (Claude Code's modelSettings for the model), env, or long
	// (TuneLong: magpie's Long conversations, "" under Full window)
	Source string `json:"source,omitempty"`
	// Locked names the variable that takes precedence over the setting,
	// which tune leaves as it is: putting the advice in would change nothing
	Locked string `json:"locked,omitempty"`
	// Ours: tune put Value in, and Undo puts back what was there
	Ours bool `json:"ours,omitempty"`
}

// TuneLong is the Source of a window that is magpie's Long conversations,
// which Apply and Undo change in magpie's settings, not in the agent's file.
const TuneLong = "long"

// codexCompactKey is the key tune wrote Codex's window to before it went
// to Long conversations; one the user wrote comes before magpie's window.
const codexCompactKey = "model_auto_compact_token_limit"

// tuneLongKey is the stash of Long conversations as it was before tune
// put a window in it, shared by every agent's card, as the value is.
const tuneLongKey = "tune.long.compact"

type tuneLong struct {
	CompactAt int  `json:"compactAt"`
	Full      bool `json:"full"`
	Set       int  `json:"set"`
}

// longKey is where Long conversations as agent reads it is stashed: the
// one for every agent, or the agent's own (settings.AgentCompacts), which
// a Use puts the advice in instead.
func longKey(s settings.Settings, agent string) string {
	if _, own := s.AgentCompacts[agent]; own {
		return tuneLongKey + "." + agent
	}
	return tuneLongKey
}

func tuneLongStashed(key string) (tuneLong, bool) {
	raw := stashShared()[key]
	var st tuneLong
	if raw == "" || json.Unmarshal([]byte(raw), &st) != nil {
		return tuneLong{}, false
	}
	return st, true
}

// longValue is Long conversations' window as the setting reads: "" under
// Full window, each model's whole one.
func longValue(s settings.Settings) string {
	if n := s.Compact(); n > 0 {
		return strconv.Itoa(n)
	}
	return ""
}

// longOurs says Long conversations, as agent reads it, still holds the
// window tune put in.
func longOurs(s settings.Settings, agent string) bool {
	st, ok := tuneLongStashed(longKey(s, agent))
	a := s.ForAgent(agent)
	return ok && !a.FullContext && a.Compact() == st.Set
}

// longApply puts n in Long conversations, what it was kept the first time
// for Undo. Sync then writes it into every agent magpie routes.
func longApply(agent string, n int) error {
	s := settings.Load()
	key, a := longKey(s, agent), s.ForAgent(agent)
	st, ok := tuneLongStashed(key)
	if !ok {
		st = tuneLong{CompactAt: a.CompactAt, Full: a.FullContext}
	}
	st.Set = n
	b, _ := json.Marshal(st)
	stash(map[string]string{key: string(b)})
	if key != tuneLongKey {
		return provider.SetAgentCompact(agent, &settings.AgentCompact{At: n})
	}
	return provider.SetCompactAt(n) // Full window off too
}

// longUndo puts Long conversations back as it was before tune, unless the
// user changed it since: then it is theirs, and only forgotten.
func longUndo(agent string) error {
	s := settings.Load()
	key := longKey(s, agent)
	st, ok := tuneLongStashed(key)
	if !ok {
		return fmt.Errorf("magpie didn't set Long conversations")
	}
	defer forget(key)
	if !longOurs(s, agent) {
		return nil
	}
	if key != tuneLongKey {
		return provider.SetAgentCompact(agent, &settings.AgentCompact{At: st.CompactAt, Full: st.Full})
	}
	if st.Full {
		return provider.SetFullContext(true)
	}
	return provider.SetCompactAt(st.CompactAt)
}

// tuneMigrateCodex moves a model_auto_compact_token_limit tune wrote into
// Codex's config.toml before (tune.codex.compact) into Long conversations,
// and puts back what the file had. Long conversations at magpie's default
// takes it, as the user's own choice; one the user set takes it only when
// it is lower, which is where Codex compacted (it stops at the lesser of
// the limit and the window it is told), so nothing changes for them. A
// limit the user changed after tune wrote it is theirs and stays.
func tuneMigrateCodex(home string) {
	st, ok := tuneStashed("codex", TuneCompact, "")
	if !ok {
		return
	}
	p := tunePlace{path: filepath.Join(here(home).codexHome(), "config.toml"), key: codexCompactKey, toml: true}
	defer forget(tuneKey("codex", TuneCompact, ""))
	v, has := p.get()
	if !has || strings.Trim(v, `"'`) != st.Set {
		return
	}
	if n, err := strconv.Atoi(st.Set); err == nil && n > 0 {
		s := settings.Load().ForAgent("codex")
		if s.CompactAt == 0 || s.FullContext || n < s.Compact() {
			if err := longApply("codex", n); err != nil {
				return
			}
		}
	}
	if st.Had {
		_ = edit.SetTOMLTop(p.path, edit.KV{Path: p.key, Value: tomlRaw(st.Raw)})
		return
	}
	_ = edit.DelTOMLTop(p.path, p.key)
}

// tuneStash is what a setting was before tune put one in.
type tuneStash struct {
	Had bool   `json:"had"`
	Raw string `json:"raw,omitempty"` // as the file had it, JSON or TOML
	Set string `json:"set"`           // the value tune put in
}

func tuneKey(agent, knob, model string) string {
	k := "tune." + agent + "." + knob
	if model != "" {
		k += "." + model
	}
	return k
}

func tuneHome(home string) string {
	if home != "" {
		return home
	}
	h, _ := os.UserHomeDir()
	return h
}

// tunePlace is where a setting lives: the file, its key path, and the
// variables that take precedence over it, first first.
type tunePlace struct {
	path   string
	key    string
	toml   bool
	env    []string
	number bool
}

func tuneWhere(home, agent, knob, model string) (tunePlace, error) {
	home = tuneHome(home)
	switch agent {
	case "claude":
		path := filepath.Join(here(home).claudeDir(), "settings.json")
		switch knob {
		case TuneCompact:
			n := claudeName(model)
			if n == "" {
				return tunePlace{}, fmt.Errorf("%q is no Claude model: Claude Code's window for it is magpie's own, under Settings", model)
			}
			return tunePlace{path: path, key: "modelSettings." + n + ".autoCompactWindow", env: []string{claudeCompactEnv}, number: true}, nil
		case TuneCache:
			return tunePlace{path: path, key: "promptCacheTtl", env: []string{"FORCE_PROMPT_CACHING_5M", "CLAUDE_CODE_PROMPT_CACHE_TTL"}}, nil
		case TuneSubCache:
			return tunePlace{path: path, key: "subagentPromptCacheTtl", env: []string{"FORCE_PROMPT_CACHING_5M", "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL"}}, nil
		}
	case "codex":
		if knob == TuneCompact {
			// the user's own limit in config.toml, which comes before Long
			// conversations; tune itself writes only the latter
			return tunePlace{path: filepath.Join(here(home).codexHome(), "config.toml"), key: codexCompactKey, toml: true, number: true}, nil
		}
	}
	return tunePlace{}, fmt.Errorf("%s has no setting %q tune can put in", agent, knob)
}

func (p tunePlace) get() (string, bool) {
	if p.toml {
		return edit.GetTOMLTop(p.path, p.key)
	}
	return edit.GetJSON(p.path, p.key)
}

// envOf is a variable Claude Code starts with: in settings.json's env, or
// magpie's own, which a terminal it is run from most likely has too.
func (p tunePlace) envOf(k string) string {
	if v, _ := edit.GetJSON(p.path, "env."+k); v != "" {
		return v
	}
	return os.Getenv(k)
}

// TuneRead is how an agent has a setting tune advises now.
func TuneRead(home, agent, knob, model string) (TuneSetting, error) {
	if agent == "codex" && knob == TuneAsync {
		return asyncRead(home), nil
	}
	p, err := tuneWhere(home, agent, knob, model)
	if err != nil {
		return TuneSetting{}, err
	}
	if agent == "codex" && knob == TuneCompact {
		tuneMigrateCodex(home)
		var s TuneSetting
		if v, ok := p.get(); ok {
			// the user's own limit: Codex stops at it before the window
			// magpie tells it
			s.Value, s.Source, s.Locked = strings.Trim(v, `"'`), "settings", codexCompactKey
			return s, nil
		}
		st := settings.Load()
		return TuneSetting{Value: longValue(st.ForAgent(agent)), Source: TuneLong, Ours: longOurs(st, agent)}, nil
	}
	var s TuneSetting
	for _, k := range p.env {
		if v := p.envOf(k); v != "" && v != "0" && v != "false" {
			if k == claudeCompactEnv && claudeLongEnv(p.path, v) {
				// as Long conversations has it now, which Sync is writing
				st := settings.Load()
				return TuneSetting{Value: longValue(st.ForAgent(agent)), Source: TuneLong, Ours: longOurs(st, agent)}, nil
			}
			s.Locked = k
			s.Value, s.Source = v, "env"
			if k == "FORCE_PROMPT_CACHING_5M" {
				s.Value = "5m"
			}
			break
		}
	}
	if s.Locked == "" {
		if v, ok := p.get(); ok && v != "auto" {
			s.Value, s.Source = strings.Trim(v, `"'`), "settings"
			if knob == TuneCompact && agent == "claude" {
				s.Source = "model"
			}
		} else if agent == "claude" && knob == TuneCompact {
			// the top-level window serves every model with none of its
			// own: magpie's Long conversations where magpie wrote it
			if v, ok := edit.GetJSON(p.path, "autoCompactWindow"); ok && v != "auto" {
				if claudeLongWindow(p.path, "autoCompactWindow", "claude.compact_default") {
					return TuneSetting{Value: v, Source: TuneLong, Ours: longOurs(settings.Load(), agent)}, nil
				}
				s.Value, s.Source = v, "settings"
			}
		}
	}
	if st, ok := tuneStashed(agent, knob, model); ok && s.Locked == "" {
		v, _ := p.get()
		s.Ours = strings.Trim(v, `"'`) == st.Set
	}
	return s, nil
}

// claudeLongEnv says the CLAUDE_CODE_AUTO_COMPACT_WINDOW Claude Code has
// is the one magpie wrote in settings.json from Long conversations
// (writeCompact), not a provider's Compact at for the main model or the
// user's own.
func claudeLongEnv(path, v string) bool {
	in, ok := edit.GetJSON(path, "env."+claudeCompactEnv)
	m := stashLoad()
	return ok && in == v && v == m["claude.compact_window"] && m["claude.compact_long"] == "1"
}

// claudeLongWindow says the window at key in Claude Code's settings.json
// is the one magpie wrote there (its stash under stashKey) from Long
// conversations, not a provider's Compact at for the main model or the
// user's own.
func claudeLongWindow(path, key, stashKey string) bool {
	v, ok := edit.GetJSON(path, key)
	if !ok || v == "" || v != stashShared()[stashKey] {
		return false
	}
	return v == longValue(settings.Load().ForAgent("claude"))
}

func tuneStashed(agent, knob, model string) (tuneStash, bool) {
	raw := stashShared()[tuneKey(agent, knob, model)]
	if raw == "" {
		return tuneStash{}, false
	}
	var st tuneStash
	if json.Unmarshal([]byte(raw), &st) != nil {
		return tuneStash{}, false
	}
	return st, true
}

// TuneApply puts value in as an agent's setting, on the user's say. What
// was there before is kept, the first time only, for TuneUndo. A variable
// that takes precedence is an error: the setting would change nothing.
func TuneApply(home, agent, knob, model, value string) error {
	if agent == "codex" && knob == TuneAsync {
		return asyncApply(home, value)
	}
	p, err := tuneWhere(home, agent, knob, model)
	if err != nil {
		return err
	}
	cur, _ := TuneRead(home, agent, knob, model)
	if cur.Locked != "" {
		return fmt.Errorf("%s=%s is set and comes before the setting: take it out first", cur.Locked, cur.Value)
	}
	if cur.Source == TuneLong {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("%q is no token count", value)
		}
		if agent == "claude" && (n < 100000 || n > 1000000) {
			return fmt.Errorf("Claude Code takes a window of 100000 to 1000000 tokens, not %d", n)
		}
		return longApply(agent, n)
	}
	var v any = value
	if p.number {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("%q is no token count", value)
		}
		if agent == "claude" && (n < 100000 || n > 1000000) {
			return fmt.Errorf("Claude Code takes a window of 100000 to 1000000 tokens, not %d", n)
		}
		v = n
	} else if value != "5m" && value != "1h" {
		return fmt.Errorf("the cache is kept 5m or 1h, not %q", value)
	}
	key := tuneKey(agent, knob, model)
	if _, ok := tuneStashed(agent, knob, model); !ok {
		raw, had := p.get()
		b, _ := json.Marshal(tuneStash{Had: had, Raw: raw, Set: value})
		stash(map[string]string{key: string(b)})
	} else {
		st, _ := tuneStashed(agent, knob, model)
		st.Set = value
		b, _ := json.Marshal(st)
		stash(map[string]string{key: string(b)})
	}
	if p.toml {
		return edit.SetTOMLTop(p.path, edit.KV{Path: p.key, Value: v})
	}
	return edit.SetJSON(p.path, edit.KV{Path: p.key, Value: v})
}

// TuneKnob is one setting of an agent's, the model a window is for, and
// how the agent has it now.
type TuneKnob struct {
	Knob    string
	Model   string
	Setting TuneSetting
}

// TuneKept are the settings in agent's own config that tune put in and
// that still hold its value (Ours), so Undo can put back what was there:
// Claude Code's cache lifetimes and per-model windows. The Usage page's
// card keeps them there when a range has too few calls to advise on
// (linkaww on Discord: a 1-hour cache put in, with no way back to 5
// minutes). The cache lifetimes are also picked in Claude Code's own
// settings (ClaudeCacheSet), which makes them the user's. One in magpie's Long
// conversations or Codex's background compaction is left out: Settings
// shows and changes those.
func TuneKept(home, agent string) []TuneKnob {
	prefix := "tune." + agent + "."
	var out []TuneKnob
	for k := range stashShared() {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		knob, model, _ := strings.Cut(rest, ".")
		if knob != TuneCompact && knob != TuneCache && knob != TuneSubCache {
			continue
		}
		s, err := TuneRead(home, agent, knob, model)
		if err != nil || !s.Ours || s.Source == TuneLong {
			continue
		}
		out = append(out, TuneKnob{Knob: knob, Model: model, Setting: s})
	}
	// the same order in every read: the cache rows, then the windows by model
	slices.SortFunc(out, func(a, b TuneKnob) int {
		window := func(k TuneKnob) int {
			if k.Knob == TuneCompact {
				return 1
			}
			return 0
		}
		return cmp.Or(window(a)-window(b), cmp.Compare(a.Knob, b.Knob), cmp.Compare(a.Model, b.Model))
	})
	return out
}

// TuneCacheHere is Claude Code's two prompt-cache lifetimes as this
// computer's Claude Code reads them, whoever set them, when its folder is
// here: the main conversation's always, the subagents' when something
// sets it. They change what this computer's Claude Code sends, whichever
// magpie its requests go on to (linkaww on Discord: Claude Code through a
// Remote magpie, too few calls read here to advise on, and the card had
// no row to tell the cache it keeps, or a way back to 5 minutes).
func TuneCacheHere(home string) []TuneKnob {
	p, err := tuneWhere(home, "claude", TuneCache, "")
	if err != nil {
		return nil
	}
	if fi, err := os.Stat(filepath.Dir(p.path)); err != nil || !fi.IsDir() {
		return nil
	}
	var out []TuneKnob
	for _, knob := range []string{TuneCache, TuneSubCache} {
		s, err := TuneRead(home, "claude", knob, "")
		if err != nil || knob == TuneSubCache && s.Value == "" {
			continue
		}
		out = append(out, TuneKnob{Knob: knob, Setting: s})
	}
	return out
}

// TuneUndo puts back what a setting was before tune put one in, unless
// the user has changed it since: then it is theirs, and only forgotten.
func TuneUndo(home, agent, knob, model string) error {
	if agent == "codex" && knob == TuneAsync {
		return asyncUndo()
	}
	p, err := tuneWhere(home, agent, knob, model)
	if err != nil {
		return err
	}
	if cur, _ := TuneRead(home, agent, knob, model); cur.Source == TuneLong {
		return longUndo(agent)
	}
	st, ok := tuneStashed(agent, knob, model)
	if !ok {
		return fmt.Errorf("magpie didn't set %s", p.key)
	}
	defer forget(tuneKey(agent, knob, model))
	if v, _ := p.get(); strings.Trim(v, `"'`) != st.Set {
		return nil
	}
	if st.Had {
		var v any
		if p.toml {
			v = tomlRaw(st.Raw)
		} else if json.Unmarshal([]byte(st.Raw), &v) != nil {
			v = st.Raw
		}
		if p.toml {
			return edit.SetTOMLTop(p.path, edit.KV{Path: p.key, Value: v})
		}
		return edit.SetJSON(p.path, edit.KV{Path: p.key, Value: v})
	}
	if p.toml {
		return edit.DelTOMLTop(p.path, p.key)
	}
	if err := edit.DelJSON(p.path, p.key); err != nil {
		return err
	}
	// an entry under modelSettings left with nothing in it goes too
	if agent == "claude" && knob == TuneCompact {
		n := claudeName(model)
		empty := func(k string) bool {
			v, ok := edit.GetJSON(p.path, k)
			return ok && strings.Join(strings.Fields(v), "") == "{}"
		}
		if empty("modelSettings." + n) {
			edit.DelJSON(p.path, "modelSettings."+n)
		}
		if empty("modelSettings") {
			edit.DelJSON(p.path, "modelSettings")
		}
	}
	return nil
}

// tomlRaw is a TOML value as GetTOMLTop read it, as a value to write.
func tomlRaw(raw string) any {
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return strings.Trim(raw, `"'`)
}

// Codex's background compaction, as tune puts the advice in it (herman_hong
// on Discord: a foreground compaction kept them waiting, so they set 384K
// by hand; the advice's 120K is wanted in the background, the 384K kept as
// the backstop). It is Settings' Background compaction
// (settings.CodexAsyncCompact, CodexAsyncCompactPercent), which Sync writes
// into Codex's config.toml as [features.async_compaction] enabled and
// model_async_compact_threshold_percent: a share of the model's window,
// not tokens (codex-rs core/src/config, 1 to 100, Codex's own 65). The
// foreground limit, the user's model_auto_compact_token_limit or Long
// conversations, isn't touched. What Settings had before is stashed, so
// Undo puts it back unless the user changed it since.

// TuneAsyncSource is the Source of the background compaction's share.
const TuneAsyncSource = "async"

const tuneAsyncKey = "tune.codex." + TuneAsync

type tuneAsyncStash struct {
	On      bool `json:"on"`
	Percent int  `json:"percent"`
	Set     int  `json:"set"`
}

func asyncStashed() (tuneAsyncStash, bool) {
	raw := stashShared()[tuneAsyncKey]
	var st tuneAsyncStash
	if raw == "" || json.Unmarshal([]byte(raw), &st) != nil {
		return tuneAsyncStash{}, false
	}
	return st, true
}

func asyncOurs(s settings.Settings) bool {
	st, ok := asyncStashed()
	return ok && s.CodexAsyncCompact && s.CodexAsyncCompactPercent == st.Set
}

// asyncRead is the share Codex compacts in the background at: "" when it
// is off, 65 for Codex's own. One the user has in config.toml comes first
// (codexAsyncOwn), and is Locked.
func asyncRead(home string) TuneSetting {
	out := TuneSetting{Source: TuneAsyncSource}
	if own := codexAsyncOwn(home); own != "" {
		out.Locked = own
		return out
	}
	s := settings.Load()
	if s.CodexAsyncCompact {
		p := s.CodexAsyncCompactPercent
		if p == 0 {
			p = 65
		}
		out.Value = strconv.Itoa(p)
	}
	out.Ours = asyncOurs(s)
	return out
}

func asyncApply(home, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 100 {
		return fmt.Errorf("Codex compacts in the background past 1 to 100%% of a model's window, not %q", value)
	}
	if own := codexAsyncOwn(home); own != "" {
		return fmt.Errorf("Codex's config.toml sets %s, which comes before magpie's: take it out first", own)
	}
	s := settings.Load()
	st, ok := asyncStashed()
	if !ok {
		st = tuneAsyncStash{On: s.CodexAsyncCompact, Percent: s.CodexAsyncCompactPercent}
	}
	st.Set = n
	b, _ := json.Marshal(st)
	stash(map[string]string{tuneAsyncKey: string(b)})
	return provider.SetCodexAsyncCompact(true, n)
}

// asyncUndo puts Background compaction back as it was before tune, unless
// the user changed it since: then it is theirs, and only forgotten.
func asyncUndo() error {
	st, ok := asyncStashed()
	if !ok {
		return fmt.Errorf("magpie didn't set Codex's background compaction")
	}
	defer forget(tuneAsyncKey)
	if !asyncOurs(settings.Load()) {
		return nil
	}
	return provider.SetCodexAsyncCompact(st.On, st.Percent)
}

// ClaudeCacheSet is one of Claude Code's prompt-cache lifetimes as the
// user picks it in Claude Code's settings, beside its compaction (Linka on
// Discord: the card offers no way to change a lifetime that is already
// the cheapest): "5m" or "1h", written where the card writes it
// (promptCacheTtl, subagentPromptCacheTtl in settings.json), or "" taking
// the key out, so Claude Code's own default holds. Claude Code reads the
// variables before the setting (FORCE_PROMPT_CACHING_5M, then
// CLAUDE_CODE_PROMPT_CACHE_TTL or CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL),
// so with one set nothing is written. The pick is the user's own: a value
// the card stashed is forgotten, so its Undo never puts an older one back
// over it.
func ClaudeCacheSet(home, knob, value string) error {
	if knob != TuneCache && knob != TuneSubCache {
		return fmt.Errorf("Claude Code has no prompt cache %q", knob)
	}
	if value != "" && value != "5m" && value != "1h" {
		return fmt.Errorf("the cache is kept 5m or 1h, not %q", value)
	}
	p, err := tuneWhere(home, "claude", knob, "")
	if err != nil {
		return err
	}
	if cur, _ := TuneRead(home, "claude", knob, ""); cur.Locked != "" {
		return fmt.Errorf("%s=%s is set and comes before the setting: take it out first", cur.Locked, cur.Value)
	}
	forget(tuneKey("claude", knob, ""))
	if value == "" {
		if _, ok := p.get(); !ok {
			return nil
		}
		return edit.DelJSON(p.path, p.key)
	}
	return edit.SetJSON(p.path, edit.KV{Path: p.key, Value: value})
}
