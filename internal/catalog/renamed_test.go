package catalog

import (
	"slices"
	"strings"
	"testing"
)

// The reporter's own test (#1383, uWydnA): a relay renames
// deepseek-v4.1-flash claude-deepseek-v4.1-flash[1M], and the renamed
// model had no levels, no window and no images.
func TestRelayRenamedIDLosesCapabilities(t *testing.T) {
	writeCatalog(t, `{
	  "deepseek": {"models": {"deepseek-v4.1-flash": {"id":"deepseek-v4.1-flash",
	    "reasoning_options":[{"type":"effort","values":["low","high","max"]}],
	    "modalities":{"input":["text","image"],"output":["text"]},
	    "limit":{"context":1000000,"output":65536}}}}}`)

	for _, id := range []string{"deepseek-v4.1-flash", "claude-deepseek-v4.1-flash[1M]"} {
		t.Run(id, func(t *testing.T) {
			if got := strings.Join(EffortsOf(id), ","); got != "low,high,max" {
				t.Errorf("EffortsOf(%q) = %q, want low,high,max", id, got)
			}
			if got := ContextOf(id); got != 1000000 {
				t.Errorf("ContextOf(%q) = %d, want 1000000", id, got)
			}
			if !SeesImages(id) {
				t.Errorf("SeesImages(%q) = false, want true", id)
			}
		})
	}
}

const renamedCatalog = `{
  "deepseek": {"name":"DeepSeek","models": {
    "deepseek-v4.1-flash": {"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash",
      "reasoning_options":[{"type":"effort","values":["low","high","max"]}],
      "modalities":{"input":["text","image"],"output":["text"]},
      "cost":{"input":0.3,"output":1.2},
      "limit":{"context":1000000,"output":65536}}}},
  "anthropic": {"name":"Anthropic","models": {
    "claude-opus-5": {"id":"claude-opus-5","name":"Claude Opus 5","reasoning":true,
      "reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
      "modalities":{"input":["text","image"],"output":["text"]},
      "cost":{"input":5,"output":25},
      "limit":{"context":200000,"output":64000}}}},
  "elsewhere": {"name":"Elsewhere","models": {
    "opus-5": {"id":"opus-5","name":"Not Claude",
      "reasoning_options":[{"type":"effort","values":["minimal"]}],
      "limit":{"context":32000,"output":4000}},
    "glm-5": {"id":"glm-5","name":"GLM-5","reasoning":true,
      "reasoning_options":[{"type":"effort","values":["low","high"]}],
      "modalities":{"input":["text","image"],"output":["text"]},
      "limit":{"context":200000,"output":8000}},
    "claude-glm-5": {"id":"claude-glm-5","name":"Claude GLM 5",
      "modalities":{"input":["text"],"output":["text"]},
      "limit":{"context":64000,"output":4000}}}},
  "model-oracle-ai": {"name":"Oracle","models": {
    "model-oracle-ai/auto": {"id":"model-oracle-ai/auto","name":"Auto Router",
      "reasoning_options":[{"type":"effort","values":["none","minimal","low","medium","high","xhigh","max"]}],
      "modalities":{"input":["text","image"],"output":["text"]},
      "limit":{"context":2000000,"output":100000}}}},
  "openai": {"name":"OpenAI","models": {
    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5",
      "reasoning_options":[{"type":"effort","values":["low","medium","high","xhigh"]}],
      "modalities":{"input":["text","image"],"output":["text"]},
      "cost":{"input":1.25,"output":10},
      "limit":{"context":400000,"output":128000}}}}
}`

// A relay's rename is matched by every getter, each of its forms, and a
// window mark names the window the relay serves it at.
func TestRelayRenameReachesEveryGetter(t *testing.T) {
	writeCatalog(t, renamedCatalog)

	for id, window := range map[string]int{
		"claude-deepseek-v4.1-flash[1M]":   1_000_000,
		"claude-deepseek-v4.1-flash[200K]": 200_000,
		"claude-deepseek-v4.1-flash":       1_000_000,
		"deepseek-v4.1-flash[1M]":          1_000_000,
		"relay/claude-DeepSeek-V4.1-Flash": 1_000_000,
	} {
		t.Run(id, func(t *testing.T) {
			if got := strings.Join(EffortsOf(id), ","); got != "low,high,max" {
				t.Errorf("EffortsOf = %q", got)
			}
			if got := ContextOf(id); got != window {
				t.Errorf("ContextOf = %d, want %d", got, window)
			}
			if got := OutputOf(id); got != 65536 {
				t.Errorf("OutputOf = %d", got)
			}
			if !SeesImages(id) || !Thinks(id) || !Knows(id) {
				t.Errorf("SeesImages %v, Thinks %v, Knows %v", SeesImages(id), Thinks(id), Knows(id))
			}
			if p, ok := PricedBy([]string{"deepseek"}, id); !ok || p.Input != 0.3 {
				t.Errorf("PricedBy = %+v, %v", p, ok)
			}
			if e, ok := ListedBy([]string{"deepseek"}, id); !ok || strings.Join(e, ",") != "low,high,max" {
				t.Errorf("ListedBy = %v, %v", e, ok)
			}
		})
	}
	// the name keeps the mark, so two of a relay's models apart by their
	// window alone read apart
	if got := NameOf("claude-deepseek-v4.1-flash[1M]"); got != "DeepSeek V4.1 Flash [1M]" {
		t.Errorf("NameOf marked = %q", got)
	}
	if got := NameOf("claude-deepseek-v4.1-flash"); got != "DeepSeek V4.1 Flash" {
		t.Errorf("NameOf = %q", got)
	}
}

// Claude Code's own [1m] on a Claude model is that model at 1M: the mark
// comes off, the "claude-" stays (claude-opus-5[1m] is not "opus-5").
func TestClaude1MMarkIsTheClaudeModel(t *testing.T) {
	writeCatalog(t, renamedCatalog)

	if got := strings.Join(EffortsOf("claude-opus-5[1m]"), ","); got != "low,medium,high" {
		t.Errorf("EffortsOf = %q", got)
	}
	if got := ContextOf("claude-opus-5[1m]"); got != 1_000_000 {
		t.Errorf("ContextOf [1m] = %d", got)
	}
	if got := ContextOf("claude-opus-5"); got != 200_000 {
		t.Errorf("ContextOf = %d", got)
	}
	if got := OutputOf("claude-opus-5[1m]"); got != 64000 {
		t.Errorf("OutputOf = %d", got)
	}
	if p, ok := PricedBy([]string{"anthropic"}, "claude-opus-5[1m]"); !ok || p.Input != 5 {
		t.Errorf("PricedBy = %+v, %v", p, ok)
	}
	if got := NameOf("claude-opus-5[1m]"); got != "Claude Opus 5 [1m]" {
		t.Errorf("NameOf = %q", got)
	}
}

// A listed id is always its own model, and a near miss is no model.
func TestRenameNeverTakesAModelForAnother(t *testing.T) {
	writeCatalog(t, renamedCatalog)

	// claude-glm-5 is listed: no levels, no images, its own window, though
	// glm-5 has them
	if got := EffortsOf("claude-glm-5"); got != nil {
		t.Errorf("EffortsOf(claude-glm-5) = %v", got)
	}
	if SeesImages("claude-glm-5") || Thinks("claude-glm-5") {
		t.Error("claude-glm-5 took glm-5's images or reasoning")
	}
	if got := ContextOf("claude-glm-5"); got != 64000 {
		t.Errorf("ContextOf(claude-glm-5) = %d", got)
	}
	if got := NameOf("claude-glm-5"); got != "Claude GLM 5" {
		t.Errorf("NameOf(claude-glm-5) = %q", got)
	}
	// the relay's own router is not models.dev's "auto" (the reporter's
	// counter-example), and ids that only look like a known one match none
	for _, id := range []string{
		"claude-auto[1M]", "claude-auto",
		"claude-deepseek-v4.1-flashy[1M]", "claude-deepseek-v4.1",
		"deepseek-v4.1-flash[fast]", "deepseek-v4.1-flash[1M]x", "deepseek-v4.1-flash[infm]",
		"anthropic-deepseek-v4.1-flash", "claude-claude-opus-5x",
		"[1m]", "claude-[1m]",
	} {
		if e, c, i, n := EffortsOf(id), ContextOf(id), SeesImages(id), NameOf(id); e != nil || c != 0 || i || n != "" || Knows(id) {
			t.Errorf("%q matched: efforts %v, context %d, images %v, name %q", id, e, c, i, n)
		}
		if p, ok := PricedBy([]string{"deepseek", "anthropic", "model-oracle-ai"}, id); ok {
			t.Errorf("%q priced %+v", id, p)
		}
	}
	// other vendors' known models are as they were
	if got := strings.Join(EffortsOf("gpt-5.5"), ","); got != "low,medium,high,xhigh" {
		t.Errorf("EffortsOf(gpt-5.5) = %q", got)
	}
	if ContextOf("openai/gpt-5.5") != 400000 || OutputOf("gpt-5.5") != 128000 || !SeesImages("gpt-5.5") {
		t.Error("gpt-5.5 changed")
	}
	if got := EffortsOf("auto"); !slices.Contains(got, "xhigh") {
		t.Errorf("EffortsOf(auto) = %v", got)
	}
	if got := strings.Join(EffortsOf("opus-5"), ","); got != "minimal" {
		t.Errorf("EffortsOf(opus-5) = %q", got)
	}
	if got := ContextOf("glm-5"); got != 200000 {
		t.Errorf("ContextOf(glm-5) = %d", got)
	}
}
