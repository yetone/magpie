package gateway

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// geminiLevelBody asks a Gemini 3 or later model, on an Anthropic or Chat
// upstream that isn't Gemini's own API (a relay serving Gemini under a
// host of its own, Mamba_asr's peta-gemini), for a thinking level, never a
// budget. Gemini 4 turns every thinkingBudget away ("Thinking budget is
// not supported for this model. Use thinking level instead.", #2083), and
// such a relay makes the thinkingBudget it sends Gemini from what it was
// sent:
//   - Anthropic: thinking enabled with budget_tokens (Claude Code's, or the
//     translation's from a Chat or Responses effort) goes as thinking
//     adaptive with output_config's effort, the budget bucketed into a
//     level as effortOfBudget reads it; an effort the request names is the
//     one kept.
//   - Chat: extra_body's google thinking_config budget goes as its
//     thinking_level.
//
// Either way the level is fitted to the model's (geminiLevelFor), and a
// reasoning_effort or output_config effort above what it has comes down
// to its highest. Any other model's body is returned as it came, and so is
// one for Gemini's own OpenAI-compatible API (geminiCompat), which takes
// a budget and whose levels the Chat path fits, and asks again when one
// is turned away (geminiRefusedLevel).
func geminiLevelBody(p provider.Provider, proto provider.Protocol, model string, body []byte) []byte {
	if proto != provider.Anthropic && proto != provider.Chat {
		return body
	}
	if proto == provider.Chat && geminiCompat(p.VendorHost(), model) {
		return body
	}
	m := strings.ToLower(model)
	if geminiGeneration(m) < 3 {
		return body
	}
	if !bodyHas(body, `"thinking`) && !bodyHas(body, `"reasoning_effort"`) && !bodyHas(body, `"output_config"`) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var top map[string]any
	if dec.Decode(&top) != nil {
		return body
	}
	fields := map[string]any{}
	efforts := p.Efforts(model)
	switch proto {
	case provider.Anthropic:
		th, _ := top["thinking"].(map[string]any)
		oc, _ := top["output_config"].(map[string]any)
		said, _ := oc["effort"].(string)
		switch t, _ := th["type"].(string); t {
		case "enabled":
			level := effortOf(said)
			if level == "" {
				n, _ := th["budget_tokens"].(json.Number)
				b, _ := n.Int64()
				level = effortOfBudget(int(b))
			}
			adaptive := map[string]any{"type": "adaptive"}
			if d, ok := th["display"]; ok {
				adaptive["display"] = d
			}
			fields["thinking"] = adaptive
			if level != "" {
				if oc == nil {
					oc = map[string]any{}
				}
				oc["effort"] = geminiLevelForEfforts(m, level, efforts)
				fields["output_config"] = oc
			}
		case "adaptive":
			if said != "" {
				if fit := geminiLevelForEfforts(m, said, efforts); fit != said {
					oc["effort"] = fit
					fields["output_config"] = oc
				}
			}
		}
	case provider.Chat:
		if e, ok := top["reasoning_effort"].(string); ok && e != "" {
			if fit := geminiLevelForEfforts(m, e, efforts); fit != e {
				fields["reasoning_effort"] = fit
			}
		}
		eb, _ := top["extra_body"].(map[string]any)
		google, _ := eb["google"].(map[string]any)
		key := "thinking_config"
		if _, ok := google[key]; !ok {
			key = "thinkingConfig"
		}
		tc, _ := google[key].(map[string]any)
		changed := false
		for _, bk := range []string{"thinking_budget", "thinkingBudget"} {
			n, ok := tc[bk].(json.Number)
			if !ok {
				continue
			}
			delete(tc, bk)
			changed = true
			if tc["thinking_level"] != nil || tc["thinkingLevel"] != nil {
				continue
			}
			switch b, _ := n.Int64(); {
			case b < 0:
				// -1: the model decides, which a level left out says
			case b == 0:
				// off: Gemini 3 can't stop thinking, and thinks least
				// at low, which every Gemini 3 text model takes
				tc["thinking_level"] = "low"
			default:
				tc["thinking_level"] = geminiLevelForEfforts(m, effortOfBudget(int(b)), efforts)
			}
		}
		if changed {
			google[key] = tc
			eb["google"] = google
			fields["extra_body"] = eb
		}
	}
	if len(fields) == 0 {
		return body
	}
	return withFields(body, fields)
}

// geminiLevelFor is effort as a Gemini 3 or later model takes it: the
// nearest of its levels where the catalog lists them, else low, medium or
// high as Google's model table has them (medium to Flash and from Gemini
// 4, high to a Gemini 3 Pro, which took low and high only), and nothing
// above high. Minimal is kept for a Gemini that may have it; one that
// hasn't turns it away by name and is asked again at its lowest
// (geminiRefusedLevel).
func geminiLevelFor(model, effort string) string {
	return geminiLevelForEfforts(model, effort, nil)
}

// geminiLevelForEfforts fits effort using the provider's levels when known,
// falling back to catalog.EffortsOf.
func geminiLevelForEfforts(model, effort string, efforts []string) string {
	level := strings.ToLower(strings.TrimSpace(effort))
	if level == "none" {
		return "low"
	}
	if level != "minimal" {
		if l := effortOf(level); l != "" {
			level = l
		}
	}
	ls := efforts
	if len(ls) == 0 {
		ls = catalog.EffortsOf(model)
	}
	if len(ls) > 0 {
		ls = slices.DeleteFunc(slices.Clone(ls), func(l string) bool { return l == "none" })
		if len(ls) > 0 {
			return catalog.NearestEffort(level, ls)
		}
	}
	switch {
	case slices.Index(effortRank, level) > slices.Index(effortRank, "high"):
		return "high"
	case level == "medium" && geminiGeneration(model) == 3 && !strings.Contains(model, "flash"):
		return "high"
	}
	return level
}
