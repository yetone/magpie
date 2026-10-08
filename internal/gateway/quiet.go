package gateway

// A routing group reasons when any member does (provider.groupEntries), so
// an agent asks a group for reasoning that a member of it may not think
// at all: the group's own model is a member's, and its levels are the ones
// the members share. Asking such a member anyway is what #950 is about:
// some vendors turn a reasoning ask away with a 400 on a model that can't
// think, where magpie knows from the model's own list that it can't.
//
// The gateway sends such a member no reasoning ask: the effort is for the
// members that take it, and a member known not to think is sent the
// request as one that asked for none. Which member goes first is not
// changed: the group routes as it did, and only the body changes. A model
// nothing speaks for isn't touched — it is sent the effort the agent asked
// for, as #597 leaves it — and neither is a request to a model of its own,
// outside a group. Nor is a member the user fixed at an effort (candidate
// effort, #189): that is the user saying this model does think, over a
// catalog that may be wrong about it — the Trae plugin's own model, whose
// plugin gave no capabilities, is what #950 was reported about — so the
// user's word goes as it is, and magpie's default doesn't override it.

import (
	"bytes"
	"encoding/json"

	"github.com/yetone/magpie/internal/provider"
)

// quietTo is whether pid/model is known not to think: magpie has a word on
// it, and that word says no reasoning. Like blindTo, it asks the served
// entry, so a provider kept unlisted still answers.
func quietTo(pid, model string) bool {
	e, ok := provider.ServedEntryOf(pid + "/" + model)
	return ok && e.Quiet()
}

// withoutReasoningAsk removes the fields that ask a model to think, in the
// agent's own protocol, keeping every other field: what a member known not
// to think is sent (#950). Chat's reasoning_effort and reasoning,
// Responses' reasoning (with reasoning_effort, which some relays take),
// Anthropic's thinking when it asks to think — thinking disabled is no
// ask and stays — and output_config's effort alone, the rest of
// output_config kept, and Gemini's generationConfig.thinkingConfig when it
// asks to think. The body goes as it is when it asked for nothing.
func withoutReasoningAsk(proto provider.Protocol, body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	changed := false
	drop := func(k string) {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	switch proto {
	case provider.Chat:
		drop("reasoning_effort")
		drop("reasoning")
	case provider.Responses:
		drop("reasoning")
		drop("reasoning_effort")
	case provider.Anthropic:
		if t, ok := m["thinking"].(map[string]any); ok {
			if s, _ := t["type"].(string); s == "enabled" || s == "adaptive" {
				drop("thinking")
			}
		}
		if oc, ok := m["output_config"].(map[string]any); ok {
			if _, ok := oc["effort"]; ok {
				delete(oc, "effort")
				changed = true
				if len(oc) == 0 {
					drop("output_config")
				}
			}
		}
	case provider.Gemini:
		// generationConfig's thinkingConfig when it asks to think (as
		// parseGemini reads it), the rest of generationConfig kept; a
		// budget of 0 is no ask and stays. The body was spelled in
		// camelCase on its way in (geminiCamel).
		if gc, ok := m["generationConfig"].(map[string]any); ok {
			if raw, err := json.Marshal(gc["thinkingConfig"]); err == nil {
				var tc gThinking
				if json.Unmarshal(raw, &tc) == nil && tc.effort() != "" {
					delete(gc, "thinkingConfig")
					changed = true
					if len(gc) == 0 {
						drop("generationConfig")
					}
				}
			}
		}
	default:
		return body
	}
	if !changed {
		return body
	}
	// the fields the agent sent, as it wrote them: <, > and & are not
	// escaped (withFields)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return body
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}
