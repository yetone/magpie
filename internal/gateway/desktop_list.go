package gateway

import (
	"encoding/json"
	"errors"
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

// desktopBehavesAs is the newest Claude model of each tier in Claude
// Desktop's own model catalog (v3739), which Claude Code 2.1.316 knows.
var desktopBehavesAs = map[string]string{
	"opus":   "claude-opus-5-5",
	"sonnet": "claude-sonnet-5-5",
	"haiku":  "claude-haiku-5-5",
	"fable":  "claude-fable-5-1",
}

// DesktopBehavesAs is the Claude model each picked tier's model runs as in
// Desktop's Code tab, by the id Desktop hands Claude Code (DesktopID); a
// model that is a Claude model itself has its own already. ok is false
// when the picks can't be read.
func DesktopBehavesAs() (as map[string]string, ok bool) {
	picked, ok := desktopTiersKnown()
	if !ok {
		return nil, false
	}
	tierOf := desktopTierOf(picked)
	shown, _ := provider.CatalogFor("claude-desktop")
	as = map[string]string{}
	for _, e := range shown {
		if t := tierOf[e.ID]; t != "" && claudeModel(e) == "" {
			as[DesktopID(e)] = desktopBehavesAs[t]
		}
	}
	return as, true
}

// DesktopInferenceModels is the inferenceModels list magpie gives Claude
// Desktop's profile: nil while no model it lists is picked for a tier,
// when Desktop finds magpie's models on its own (/v1/models). ok is false
// when the picks can't be read: whatever is written stays.
func DesktopInferenceModels() (list []map[string]any, ok bool) {
	picked, ok := desktopTiersKnown()
	if !ok {
		return nil, false
	}
	tierOf := desktopTierOf(picked)
	shown, _ := provider.CatalogFor("claude-desktop")
	rows := desktopModels(shown, true)
	tagged := false
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
			if claudeModel(e) == "" {
				entry["behavesAs"] = desktopBehavesAs[t]
			}
		}
		list = append(list, entry)
	}
	if !tagged {
		return nil, true
	}
	return list, true
}

// desktopTierOf is the tier each picked model is tagged with: a model
// picked for two, the first (desktopModels).
func desktopTierOf(picked map[string]string) map[string]string {
	tierOf := map[string]string{}
	for _, t := range DesktopTierNames {
		if id := picked[t]; id != "" && tierOf[id] == "" {
			tierOf[id] = t
		}
	}
	return tierOf
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
