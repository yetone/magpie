package agent

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Claude Code reads its endpoint from the `env` block of settings.json.
// Pointing ANTHROPIC_BASE_URL at the gateway and naming a catalog model as
// its model (and in the variables the aliases opus/sonnet/haiku resolve
// through) is all it takes to run it on any provider.

// env vars magpie sets while routing through the gateway. ANTHROPIC_MODEL
// only an older magpie set: it outranks settings.json's model, so a model
// picked in Claude Code's /model lasted only the session; it is taken out.
var claudeEnv = append([]string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}, claudeAboutEnv...)

// claudeAbout are what Claude Code's env says about a tier's model, after its
// ANTHROPIC_DEFAULT_<TIER>_MODEL: _NAME and _DESCRIPTION, the label and
// description /model shows for the tier, and _SUPPORTED_CAPABILITIES, what
// it takes the model to do (2.1.293). Each is about the model it was written
// beside, so one written for the user's own model, or for a model of
// magpie's the tier has left, names a model the tier no longer runs (#1227:
// every tier on deepseek-flash, shown as grok-4.5).
var claudeAbout = []string{"_NAME", "_DESCRIPTION", "_SUPPORTED_CAPABILITIES"}

// claudeAboutEnv are claudeAbout's keys for every tier.
var claudeAboutEnv = func() []string {
	var out []string
	for _, t := range claudeTiers {
		for _, s := range claudeAbout {
			out = append(out, tierEnv(t)+s)
		}
	}
	return out
}()

// claudeOwnEnv are the models of claudeEnv a user may have set for their
// own endpoint, which magpie's take the place of while it is wired in and
// gives back when it steps out, with what the user's env says about each
// tier's model. ANTHROPIC_MODEL, their main model, too: magpie takes it
// out while wired in (writeTiers), and it was lost for good. So is their
// auto mode classifier's model (claudeAutoModeEnv), which isn't in
// claudeEnv: one set while magpie is wired in is left to the user.
var claudeOwnEnv = append([]string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	claudeAutoModeEnv,
}, claudeAboutEnv...)

// claudeAutoModeEnv is the model Claude Code's client-side auto mode
// classifier runs on, the one that judges each action before it runs
// (2.1.302; Linka on Discord). Unset, Claude Code picks its own (the
// sonnet tier's model, else Sonnet). It doesn't move server-side auto
// mode, and a classifier model Anthropic configures for the account
// comes before it.
const claudeAutoModeEnv = "CLAUDE_CODE_AUTO_MODE_MODEL"

// claudeNoClassifier are the Claude models Claude Code 2.1.303 won't run
// its classifier on from claudeAutoModeEnv ("the API's safeguards refuse
// the classifier's escalated check on it"): it warns and takes its own
// pick instead, so the auto mode field doesn't offer them.
var claudeNoClassifier = []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-opus-5"}

// claudeClouds are the clouds Claude Code's provider switches
// (agentenv.ClaudeProviders) send its requests to, named as Claude Code
// 2.1.300 names them. Each has an endpoint variable of its own
// (ANTHROPIC_BEDROCK_BASE_URL, …), and ANTHROPIC_BASE_URL is passed over,
// so a Claude Code with one on never reaches magpie: Bedrock refused
// magpie's model ids (jw1dev on X).
var claudeClouds = map[string]string{
	"CLAUDE_CODE_USE_BEDROCK":                "Amazon Bedrock",
	"CLAUDE_CODE_USE_VERTEX":                 "Google Vertex AI",
	"CLAUDE_CODE_USE_FOUNDRY":                "Microsoft Foundry",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS":          "Claude Platform on AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD": "Claude Platform on Google Cloud",
	"CLAUDE_CODE_USE_MANTLE":                 "Amazon Bedrock (Mantle)",
}

// claudeSwitchOn is whether Claude Code reads a switch's value as on: 1,
// true, yes or on, in any case, spaces around it taken off (2.1.300). "0",
// "false" or "" is off, and stays as the user wrote it.
func claudeSwitchOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// claudeShellEnv is a variable as a Claude Code started in a terminal has
// it: the login shell's, else magpie's own; a var so tests can say.
var claudeShellEnv = func(k string) string {
	if v := loginEnv(k); v != "" {
		return v
	}
	return os.Getenv(k)
}

// claudeCloud says which provider switch (claudeClouds) takes the Claude
// Code whose settings.json is at path onto a cloud, past magpie, and where
// it is set; "" when none is on. Claude Code puts the env of .claude.json,
// then of the user's settings, then of an administrator's managed settings
// over the environment it was started in, so the last of those that has
// the variable is the one it goes by. A project's .claude/settings.json is
// not looked at: magpie doesn't know which folders Claude Code runs in.
func claudeCloud(at place, path string) string {
	managed := claudeManaged()
	if at.sys != nil {
		managed = at.sys("/etc/claude-code/managed-settings.json")
	}
	dir := filepath.Dir(path)
	global := filepath.Join(dir, ".claude.json")
	if dir == at.join(".claude") {
		global = at.join(".claude.json")
	}
	for _, k := range agentenv.ClaudeProviders {
		set := false
		for _, f := range []string{managed, path, global} {
			v, has := edit.GetJSON(f, "env."+k)
			if !has {
				continue
			}
			if claudeSwitchOn(v) {
				return noticeClaudeCloudFile.say("file", at.native(f), "var", k, "cloud", claudeClouds[k])
			}
			set = true
			break
		}
		// a distro's environment magpie can't read
		if !set && at.spell == nil && claudeSwitchOn(claudeShellEnv(k)) {
			return noticeClaudeCloudShell.say("var", k, "cloud", claudeClouds[k])
		}
	}
	return ""
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
var claudeNameRe = lazyre.New(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:[^0-9]|$)`)

// claudeName is the Claude model a model id names, "" for another vendor's,
// an alias (opus) or none. A version written with a dot (claude-opus-4.6)
// is the one Claude Code is given with dashes (provider.ClaudeSpelled).
func claudeName(model string) string {
	m := claudeNameRe.FindStringSubmatch(strings.ToLower(provider.ClaudeDashed(model)))
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
// 5.5, Fable 5.2 and every Claude model after them (any Claude Code doesn't
// know) read only modelSettings.<model>.effortLevel (2.1.251 on; 2.1.321's
// list of models that read the top one ends at Fable 5.1 and Mythos 5.1);
// another vendor's reads the top one.
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
// is 1M whatever it says. It is the one window Claude Code knows for any
// such model (2.1.293: none is taken from a gateway), Claude Desktop's Code
// tab's too, which runs on this settings.json (claudeDesktopWindow).
const claudeContextEnv = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

// claudeCompactEnv is where Claude Code compacts a conversation, the smaller
// of it and the model's window, a [1m] one's too. magpie sets it to
// settings.WorkingWindow, so a 1M model isn't run to 1M with every turn
// sending all of it (X: Chen, turns of ~550K tokens waiting 70–90s for a
// first token); settings.FullContext leaves it out, and so does a Claude
// model, which Anthropic runs to its whole window.
const claudeCompactEnv = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

// claudeOutputEnv is the longest reply Claude Code asks a model for. One it
// doesn't know (any but Claude's own names) it asks for 32000 at most
// (claudeUnknownOutput), whatever the model can write, so a model that
// writes more — a reasoning one, whose thinking counts toward the reply —
// stopped at "Claude's response exceeded the 32000 output token maximum"
// (H20 on Discord, DeepSeek through WorkBuddy). Claude Code caps the value
// at 128000 for such a model, and at its own maximum for a Claude model.
const claudeOutputEnv = "CLAUDE_CODE_MAX_OUTPUT_TOKENS"

// claudeUnknownOutput is the reply Claude Code asks a model it doesn't know
// for without claudeOutputEnv (2.1.291).
const claudeUnknownOutput = 32000

// claudeCapsEnv tells Claude Code what a model it doesn't know can do, as
// "<model>=effort,xhigh_effort;<model>=…" (a trailing * a prefix, [1m]
// taken off the model first, the model lowercased, the pattern not). Run on
// a gateway (CLAUDE_CODE_USE_GATEWAY, as Claude Desktop's Code tab runs it)
// it otherwise takes no model of magpie's for one with effort, xhigh or max,
// so "ultracode": true, which needs xhigh, did nothing (#430). Each magpie
// model is said to have only the levels magpie knows it has, and xhigh
// with max: the gateway runs xhigh at max for one without it, and without
// xhigh_effort Claude Code turns Ultracode down (leesunrise2364 on Discord).
const claudeCapsEnv = "CLAUDE_CODE_MODEL_CAPABILITIES"

// claudeReviewEnv is whether Claude Code's auto mode asks the API to review
// its actions (the dangerous-tool-use beta and `safeguards`), "0" for no.
// Behind a gateway whose model doesn't answer, it classifies locally anyway
// and names the gateway in a notice, "this session isn't eligible because
// your requests go through 127.0.0.1:3425…" (1EFLOPS on X, Claude Code on
// Codex), or, where Anthropic has turned its local classifier off (2.1.312
// and later), asks for approval of each action the check would judge,
// whichever the value (gateway/automode.go). Anthropic's page on it says
// to set it to 0 for a gateway that can't run the checks: magpie does
// while none of the models Claude Code
// can reach through it is reviewed (claudeReviewed), and leaves it unset
// while one is. One the user set is theirs.
const claudeReviewEnv = "CLAUDE_CODE_AUTO_MODE_SERVER"

// claudeSearchEnv turns Claude Code's tool search on: MCP tools are named
// in a reminder and loaded by ToolSearch when the model needs them, not
// sent whole on every request. Before 2.1.304 Claude Code kept it off
// behind an ANTHROPIC_BASE_URL other than Anthropic's, so magpie's MCP
// router sent its 245 tools' definitions, 124K tokens, on every request
// (cybercaius on Discord). 2.1.304 and later turn it on there themselves;
// "true" also keeps it on in a conversation begun without it, which
// Claude Code otherwise holds off until it is compacted. The gateway
// carries deferred tools to every upstream (#2660, anthropic_shapes.go).
// One the user set is theirs.
const claudeSearchEnv = "ENABLE_TOOL_SEARCH"

// Claude Code 2.1.295's Explore inherit cap can replace Desktop's
// mythos-magpie compatibility alias with Opus. Disable that cap while
// Desktop is on magpie, without forcing other subagents onto a model.
const claudeExploreEnv = "CLAUDE_CODE_DISABLE_EXPLORE_INHERIT_CAP"

// claudeReviewed reports whether a model Claude Code can reach through
// magpie has its actions reviewed by Anthropic (gateway.AutoModeReviewable:
// a Claude model asked on the Messages API, not translated):
// one shown to the agents in catalogs (Claude Code, Claude Desktop whose
// Code tab is Claude Code, T3 Code's instance of it), a routing group's
// member, a provider's fallback, or one of models (those it is set to),
// judged where it resolves to.
// Any such keeps the review on, as /model can take a session there.
func claudeReviewed(catalogs []string, models ...string) bool {
	var entries []provider.Entry
	for _, c := range catalogs {
		shown, _ := provider.CatalogFor(c)
		entries = append(entries, shown...)
	}
	reviewed := func(p provider.Provider, model string) bool {
		if gateway.AutoModeReviewable(p, model) {
			return true
		}
		return slices.ContainsFunc(provider.FallbackModels(p), func(m provider.Member) bool {
			return gateway.AutoModeReviewable(m.Provider, m.Model)
		})
	}
	find := provider.GroupFinder()
	for _, e := range entries {
		if e.Group == "" {
			if reviewed(e.Provider, e.Model) {
				return true
			}
			continue
		}
		_, ms, _ := find(e.ID)
		if slices.ContainsFunc(ms, func(m provider.Member) bool { return reviewed(m.Provider, m.Model) }) {
			return true
		}
	}
	return slices.ContainsFunc(models, func(id string) bool {
		if _, ms, ok := find(id); ok {
			return slices.ContainsFunc(ms, func(m provider.Member) bool { return reviewed(m.Provider, m.Model) })
		}
		if p, m, ok := provider.Resolve(id); ok {
			return reviewed(p, m)
		}
		// Not one magpie knows: judged by its name, as the gateway would
		// judge a request for it.
		return gateway.AutoModeReviewed(provider.Provider{}, id)
	})
}

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
// before the rest. own leaves out the models Claude Code itself is shown,
// for when only Desktop runs on magpie. A model of Claude's it knows is
// said to have no more than it gives it, one with no levels isn't named.
func claudeCapabilities(first []string, own, desktop bool) string {
	type seg struct{ id, caps string }
	var segs []seg
	add := func(id string, levels []string) {
		// as Claude Code is given it (provider.ClaudeSpelled)
		id = strings.ToLower(strings.TrimSuffix(provider.ClaudeSpelled(id), "[1m]"))
		if id == "" || strings.ContainsAny(id, ";=,") || strings.HasSuffix(id, "*") {
			return
		}
		levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return !contains(claudeEffortsFor(id), l) })
		// one that reaches max without xhigh runs xhigh at max (the
		// gateway's fitEffort), and Claude Code's Ultracode needs
		// xhigh_effort (2.1.299: "Ultracode isn't available on …")
		if contains(levels, "max") && !contains(levels, "xhigh") && contains(claudeEffortsFor(id), "xhigh") {
			levels = append(levels, "xhigh")
		}
		if c := claudeCaps(levels); c != "" && !slices.ContainsFunc(segs, func(s seg) bool { return s.id == id }) {
			segs = append(segs, seg{id, c})
		}
	}
	if own {
		shown, _ := provider.CatalogFor("claude")
		for _, e := range shown {
			add(e.ID, e.Efforts)
		}
	}
	if desktop {
		shown, _ := provider.CatalogFor("claude-desktop")
		again := gateway.DesktopTierAliases()
		for _, e := range shown {
			// one named as a Claude model Claude Code knows on its own;
			// a model picked for two tiers is listed for the second too
			for _, id := range append([]string{gateway.DesktopID(e)}, again[e.ID]...) {
				if claudeName(id) == "" {
					add(id, e.Efforts)
				}
			}
		}
	}
	rank := func(s seg) int {
		if slices.ContainsFunc(first, func(f string) bool {
			return strings.ToLower(strings.TrimSuffix(provider.ClaudeSpelled(f), "[1m]")) == s.id
		}) {
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

// follow is the model tier takes while it follows the main model main:
// main, but for haiku, on a Claude model, the Haiku its provider serves.
// Claude Code runs its own small asks on the haiku tier — a session's
// title, Explore subagents, WebFetch's reading — on Haiku, which on a Claude
// subscription costs a fraction of Opus's limits (X, AncientTwo: the limits
// went much faster through magpie than in Claude Code itself).
//
// For fable, on any model that is no Fable, it is none: the fable tier is
// left unset. Claude Code takes the model ANTHROPIC_DEFAULT_FABLE_MODEL
// names for a Fable model (2.1.293: a model id equal to it is Fable), so the
// main model written there made it a Fable model to Claude Code, and on a
// Claude Pro sign-in without Fable it asked for usage credits before the
// first turn: Claude Opus fell back to Haiku (Zhenzhen on Discord), and
// another vendor's model, GPT-6.1 Sol through Codex, didn't run at all
// (#2090). Claude Code's own /model fable, asked of the gateway, still runs
// on the main model (claudeStandInAt).
func follow(tier, main string) string {
	switch tier {
	case "haiku":
		if l := claudeLight(main); l != "" {
			return l
		}
	case "fable":
		if !strings.HasPrefix(claudeName(main), "claude-fable-") {
			return ""
		}
	}
	return main
}

// followAt is the model tier takes, fixed at effort, while it follows the
// main model main: follow's, but at an effort of its own on the main model
// where follow has none, the effort being the user's pick for the tier.
func followAt(tier, main, effort string) string {
	m := follow(tier, main)
	if m == "" && effort != "" {
		m = main
	}
	return tierWith(m, effort)
}

// claudeLight is the newest Haiku served where the Claude model main is —
// the same provider, the same routing group naming (group/auto-claude-…), the
// same vendor on a router (openrouter/anthropic/claude-…) — "" when main is
// no Claude model, is a Haiku already, or there is none. Of one model's
// names it takes the one without a date.
func claudeLight(main string) string {
	ref := strings.TrimSuffix(main, "[1m]")
	i := strings.Index(ref, "/claude-")
	if j := strings.Index(ref, "-claude-"); i < 0 || j >= 0 && j < i {
		i = j
	}
	if i < 0 || strings.Contains(ref[i:], "haiku") {
		return ""
	}
	prefix := ref[:i+1] + "claude-haiku-"
	undated := func(id string) string {
		if k := strings.LastIndex(id, "-"); k >= 0 && len(id)-k == 9 && strings.Trim(id[k+1:], "0123456789") == "" {
			return id[:k]
		}
		return id
	}
	best := ""
	for _, m := range sharedModels("claude") {
		if !strings.HasPrefix(m.ID, prefix) {
			continue
		}
		if u, b := undated(m.ID), undated(best); best == "" || u > b || u == b && len(m.ID) < len(best) {
			best = m.ID
		}
	}
	return best
}

func tierEnv(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

// claudeModelEnv are the keys of Claude Code's env that name a model, each
// written as Claude Code is given it (provider.ClaudeSpelled).
var claudeModelEnv = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", claudeAutoModeEnv,
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// claudeMagpieModel says a model in Claude Code's env or its model, as
// written there, is one of magpie's: a provider/model name only magpie's
// gateway serves, at an effort or marked 1M or not. On Anthropic's own
// endpoint it is a 404, "There's an issue with the selected model"
// (roswellcsy on X).
func claudeMagpieModel(v string) bool {
	m, _ := tierAt(provider.ClaudeRead(v))
	return isMagpie(strings.TrimSuffix(m, "[1m]"))
}

// claudeSameModel says two of a tier's models as written are one model,
// whatever effort each is fixed at and however its 1M is marked: what the
// env says about the one is about the other.
func claudeSameModel(a, b string) bool {
	ma, _ := tierAt(a)
	mb, _ := tierAt(b)
	return strings.EqualFold(strings.TrimSuffix(ma, "[1m]"), strings.TrimSuffix(mb, "[1m]"))
}

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
	for _, m := range sharedModels("claude") {
		if m.ID == id && len(m.Efforts) > 0 {
			return slices.DeleteFunc(slices.Clone(m.Efforts), func(l string) bool { return !contains(provider.MemberEfforts, l) })
		}
	}
	return claudeEffortsFor(model)
}

func claude(home string) *Agent { return claudeIn(here(home)) }

// claudeRouted says the Claude Code at a place, its settings.json at path,
// sends its requests to magpie's gateway: its ANTHROPIC_BASE_URL is the
// gateway, or a proxy to it where magpie has answered (#1985,
// claudeProxied). Whatever its model and however it signs in to magpie.
func claudeRouted(at place, path string) bool {
	u, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL")
	return u == at.gw() || claudeProxied(at, path, u)
}

// claudeIn is Claude Code as it lives at a place: this machine's home, or
// a WSL distro's (see wsl.go), its settings.json naming the gateway as it
// reaches it from there.
func claudeIn(at place) *Agent {
	path := filepath.Join(at.claudeDir(), "settings.json")
	// a model in the env is read as magpie serves it, its dashed spelling
	// taken back (claudeModelEnv)
	rawEnv := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	env := func(k string) string {
		v := rawEnv(k)
		if contains(claudeModelEnv, k) {
			v = provider.ClaudeRead(v)
		}
		return v
	}
	// spell is a model of magpie's as Claude Code is given it: a dotted
	// Claude version dashed, which it would read as Claude Opus 4
	spell := provider.ClaudeSpelled
	model := jsonGet(path, "model")
	// routed: on magpie's gateway, or on a proxy to it where magpie has
	// answered (#1985, claudeProxied), which a pick keeps (gwAt)
	routed := func() bool { return claudeRouted(at, path) }
	gwAt := func() string {
		if u := env("ANTHROPIC_BASE_URL"); claudeProxied(at, path, u) {
			return u
		}
		return at.gw()
	}
	// the main model while routed: settings.json's model, which Claude
	// Code's /model saves, so a model picked there holds for its next
	// sessions too (the owner: 去掉 ANTHROPIC_MODEL，让 /model 的选择对新会话也生效)
	mainModel := func() string { return claudeMain(path) }
	// the main model magpie last wrote: a tier or the subagents on it follow
	// the main model, as they do on none; on another they have one of their
	// own. A pick in /model moves the main model alone, and those following
	// it go with it when magpie next looks (Follow).
	mainKey := at.key("claude.main")
	wroteMain := func() string { return cmp.Or(stashShared()[mainKey], env("ANTHROPIC_MODEL")) }
	// how Claude Code signs in to magpie (its sign-in field): with magpie's
	// key, or ("claudeai") with its own claude.ai sign-in, which it keeps
	// so: an empty ANTHROPIC_AUTH_TOKEN leaves it signed in, where a key
	// signs it out of claude.ai, and the gateway never passes the sign-in
	// on. Only where the gateway takes any key: from another machine,
	// magpie shared, it takes its sharing key alone. Kept in the stash,
	// where set("") and Unwire leave it for the next time it is routed.
	loginKey := at.key("claude.login")
	signInHere := func() bool { return at.gwKey() == gateway.Token }
	keepSignIn := func() bool { return stashShared()[loginKey] == "claudeai" && signInHere() }
	wiredKey := func() string {
		if keepSignIn() {
			return ""
		}
		return at.gwKey()
	}
	// the gateway address magpie last wrote as Claude Code's base URL
	wroteAt := at.key("claude" + wiredAt)
	// the key in env is one magpie wrote, at this gateway address or an
	// older one: its own, or the empty one of the claude.ai sign-in kept,
	// beside the address magpie wrote with it. An empty key at an endpoint
	// of the user's own is theirs, though magpie once kept the sign-in.
	keyOurs := func() bool {
		tok, has := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN")
		if ourKey(tok) {
			return true
		}
		st := stashLoad()
		u := env("ANTHROPIC_BASE_URL")
		return has && tok == "" && st[loginKey] == "claudeai" && u != "" && u == st[wroteAt]
	}
	// a tier (or the subagents, "subagent") the user gave a model of their
	// own, though it is the one it would follow: it stays when the main
	// model changes
	ownKey := func(t string) string { return at.key("claude.tier_own." + t) }
	own := func(t string) bool { return stashShared()[ownKey(t)] != "" }
	// the auto mode classifier's model as magpie last wrote it (the auto
	// mode field); autoOurs: the env's is that one or another of magpie's
	// models, which goes when magpie steps out. Any other is the user's.
	autoKey := at.key("claude.auto_mode_model")
	autoOurs := func() bool {
		v := rawEnv(claudeAutoModeEnv)
		return v != "" && (v == stashShared()[autoKey] || isMagpie(env(claudeAutoModeEnv)))
	}
	// set once magpie has taken what the env said about each tier's model
	// from before it routed Claude Code (claudeAbout): what is there after
	// that the user wrote about magpie's model, and stays while the tier
	// is on it
	aboutKey := at.key("claude.tier_about")
	// staleAbout is what the env says about a tier's model that the model
	// it goes on (tiers, nil for each staying) isn't: all of it before
	// magpie has looked, kept to give back when magpie steps out (an older
	// magpie routed Claude Code without taking it), else a tier's whose
	// model changes
	staleAbout := func(tiers map[string]string) []string {
		before := stashShared()[aboutKey] == ""
		keep := map[string]string{}
		var keys []string
		for _, t := range claudeTiers {
			if !before && (tiers == nil || claudeSameModel(env(tierEnv(t)), tiers[t])) {
				continue
			}
			for _, s := range claudeAbout {
				k := tierEnv(t) + s
				v, has := edit.GetJSON(path, "env."+k)
				if !has {
					continue
				}
				if before && v != "" && stashShared()[at.key("claude.env."+k)] == "" {
					keep[at.key("claude.env."+k)] = v
				}
				keys = append(keys, "env."+k)
			}
		}
		if len(keep) > 0 {
			stash(keep)
		}
		return keys
	}
	dropAbout := func(tiers map[string]string) error {
		if keys := staleAbout(tiers); len(keys) > 0 {
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
		}
		stash(map[string]string{aboutKey: "1"})
		return nil
	}
	// follows says a tier on model m (its effort apart) follows the main
	// model: on none, on the main model magpie last wrote, or on the model
	// it takes after that one (follow)
	follows := func(t, m string) bool {
		if own(t) {
			return false
		}
		was := strings.TrimSuffix(wroteMain(), "[1m]")
		m = strings.TrimSuffix(m, "[1m]")
		return m == "" || m == was || m == follow(t, was)
	}

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := mainModel(); m != "" {
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
		return cur != "" && cur == stashShared()[windowKey]
	}
	// the modelSettings windows magpie last wrote while Claude Desktop is
	// on magpie (claudePlan, #2025), as JSON {id: window}, and the
	// top-level autoCompactWindow it wrote beside them: each is magpie's
	// only while it still says what magpie wrote, so the user's own, an
	// /autocompact among them, is never taken for magpie's
	budgetsKey := at.key("claude.model_budgets")
	defaultKey := at.key("claude.compact_default")
	budgetsOurs := func() map[string]int {
		var m map[string]int
		_ = json.Unmarshal([]byte(stashShared()[budgetsKey]), &m)
		return m
	}
	defaultOurs := func() bool {
		cur, ok := edit.GetJSON(path, "autoCompactWindow")
		return ok && cur != "" && cur == stashShared()[defaultKey]
	}
	// writeBudgets puts in entries and the default (0 for none), and takes
	// out what magpie wrote before that they no longer have
	writeBudgets := func(def int, entries map[string]int) error {
		raw, _ := edit.GetJSON(path, "modelSettings")
		cur := budgetsRead(raw)
		var del []string
		for id, v := range budgetsOurs() {
			if _, keep := entries[id]; keep || cur[id] != strconv.Itoa(v) {
				continue
			}
			del = append(del, budgetPath(id))
		}
		if len(del) > 0 {
			if err := edit.DelJSON(path, del...); err != nil {
				return err
			}
			// an id's object magpie emptied goes too
			raw, _ = edit.GetJSON(path, "modelSettings")
			var per map[string]map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &per) == nil {
				var empty []string
				for _, p := range del {
					id := strings.TrimSuffix(strings.TrimPrefix(p, "modelSettings."), ".autoCompactWindow")
					if o, ok := per[strings.ReplaceAll(id, `\.`, ".")]; ok && len(o) == 0 {
						empty = append(empty, "modelSettings."+id)
					}
				}
				if len(empty) > 0 && len(empty) == len(per) {
					empty = []string{"modelSettings"}
				}
				if len(empty) > 0 {
					if err := edit.DelJSON(path, empty...); err != nil {
						return err
					}
				}
			}
		}
		var kvs []edit.KV
		for id, v := range entries {
			if cur[id] != strconv.Itoa(v) {
				kvs = append(kvs, edit.KV{Path: budgetPath(id), Value: v})
			}
		}
		if len(entries) > 0 {
			b, _ := json.Marshal(entries)
			stash(map[string]string{budgetsKey: string(b)})
		} else {
			forget(budgetsKey)
		}
		top, has := edit.GetJSON(path, "autoCompactWindow")
		switch {
		case def > 0 && (!has || defaultOurs()):
			stash(map[string]string{defaultKey: strconv.Itoa(def)})
			if top != strconv.Itoa(def) {
				kvs = append(kvs, edit.KV{Path: "autoCompactWindow", Value: def})
			}
		case def > 0:
			forget(defaultKey)
		default:
			if defaultOurs() {
				if err := edit.DelJSON(path, "autoCompactWindow"); err != nil {
					return err
				}
			}
			forget(defaultKey)
		}
		if len(kvs) == 0 {
			return nil
		}
		return edit.SetJSON(path, kvs...)
	}
	dropBudgets := func() error { return writeBudgets(0, nil) }
	dropWindow := func() error {
		defer forget(windowKey)
		if err := dropBudgets(); err != nil {
			return err
		}
		if windowOurs() {
			return edit.DelJSON(path, "env."+claudeContextEnv)
		}
		return nil
	}
	// the reply length magpie last wrote, kept apart from the user's own
	// in the same way
	outputKey := at.key("claude.output_tokens")
	outputOurs := func() bool {
		cur := env(claudeOutputEnv)
		return cur != "" && cur == stashShared()[outputKey]
	}
	dropOutput := func() error {
		defer forget(outputKey)
		if outputOurs() {
			return edit.DelJSON(path, "env."+claudeOutputEnv)
		}
		return nil
	}
	// the capabilities magpie last wrote, kept apart from the user's own in
	// the same way: theirs is left as it is, magpie's adds to nothing
	capsKey := at.key("claude.capabilities")
	capsOurs := func() bool {
		cur := env(claudeCapsEnv)
		return cur != "" && cur == stashShared()[capsKey]
	}
	dropCaps := func() error {
		defer forget(capsKey)
		if capsOurs() {
			return edit.DelJSON(path, "env."+claudeCapsEnv)
		}
		return nil
	}
	// the auto-compact window magpie last wrote, kept apart from the
	// user's own in the same way
	compactKey := at.key("claude.compact_window")
	compactOurs := func() bool {
		cur := env(claudeCompactEnv)
		return cur != "" && cur == stashShared()[compactKey]
	}
	// set while the window magpie wrote is Long conversations' (not a
	// provider's Compact at): tune's card reads it as that one value
	compactLongKey := at.key("claude.compact_long")
	dropCompact := func() error {
		defer forget(compactKey, compactLongKey)
		if compactOurs() {
			return edit.DelJSON(path, "env."+claudeCompactEnv)
		}
		return nil
	}
	// budgeted is set by writeWindow, which runs before writeCompact,
	// while each model has its own window under modelSettings (claudePlan):
	// the variable would take precedence over them, so it goes
	budgeted := false
	// writeCompact has Claude Code compact at the working window
	// (settings.WorkingWindow) on magpie, unless settings.FullContext or the
	// main model is one of Anthropic's Claude models: Anthropic runs a [1m]
	// one to its whole 1M, so it gets its own window (Max on Discord: a
	// [1m] model stopped at 272K), or a routing group made only of
	// them (provider.ClaudeGroup, #1741)
	writeCompact := func() error {
		if env(claudeCompactEnv) != "" && !compactOurs() {
			forget(compactKey)
			return nil
		}
		if budgeted {
			return dropCompact()
		}
		// a threshold the user set on the main model or its provider comes
		// first (#876), a Claude model's included; then the one for every
		// model (settings.Compact), none under Full window
		main := mainModel()
		n := provider.CompactSet(main)
		long := false
		if s := settings.Load().ForAgent("claude"); n == 0 && !claudeModel(main) && !provider.ClaudeGroup(main) {
			n, long = s.Compact(), true
			// magpie's default gives way to an autoCompactWindow the user
			// set in Claude Code (/autocompact, its settings), which the env
			// would take precedence over; one typed in magpie doesn't (#876).
			// So does it to a window the user typed for the main model, on
			// its group or its provider: Claude Code then runs to the window
			// it is told (claudeContextEnv), as Codex is (provider.CompactFor,
			// #1809)
			if s.CompactAt == 0 && (claudeOwnCompact(path, main) || provider.WindowTyped(main)) {
				n = 0
			}
		}
		if n == 0 {
			return dropCompact()
		}
		w := strconv.Itoa(n)
		stash(map[string]string{compactKey: w})
		if long {
			stash(map[string]string{compactLongKey: "1"})
		} else {
			forget(compactLongKey)
		}
		if env(claudeCompactEnv) == w {
			return nil
		}
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeCompactEnv, Value: w})
	}
	// Claude Code's /model lists what modelPicker says (2.1.287): while it
	// runs on magpie, that is every one of magpie's models, the routing
	// groups first as viaMagpie lists them, so the user picks among them
	// in Claude Code itself. Groups were left out, so one magpie had just
	// made the main model and every tier wasn't in /model (#1575). One the
	// user wrote is theirs. One whose every row says "via magpie" is
	// magpie's though its mark is gone (an older magpie's stash lost it),
	// so it is kept up to date rather than left as the user's: a WSL
	// Claude Code's /model kept a list without the models added since
	// (#2046, cjyrainbow).
	pickerKey := at.key("claude.model_picker")
	pickerOurs := func() bool { return stashShared()[pickerKey] != "" || magpiesPicker(path) }
	dropPicker := func() error {
		defer forget(pickerKey)
		if _, has := edit.GetJSON(path, "modelPicker"); has && pickerOurs() {
			return edit.DelJSON(path, "modelPicker")
		}
		return nil
	}
	writePicker := func() error {
		if _, has := edit.GetJSON(path, "modelPicker"); has && !pickerOurs() {
			return nil
		}
		mark1M := claude1M()
		// a model a tier is on is in /model as that tier: Claude Code
		// leaves out a picker row naming a tier's model, but compares the
		// ids whole, so a tier fixed at an effort (codex/gpt-6-luna:max)
		// kept its model's row as a second entry (#1616, iTanX). That row
		// is left out here; a tier at no effort Claude Code matches itself.
		// With the own-models switch on, replaceBuiltInOptions takes the
		// tier rows out of /model with the built-in lineup (Claude Code
		// keeps only the Default row and the picker's own), so a tier's
		// model keeps its row then, or it would be in /model nowhere.
		//
		// A list narrowed in magpie (only the picked models, or some taken
		// out) asks the same: the built-in lineup's rows go to magpie too
		// while Claude Code runs on it, and the one no tier names (Fable,
		// left unset for a main of no Fable) listed a model the user had
		// not picked beside the routing groups they had (yyhhyyyyyy on
		// Discord). A custom option of the user's own
		// (ANTHROPIC_CUSTOM_MODEL_OPTION), which the flag hides too, keeps
		// the lineup.
		// ownOption: the user's custom row, in settings.json's env or, for
		// this computer's Claude Code, the login shell's (a distro's
		// environment magpie can't read)
		ownOption := func() bool {
			return env(claudeCustomOptionEnv) != "" || at.spell == nil && claudeShellEnv(claudeCustomOptionEnv) != ""
		}
		replace := provider.OwnModelsHidden("claude") || claudeNarrowed() && !ownOption()
		var onTier []string
		for _, t := range claudeTiers {
			if replace {
				break
			}
			if v := env(tierEnv(t)); v != "" {
				if _, e := tierAt(v); e != "" {
					onTier = append(onTier, v)
				}
			}
		}
		// each row's label is the model's name as every agent's list calls
		// it, by the Provider in model names setting (provider.Labels): the
		// picker took the name alone, so none of the setting's three ways
		// changed /model (love1sbug on X). A description that would say the
		// provider the label already says is left "via magpie".
		labels := listLabels("claude")
		rows := []any{}
		for _, o := range viaMagpieShared("claude", "") {
			if slices.ContainsFunc(onTier, func(v string) bool { return claudeSameModel(v, o.Value) }) {
				continue
			}
			label, note := cmp.Or(labels[o.Ref], o.Label, o.Value), o.Note
			// a subscription's note is its account alone, so a label left
			// without its provider (Off, or a name the user gave) named
			// neither in /model; the provider goes before the account then,
			// as an API key's note says it (#1136, cjyrainbow)
			if by, ok := strings.CutSuffix(note, " · via magpie"); ok {
				switch {
				case label != o.Label && strings.HasSuffix(label, " · "+by):
					note = "via magpie"
				case o.Group != "" && o.Group != RoutingGroups && by != o.Group && !strings.HasSuffix(label, " · "+o.Group):
					note = o.Group + " · " + note
				}
			}
			rows = append(rows, map[string]any{"model": spell(mark1M(o.Value)), "label": label, "description": note})
		}
		if len(rows) == 0 {
			return dropPicker()
		}
		// the switch that leaves a picker's own models out (#1642) reaches
		// here too (#4548, raclen): /model is fed by magpie all the same, so
		// with the switch off it says replaceBuiltInOptions, which takes
		// Claude Code's built-in lineup rows back out of /model — the Default
		// row stays, Claude Code keeping it as its own fallback. The flag
		// goes again with the switch on, the whole picker written either way.
		picker := map[string]any{"options": rows}
		if replace {
			picker["replaceBuiltInOptions"] = true
		}
		stash(map[string]string{pickerKey: "1"})
		return edit.SetJSON(path, edit.KV{Path: "modelPicker", Value: picker})
	}
	// pickerRefs are the models writePicker lists in /model, as it lists
	// them ("[1m]" on a 1M one), none while the picker is the user's.
	// Claude Code runs a model picked there on the window it was told, and
	// beside a Claude main it was told none: a routing group of a 500K GPT
	// and a 1M Claude picked in /model ran at Claude Code's own 200K
	// (young02036 on Discord)
	pickerRefs := func() []string {
		if _, has := edit.GetJSON(path, "modelPicker"); has && !pickerOurs() {
			return nil
		}
		mark1M := claude1M()
		var refs []string
		for _, o := range viaMagpieShared("claude", "") {
			refs = append(refs, mark1M(o.Value))
		}
		return refs
	}
	// writeCaps says what magpie's models can do, the ones in models first;
	// Claude Desktop's ids too while it runs on magpie, its Code tab being
	// Claude Code on this settings.json
	desktopOn := func() bool {
		// a distro's: Claude Desktop on Windows runs a Code tab session
		// whose host is WSL on the distro's Claude Code, which reads
		// ${CLAUDE_CONFIG_DIR:-$HOME/.claude} there (4.29.0's remote
		// probe), not Windows' settings.json, and is handed the same
		// mythos-magpie id and no window; only Windows' Desktop wired
		// tells (young02036 on Discord: 500K group at "/ 200k" in WSL)
		if at.sys != nil {
			return desktopWiredAny(desktopSetsHere())
		}
		return at.id == "" && desktopWiredAny(desktopSets(desktopdir.OS, at.home, os.Getenv))
	}
	// Desktop's Explore compatibility flag has its own ownership: taking
	// Claude Code off magpie must leave it while Desktop still needs it.
	exploreKey := at.key("claude.explore_inherit_cap")
	writeExplore := func() error {
		cur, has := edit.GetJSON(path, "env."+claudeExploreEnv)
		ours := has && cur == "1" && stashShared()[exploreKey] == "1"
		if !desktopOn() {
			if ours {
				if err := edit.DelJSON(path, "env."+claudeExploreEnv); err != nil {
					return err
				}
			}
			if stashShared()[exploreKey] != "" {
				forget(exploreKey)
			}
			return nil
		}
		// Presence matters: an explicit empty value is the user's too.
		if has {
			if !ours && stashShared()[exploreKey] != "" {
				forget(exploreKey)
			}
			return nil
		}
		if err := edit.SetJSON(path, edit.KV{Path: "env." + claudeExploreEnv, Value: "1"}); err != nil {
			return err
		}
		stash(map[string]string{exploreKey: "1"})
		return nil
	}
	// putCaps is writeCaps with own saying whether Claude Code's own
	// models are among them, as they are while it runs on magpie
	putCaps := func(own bool, models ...string) error {
		if env(claudeCapsEnv) != "" && !capsOurs() {
			forget(capsKey)
			return nil
		}
		v := claudeCapabilities(models, own, desktopOn())
		if v == "" {
			return dropCaps()
		}
		if v == env(claudeCapsEnv) {
			return nil
		}
		stash(map[string]string{capsKey: v})
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeCapsEnv, Value: v})
	}
	writeCaps := func(models ...string) error { return putCaps(true, models...) }
	// the auto mode review magpie turned off (claudeReviewEnv), kept apart
	// from the user's own in the same way
	reviewKey := at.key("claude.auto_mode_server")
	reviewOurs := func() bool {
		cur := rawEnv(claudeReviewEnv)
		return cur != "" && cur == stashShared()[reviewKey]
	}
	dropReview := func() error {
		defer forget(reviewKey)
		if reviewOurs() {
			return edit.DelJSON(path, "env."+claudeReviewEnv)
		}
		return nil
	}
	// writeReview turns the review off while no model Claude Code can reach
	// through magpie runs it, and back on (unset) once one does
	writeReview := func(models ...string) error {
		if rawEnv(claudeReviewEnv) != "" && !reviewOurs() {
			forget(reviewKey)
			return nil
		}
		catalogs := []string{"claude"}
		if desktopOn() {
			catalogs = append(catalogs, "claude-desktop")
		}
		if claudeReviewed(catalogs, models...) {
			return dropReview()
		}
		stash(map[string]string{reviewKey: "0"})
		if rawEnv(claudeReviewEnv) == "0" {
			return nil
		}
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeReviewEnv, Value: "0"})
	}
	// tool search magpie turned on (claudeSearchEnv) while Claude Code is
	// on magpie, kept apart from the user's own as the review is: a value
	// there that isn't the one magpie wrote, an empty one too, is theirs
	searchKey := at.key("claude.tool_search")
	searchOurs := func() bool {
		cur, has := edit.GetJSON(path, "env."+claudeSearchEnv)
		return has && cur != "" && cur == stashShared()[searchKey]
	}
	dropSearch := func() error {
		defer forget(searchKey)
		if searchOurs() {
			return edit.DelJSON(path, "env."+claudeSearchEnv)
		}
		return nil
	}
	writeSearch := func() error {
		if _, has := edit.GetJSON(path, "env."+claudeSearchEnv); has && !searchOurs() {
			forget(searchKey)
			return nil
		}
		stash(map[string]string{searchKey: "true"})
		if rawEnv(claudeSearchEnv) == "true" {
			return nil
		}
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeSearchEnv, Value: "true"})
	}
	// desktopPlan is what writeWindow writes while Claude Desktop is on
	// magpie (claudePlan, #2025): the models its Code tab runs Claude Code
	// on, and while Claude Code itself is on magpie (main set) its main
	// model's, tiers' and those /model offers (pickerRefs), each with its
	// own window under the largest. A model Claude Code knows as Claude's has its own window.
	desktopPlan := func(main string, tiers map[string]string) (top, def int, entries map[string]int) {
		s := settings.Load().ForAgent("claude")
		// the user's own top-level autoCompactWindow
		cur, userTop := edit.GetJSON(path, "autoCompactWindow")
		userTop = userTop && !defaultOurs()
		// the window each model of magpie's compacts at (claudeBudget's
		// compact), as writeCompact gives the main model: the user's
		// threshold for it first (#876); a Claude model, or a group of them,
		// runs to its own (#1741), as does one whose window the user typed
		// (#1809); the user's autoCompactWindow, unless they gave magpie a
		// threshold for every model; then that one, the working window
		// unless Full window. It is the model's own entry, so Desktop's
		// models have it with Claude Code itself off magpie too, where
		// Claude's own models compact as they did
		work := func(ref string) int {
			if n := provider.CompactSet(ref); n > 0 {
				return n
			}
			if claudeModel(ref) || provider.ClaudeGroup(ref) {
				return -1
			}
			if s.CompactAt == 0 && userTop {
				return 0
			}
			if s.CompactAt == 0 && provider.WindowTyped(ref) {
				return -1
			}
			return cmp.Or(s.Compact(), -1)
		}
		var models []claudeBudget
		if desktopOn() {
			shown, _ := provider.CatalogFor("claude-desktop")
			behaves, _ := gateway.DesktopBehavesAs()
			again := gateway.DesktopTierAliases()
			for _, e := range shown {
				for _, id := range append([]string{gateway.DesktopID(e)}, again[e.ID]...) {
					if claudeName(id) == "" {
						models = append(models, claudeBudget{id: id, window: e.Context, long: gateway.DesktopListed1M(e), compact: work(e.ID), behaves: behaves[id] != ""})
					}
				}
			}
		}
		if main != "" {
			window := map[string]int{}
			for _, m := range sharedModels("claude") {
				window[m.ID] = m.Context
			}
			// a tier fixed at an effort is handed Claude Code with it, and
			// matched by it
			refs := []string{main}
			for _, t := range claudeTiers {
				refs = append(refs, tiers[t])
			}
			// and the models /model offers, each of which it may run on
			refs = append(refs, pickerRefs()...)
			for _, v := range refs {
				m, _ := tierAt(v)
				bare := strings.TrimSuffix(m, "[1m]")
				if v == "" || claudeModel(bare) || provider.ClaudeGroup(bare) {
					continue
				}
				models = append(models, claudeBudget{id: spell(v), window: window[bare], long: strings.HasSuffix(v, "[1m]"), compact: work(bare)})
			}
			// the one for every model else, as the variable gave it: none
			// on a Claude main model, which Anthropic runs to its own
			if m, _ := tierAt(main); !claudeModel(strings.TrimSuffix(m, "[1m]")) && !provider.ClaudeGroup(strings.TrimSuffix(m, "[1m]")) {
				def = s.Compact()
			}
		}
		// the user's own top-level window is the one a model without an
		// entry runs at ("auto": the one Claude Code is told), in place of
		// magpie's
		base := def
		if userTop {
			def = 0
			base, _ = strconv.Atoi(cur)
		}
		raw, _ := edit.GetJSON(path, "modelSettings")
		ours := budgetsOurs()
		theirs := map[string]string{}
		for k, v := range budgetsRead(raw) {
			if n, ok := ours[k]; ok && v == strconv.Itoa(n) {
				continue
			}
			theirs[budgetKey(k)] = v
		}
		top, entries = claudePlan(models, base, theirs)
		return top, def, entries
	}
	// pickerApart says the main is one of Claude's own models (or a group
	// of them), which Anthropic runs to its own window, magpie tells
	// Claude Code no window for the rest (claudeWindow 0), and /model
	// offers a model of magpie's whose window is known: picked there, it
	// would run at Claude Code's own 200K, so each gets its own through
	// desktopPlan. A model Claude Code takes for 1M by its "[1m]", or
	// knows as Claude's, has its own already. Any other main is as
	// before: one whose window is told serves a /model pick too, and one
	// of no known window keeps the last.
	pickerApart := func(main string, tiers map[string]string) bool {
		m, _ := tierAt(main)
		bare := strings.TrimSuffix(m, "[1m]")
		if main == "" || !claudeModel(bare) && !provider.ClaudeGroup(bare) || claudeWindow(main, tiers) > 0 {
			return false
		}
		window := map[string]int{}
		for _, m := range sharedModels("claude") {
			window[m.ID] = m.Context
		}
		for _, v := range pickerRefs() {
			m, _ := tierAt(v)
			if strings.HasSuffix(m, "[1m]") || claudeModel(m) || provider.ClaudeGroup(m) {
				continue
			}
			if window[m] > 0 {
				return true
			}
		}
		return false
	}
	// writeWindow tells Claude Code the window of the models it runs on
	// (claudeWindow). While Claude Desktop runs on magpie too, it is the
	// largest of theirs and each model below it gets its own
	// (desktopPlan), and so while the main model is a [1m] one beside a
	// tier that isn't: the main's own window, with each tier below it held
	// to the tier's (多喝热水 on Discord: the least tier's 246K beside a
	// [1m] DeepSeek read as the main's window under Full window, and was
	// the one it fell back to whenever Claude Code stopped taking it for
	// 1M). With a
	// CLAUDE_CODE_AUTO_COMPACT_WINDOW of the user's, which takes
	// precedence over those, the least of them, as one value serves them
	// all. One the user set is theirs.
	writeWindow := func(main string, tiers map[string]string) error {
		budgeted = false
		if env(claudeContextEnv) != "" && !windowOurs() {
			forget(windowKey)
			return dropBudgets()
		}
		if (desktopOn() || claudeLongMain(main) && claudeWindow(main, tiers) > 0 || pickerApart(main, tiers)) && (env(claudeCompactEnv) == "" || compactOurs()) {
			top, def, entries := desktopPlan(main, tiers)
			if top <= 0 {
				return dropWindow()
			}
			if err := writeBudgets(def, entries); err != nil {
				return err
			}
			budgeted = true
			v := strconv.Itoa(top)
			stash(map[string]string{windowKey: v})
			if v == env(claudeContextEnv) {
				return nil
			}
			return edit.SetJSON(path, edit.KV{Path: "env." + claudeContextEnv, Value: v})
		}
		if err := dropBudgets(); err != nil {
			return err
		}
		w := claudeWindow(main, tiers)
		if desktopOn() {
			if d := claudeDesktopWindow(); d > 0 && (w == 0 || d < w) {
				w = d
			}
		}
		if w <= 0 {
			return dropWindow()
		}
		v := strconv.Itoa(w)
		stash(map[string]string{windowKey: v})
		if v == env(claudeContextEnv) {
			return nil
		}
		return edit.SetJSON(path, edit.KV{Path: "env." + claudeContextEnv, Value: v})
	}
	// dropStray takes magpie's model names out of a Claude Code on
	// Anthropic's own endpoint, where each is a 404 ("There's an issue
	// with the selected model (claude/claude-opus-5-5)", roswellcsy on X):
	// left in its env, ANTHROPIC_MODEL over settings.json's model, by an
	// older magpie that gave one back from the stash as the user's own on
	// every Disconnect. The user's own models, and any at an endpoint of
	// theirs, which may name its models so, are left as they are.
	dropStray := func() error {
		if env("ANTHROPIC_BASE_URL") != "" {
			return nil
		}
		var keys []string
		for _, k := range claudeModelEnv {
			if claudeMagpieModel(rawEnv(k)) {
				keys = append(keys, "env."+k)
			}
		}
		if claudeMagpieModel(model()) {
			keys = append(keys, "model")
		}
		if len(keys) == 0 {
			return nil
		}
		return edit.DelJSON(path, keys...)
	}
	// desktopOnly is what is written while Claude Code itself isn't on
	// magpie: Claude Desktop's Code tab still runs Claude Code on this
	// settings.json while Desktop is, so the window and capabilities of
	// Desktop's models alone; none at all when Desktop isn't either. Only
	// Claude Code's being wired in wrote them before, so a user who wired
	// in Desktop alone had 200K for every Desktop model (#1458).
	desktopOnly := func() error {
		if err := writeExplore(); err != nil {
			return err
		}
		// the review is turned off only for Claude Code's own requests to
		// magpie
		if err := dropReview(); err != nil {
			return err
		}
		if err := dropSearch(); err != nil {
			return err
		}
		if !desktopOn() {
			if err := dropWindow(); err != nil {
				return err
			}
			return dropCaps()
		}
		if err := writeWindow("", nil); err != nil {
			return err
		}
		return putCaps(false)
	}
	// what was last written that an open Claude Code session doesn't see:
	// it reads settings.json at start-up, only its env as it goes
	stale := ""
	// the model a subagent runs on when neither its definition nor the
	// call names one, if the user gave it one of its own: Claude Code
	// takes the Agent call's model, then the agent's frontmatter, then
	// CLAUDE_CODE_SUBAGENT_MODEL, then the session's (2.1.287). Empty
	// while it follows the main model, as magpie writes it by itself.
	// An effort with no model of their own is never written: the main
	// model at that effort in CLAUDE_CODE_SUBAGENT_MODEL pins every
	// subagent to the main model (KorenKrita on Discord).
	subagentOwn := func() string {
		w := env("CLAUDE_CODE_SUBAGENT_MODEL")
		if m, _ := tierAt(w); routed() && isMagpie(m) && (own("subagent") || m != wroteMain()) {
			return w
		}
		return ""
	}
	// the effort an older magpie wrote for subagents that follow the main
	// model, as the main model at that effort: "" for none
	subagentFollowing := func() string {
		m, e := tierAt(env("CLAUDE_CODE_SUBAGENT_MODEL"))
		if routed() && e != "" && m == wroteMain() && !own("subagent") {
			return e
		}
		return ""
	}
	// the subagents' effort the user gave with no model of their own: kept
	// in the stash, not written, and applied once they have a model
	subEffortKey := at.key("claude.subagent_effort")
	subagentSaved := func() string { return cmp.Or(stashShared()[subEffortKey], subagentFollowing()) }
	// the subagents' model as written, its effort apart: "" for each while
	// it follows the main model, which takes no effort of theirs
	subagentAt := func() (string, string) {
		m, e := tierAt(subagentOwn())
		if m == wroteMain() && !own("subagent") {
			m = ""
		}
		if m == "" {
			e = ""
		}
		return m, e
	}
	// the main model and each tier's, one that has none or follows it on
	// the main one, at its effort
	curTiers := func() (string, map[string]string) {
		main := mainModel()
		tiers := map[string]string{}
		for _, t := range claudeTiers {
			tiers[t] = env(tierEnv(t))
			if m, e := tierAt(tiers[t]); follows(t, m) {
				tiers[t] = followAt(t, main, e)
			}
		}
		return main, tiers
	}
	// a provider switch the env has on (claudeClouds) sends Claude Code to
	// that cloud, past ANTHROPIC_BASE_URL, so routing it through magpie
	// takes the switch out, its value kept in the stash as the user wrote
	// it (a string or JSON's true), and magpie stepping out puts it back
	// (jw1dev on X: CLAUDE_CODE_USE_BEDROCK=1 beside magpie's wiring, and
	// Bedrock refused magpie's model). The cloud's own variables (AWS_*,
	// ANTHROPIC_VERTEX_PROJECT_ID, …) stay: without the switch Claude Code
	// doesn't read them. One that is off stays as it is.
	cloudKey := func(k string) string { return at.key("claude.cloud." + k) }
	dropClouds := func() error {
		var gone []string
		kept := map[string]string{}
		for _, k := range agentenv.ClaudeProviders {
			raw, has := edit.GetJSONRaw(path, "env."+k)
			if v, _ := edit.GetJSON(path, "env."+k); !has || !claudeSwitchOn(v) {
				continue
			}
			kept[cloudKey(k)] = raw
			gone = append(gone, "env."+k)
		}
		if len(gone) == 0 {
			return nil
		}
		stash(kept)
		return edit.DelJSON(path, gone...)
	}
	putClouds := func() error {
		var back []edit.KV
		for _, k := range agentenv.ClaudeProviders {
			raw := unstash(cloudKey(k))
			if raw == "" || !json.Valid([]byte(raw)) {
				continue
			}
			// one the user set again since is theirs as it is
			if _, has := edit.GetJSON(path, "env."+k); has {
				continue
			}
			back = append(back, edit.KV{Path: "env." + k, Value: json.RawMessage(raw)})
		}
		if len(back) == 0 {
			return nil
		}
		return edit.SetJSON(path, back...)
	}
	// unroute takes magpie's endpoint and models out of Claude Code's env
	// and puts back the endpoint and token the stash kept from before magpie
	// was wired in; it answers the model Claude Code was on then, for
	// Unwire to go back to
	unroute := func() (string, error) {
		if err := dropOutput(); err != nil {
			return "", err
		}
		if err := dropCompact(); err != nil {
			return "", err
		}
		// the window and capabilities stay as Claude Desktop's Code tab
		// needs them while Desktop is on magpie (#1458)
		if err := desktopOnly(); err != nil {
			return "", err
		}
		if err := dropPicker(); err != nil {
			return "", err
		}
		if err := putClouds(); err != nil {
			return "", err
		}
		if !routed() {
			// magpie's, left at a gateway address since changed: its
			// endpoint, key and models all go, as set("") takes them
			if keyOurs() {
				keys := make([]string, len(claudeEnv))
				for i, k := range claudeEnv {
					keys[i] = "env." + k
				}
				if err := edit.DelJSON(path, keys...); err != nil {
					return "", err
				}
			}
			return "", dropStray()
		}
		keys := make([]string, len(claudeEnv))
		for i, k := range claudeEnv {
			keys[i] = "env." + k
		}
		if err := edit.DelJSON(path, keys...); err != nil {
			return "", err
		}
		forget(aboutKey)
		// the auto mode classifier's model goes only where it is magpie's
		if autoOurs() {
			if err := edit.DelJSON(path, "env."+claudeAutoModeEnv); err != nil {
				return "", err
			}
		}
		forget(autoKey)
		was := unstash(at.key("claude.model"))
		var back []edit.KV
		for _, k := range claudeOwnEnv {
			// one the user set again since is theirs as it is
			// one of magpie's kept by an older magpie as the user's is no
			// model of their endpoint's (roswellcsy on X)
			if v := unstash(at.key("claude.env." + k)); v != "" && rawEnv(k) == "" && !(contains(claudeModelEnv, k) && claudeMagpieModel(v)) {
				back = append(back, edit.KV{Path: "env." + k, Value: v})
			}
		}
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
			// Claude Code's own model, on the endpoint the user had before
			// magpie: magpie steps out, putting back their endpoint and
			// token, which stay theirs (__jingling on X: magpie claude
			// model default took their ANTHROPIC_BASE_URL and token away
			// for good)
			if _, err := unroute(); err != nil {
				return err
			}
			keys := []string{"model"}
			// magpie's, left at a gateway address since changed: its key,
			// or, where Claude Code kept its claude.ai sign-in, an empty
			// one beside magpie's model
			if keyOurs() {
				for _, k := range claudeEnv {
					keys = append(keys, "env."+k)
				}
			}
			forget(at.key("claude.model"), at.key("claude.base_url"), at.key("claude.auth_token"), mainKey, wroteAt, aboutKey)
			for _, k := range claudeOwnEnv {
				forget(at.key("claude.env." + k))
			}
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			if !routed() {
				// the user's own model, endpoint, token, tiers and
				// subagent model: back with it when magpie steps out
				// (#1050). At an older gateway address they're all
				// magpie's, and what was kept when magpie was wired in
				// stays kept.
				if !keyOurs() {
					kept := map[string]string{
						at.key("claude.model"):      model(),
						at.key("claude.base_url"):   env("ANTHROPIC_BASE_URL"),
						at.key("claude.auth_token"): env("ANTHROPIC_AUTH_TOKEN"),
					}
					for _, k := range claudeOwnEnv {
						// as the user wrote it, never respelled; one of
						// magpie's own left there is not theirs to get back
						// (roswellcsy on X: it came back as their
						// ANTHROPIC_MODEL with every Disconnect)
						v := rawEnv(k)
						if contains(claudeModelEnv, k) && claudeMagpieModel(v) {
							v = ""
						}
						kept[at.key("claude.env."+k)] = v
					}
					// the auto mode classifier's model of their own
					// endpoint waits in the stash, out of the env, as
					// their tiers' do; one of magpie's stays as it is
					theirs := rawEnv(claudeAutoModeEnv) != "" && !autoOurs()
					if !theirs {
						kept[at.key("claude.env."+claudeAutoModeEnv)] = ""
					}
					stash(kept)
					if theirs {
						if err := edit.DelJSON(path, "env."+claudeAutoModeEnv); err != nil {
							return err
						}
					}
				}
			}
			// tiers that followed the old model follow the new one; the
			// ones given a model of their own keep it, the one the new
			// main model is too, so it stays when the main model moves
			// on again (#1050: a sonnet tier on Sonnet went to Opus with
			// the main model's next move). The new main model itself is
			// one too where the tier would follow another or none: a fable
			// tier given GPT-6.1 Sol, the main model moved onto it, read as
			// following and was lost on the next move (#2257), as was a
			// haiku tier given Opus (it follows Opus on its Haiku).
			same := func(t, m string) bool {
				m = strings.TrimSuffix(m, "[1m]")
				return m == strings.TrimSuffix(v, "[1m]") || m == strings.TrimSuffix(follow(t, v), "[1m]")
			}
			if !routed() {
				forget(ownKey("subagent"))
			} else if m, _ := subagentAt(); m != "" && same("subagent", m) {
				stash(map[string]string{ownKey("subagent"): "1"})
			}
			tiers := map[string]string{}
			for _, t := range claudeTiers {
				tiers[t] = follow(t, v)
				if !routed() {
					forget(ownKey(t))
				}
				if w := env(tierEnv(t)); routed() && w != "" {
					// at an effort of its own, it keeps that on the new model
					// one not magpie's on a tier that follows none (fable on a
					// Claude model) is the user's own, left as written
					if m, e := tierAt(w); !follows(t, m) && (isMagpie(m) || follow(t, v) == "") {
						tiers[t] = w
						if same(t, m) {
							stash(map[string]string{ownKey(t): "1"})
						}
					} else if e != "" {
						tiers[t] = followAt(t, v, e)
					}
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
		// what the env says about a tier's model goes with the model
		if err := dropAbout(tiers); err != nil {
			return err
		}
		if err := dropClouds(); err != nil {
			return err
		}
		// subagents given a model of their own keep it; the others run on
		// the session's, whatever /model picked, or on the tier they ask for.
		// An older magpie's effort on the main model goes out, kept unwritten.
		if e := subagentFollowing(); e != "" && stashShared()[subEffortKey] == "" {
			stash(map[string]string{subEffortKey: e})
		}
		sub := subagentOwn()
		// the main model is settings.json's alone: ANTHROPIC_MODEL would
		// outrank a pick in /model
		if _, has := edit.GetJSON(path, "env.ANTHROPIC_MODEL"); has {
			if err := edit.DelJSON(path, "env.ANTHROPIC_MODEL"); err != nil {
				return err
			}
		}
		gw := gwAt()
		tok := wiredKey()
		// a caller key of the gateway's own where magpie's token goes goes
		// through magpie all the same: theirs stays, as a sync keeps one
		// (mergeKept, keyHolds); only a value the gateway would refuse is
		// wired over (#2545). The sharing key magpie made is magpie's own,
		// however much Authenticate takes it, so it never counts as theirs
		// (Default): sharing going off brings gateway.Token back
		if v, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); v != tok && keyHolds(v, tok) && !access.Default(v) {
			tok = v
		}
		kvs := []edit.KV{
			{Path: "env.ANTHROPIC_BASE_URL", Value: gw},
			{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: tok},
			{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: spell(tiers["haiku"])},
			{Path: "model", Value: spell(main)},
		}
		// a tier on no model is left out, for Claude Code's own (fable on
		// a Claude model, follow)
		var none []string
		for _, t := range claudeTiers {
			if tiers[t] == "" {
				none = append(none, "env."+tierEnv(t))
				continue
			}
			kvs = append(kvs, edit.KV{Path: "env." + tierEnv(t), Value: spell(tiers[t])})
		}
		if len(none) > 0 {
			if err := edit.DelJSON(path, none...); err != nil {
				return err
			}
		}
		if sub != "" {
			kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: spell(mark(sub))})
		} else if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
			return err
		}
		stash(map[string]string{mainKey: main, wroteAt: gw})
		// the new model's window replaces the old one's, when magpie knows
		// it; one the user set is theirs
		if err := writeWindow(main, tiers); err != nil {
			return err
		}
		models := []string{main}
		for _, t := range claudeTiers {
			m, _ := tierAt(tiers[t])
			models = append(models, m)
		}
		outModels := models
		if sub != "" {
			m, _ := tierAt(sub)
			outModels = append(slices.Clip(models), m)
		}
		// the longest reply the models can write, past the 32000 Claude
		// Code would ask them for; one the user set is theirs
		if env(claudeOutputEnv) == "" || outputOurs() {
			if n := claudeOutput(outModels...); n > 0 {
				kvs = append(kvs, edit.KV{Path: "env." + claudeOutputEnv, Value: strconv.Itoa(n)})
				stash(map[string]string{outputKey: strconv.Itoa(n)})
			} else if err := dropOutput(); err != nil {
				return err
			}
		} else {
			forget(outputKey)
		}
		if err := edit.SetJSON(path, kvs...); err != nil {
			return err
		}
		if err := writeCompact(); err != nil {
			return err
		}
		if err := writePicker(); err != nil {
			return err
		}
		if err := writeReview(models...); err != nil {
			return err
		}
		if err := writeSearch(); err != nil {
			return err
		}
		if err := writeExplore(); err != nil {
			return err
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
			own := claudeOwn(cur["model"], tier, provider.ClaudeCodeModels())
			for i := range own {
				own[i].Direct = direct
			}
			// magpie's Claude Code account is the one Claude Code asks on
			// its own while that is Anthropic: the same models a second
			// time, folded in the picker (#496). Only this machine's: a
			// distro's Claude Code has a sign-in of its own, which magpie
			// doesn't read
			fold, key := name == "Claude Code" && at.id == "", "\x00claude-via"
			if fold {
				key += "-folded"
			}
			return joinedVia(group(name, own), claudeViaShared(fold), [2]string{key, at.id})
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
				own := ""
				if n != "" {
					own, _ = edit.GetJSON(path, "modelSettings."+n+".effortLevel")
					if err := claudeDropModelEffort(path, n); err != nil {
						return err
					}
				}
				// the top one goes with it when it is the model's level
				// written beside it below; a level of the user's own
				// for the other models stays
				if top := jsonGet(path, "effortLevel")(); claudeReadsTop(m) || top != "" && top == own {
					return edit.DelJSON(path, "effortLevel")
				}
				return nil
			}
			// the top one too for a model that reads only its own: a model
			// picked in Claude Code later (a routing group, another
			// vendor's) reads the top one, and starts at the chosen level
			// rather than its default (Creat.wu on Discord)
			kvs := []edit.KV{{Path: "effortLevel", Value: v}}
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
				if w, _ := tierAt(env(tierEnv(tier))); routed() && w != "" && (w != wroteMain() || own(tier)) {
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
				// a model picked is the user's own, the main model or the
				// one the tier would follow too, so it stays when the main
				// model moves (#2654: a sonnet tier given the main model
				// read as "Same as main model" and went with its next
				// move); only "Same as main model" follows
				if v != "" {
					stash(map[string]string{ownKey(tier): "1"})
					tiers[tier] = tierWith(v, e)
				} else {
					forget(ownKey(tier))
					tiers[tier] = followAt(tier, main, e)
				}
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
	// setSubagent writes the subagents' model and effort. With no model,
	// the variable goes and the effort is kept unwritten (subagentSaved):
	// the main model at an effort would pin every subagent to it.
	setSubagent := func(model, effort string) error {
		if model == "" {
			if effort != "" {
				stash(map[string]string{subEffortKey: effort})
			} else {
				forget(subEffortKey)
			}
			if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
				return err
			}
		} else {
			forget(subEffortKey)
			if err := edit.SetJSON(path, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: spell(tierWith(model, effort))}); err != nil {
				return err
			}
		}
		return writeTiers(curTiers())
	}
	fields = append(fields, Field{
		Key: "subagent", Label: "subagents", Quiet: true, Follows: "model",
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
			m, e := subagentAt()
			if m == "" {
				e = subagentSaved()
			}
			// a model picked is the user's own, the main model too
			// (#2654: given the main model, the subagents' was left out
			// and they followed the main model's next move); only "Same
			// as main model" follows
			if v != "" {
				stash(map[string]string{ownKey("subagent"): "1"})
			} else {
				forget(ownKey("subagent"))
			}
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
				return static(tierEfforts(cmp.Or(m, mainModel()))...)
			},
		}
	}
	for _, tier := range claudeTiers {
		at := func() (string, string) {
			m, e := tierAt(env(tierEnv(tier)))
			if m == wroteMain() {
				m = ""
			}
			return m, e
		}
		put := func(model, effort string) error {
			main, tiers := curTiers()
			if model != "" {
				tiers[tier] = tierWith(model, effort)
			} else {
				tiers[tier] = followAt(tier, main, effort)
			}
			return writeTiers(main, tiers)
		}
		fields = append(fields, effortField(tier+"_effort", tier+" effort", tier, at, put))
	}
	fields = append(fields, effortField("subagent_effort", "subagent effort", "its subagents", subagentAt, setSubagent))
	// auto mode: the model Claude Code's client-side auto mode classifier
	// runs on (claudeAutoModeEnv). Unset, Claude Code's own pick; it
	// follows no field, so it isn't Follows "model". The env is read as a
	// session goes, so a pick needs no restart.
	fields = append(fields, Field{
		Key: "auto_mode", Label: "auto mode", Quiet: true,
		Get: func() string {
			if !routed() {
				return ""
			}
			return env(claudeAutoModeEnv)
		},
		Set: func(v string) error {
			v = strings.TrimSpace(v)
			if !routed() {
				if v == "" {
					return nil
				}
				return fmt.Errorf("pick a model through magpie for Claude Code first; its auto mode can then have a model of its own")
			}
			if v != "" && !isMagpie(v) {
				return fmt.Errorf("auto mode: %q is not a model magpie serves", v)
			}
			if contains(claudeNoClassifier, claudeName(v)) {
				return fmt.Errorf("auto mode: Claude Code doesn't run its classifier on %s, and would take its own pick instead", v)
			}
			if v == "" {
				forget(autoKey)
				return edit.DelJSON(path, "env."+claudeAutoModeEnv)
			}
			w := spell(v)
			stash(map[string]string{autoKey: w})
			return edit.SetJSON(path, edit.KV{Path: "env." + claudeAutoModeEnv, Value: w})
		},
		Options: func(map[string]string) []Option {
			if !routed() {
				return nil
			}
			return slices.DeleteFunc(claudeViaMagpie(false), func(o Option) bool {
				return contains(claudeNoClassifier, claudeName(o.Value))
			})
		},
	})
	// its sign-in while it runs through magpie: magpie's key, or its own
	// claude.ai sign-in kept (loginKey), where the gateway takes any key
	fields = append(fields, Field{
		Key: "login", Label: "sign-in", Quiet: true,
		Get: func() string {
			if routed() && keepSignIn() {
				return "claudeai"
			}
			return ""
		},
		Set: func(v string) error {
			if v != "" && v != "claudeai" {
				return fmt.Errorf("sign-in is claudeai or empty (magpie's key), not %q", v)
			}
			if !routed() {
				if v == "" {
					stash(map[string]string{loginKey: ""})
					return nil
				}
				return fmt.Errorf("pick a model through magpie for Claude Code first; it can then keep its claude.ai sign-in")
			}
			// one kept before stays so, taking effect where it can again
			if v != "" && !signInHere() && stashShared()[loginKey] != v {
				return fmt.Errorf("Claude Code reaches magpie from another machine (%s), where magpie, shared, takes only its sharing key, so it can't keep its claude.ai sign-in there", at.gw())
			}
			stash(map[string]string{loginKey: v, wroteAt: at.gw()})
			return edit.SetJSON(path, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: wiredKey()})
		},
		Options: func(map[string]string) []Option {
			if !routed() || !signInHere() {
				return nil
			}
			return claudeSignIns()
		},
	})
	// magpie's status line, offered where the user has none (claude_statusline.go)
	statusField := claudeStatusField(at, path, func() { stale = "" })
	fields = append(fields, statusField)
	// a write asks a proxy in front of magpie first (claudeAsk): each
	// field's Set, Sync and Unwire. Follow writes only once routed, so a
	// first look there leaves it for the next one, as reads do.
	ask := func() { claudeAsk(at, path, env("ANTHROPIC_BASE_URL")) }
	for i := range fields {
		s := fields[i].Set
		fields[i].Set = func(v string) error { ask(); return s(v) }
	}

	var self *Agent
	self = &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		UA:  []string{"claude-cli", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields:       fields,
		ModelAliases: claudeAliases,
		Telemetry:    claudeTelemetry(at, path),
		// Claude Code as it was before magpie: its default puts it back as
		// installed, on Anthropic's endpoint, where this brings back the
		// endpoint, token and model the user had
		Unwire: func() error {
			ask()
			was, err := unroute()
			if err != nil {
				return err
			}
			forget(at.key("claude.model"), at.key("claude.base_url"), at.key("claude.auth_token"), mainKey, wroteAt, aboutKey)
			for _, k := range claudeOwnEnv {
				forget(at.key("claude.env." + k))
			}
			// magpie's status line goes with it; one of the user's own stays
			if err := statusField.Set(""); err != nil {
				return err
			}
			// magpie's level in the env goes alone: the effortLevel under
			// it is the user's own, which Claude Code is back on
			if e := env(claudeEffortEnv); e != "" && appliedOf(self.ID).Fields["effort"] == e {
				if err := edit.DelJSON(path, "env."+claudeEffortEnv); err != nil {
					return err
				}
			}
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
			ask()
			if !routed() {
				if err := dropStray(); err != nil {
					return err
				}
				return desktopOnly()
			}
			// a switch turned on since, or left by an older magpie
			if err := dropClouds(); err != nil {
				return err
			}
			// routed by an older magpie: what was said about the models
			// before it is taken out as it is now when magpie is wired in
			if stashShared()[aboutKey] == "" {
				if err := dropAbout(nil); err != nil {
					return err
				}
			}
			// subagents an older magpie pinned to the main model at an
			// effort of theirs follow it again, the effort kept unwritten
			if subagentFollowing() != "" {
				return writeTiers(curTiers())
			}
			// a model of magpie's an older magpie wrote with a dotted Claude
			// version goes in as Claude Code is given it now (spell)
			raw := []string{jsonGet(path, "model")()}
			for _, k := range claudeModelEnv {
				raw = append(raw, rawEnv(k))
			}
			if slices.ContainsFunc(raw, func(v string) bool {
				m, _ := tierAt(provider.ClaudeRead(v))
				return v != "" && isMagpie(m) && spell(provider.ClaudeRead(v)) != v
			}) {
				return writeTiers(curTiers())
			}
			models := []string{mainModel()}
			tiers := map[string]string{}
			for _, t := range claudeTiers {
				tiers[t] = env(tierEnv(t))
				models = append(models, tiers[t])
			}
			// the windows of the models as they are now, Claude Desktop's
			// among them (#1458)
			if err := writeWindow(mainModel(), tiers); err != nil {
				return err
			}
			if err := writeCompact(); err != nil {
				return err
			}
			if err := writePicker(); err != nil {
				return err
			}
			if err := writeReview(models...); err != nil {
				return err
			}
			if err := writeSearch(); err != nil {
				return err
			}
			if err := writeExplore(); err != nil {
				return err
			}
			return writeCaps(models...)
		},
		// a model picked in Claude Code's /model is its main model: the tiers
		// and subagents that followed the one before go with it, and an
		// older magpie's ANTHROPIC_MODEL, which held every session to that
		// one, comes out
		Follow: func() error {
			main := mainModel()
			bare := strings.TrimSuffix(main, "[1m]")
			if !routed() || !isMagpie(bare) {
				return nil
			}
			// a haiku tier an older magpie left on the main model, where it
			// now follows on that model's Haiku, moves too
			m, _ := tierAt(env(tierEnv("haiku")))
			light := follow("haiku", bare)
			stays := light == bare || strings.TrimSuffix(m, "[1m]") == light || !follows("haiku", m)
			// so does a fable tier it left on a Claude model, where it now
			// follows on none (follow), unless at an effort of its own
			fm, fe := tierAt(env(tierEnv("fable")))
			stays = stays && (follow("fable", bare) != "" || fm == "" || fe != "" || !follows("fable", fm))
			if _, has := edit.GetJSON(path, "env.ANTHROPIC_MODEL"); !has && main == stashShared()[mainKey] && stays {
				return nil
			}
			return set(bare)
		},
		Check: func() string {
			if !isMagpie(get()) {
				return ""
			}
			getEnv := func(k string) (string, bool) { return edit.GetJSON(path, "env."+k) }
			// an address that isn't the gateway's is asked whether it is
			// magpie behind a proxy (#1985) only while what Claude Code
			// sends is for magpie (claudeKeyed): with a key of the user's
			// own it is their endpoint, told as it is, never asked (#3480)
			keyed := claudeKeyed(at, path)
			// an administrator's settings win over the user's
			managed := claudeManaged()
			if at.sys != nil {
				managed = at.sys("/etc/claude-code/managed-settings.json")
			}
			if u, _ := edit.GetJSON(managed, "env.ANTHROPIC_BASE_URL"); u != "" && u != at.gw() && !(keyed && reachesMagpie(u, at.gw())) {
				return "Claude Code's managed settings (" + at.native(managed) + ") set ANTHROPIC_BASE_URL to " + u + ", which wins over magpie's"
			}
			if keyed {
				if off := wiringOff("Claude Code", path, getEnv, "ANTHROPIC_BASE_URL", at.gw()); off != "" {
					return off
				}
			} else if u, _ := getEnv("ANTHROPIC_BASE_URL"); u != at.gw() {
				return whatsOff("Claude Code", path, "ANTHROPIC_BASE_URL", u, at.gw())
			}
			if why := claudeCloud(at, path); why != "" {
				return why
			}
			// the key it sends: none where it keeps its claude.ai sign-in,
			// which a key there would take the place of; magpie's otherwise,
			// one gone sending that sign-in instead, if it has one
			tok, has := getEnv("ANTHROPIC_AUTH_TOKEN")
			where := "Claude Code's ANTHROPIC_AUTH_TOKEN (" + filepath.Base(path) + ")"
			switch {
			case keepSignIn() && tok != "":
				return where + " is set, so Claude Code sends magpie that key, not its claude.ai sign-in as magpie set it"
			case keepSignIn():
				return ""
			case tok == "" && signInHere():
				how := "is gone"
				if has {
					how = "is empty"
				}
				return where + " " + how + ", so Claude Code sends magpie its claude.ai sign-in, if it has one, not magpie's key — to keep it so, set its sign-in to claude.ai"
			}
			return wiringOff("Claude Code", path, getEnv, "ANTHROPIC_AUTH_TOKEN", at.gwKey())
		},
		// every prompt typed into Claude Code goes into history.jsonl, and
		// so does every one magpie's bridge gives it in magpie's own work
		// folder, which went through magpie (ifccod7391564 on X)
		LastUsed: func() time.Time {
			return lastJSONLTime(filepath.Join(filepath.Dir(path), "history.jsonl"), "timestamp", "display", inMagpieWork)
		},
		// a session takes a new CLAUDE_CODE_EFFORT_LEVEL on its next
		// request, but settings.json's effort and ultracode, and the env's
		// level taken away, only when started again. Windows can't be asked
		// what runs.
		Notice: func() string {
			if stale == "" || !claudeRunning() {
				return ""
			}
			if stale == "ultracode" {
				return noticeClaudeUltracode.String()
			}
			return noticeClaudeEffort.String()
		},
	}
	return self
}

// claudeSignIns are the ways Claude Code signs in to magpie, its sign-in
// field's options: what its claude.ai sign-in gives it is off under a key
// (ANTHROPIC_AUTH_TOKEN), and Remote Control and ultrareview are off on any
// address but Anthropic's own (2.1.290).
func claudeSignIns() []Option {
	return []Option{
		{Value: "", Label: "magpie's key", Note: "Claude Code sends magpie its key and is signed out of claude.ai while it runs through magpie: claude.ai's plan limits in /usage, its connectors, voice and /teleport are off"},
		{Value: "claudeai", Label: "claude.ai", Note: "Claude Code keeps its claude.ai sign-in (/login), so those work; it sends that sign-in to magpie, which never passes it on. Remote Control and ultrareview stay off: Claude Code has them only on Anthropic's own address"},
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

// claudeOwn is Anthropic's models as Claude Code takes them, ms: the
// catalog's, and on this computer the ones Anthropic told Claude Code
// its account has beyond them (provider.ClaudeCodeModels: Claude Mythos
// 5.1); Claude Code's own short aliases are not something any API lists,
// and a compiled-in copy would just go stale. One the user's
// settings.json names (cur: "model": "sonnet") comes first, said as the
// model it stands for, so the row doesn't show a bare word; tier is the
// model the user's env gives a tier, "" for none.
func claudeOwn(cur string, tier func(string) string, ms []catalog.Model) []Option {
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
// picker that lists Claude Code's own models above them. Each caller has
// a copy of its own of the list built last (builtOptions): its model and
// tier fields asked it each poll, for the picker and again for Drift, a
// list built anew each time, 30 MB of forty polls with real accounts.
func claudeViaMagpie(fold bool) []Option {
	return slices.Clone(claudeViaShared(fold))
}

// claudeViaShared is claudeViaMagpie without the caller's own copy, the
// list kept, read only: for a caller appending it to a list of its own,
// which copies it anyway (Claude Code's model field, 5 MB of forty polls
// copied twice).
func claudeViaShared(fold bool) []Option {
	key := "\x00claude-via"
	if fold {
		key += "-folded"
	}
	return builtOptions.shared(key, func() []Option { return buildClaudeVia(fold) })
}

// buildClaudeVia is claudeViaMagpie built afresh.
func buildClaudeVia(fold bool) []Option {
	big := map[string]bool{}
	for _, m := range sharedModels("claude") {
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
	for _, m := range sharedModels(agent) {
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

// claudeModel says a model ref (magpie/v/claude-opus-5-5[1m], or Claude
// Code's own claude-opus-5-5) is one of Anthropic's Claude models, by
// whichever provider it comes and by whatever name it gives it: Amazon
// Bedrock's us.anthropic.claude-opus-… too, which Claude Code knows by the
// same "opus-…" it looks for, and which a group of was already taken for
// Claude's (provider.ClaudeGroup) while the model alone was held to the
// working window (划船不靠桨 on Discord).
func claudeModel(ref string) bool {
	return provider.ClaudeModel(strings.TrimSuffix(ref, "[1m]"))
}

// claudeOwnCompact says the settings.json at path has an auto-compact
// window of the user's for model: autoCompactWindow, or one under
// modelSettings for it (as /autocompact saves it), a number or "auto".
// Claude Code takes CLAUDE_CODE_AUTO_COMPACT_WINDOW before either.
func claudeOwnCompact(path, model string) bool {
	if _, ok := edit.GetJSON(path, "autoCompactWindow"); ok {
		return true
	}
	raw, ok := edit.GetJSON(path, "modelSettings")
	if !ok {
		return false
	}
	var per map[string]struct {
		Window any `json:"autoCompactWindow"`
	}
	if json.Unmarshal([]byte(raw), &per) != nil {
		return false
	}
	// Claude Code keys it by the model as it was given it (ClaudeSpelled)
	model = strings.ToLower(provider.ClaudeDashed(strings.TrimSuffix(strings.TrimPrefix(model, "magpie/"), "[1m]")))
	for k, v := range per {
		if v.Window != nil && strings.ToLower(provider.ClaudeDashed(strings.TrimSuffix(k, "[1m]"))) == model {
			return true
		}
	}
	return false
}

// claudeLongMain says main is a model of magpie's Claude Code takes for 1M
// by its "[1m]", not one of Claude's own, which Anthropic runs to its own.
func claudeLongMain(main string) bool {
	m, _ := tierAt(main)
	bare := strings.TrimSuffix(m, "[1m]")
	return bare != m && !claudeModel(bare) && !provider.ClaudeGroup(bare)
}

// claudeWindow is the context window to tell Claude Code for the models it
// runs on, 0 when magpie doesn't know it. One value serves every model not
// marked [1m]: the main model's, or when that one is marked, the smallest of
// the tiers' that aren't, so none of them outgrows its own.
func claudeWindow(main string, tiers map[string]string) int {
	const mark = "[1m]"
	window := map[string]int{}
	for _, m := range sharedModels("claude") {
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

// claudeDesktopWindow is the least window of the models Claude Desktop's
// Code tab runs Claude Code on that it takes claudeContextEnv for, 0 when
// it knows none. Desktop hands Claude Code the id it lists a model by
// (gateway.DesktopID) and nothing of its window: it reads the gateway's
// max_input_tokens only to offer a 1M entry (2.31226.1), and Claude Code
// takes no window from a gateway's /v1/models (its model catalog is
// Anthropic's alone, and even there one past 200K counts as 200K for a
// model it doesn't know). So a mythos-magpie id of a 272K model ran as
// 200K, and with the auto-compact window shown in its place, a 1M one as
// that (#1458). Left out are a model listed by its "[1m]" id, which is 1M
// whatever this says, and one named as a Claude model Claude Code knows,
// which has its own; Claude Code takes one value for all the rest, so it
// is the least, which none of them outgrows.
func claudeDesktopWindow() int {
	shown, _ := provider.CatalogFor("claude-desktop")
	w := 0
	for _, e := range shown {
		if e.Context <= 0 || gateway.DesktopListed1M(e) || claudeName(gateway.DesktopID(e)) != "" {
			continue
		}
		if w == 0 || e.Context < w {
			w = e.Context
		}
	}
	return w
}

// claudeOutput is the reply length to tell Claude Code for the models it
// runs on (main first), 0 to leave it to Claude Code: when the main model is
// a Claude one, which Claude Code knows; when a model it runs on that isn't
// has no known limit, as one value serves them all; and when the least of
// theirs is no more than what Claude Code asks for anyway.
func claudeOutput(models ...string) int {
	if len(models) == 0 || claudeModel(models[0]) {
		return 0
	}
	output := map[string]int{}
	for _, m := range sharedModels("claude") {
		output[m.ID] = m.Output
	}
	n := 0
	for _, ref := range models {
		if ref == "" || claudeModel(ref) {
			continue
		}
		ref = strings.TrimSuffix(ref, "[1m]")
		o := cmp.Or(output[ref], catalog.OutputOf(ref))
		if o <= 0 {
			return 0
		}
		if n == 0 || o < n {
			n = o
		}
	}
	if n <= claudeUnknownOutput {
		return 0
	}
	return n
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
// model. For Codex, the model it is set to (codexStandIn), in a WSL
// distro the distro's config names (wslCodexStandIn). "" when the
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
		if m := codexStandIn(filepath.Join(here(home).codexHome(), "config.toml")); m != "" || runtime.GOOS != "windows" {
			return m
		}
		// a Codex in a WSL distro is known by the same client
		return wslCodexStandIn()
	}
	if m := claudeStandIn(filepath.Join(here(home).claudeDir(), "settings.json"), model); m != "" {
		return m
	}
	// a claude-magpie session, its settings.json not on magpie, is on
	// the launch file's models (claude_launch.go)
	if m := claudeLaunchStandIn(model); m != "" || runtime.GOOS != "windows" {
		return m
	}
	// a Claude Code in WSL is known by the same User-Agent
	return wslClaudeStandIn(model)
}

// ClassifierStandIn is the model Claude Code's auto mode classifier goes
// to in place of the one it named, while the sonnet tier it named follows
// a main model that /model has moved since magpie last wrote the tiers.
// Claude Code 2.1.310 runs the classifier, with CLAUDE_CODE_AUTO_MODE_MODEL
// unset, on ANTHROPIC_DEFAULT_SONNET_MODEL ($Xe, BB in its bundle), not on
// the session's model. A pick in /model saves the main model alone, and
// the tiers following it move only when magpie next looks (Follow), so
// the first classifier request after a switch named the model before it
// (CyberCaius on Discord). "" leaves the model as asked. For
// gateway.ClassifierStandIn.
func ClassifierStandIn(agent, model string) string {
	if agent != "claude" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if m := claudeClassifierIn(here(home), model); m != "" || runtime.GOOS != "windows" {
		return m
	}
	return wslClaudeClassifier(model)
}

// claudeClassifierIn is ClassifierStandIn for the Claude Code at a place:
// the main model the sonnet tier follows, at the tier's effort, when the
// classifier named that tier as magpie last wrote it and the main model
// has moved on. A tier the user put on a model of its own, or an auto
// mode model set for the classifier, is left as it is.
func claudeClassifierIn(at place, model string) string {
	path := filepath.Join(at.claudeDir(), "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return provider.ClaudeRead(v) }
	if u := env("ANTHROPIC_BASE_URL"); u != at.gw() && !claudeProxied(at, path, u) {
		return ""
	}
	if env(claudeAutoModeEnv) != "" {
		return ""
	}
	st := stashLoad()
	if st[at.key("claude.tier_own.sonnet")] != "" {
		return ""
	}
	bare := func(v string) string { m, _ := tierAt(v); return strings.TrimSuffix(m, "[1m]") }
	tier, effort := tierAt(env(tierEnv("sonnet")))
	if tier == "" || bare(provider.ClaudeRead(model)) != bare(tier) {
		return ""
	}
	was := bare(cmp.Or(st[at.key("claude.main")], env("ANTHROPIC_MODEL")))
	if was == "" || bare(tier) != bare(follow("sonnet", was)) {
		return "" // the tier is on a model of its own
	}
	main := claudeMain(path)
	if main == "" || bare(main) == was || !isMagpie(bare(main)) {
		return ""
	}
	return followAt("sonnet", bare(main), effort)
}

func claudeStandIn(path, model string) string { return claudeStandInAt(here(""), path, model) }

// claudeStandInAt is claudeStandIn for the Claude Code at a place, which
// reaches the gateway at its own address.
func claudeStandInAt(at place, path, model string) string {
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return provider.ClaudeRead(v) }
	if u := env("ANTHROPIC_BASE_URL"); u != at.gw() && !claudeProxied(at, path, u) {
		return ""
	}
	// the model Claude Code is set to itself, by that id, is its own pick
	// rather than a tier's
	bare := func(v string) string { m, _ := tierAt(v); return strings.TrimSuffix(m, "[1m]") }
	main := claudeMain(path)
	if main != "" && bare(main) == bare(model) {
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
	return main
}

// claudeKeyed says what the Claude Code at a place (its settings at path)
// sends is for magpie: a key the gateway takes (keyHolds, as Check asks of
// it), or none where it keeps its claude.ai sign-in through magpie, which
// magpie set (claude.login) on a Claude Code it wired (wired_at, which
// Disconnect forgets) where the gateway takes any key (signInHere).
func claudeKeyed(at place, path string) bool {
	if tok, _ := edit.GetJSON(path, "env.ANTHROPIC_AUTH_TOKEN"); tok != "" {
		return keyHolds(tok, at.gwKey())
	}
	st := stashLoad()
	return at.gwKey() == gateway.Token && st[at.key("claude.login")] == "claudeai" && st[at.key("claude"+wiredAt)] != ""
}

// claudeProxied says u, the base URL the Claude Code at a place (its
// settings at path) has in place of the gateway's, is a proxy to it where
// magpie has answered (#1985, surelyMagpie). It is asked only where it can
// be another way to magpie (proxyBase), and only while Claude Code sends
// it what is for magpie (claudeKeyed): with a key of the user's own, u is
// their endpoint. zhangt2333 (#3480) never connected Claude Code, and the
// Agents page's reads of it sent GET / about every 30s to the endpoint in
// its settings.
func claudeProxied(at place, path, u string) bool {
	_, ok := claudeProxyBase(at, path, u)
	return ok && surelyMagpie(u, at.gw())
}

// claudeProxyBase is where claudeProxied asks whether magpie answers: u's
// proxyBase, while Claude Code sends it what is for magpie (claudeKeyed).
func claudeProxyBase(at place, path, u string) (string, bool) {
	base, ok := proxyBase(u, at.gw())
	if !ok || u == at.gw() {
		return "", false
	}
	return base, claudeKeyed(at, path)
}

// claudeAsk is what each write to the Claude Code at a place does first:
// the proxy claudeProxied would ask about is asked now where magpie hasn't
// answered there yet (magpieAsked). Every write reads routed, and the first
// one in a magpie just started, the CLI's, read a proxy not asked yet as
// no: a pick wrote 127.0.0.1 over it, the sync a provider added runs
// turned the auto mode review back on, Disconnect left magpie in and
// forgot the endpoint the user had (#1985).
func claudeAsk(at place, path, u string) {
	if base, ok := claudeProxyBase(at, path, u); ok {
		magpieAsked(base)
	}
}

// claudeMain is the model a Claude Code routed through magpie runs on, as
// settings.json names it: its model, which /model saves, an alias there
// resolved through its tier. With none, the ANTHROPIC_MODEL an older
// magpie wrote, else the sonnet tier's, as Claude Code on an API key
// starts with.
func claudeMain(path string) string {
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	m, _ := edit.GetJSON(path, "model")
	if t := strings.TrimSuffix(m, "[1m]"); contains(claudeTiers, t) {
		m = env(tierEnv(t))
	}
	if m == "" {
		m = env("ANTHROPIC_MODEL")
	}
	if m == "" {
		m = env(tierEnv("sonnet"))
	}
	m, _ = tierAt(provider.ClaudeRead(m))
	return m
}

// what an open Claude Code session keeps (notice.go)
var (
	noticeClaudeEffort    = newNotice("an open Claude Code session keeps the effort it started with — restart it to use this.")
	noticeClaudeUltracode = newNotice("an open Claude Code session keeps the ultracode it started with — restart it to use this.")
)

// why a Claude Code wired to magpie goes to a cloud instead (claudeCloud)
var (
	noticeClaudeCloudFile  = newNotice("{file} sets {var}, so Claude Code sends its requests to {cloud}, not to magpie: take it out there to go through magpie.")
	noticeClaudeCloudShell = newNotice("{var} is set in your shell's environment, so Claude Code started there sends its requests to {cloud}, not to magpie: take it out of your shell's profile to go through magpie.")
)

// magpiesPicker says whether settings.json's modelPicker is one magpie
// wrote: it has rows, and each one's description is magpie's, which ends
// "via magpie" (writePicker in claudeIn).
func magpiesPicker(path string) bool {
	raw, has := edit.GetJSON(path, "modelPicker")
	if !has {
		return false
	}
	var p struct {
		Options []struct {
			Model       string `json:"model"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil || len(p.Options) == 0 {
		return false
	}
	for _, o := range p.Options {
		if o.Model == "" || !strings.HasSuffix(o.Description, "via magpie") {
			return false
		}
	}
	return true
}

// claudeCustomOptionEnv is the one row a user adds to Claude Code's /model
// themselves, beside its built-in lineup; replaceBuiltInOptions hides it
// with the lineup.
const claudeCustomOptionEnv = "ANTHROPIC_CUSTOM_MODEL_OPTION"

// claudeNarrowed says whether Claude Code's list leaves out some of
// magpie's catalog: only the models picked for it, some taken out one by
// one, or a visibility of families (provider.CatalogFor's kept list).
func claudeNarrowed() bool {
	_, hidden := provider.CatalogFor("claude")
	return len(hidden) > 0
}

// inMagpieWork: a line of Claude Code's history.jsonl is a prompt magpie's
// bridge gave it, in magpie's own work folder (os.TempDir()'s
// magpie-claude-work, magpie-claude-work-<uid> off Windows), whatever the
// path to it (/private/var/… on a Mac, C:/… or a WSL mount on Windows).
func inMagpieWork(m map[string]json.RawMessage) bool {
	var project string
	if json.Unmarshal(m["project"], &project) != nil {
		return false
	}
	base := strings.TrimRight(project, `/\`)
	base = base[strings.LastIndexAny(base, `/\`)+1:]
	return base == "magpie-claude-work" || strings.HasPrefix(base, "magpie-claude-work-")
}
