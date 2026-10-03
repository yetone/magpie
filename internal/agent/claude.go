package agent

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code reads its endpoint from the `env` block of settings.json.
// Pointing ANTHROPIC_BASE_URL at the gateway and naming a catalog model in
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku resolve through) is
// all it takes to run it on any provider.

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are the aliases Claude Code resolves (/model opus, a
// subagent's "model: haiku", …), each of which can have a model of its own.
// A tier that has none follows the main model.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

// claudeEfforts are the levels Claude Code starts with: settings.json
// keeps the first four, max is claudeEffortEnv.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEffortEnv is the effort every Claude Code session asks for, max
// among them. It outranks /effort, so only max, which settings.json can't
// keep, is written there.
const claudeEffortEnv = "CLAUDE_CODE_EFFORT_LEVEL"

// claudeNameRe finds the Claude model an id names, as Claude Code matches it
// to its settings: claude-opus-5-5 in claude-opus-5-5[1m], in a dated id or
// under a provider's prefix (anthropic/…).
var claudeNameRe = regexp.MustCompile(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:[^0-9]|$)`)

// claudeName is the Claude model a model id names, "" for another vendor's,
// an alias (opus) or none.
func claudeName(model string) string {
	m := claudeNameRe.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return ""
	}
	name := "claude-" + m[1] + "-" + m[2]
	if m[3] != "" {
		name += "-" + m[3]
	}
	return name
}

// claudeEffortsFor is the levels Claude Code sends a model at, as 2.1.285
// sends them: Opus 4.5 takes up to high, Opus and Sonnet 4.6 skip xhigh, the
// Haiku and older Claude models none; any higher level runs as the highest
// below it. A model it doesn't know (another vendor's through magpie, a Claude
// newer than it) gets the level as set.
func claudeEffortsFor(model string) []string {
	switch claudeName(model) {
	case "claude-opus-4-5":
		return []string{"low", "medium", "high"}
	case "claude-opus-4-6", "claude-sonnet-4-6":
		return []string{"low", "medium", "high", "max"}
	case "claude-haiku-4-5", "claude-sonnet-4-5", "claude-opus-4", "claude-sonnet-4":
		return nil
	case "":
		if strings.Contains(strings.ToLower(model), "claude-3") {
			return nil
		}
	}
	return claudeEfforts
}

// claudeTopEffort is the models the effortLevel at the top of the user's
// settings.json applies to: Opus 5, Fable 5.1 and those before them. Opus
// 5.5 and every Claude model after it (any Claude Code doesn't know) read
// only modelSettings.<model>.effortLevel (2.1.251 on); another vendor's
// reads the top one.
var claudeTopEffort = []string{
	"claude-opus-4", "claude-sonnet-4", "claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5",
	"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4-7", "claude-opus-4-8",
	"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-fable-5-1",
}

func claudeReadsTop(model string) bool {
	n := claudeName(model)
	return n == "" || contains(claudeTopEffort, n)
}

// claudeClamp is the level Claude Code runs a model at when set to v: the
// highest the model takes at or below it, "" when it takes none.
func claudeClamp(v string, levels []string) string {
	if v == "" || len(levels) == 0 || contains(levels, v) {
		if len(levels) == 0 {
			return ""
		}
		return v
	}
	at := slices.Index(claudeEfforts, v)
	if at < 0 {
		return v
	}
	out := levels[0]
	for _, l := range levels {
		if slices.Index(claudeEfforts, l) <= at {
			out = l
		}
	}
	return out
}

// claudeAliases are the names Claude Code takes for a model besides ids.
var claudeAliases = []string{"default", "best", "opus", "sonnet", "haiku", "fable", "opusplan"}

// claudeContextEnv is the context window Claude Code takes a model it
// doesn't know for (any but Claude's own names); without it, 200K. Its
// auto-compact window (CLAUDE_CODE_AUTO_COMPACT_WINDOW, the autoCompactWindow
// setting) is only ever the smaller of its own value and this one, so this
// is what tells it where a 128K or a 400K model runs out. A name marked [1m]
// is 1M whatever it says.
const claudeContextEnv = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

// claudeCapsEnv tells Claude Code what a model it doesn't know can do, as
// "<model>=effort,xhigh_effort;<model>=…" (a trailing * a prefix, [1m]
// taken off the model first, the model lowercased, the pattern not). Run on
// a gateway (CLAUDE_CODE_USE_GATEWAY, as Claude Desktop's Code tab runs it)
// it otherwise takes no model of magpie's for one with effort, xhigh or max,
// so "ultracode": true, which needs xhigh, did nothing (#430). Each magpie
// model is said to have only the levels magpie knows it has.
const claudeCapsEnv = "CLAUDE_CODE_MODEL_CAPABILITIES"

// claudeCapsMax bounds claudeCapsEnv: the models Claude Code is set to go
// first, and those that don't fit after them are left as Claude Code takes
// them.
const claudeCapsMax = 8000

// claudeCaps is what Claude Code is told a model at these levels can do:
// effort for any, xhigh_effort and max_effort for those levels.
func claudeCaps(levels []string) string {
	var caps []string
	if slices.ContainsFunc(levels, func(l string) bool { return contains(claudeEfforts, l) }) {
		caps = append(caps, "effort")
		if contains(levels, "xhigh") {
			caps = append(caps, "xhigh_effort")
		}
		if contains(levels, "max") {
			caps = append(caps, "max_effort")
		}
	}
	return strings.Join(caps, ",")
}

// claudeCapabilities is claudeCapsEnv's value for magpie's models as Claude
// Code is shown them, and with desktop, as Claude Desktop hands them to it
// (mythos-magpie-<number>); first are the models it is set to, which go in
// before the rest. A model of Claude's it knows is said to have no more
// than it gives it, one with no levels isn't named.
func claudeCapabilities(first []string, desktop bool) string {
	type seg struct{ id, caps string }
	var segs []seg
	add := func(id string, levels []string) {
		id = strings.ToLower(strings.TrimSuffix(id, "[1m]"))
		if id == "" || strings.ContainsAny(id, ";=,") || strings.HasSuffix(id, "*") {
			return
		}
		levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return !contains(claudeEffortsFor(id), l) })
		if c := claudeCaps(levels); c != "" && !slices.ContainsFunc(segs, func(s seg) bool { return s.id == id }) {
			segs = append(segs, seg{id, c})
		}
	}
	shown, _ := provider.CatalogFor("claude")
	for _, e := range shown {
		add(e.ID, e.Efforts)
	}
	if desktop {
		shown, _ := provider.CatalogFor("claude-desktop")
		for _, e := range shown {
			// one named as a Claude model Claude Code knows on its own
			if id := gateway.DesktopID(e); claudeName(id) == "" {
				add(id, e.Efforts)
			}
		}
	}
	rank := func(s seg) int {
		if slices.ContainsFunc(first, func(f string) bool { return strings.ToLower(strings.TrimSuffix(f, "[1m]")) == s.id }) {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(segs, func(a, b seg) int { return rank(a) - rank(b) })
	var b strings.Builder
	for _, s := range segs {
		part := s.id + "=" + s.caps
		if b.Len() > 0 {
			part = ";" + part
		}
		if b.Len()+len(part) > claudeCapsMax {
			break
		}
		b.WriteString(part)
	}
	return b.String()
}

func tierEnv(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

// A tier (and the subagents' model) may run at an effort of its own (#536):
// the model it is on, as Claude Code is given it, is "<model>:<level>"
// (before a [1m] mark, which Claude Code takes off), and magpie's gateway
// asks that model for that level whatever Claude Code asked, as it does a
// routing group's member fixed at one (#189). Claude Code has one effort for
// a session, the main model's; the tier's model at that level is a model id
// of its own to it, so nothing else in its settings changes.

// tierAt splits a tier's model as written into the model, [1m] mark and
// all, and the effort it is fixed at, "" for none.
func tierAt(v string) (model, effort string) {
	const mark = "[1m]"
	bare, marked := strings.CutSuffix(v, mark)
	m, e := provider.MemberEffort(bare)
	if e == "" {
		return v, ""
	}
	if marked {
		m += mark
	}
	return m, e
}

// tierWith is model fixed at effort, as tierAt reads it back.
func tierWith(model, effort string) string {
	if model == "" || effort == "" {
		return model
	}
	const mark = "[1m]"
	bare, marked := strings.CutSuffix(model, mark)
	v := bare + ":" + effort
	if marked {
		v += mark
	}
	return v
}

// tierEfforts are the levels a tier on model can be fixed at: the ones
// magpie knows the model has, else those Claude Code sends it.
func tierEfforts(model string) []string {
	id := strings.TrimSuffix(model, "[1m]")
	for _, m := range magpieModels("claude") {
		if m.ID == id && len(m.Efforts) > 0 {
			return slices.DeleteFunc(slices.Clone(m.Efforts), func(l string) bool { return !contains(provider.MemberEfforts, l) })
		}
	}
	return claudeEffortsFor(model)
}

func claude(home string) *Agent { return claudeIn(here(home)) }

// claudeIn is Claude Code as it lives at a place: this machine's home, or
// a WSL distro's (see wsl.go), its settings.json naming the gateway as it
// reaches it from there.
func claudeIn(at place) *Agent {
	path := filepath.Join(at.home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == at.gw() }

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := env("ANTHROPIC_MODEL"); m != "" {
				return m
			}
		}
		return model()
	}
	// the context window magpie last wrote, so a value the user wrote is
	// never taken for magpie's: that one is left as it is
	windowKey := at.key("claude.context_tokens")
	windowOurs := func() bool {
		cur := env(claudeContextEnv)
		return cur != "" && cur == stashLoad()[windowKey]
	}
	dropWindow := func() error {
		defer forget(windowKey)
		if windowOurs() {
			return edit.DelJSON(path, "env."+claudeContextEnv)
		}
		return nil
	}
	// the capabilities magpie last wrote, kept apart from the user's own in
	// the same way: theirs is left as it is, magpie's adds to nothing
	capsKey := at.key("claude.capabilities")
	capsOurs := func() bool {
		cur := env(claudeCapsEnv)
		return cur != "" && cur == stashLoad()[capsKey]
	}
	dropCaps := func() error {
		defer forget(capsKey)
		if capsOurs() {
			return edit.DelJSON(path, "env."+claudeCapsEnv)
		}
		return nil
	}
	// writeCaps says what magpie's models can do, the ones in models first;
	// Claude Desktop's ids too while it runs on magpie, its Code tab being
	// Claude Code on this settings.json
	writeCaps := func(models ...string) error {
		if env(claudeCapsEnv) != "" && !capsOurs() {
			forget(capsKey)
			return nil
		}
		desktop := at.id == "" && at.sys == nil && desktopWired(desktopPathsOf(desktopDirs(runtime.GOOS, at.home, os.Getenv)))
		v := claudeCapabilities(models, desktop)
		if v == "" {
			return dropCaps()
		}
		if v == env(claudeCapsEnv) {
			return nil
		}
		stash(map[string]string{capsKey: v})
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeCapsEnv, Value: v})
	}
	// what was last written that an open Claude Code session doesn't see:
	// it reads settings.json at start-up, only its env as it goes
	stale := ""
	// the model a subagent runs on when neither its definition nor the
	// call names one, if the user gave it one of its own: Claude Code
	// takes the Agent call's model, then the agent's frontmatter, then
	// CLAUDE_CODE_SUBAGENT_MODEL, then the session's (2.1.287). Empty
	// while it follows the main model, as magpie writes it by itself.
	subagentOwn := func() string {
		w := env("CLAUDE_CODE_SUBAGENT_MODEL")
		if m, e := tierAt(w); routed() && w != env("ANTHROPIC_MODEL") && isMagpie(m) && (e != "" || m != env("ANTHROPIC_MODEL")) {
			return w
		}
		return ""
	}
	// the subagents' model as written, its effort apart: "" for each while
	// it follows the main model
	subagentAt := func() (string, string) {
		m, e := tierAt(subagentOwn())
		if m == env("ANTHROPIC_MODEL") {
			m = ""
		}
		return m, e
	}
	// the main model and each tier's, one that has none on the main one
	curTiers := func() (string, map[string]string) {
		main := env("ANTHROPIC_MODEL")
		tiers := map[string]string{}
		for _, t := range claudeTiers {
			tiers[t] = env(tierEnv(t))
			if tiers[t] == "" {
				tiers[t] = main
			}
		}
		return main, tiers
	}
	// unroute takes magpie's endpoint and models out of Claude Code's env
	// and puts back the endpoint and token the stash kept from before magpie
	// was wired in; it answers the model Claude Code was on then, for
	// Unwire to go back to
	unroute := func() (string, error) {
		if err := dropWindow(); err != nil {
			return "", err
		}
		if err := dropCaps(); err != nil {
			return "", err
		}
		if !routed() {
			return "", nil
		}
		keys := make([]string, len(claudeEnv))
		for i, k := range claudeEnv {
			keys[i] = "env." + k
		}
		if err := edit.DelJSON(path, keys...); err != nil {
			return "", err
		}
		was := unstash(at.key("claude.model"))
		var back []edit.KV
		if u := unstash(at.key("claude.base_url")); u != "" {
			back = append(back, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: u})
		}
		if t := unstash(at.key("claude.auth_token")); t != "" {
			back = append(back, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: t})
		}
		if len(back) > 0 {
			if err := edit.SetJSON(path, back...); err != nil {
				return "", err
			}
		}
		return was, nil
	}
	var writeTiers func(main string, tiers map[string]string) error
	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget(at.key("claude.model"), at.key("claude.base_url"), at.key("claude.auth_token"))
			if err := dropWindow(); err != nil {
				return err
			}
			if err := dropCaps(); err != nil {
				return err
			}
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{
					at.key("claude.model"):      model(),
					at.key("claude.base_url"):   env("ANTHROPIC_BASE_URL"),
					at.key("claude.auth_token"): env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			// tiers that followed the old model follow the new one; the
			// ones given a model of their own keep it
			tiers := map[string]string{}
			for _, t := range claudeTiers {
				tiers[t] = v
				if w := env(tierEnv(t)); routed() && w != "" {
					// at an effort of its own, it keeps that on the new model
					if m, e := tierAt(w); m != env("ANTHROPIC_MODEL") && isMagpie(m) {
						tiers[t] = w
					} else if e != "" {
						tiers[t] = tierWith(v, e)
					}
				}
			}
			// so do subagents that follow it at an effort of their own
			if m, e := subagentAt(); routed() && m == "" && e != "" {
				if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: tierWith(v, e)}); err != nil {
					return err
				}
			}
			return writeTiers(v, tiers)
		}
		// a word that is no model: an effort level the model doesn't take,
		// ultracode (magpie claude ultra), a typo. Another endpoint's
		// names are its own.
		if u := env("ANTHROPIC_BASE_URL"); u == "" || routed() {
			if err := claudeModelWord(v, get()); err != nil {
				return err
			}
		}
		if _, err := unroute(); err != nil {
			return err
		}
		return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
	}

	// writeTiers routes Claude Code through the gateway with main as its
	// model and each tier on the model given.
	writeTiers = func(main string, tiers map[string]string) error {
		// a 1M model goes in marked [1m] however it was named, or Claude
		// Code takes it for 200K
		mark1M := claude1M()
		mark := func(v string) string { m, e := tierAt(v); return tierWith(mark1M(m), e) }
		main = mark1M(main)
		for t, v := range tiers {
			tiers[t] = mark(v)
		}
		kvs := []edit.KV{
			{Path: "env.ANTHROPIC_BASE_URL", Value: at.gw()},
			{Path: "env.ANTHROPIC_MODEL", Value: main},
			{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: tiers["haiku"]},
			{Path: "model", Value: main},
		}
		if !provider.ClaudeHasAccount() {
			kvs = append(kvs, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token})
		} else {
			_ = edit.DelJSON(path, "env.ANTHROPIC_AUTH_TOKEN")
		}
		same := true
		for _, t := range claudeTiers {
			kvs = append(kvs, edit.KV{Path: "env." + tierEnv(t), Value: tiers[t]})
			same = same && tiers[t] == main
		}
		// subagents given a model of their own keep it
		if sub := subagentOwn(); sub != "" {
			kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: mark(sub)})
		} else if !same {
			// one model for every subagent would override the tiers they ask for
			if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
				return err
			}
		} else {
			kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
		}
		// the new model's window replaces the old one's, when magpie knows
		// it; one the user set is theirs
		if env(claudeContextEnv) == "" || windowOurs() {
			if w := claudeWindow(main, tiers); w > 0 {
				kvs = append(kvs, edit.KV{Path: "env." + claudeContextEnv, Value: strconv.Itoa(w)})
				stash(map[string]string{windowKey: strconv.Itoa(w)})
			} else if err := dropWindow(); err != nil {
				return err
			}
		} else {
			forget(windowKey)
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		models := []string{main}
		for _, t := range claudeTiers {
			m, _ := tierAt(tiers[t])
			models = append(models, m)
		}
		return writeCaps(models...)
	}

	fields := []Field{{
		Key: "model", Label: "model",
		Get: get,
		Set: set,
		Options: func(cur map[string]string) []Option {
			name, direct := "Claude Code", "Anthropic"
			if u := env("ANTHROPIC_BASE_URL"); u != "" && !routed() {
				name += " · " + hostOf(u)
				direct = hostOf(u)
			}
			// an alias names the model the user's own env gives its tier,
			// as Claude Code reads it; magpie's, while routed, isn't theirs
			tier := func(t string) string {
				if routed() {
					return ""
				}
				return env(tierEnv(t))
			}
			own := claudeOwn(cur["model"], tier)
			for i := range own {
				own[i].Direct = direct
			}
			// magpie's Claude Code account is the one Claude Code asks on
			// its own while that is Anthropic: the same models a second
			// time, folded in the picker (#496). Only this machine's: a
			// distro's Claude Code has a sign-in of its own, which magpie
			// doesn't read
			return append(group(name, own), claudeViaMagpie(name == "Claude Code" && at.id == "")...)
		},
	}, {
		// the effort Claude Code starts with, as its /effort saves it: under
		// modelSettings for the model (Opus 5.5 and later read only that),
		// and at the top for the models before them and other vendors'. max
		// lasts a session there, so it is CLAUDE_CODE_EFFORT_LEVEL in its
		// env, which every session starts with and asks for. Shown as the
		// level the model runs at: Opus 4.5 set to max runs at high.
		Key: "effort", Label: "effort",
		Get: func() string {
			m := get()
			levels := claudeEffortsFor(m)
			if e := env(claudeEffortEnv); e != "" {
				return claudeClamp(e, levels)
			}
			if n := claudeName(m); n != "" {
				if e, _ := edit.GetJSON(path, "modelSettings."+n+".effortLevel"); e != "" {
					return claudeClamp(e, levels)
				}
			}
			if claudeReadsTop(m) {
				return claudeClamp(jsonGet(path, "effortLevel")(), levels)
			}
			return ""
		},
		Set: func(v string) error {
			m := get()
			if v != "" && !contains(claudeEfforts, v) {
				if v == "ultra" || v == "ultracode" {
					return fmt.Errorf("ultracode is a switch of Claude Code's beside its effort: magpie claude ultracode on")
				}
				return fmt.Errorf("Claude Code keeps an effort of %s, not %q", strings.Join(claudeEfforts, ", "), v)
			}
			if err := claudeTakes(m, v); err != nil {
				return err
			}
			if v == "max" {
				stale = ""
				return edit.SetJSON(path, edit.KV{Path: "env." + claudeEffortEnv, Value: v})
			}
			// what an open session reads is its env: the level written below
			// waits for the next one
			stale = "effort"
			if env(claudeEffortEnv) != "" {
				if err := edit.DelJSON(path, "env."+claudeEffortEnv); err != nil {
					return err
				}
			}
			n := claudeName(m)
			if v == "" {
				// the model's own default, as /effort auto leaves it: its
				// level goes, its other keys (maxEffortLevel) stay
				if n != "" {
					if err := claudeDropModelEffort(path, n); err != nil {
						return err
					}
				}
				if claudeReadsTop(m) {
					return edit.DelJSON(path, "effortLevel")
				}
				return nil
			}
			var kvs []edit.KV
			if claudeReadsTop(m) {
				kvs = append(kvs, edit.KV{Path: "effortLevel", Value: v})
			}
			if n != "" {
				kvs = append(kvs, edit.KV{Path: "modelSettings." + n + ".effortLevel", Value: v})
			}
			return edit.SetJSON(path, kvs...)
		},
		Options: func(cur map[string]string) []Option {
			return static(claudeEffortsFor(cur["model"])...)
		},
	}, {
		// ultracode (2.1.284 on): Claude plans a workflow for each
		// substantive task, at whatever effort. Only a model that takes
		// xhigh has it.
		Key: "ultracode", Label: "ultracode", Quiet: true,
		// shown off for a model without it, which Claude Code runs so
		Get: func() string {
			if v, _ := edit.GetJSON(path, "ultracode"); v == "true" && contains(claudeEffortsFor(get()), "xhigh") {
				return "on"
			}
			return ""
		},
		Set: func(v string) error {
			switch v {
			case "", "off":
				stale = "ultracode"
				return edit.DelJSON(path, "ultracode")
			case "on":
				if m := get(); !contains(claudeEffortsFor(m), "xhigh") {
					return fmt.Errorf("Claude Code has ultracode only on a model that takes xhigh effort, and %s doesn't", orDefault(m))
				}
				stale = "ultracode"
				return edit.SetJSON(path, edit.KV{Path: "ultracode", Value: true})
			}
			return fmt.Errorf("ultracode is on or off, not %q", v)
		},
		Options: func(cur map[string]string) []Option {
			if !contains(claudeEffortsFor(cur["model"]), "xhigh") {
				return nil
			}
			return []Option{{Value: "on", Note: "Claude plans a workflow for each substantive task"}}
		},
	}}
	for _, tier := range claudeTiers {
		fields = append(fields, Field{
			Key: tier, Label: tier, Quiet: true, Follows: "model",
			// empty while the tier follows the main model
			Get: func() string {
				if w, _ := tierAt(env(tierEnv(tier))); routed() && w != env("ANTHROPIC_MODEL") {
					return w
				}
				return ""
			},
			Set: func(v string) error {
				if !routed() {
					if v == "" {
						return nil
					}
					return fmt.Errorf("pick a model through magpie for Claude Code first; %s can then have its own", tier)
				}
				if v != "" && !isMagpie(v) {
					return fmt.Errorf("%s: %q is not a model magpie serves", tier, v)
				}
				main, tiers := curTiers()
				// its effort, if it has one, goes with it to the new model
				_, e := tierAt(tiers[tier])
				tiers[tier] = tierWith(cmp.Or(v, main), e)
				return writeTiers(main, tiers)
			},
			Options: func(map[string]string) []Option {
				if !routed() {
					return nil
				}
				return claudeViaMagpie(false)
			},
		})
	}
	// subagents: the model one runs on when it names none (general-purpose,
	// an agent of the user's without a model:). Unset, the session's, as
	// before; an agent that names a tier (Explore's haiku) takes the tier's.
	// setSubagent writes the subagents' model and effort, the main model's
	// at none taking the variable away
	setSubagent := func(model, effort string) error {
		if model == "" && effort == "" {
			if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
				return err
			}
		} else if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: tierWith(cmp.Or(model, env("ANTHROPIC_MODEL")), effort)}); err != nil {
			return err
		}
		return writeTiers(curTiers())
	}
	fields = append(fields, Field{
		Key: "subagent", Label: "subagents", Quiet: true,
		Get: func() string { m, _ := subagentAt(); return m },
		Set: func(v string) error {
			if !routed() {
				if v == "" {
					return nil
				}
				return fmt.Errorf("pick a model through magpie for Claude Code first; its subagents can then have one of their own")
			}
			if v != "" && !isMagpie(v) {
				return fmt.Errorf("subagents: %q is not a model magpie serves", v)
			}
			_, e := subagentAt()
			return setSubagent(v, e)
		},
		Options: func(map[string]string) []Option {
			if !routed() {
				return nil
			}
			return claudeViaMagpie(false)
		},
	})

	// each tier's effort, and the subagents', fixed by magpie's gateway on
	// the model it is on (#536); empty, the session's effort as before
	effortField := func(key, label, who string, at func() (string, string), put func(model, effort string) error) Field {
		return Field{
			Key: key, Label: label, Quiet: true,
			Get: func() string {
				if !routed() {
					return ""
				}
				_, e := at()
				return e
			},
			Set: func(v string) error {
				v = strings.ToLower(strings.TrimSpace(v))
				if !routed() {
					if v == "" {
						return nil
					}
					return fmt.Errorf("pick a model through magpie for Claude Code first; %s can then have an effort of its own", who)
				}
				if v != "" && !contains(provider.MemberEfforts, v) {
					return fmt.Errorf("%s: an effort is one of %s, not %q", label, strings.Join(provider.MemberEfforts, ", "), v)
				}
				m, _ := at()
				return put(m, v)
			},
			Options: func(map[string]string) []Option {
				if !routed() {
					return nil
				}
				m, _ := at()
				return static(tierEfforts(cmp.Or(m, env("ANTHROPIC_MODEL")))...)
			},
		}
	}
	for _, tier := range claudeTiers {
		at := func() (string, string) {
			m, e := tierAt(env(tierEnv(tier)))
			if m == env("ANTHROPIC_MODEL") {
				m = ""
			}
			return m, e
		}
		put := func(model, effort string) error {
			main, tiers := curTiers()
			tiers[tier] = tierWith(cmp.Or(model, main), effort)
			return writeTiers(main, tiers)
		}
		fields = append(fields, effortField(tier+"_effort", tier+" effort", tier, at, put))
	}
	fields = append(fields, effortField("subagent_effort", "subagent effort", "its subagents", subagentAt, setSubagent))

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		UA:  []string{"claude-cli", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
		// Claude Code as it was before magpie: its default puts it back as
		// installed, on Anthropic's endpoint, where this brings back the
		// endpoint, token and model the user had
		Unwire: func() error {
			was, err := unroute()
			if err != nil {
				return err
			}
			forget(at.key("claude.model"), at.key("claude.base_url"), at.key("claude.auth_token"))
			// the model left alone where magpie had none to take over
			switch {
			case was != "" && !isMagpie(was):
				return edit.SetJSON(path, edit.KV{Path: "model", Value: was})
			case isMagpie(get()):
				return edit.DelJSON(path, "model")
			}
			return nil
		},
		// the catalog's models, with their levels, as Claude Code is told
		// them, while magpie's are the ones it has
		Sync: func() error {
			if !routed() {
				return nil
			}
			models := []string{env("ANTHROPIC_MODEL")}
			for _, t := range claudeTiers {
				models = append(models, env(tierEnv(t)))
			}
			return writeCaps(models...)
		},
		Check: func() string {
			if !isMagpie(get()) {
				return ""
			}
			// an administrator's settings win over the user's
			managed := claudeManaged()
			if at.sys != nil {
				managed = at.sys("/etc/claude-code/managed-settings.json")
			}
			if u, _ := edit.GetJSON(managed, "env.ANTHROPIC_BASE_URL"); u != "" && u != at.gw() {
				return "Claude Code's managed settings (" + at.native(managed) + ") set ANTHROPIC_BASE_URL to " + u + ", which wins over magpie's"
			}
			if provider.ClaudeHasAccount() {
				return wiringOff("Claude Code", path, func(k string) (string, bool) { return edit.GetJSON(path, "env."+k) },
					"ANTHROPIC_BASE_URL", at.gw())
			}
			return wiringOff("Claude Code", path, func(k string) (string, bool) { return edit.GetJSON(path, "env."+k) },
				"ANTHROPIC_BASE_URL", at.gw(), "ANTHROPIC_AUTH_TOKEN", gateway.Token)
		},
		// every prompt typed into Claude Code goes into history.jsonl
		LastUsed: func() time.Time {
			return lastJSONLTime(filepath.Join(filepath.Dir(path), "history.jsonl"), "timestamp", "display")
		},
		// a session takes a new CLAUDE_CODE_EFFORT_LEVEL on its next
		// request, but settings.json's effort and ultracode, and the env's
		// level taken away, only when started again. Windows can't be asked
		// what runs.
		Notice: func() string {
			if stale == "" || !claudeRunning() {
				return ""
			}
			return "an open Claude Code session keeps the " + stale + " it started with — restart it to use this."
		},
	}
}

// claudeRunning says whether a Claude Code may be open; a var so tests can
// fake it.
var claudeRunning = func() bool { return runtime.GOOS == "windows" || Running(`(^|/)claude( |$)`) }

// claudeTakes says why Claude Code can't run model at effort v.
func claudeTakes(model, v string) error {
	levels := claudeEffortsFor(model)
	if v == "" || contains(levels, v) {
		return nil
	}
	if len(levels) == 0 {
		return fmt.Errorf("Claude Code sends %s no effort", model)
	}
	return fmt.Errorf("Claude Code runs %s at %s, not %s (it would send %s)", model, strings.Join(levels, ", "), v, claudeClamp(v, levels))
}

// claudeModelWord refuses a value given for Claude Code's model that is a
// bare word and not one of its aliases: `magpie claude ultra` wrote
// "model": "ultra" over the model.
func claudeModelWord(v, model string) error {
	w := strings.TrimSuffix(v, "[1m]")
	if contains(claudeAliases, w) || strings.ContainsAny(w, "-./:_0123456789") {
		return nil
	}
	switch {
	case w == "ultra" || w == "ultracode":
		return fmt.Errorf("ultracode is a switch of Claude Code's, not a model: magpie claude ultracode on")
	case contains(claudeEfforts, w):
		if err := claudeTakes(model, w); err != nil {
			return err
		}
	}
	return fmt.Errorf("%q is no model of Claude Code's: give a model id (claude-…), one of its aliases (%s) or a model magpie serves", v, strings.Join(claudeAliases, ", "))
}

// claudeDropModelEffort takes the effort saved for model n out of
// modelSettings, and its entry, and modelSettings, if nothing else is left.
func claudeDropModelEffort(path, n string) error {
	if err := edit.DelJSON(path, "modelSettings."+n+".effortLevel"); err != nil {
		return err
	}
	empty := func(k string) bool {
		v, ok := edit.GetJSON(path, k)
		return ok && strings.Join(strings.Fields(v), "") == "{}"
	}
	if empty("modelSettings." + n) {
		if err := edit.DelJSON(path, "modelSettings."+n); err != nil {
			return err
		}
	}
	if empty("modelSettings") {
		return edit.DelJSON(path, "modelSettings")
	}
	return nil
}

// claudeOwn is Anthropic's models as Claude Code takes them: only the
// catalog's; Claude Code's own short aliases are not something any API
// lists, and a compiled-in copy would just go stale. One the user's
// settings.json names (cur: "model": "sonnet") comes first, said as the
// model it stands for, so the row doesn't show a bare word; tier is the
// model the user's env gives a tier, "" for none.
func claudeOwn(cur string, tier func(string) string) []Option {
	ms := catalog.Provider("anthropic")
	var own []Option
	if o, ok := claudeAliasOption(cur, ms, tier); ok {
		own = append(own, o)
	}
	for _, m := range ms {
		if strings.HasPrefix(m.ID, "claude") {
			own = append(own, Option{Value: m.ID, Note: m.Name, Icon: "claude-color"})
		}
	}
	return claudeDated(own)
}

// claudeDated marks each dated Claude id that another option of its group
// names undated (models.dev lists claude-opus-4-5 and
// claude-opus-4-5-20251101, the one model) as that one's Alias. Two dated
// ids of one name are two models, and left as they are.
func claudeDated(opts []Option) []Option {
	const mark = "[1m]"
	bare := func(v string) string { return strings.TrimSuffix(v, mark) }
	type key struct{ group, value string }
	alias := map[key]int{}
	for i, o := range opts {
		if !dated.MatchString(bare(o.Value)) {
			alias[key{o.Group, bare(o.Value)}] = i
		}
	}
	twins := map[key][]int{}
	for i, o := range opts {
		v := bare(o.Value)
		if base := dated.ReplaceAllString(v, ""); base != v && claudeName(base) != "" {
			if _, ok := alias[key{o.Group, base}]; ok {
				twins[key{o.Group, base}] = append(twins[key{o.Group, base}], i)
			}
		}
	}
	for k, is := range twins {
		if len(is) == 1 {
			opts[is[0]].Alias = opts[alias[k]].Value
		}
	}
	return opts
}

// claudeAliasOption is the option for one of Claude Code's aliases, named
// as it resolves it: sonnet is the newest Sonnet (the catalog's newest
// claude-sonnet-…, or the model ANTHROPIC_DEFAULT_SONNET_MODEL gives), best
// the newest Opus, [1m] the same at 1M, opusplan Opus in plan mode and
// Sonnet otherwise. default is the one Claude Code picks by the account's
// plan, which magpie doesn't say. ok is false for a value that is no alias.
func claudeAliasOption(v string, ms []catalog.Model, tier func(string) string) (Option, bool) {
	const mark = "[1m]"
	w := strings.TrimSuffix(v, mark)
	if w == "" || !contains(claudeAliases, w) {
		return Option{}, false
	}
	// latest is the model a tier's alias stands for, and its name
	latest := func(family string) (string, string) {
		if tier != nil {
			if m := tier(family); m != "" {
				return m, m
			}
		}
		for _, m := range ms {
			if claudeName(m.ID) == m.ID && strings.HasPrefix(m.ID, "claude-"+family+"-") {
				return m.ID, m.Name
			}
		}
		return "", ""
	}
	o := Option{Value: v, Icon: "claude-color"}
	family := w
	switch w {
	case "default":
		return o, true
	case "best":
		family = "opus"
	case "opusplan":
		plan, planName := latest("opus")
		work, workName := latest("sonnet")
		if plan == "" || work == "" {
			return o, true
		}
		o.Label = v + " · " + plan + " / " + work
		o.Note = planName + " / " + workName
		return o, true
	}
	id, name := latest(family)
	if id == "" {
		return o, true
	}
	if !strings.HasPrefix(id, "claude") {
		o.Icon = modelIcon("", id)
	}
	if strings.HasSuffix(v, mark) && !strings.HasSuffix(id, mark) {
		id += mark
	}
	o.Label, o.Note = v+" · "+id, name
	return o, true
}

// claudeViaMagpie is what magpie serves Claude Code, a model with a window
// of 1M or more marked [1m]: Claude Code takes any other for 200K, and
// compacts long before a 1M model needs it. It drops the mark before asking.
// fold marks Same those on the account Claude Code is signed in to, for a
// picker that lists Claude Code's own models above them.
func claudeViaMagpie(fold bool) []Option {
	big := map[string]bool{}
	for _, m := range magpieModels("claude") {
		big[m.ID] = m.Context >= 1_000_000
	}
	opts := viaMagpie("claude", "")
	for i, o := range opts {
		if big[o.Ref] {
			opts[i].Value += "[1m]"
		}
		opts[i].Same = fold && o.own
	}
	return claudeDated(opts)
}

// claude1M marks [1m] a magpie model whose window is 1M or more, as
// magpie's list or models.dev gives it. Claude Code (2.1.284 and before)
// takes any Claude model not so marked for 200K, CLAUDE_CODE_MAX_CONTEXT_TOKENS
// or not, and compacts it, over and over, long before it runs out; a ref
// written in bare (typed, or picked while the window wasn't known) was left
// that way.
func claude1M() func(ref string) string { return claude1MFor("claude") }

// claude1MFor is claude1M for a Claude Code run by another agent, by the
// models magpie shows that one (T3 Code's, t3code.go).
func claude1MFor(agent string) func(ref string) string {
	const mark = "[1m]"
	window := map[string]int{}
	for _, m := range magpieModels(agent) {
		window[m.ID] = m.Context
	}
	return func(ref string) string {
		if ref == "" || strings.HasSuffix(ref, mark) {
			return ref
		}
		if cmp.Or(window[ref], catalog.ContextOf(ref)) >= 1_000_000 {
			return ref + mark
		}
		return ref
	}
}

// claudeWindow is the context window to tell Claude Code for the models it
// runs on, 0 when magpie doesn't know it. One value serves every model not
// marked [1m]: the main model's, or when that one is marked, the smallest of
// the tiers' that aren't, so none of them outgrows its own.
func claudeWindow(main string, tiers map[string]string) int {
	const mark = "[1m]"
	window := map[string]int{}
	for _, m := range magpieModels("claude") {
		window[m.ID] = m.Context
	}
	if !strings.HasSuffix(main, mark) {
		return window[main]
	}
	w := 0
	for _, t := range claudeTiers {
		if v, _ := tierAt(tiers[t]); !strings.HasSuffix(v, mark) {
			if c := window[v]; c > 0 && (w == 0 || c < w) {
				w = c
			}
		}
	}
	return w
}

// claudeManaged is where an administrator's Claude Code settings live; a var
// so tests can point it elsewhere.
var claudeManaged = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "windows":
		return `C:\ProgramData\ClaudeCode\managed-settings.json`
	}
	return "/etc/claude-code/managed-settings.json"
}

// StandIn is the model Claude Code is set to use in place of one it named
// that magpie doesn't serve: claude-haiku-4-5-… for a title or a small
// task goes to its haiku tier's model, and a name of no tier to its main
// model. For Codex, the model it is set to (codexStandIn). "" when the
// agent isn't routed through magpie or is neither. For gateway.StandIn.
func StandIn(agent, model string) string {
	if agent != "claude" && agent != "codex" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if agent == "codex" {
		return codexStandIn(filepath.Join(home, ".codex", "config.toml"))
	}
	if m := claudeStandIn(filepath.Join(home, ".claude", "settings.json"), model); m != "" || runtime.GOOS != "windows" {
		return m
	}
	// a Claude Code in WSL is known by the same User-Agent
	return wslClaudeStandIn(model)
}

func claudeStandIn(path, model string) string { return claudeStandInAt(path, model, gateway.URL()) }

// claudeStandInAt is claudeStandIn for a Claude Code that reaches the
// gateway at gw.
func claudeStandInAt(path, model, gw string) string {
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	if env("ANTHROPIC_BASE_URL") != gw {
		return ""
	}
	// the model Claude Code is set to itself, by that id, is its own pick
	// rather than a tier's
	bare := func(v string) string { m, _ := tierAt(v); return strings.TrimSuffix(m, "[1m]") }
	if main := env("ANTHROPIC_MODEL"); main != "" && bare(main) == bare(model) {
		return main
	}
	m := strings.ToLower(model)
	for _, t := range claudeTiers {
		if strings.Contains(m, t) {
			if v := env(tierEnv(t)); v != "" {
				return v
			}
			if t == "haiku" {
				if v := env("ANTHROPIC_SMALL_FAST_MODEL"); v != "" {
					return v
				}
			}
			break
		}
	}
	return env("ANTHROPIC_MODEL")
}
