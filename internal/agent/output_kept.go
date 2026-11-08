package agent

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// A reply limit typed by hand into an agent's file stays (sayagoodbyeplz
// on Discord: WorkBuddy's deepseek-v4.1-flash replies 128000 tokens, not
// the 384000 models.dev says, and Pi's maxTokens set to 128000 was written
// back to 384000 on the next sync). magpie writes its models whole, so a
// value in the file is told apart by what magpie has wanted there: the
// stash keeps, per file and model, the reply limits magpie handed it
// (outputsWantedKey), and one in the file that is none of them is the
// user's. It stays until the user sets the model's Max output in magpie
// (the editor's Model settings, or `magpie model output`), which is their
// word on it there and is written over it, as is magpie's when no record
// says what magpie wanted (a model new to the file, a stash that can't be
// read: unknown is magpie's, as before).
//
// The record is of every value wanted, not only the last written: an
// agent's block is also built to be compared (Drift) and never written,
// and an old value of magpie's left in the file is still magpie's.

// outputsWantedKey is where the stash keeps the reply limits magpie has
// wanted in the file at path, as JSON: {model: [n, …]}, newest last.
func outputsWantedKey(path string) string { return "output:" + path + ":wanted" }

// outputsRemembered is how many values a model's record keeps.
const outputsRemembered = 8

// outputsKept is want, the reply limit magpie hands each model of the file
// at path, with have's, the file's own now, kept where it is the user's.
// It records want's values as wanted.
func outputsKept(path string, want, have map[string]int) map[string]int {
	key := outputsWantedKey(path)
	rec := map[string][]int{}
	if v := stashShared()[key]; v != "" && json.Unmarshal([]byte(v), &rec) != nil {
		rec = map[string][]int{}
	}
	out := make(map[string]int, len(want))
	next := make(map[string][]int, len(want))
	var s *settings.Settings
	for id, w := range want {
		out[id] = w
		was, known := rec[id]
		if f := have[id]; known && f > 0 && f != w && !slices.Contains(was, f) {
			if s == nil {
				l := settings.Load()
				s = &l
			}
			if !outputSetIn(*s, id) {
				out[id] = f
			}
		}
		if w <= 0 || slices.Contains(was, w) {
			next[id] = was
			continue
		}
		was = append(slices.Clone(was), w)
		if len(was) > outputsRemembered {
			was = was[len(was)-outputsRemembered:]
		}
		next[id] = was
	}
	for id, v := range next {
		if len(v) == 0 {
			delete(next, id)
		}
	}
	if !maps.EqualFunc(rec, next, slices.Equal) {
		b, _ := json.Marshal(next)
		if len(next) == 0 {
			b = nil
		}
		stash(map[string]string{key: string(b)})
	}
	return out
}

// outputSetIn says the user set a Max output in magpie for the model an
// agent lists as id, "provider/model": its own or its provider's.
func outputSetIn(s settings.Settings, id string) bool {
	prov, _, ok := strings.Cut(id, "/")
	return ok && (s.ModelOutputs[id] > 0 || s.ModelOutputs[prov+"/*"] > 0)
}

// numberOf is a JSON number as an int, 0 for anything else.
func numberOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// piOutputsKept keeps a maxTokens the user typed into magpie's models in
// Pi's models.json at path (outputsKept); v is the block magpie writes.
func piOutputsKept(path string, v any) {
	block, _ := v.(map[string]any)
	ms, _ := block["models"].([]map[string]any)
	want := map[string]int{}
	for _, e := range ms {
		if id, _ := e["id"].(string); id != "" {
			if n := numberOf(e["maxTokens"]); n > 0 {
				want[id] = n
			}
		}
	}
	if len(want) == 0 {
		return
	}
	have := map[string]int{}
	if raw, ok := edit.GetJSON(path, "providers."+magpieID+".models"); ok {
		var cur []struct {
			ID        string `json:"id"`
			MaxTokens int    `json:"maxTokens"`
		}
		if json.Unmarshal([]byte(raw), &cur) == nil {
			for _, c := range cur {
				have[c.ID] = c.MaxTokens
			}
		}
	}
	kept := outputsKept(path, want, have)
	for _, e := range ms {
		if id, _ := e["id"].(string); kept[id] > 0 {
			e["maxTokens"] = kept[id]
		}
	}
}

// openCodeOutputsKept keeps a limit.output the user typed into magpie's
// models in an OpenCode config at path (outputsKept); ms are the models
// magpie writes.
func openCodeOutputsKept(path string, ms orderedJSON) {
	want := map[string]int{}
	for _, p := range ms {
		e, _ := p.v.(map[string]any)
		l, _ := e["limit"].(map[string]any)
		if n := numberOf(l["output"]); n > 0 {
			want[p.k] = n
		}
	}
	if len(want) == 0 {
		return
	}
	have := map[string]int{}
	if raw, ok := edit.GetJSON(path, "provider."+magpieID+".models"); ok {
		var cur map[string]struct {
			Limit struct {
				Output int `json:"output"`
			} `json:"limit"`
		}
		if json.Unmarshal([]byte(raw), &cur) == nil {
			for id, c := range cur {
				have[id] = c.Limit.Output
			}
		}
	}
	kept := outputsKept(path, want, have)
	for _, p := range ms {
		e, _ := p.v.(map[string]any)
		if l, _ := e["limit"].(map[string]any); l != nil && kept[p.k] > 0 {
			l["output"] = kept[p.k]
		}
	}
}
