package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// A model the user picks for one of Claude Desktop's tiers is, for the
// Code tab's Claude Code, a model it doesn't know: it runs it with its
// generic prompt, not the one it has for the Claude model of that tier
// (moer2831 on Discord: 通用提示词和对应模型提示词差距挺大的). Desktop
// 4.29 and later take behavesAs, the Claude model whose prompt profile,
// capability and effort defaults apply, only from the inferenceModels list
// of a profile (models.list in its app.asar, 4.4212: z3t hands Claude Code
// modelPicker.options with each entry's behavesAs; the gateway's
// behaves_as feeds Desktop's own effort profile only). A list there
// replaces the gateway's /v1/models in Desktop's menu, and while none of
// its names is a bare tier word Desktop doesn't read /v1/models at all
// (adminListObviatesDiscovery), so the list magpie writes is the whole of
// what /v1/models gives Desktop, in its order, by its ids, each with what
// Desktop would have read off the row: its display_name as labelOverride,
// supports1m for a window of 1M and up (and prefer1m for a model listed by
// its 1M id alone), the tier of a pick. Older builds take the entries' names and
// drop the rest (2.7032's entry is a zod object, which strips unknown
// keys).

// desktopBehavesAs is the newest Claude model of each tier as Claude Code
// 2.1.321 knows them (its latest_per_family; Fable 5.2 has a prompt bundle
// of its own there). The Desktop catalog that build carries (v4371,
// 2026-11-09) still ends the fable tier at Fable 5.1, and a Claude Code
// older than 2.1.321 doesn't know Fable 5.2, so on one the fable tier runs
// with the generic prompt until it updates.
var desktopBehavesAs = map[string]string{
	"opus":   "claude-opus-5-5",
	"sonnet": "claude-sonnet-5-5",
	"haiku":  "claude-haiku-5-5",
	"fable":  "claude-fable-5-2",
}

// DesktopBehavesAs is the Claude model each picked tier's model runs as in
// Desktop's Code tab, by the id Desktop hands Claude Code (DesktopID, and
// for a tier after the first its desktopTierAlias); one Claude Code knows
// as a Claude model by that id has its own already (desktopRunsAs). ok is
// false when the picks can't be read.
func DesktopBehavesAs() (as map[string]string, ok bool) {
	picked, ok := desktopTiersKnown()
	if !ok {
		return nil, false
	}
	tierOf, more := desktopTierOf(picked), desktopSharedTiers(picked)
	shown, _ := provider.CatalogFor("claude-desktop")
	as = map[string]string{}
	for _, e := range shown {
		if t := tierOf[e.ID]; t != "" {
			if b := desktopRunsAs(e, DesktopID(e), t); b != "" {
				as[DesktopID(e)] = b
			}
		}
		for _, t := range more[e.ID] {
			id := desktopTierAlias(e, t)
			if b := desktopRunsAs(e, id, t); b != "" {
				as[id] = b
			}
		}
	}
	return as, true
}

// DesktopTierAliases are, by a picked model's catalog id, the ids Claude
// Desktop is shown it by for the tiers it is picked for after the first
// (desktopTierAlias): nil when no model is picked for two, or the picks
// can't be read.
func DesktopTierAliases() map[string][]string {
	picked, ok := desktopTiersKnown()
	if !ok {
		return nil
	}
	more := desktopSharedTiers(picked)
	if len(more) == 0 {
		return nil
	}
	out := map[string][]string{}
	shown, _ := provider.CatalogFor("claude-desktop")
	for _, e := range shown {
		for _, t := range more[e.ID] {
			out[e.ID] = append(out[e.ID], desktopTierAlias(e, t))
		}
	}
	return out
}

// DesktopInferenceModels is the inferenceModels list magpie gives Claude
// Desktop's profile: nil while no model it lists is picked for a tier,
// when Desktop finds magpie's models on its own (/v1/models). ok is false
// when the picks can't be read: whatever is written stays. A model picked
// for a tier after the first it is picked for is listed once more after
// the rows, for that tier (desktopSharedTiers), so the first row, which a
// new session starts on, stays the first.
func DesktopInferenceModels() (list []map[string]any, ok bool) {
	picked, ok := desktopTiersKnown()
	if !ok {
		return nil, false
	}
	tierOf, more := desktopTierOf(picked), desktopSharedTiers(picked)
	shown, _ := provider.CatalogFor("claude-desktop")
	rows := desktopModels(shown, true)
	tagged := false
	var again []map[string]any
	for i, m := range rows {
		e := shown[i]
		id, _ := m["id"].(string)
		name, long := strings.CutSuffix(id, "[1m]")
		// Desktop reads /v1/models no more once a profile lists models
		// whose names aren't bare tier words (adminListObviatesDiscovery
		// in its app.asar, 4.4212), and names an entry by its
		// labelOverride, else its catalog's name, else the discovered
		// row's, else the id itself (pzn, ngn): every mythos-magpie-<n>
		// read as that id (chengming0v0 on Discord). Each entry is
		// labelled as /v1/models labels its row, a catalog-named Claude
		// model too, whose provider the skipped description said
		// (desktopSaidBy).
		entry := map[string]any{"name": name, "labelOverride": m["display_name"]}
		if long {
			entry["supports1m"], entry["prefer1m"] = true, true
		} else if e.Context >= desktop1M {
			entry["supports1m"] = true
		}
		if t := tierOf[e.ID]; t != "" {
			tagged = true
			entry["anthropicFamilyTier"], entry["isFamilyDefault"] = t, true
			if b := desktopRunsAs(e, name, t); b != "" {
				entry["behavesAs"] = b
			}
		}
		list = append(list, entry)
		for _, t := range more[e.ID] {
			alias := desktopTierAlias(e, t)
			x := map[string]any{"name": alias, "labelOverride": fmt.Sprint(m["display_name"]) + " · " + t,
				"anthropicFamilyTier": t, "isFamilyDefault": true}
			for _, k := range []string{"supports1m", "prefer1m"} {
				if v, has := entry[k]; has {
					x[k] = v
				}
			}
			if b := desktopRunsAs(e, alias, t); b != "" {
				x["behavesAs"] = b
			}
			again = append(again, x)
		}
	}
	if !tagged {
		return nil, true
	}
	return append(list, again...), true
}

// desktopTierOf is the tier each picked model is tagged with: a model
// picked for two, the first (desktopModels); desktopSharedTiers has the
// rest.
func desktopTierOf(picked map[string]string) map[string]string {
	tierOf := map[string]string{}
	for _, t := range DesktopTierNames {
		if id := picked[t]; id != "" && tierOf[id] == "" {
			tierOf[id] = t
		}
	}
	return tierOf
}

// desktopSharedTiers are, by a picked model's catalog id, the tiers it is
// picked for after the first (desktopTierOf). Desktop keys inferenceModels
// by name and reads one tier off an entry (FPn in its app.asar, 4.23118:
// the entry of a tier's anthropicFamilyTier names its
// ANTHROPIC_DEFAULT_<TIER>_MODEL), so a model picked for sonnet and haiku
// left haiku with no model at all: Claude Code in the Code tab got an
// empty ANTHROPIC_DEFAULT_HAIKU_MODEL and ran its small tasks, WebFetch's
// page summaries among them, on the session's model (copilot's
// claude-opus-5.5, 94–244 s each, where the haiku pick answers in seconds).
// Each such tier gets an entry of its own (desktopTierAlias).
func desktopSharedTiers(picked map[string]string) map[string][]string {
	tierOf := desktopTierOf(picked)
	more := map[string][]string{}
	for _, t := range DesktopTierNames {
		if id := picked[t]; id != "" && tierOf[id] != t {
			more[id] = append(more[id], t)
		}
	}
	return more
}

// desktopTierKey is what the number of e's id for tier t is made of
// (desktopTierAlias), which aliased reads back.
func desktopTierKey(id, t string) string { return id + "\x00" + t }

// desktopTierAlias is the id Claude Desktop is shown e by for a tier it is
// picked for after the first (desktopSharedTiers): its own id's form
// (claudeLooking) under a number of its own, so Desktop keeps the two
// entries apart and Claude Code knows a Claude model by either, which
// aliased reads back to e and the gateway serves as the pick. A model
// listed by its own id has no number to change, and is listed for the
// tier as anthropic/magpie-<number>, which runs as e (desktopRunsAs).
func desktopTierAlias(e provider.Entry, t string) string {
	n := aliasNumber(desktopTierKey(e.ID, t))
	id := claudeLooking(e)
	if rest, ok := strings.CutPrefix(id, "magpie-"); ok {
		if i := strings.Index(rest, desktopClaudeInfix); i > 0 {
			return "magpie-" + n + rest[i:]
		}
	}
	if strings.HasPrefix(id, desktopEffortAlias) {
		return desktopEffortAlias + n
	}
	return desktopAlias + n
}

// desktopTiersKnown is DesktopTiers, ok false when the file is there and
// can't be read or parsed: no picks is something else than unknown picks.
func desktopTiersKnown() (map[string]string, bool) {
	out := map[string]string{}
	b, err := os.ReadFile(desktopTiersPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return out, true
	case err != nil:
		return nil, false
	}
	if json.Unmarshal(b, &out) != nil {
		return nil, false
	}
	return out, true
}

// desktopRunsAs is the behavesAs of e, picked for tier t, listed by id:
// none when Claude Code knows a Claude model by that id on its own, else
// the Claude model e is, else t's newest. A routing group led by a Claude
// model is shown by its mythos-magpie-<n> (claudeLooking), which Claude
// Code doesn't know, so it is given the group's model, as a model listed
// by anthropic/magpie-<n> is.
func desktopRunsAs(e provider.Entry, id, t string) string {
	m := claudeModel(e)
	if m == "" {
		return desktopBehavesAs[t]
	}
	if strings.HasPrefix(id, desktopEffortAlias) || strings.HasPrefix(id, desktopAlias) {
		return m
	}
	return ""
}
