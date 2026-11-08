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

// canonicalRenameCatalog is models.dev's api.json as fetched on 2026-10-11
// for the entries in the shape this is about: OpenAI lists its cyber model
// as gpt-daybreak-red-latest, canonically openai/gpt-5.6-cyber, the id the
// relays serve it under — and no catalog lists gpt-5.6-cyber by it (one of
// the canonical-only ids, as deepseek-v4.1-flash was before DeepSeek's own
// resellers listed it).
const canonicalRenameCatalog = `{
  "openai": {"name":"OpenAI","models": {
    "gpt-daybreak-red-latest": {"id":"gpt-daybreak-red-latest","name":"Daybreak Red","reasoning":true,
      "modalities":{"input":["text","image"],"output":["text"]},
      "cost":{"input":12.5,"output":75,"cache_read":1.25,"cache_write":15.625},
      "limit":{"context":400000,"input":272000,"output":128000},
      "canonical_model_id":"openai/gpt-5.6-cyber"}}}
}`

// A model models.dev publishes only as another entry's canonical_model_id
// is a model like a listed one, so a relay's rename of it renames too, and
// its price and a window mark go with the rename (#1520 on the id
// #1383's relays put their prefix in front of).
func TestRelayRenameReachesACanonicalOnlyID(t *testing.T) {
	writeCatalog(t, canonicalRenameCatalog)

	// the bare canonical id is priced by its maker's entry (#1520)
	if p, ok := PricedBy([]string{"openai"}, "gpt-5.6-cyber"); !ok || p.Input != 12.5 {
		t.Fatalf("PricedBy(gpt-5.6-cyber) = %+v, %v, want 12.5", p, ok)
	}
	for _, id := range []string{"claude-gpt-5.6-cyber", "gpt-5.6-cyber[1m]", "claude-gpt-5.6-cyber[1m]"} {
		if p, ok := PricedBy([]string{"openai"}, id); !ok || p.Input != 12.5 {
			t.Errorf("PricedBy(%q) = %+v, %v, want 12.5", id, p, ok)
		}
	}
	// and the window mark on the renamed id names the window, as a listed
	// model's does
	if got := ContextOf("claude-gpt-5.6-cyber[1m]"); got != 1_000_000 {
		t.Errorf("ContextOf(claude-gpt-5.6-cyber[1m]) = %d, want 1000000", got)
	}
	// a near miss of the canonical id is still no model
	for _, id := range []string{"claude-gpt-5.6-cyberx", "claude-gpt-5.6", "gpt-5.6-cyber[fast]"} {
		if p, ok := PricedBy([]string{"openai"}, id); ok {
			t.Errorf("%q priced %+v", id, p)
		}
	}
}

// The word an entry says of the model its canonical_model_id names is the
// model's, though no catalog lists the model by that id: Daybreak Red's
// entry is gpt-5.6-cyber, so its window, reply limit, thinking, images and
// name are gpt-5.6-cyber's — asked as given (WorkBuddy's shape, #1520) and
// behind a relay's prefix (#1383's).
func TestCanonicalOnlyIDCarriesItsMakerWord(t *testing.T) {
	writeCatalog(t, canonicalRenameCatalog)

	for _, id := range []string{"gpt-5.6-cyber", "claude-gpt-5.6-cyber"} {
		if got := ContextOf(id); got != 272_000 {
			t.Errorf("ContextOf(%q) = %d, want 272000", id, got)
		}
		if got := OutputOf(id); got != 128_000 {
			t.Errorf("OutputOf(%q) = %d, want 128000", id, got)
		}
		if !Thinks(id) {
			t.Errorf("Thinks(%q) = false, want true", id)
		}
		if !SeesImages(id) {
			t.Errorf("SeesImages(%q) = false, want true", id)
		}
		if got := NameOf(id); got != "Daybreak Red" {
			t.Errorf("NameOf(%q) = %q, want Daybreak Red", id, got)
		}
	}
	// a listed model's answers are as they were: the daybreak entry's own
	// id keeps its own window and name
	if got := ContextOf("gpt-daybreak-red-latest"); got != 272_000 {
		t.Errorf("ContextOf(gpt-daybreak-red-latest) = %d, want 272000", got)
	}
	if got := NameOf("gpt-daybreak-red-latest"); got != "Daybreak Red" {
		t.Errorf("NameOf(gpt-daybreak-red-latest) = %q, want Daybreak Red", got)
	}
}

// canonicalPrefixCatalog is models.dev's api.json as fetched on 2026-10-11
// for the entries in the shape the guard is about: Cortecs lists Claude's
// Sonnet 4 as claude-sonnet-4, canonically anthropic/claude-sonnet-4-0 —
// the canonical id itself starts with the prefix a relay puts in front —
// and no catalog lists claude-sonnet-4-0 by it (one of the canonical-only
// ids). A catalog naming a sonnet-4-0 of its own, here Drift's, must not
// take Claude's model for it.
const canonicalPrefixCatalog = `{
  "cortecs": {"name":"Cortecs","models": {
    "claude-sonnet-4": {"id":"claude-sonnet-4","name":"Claude Sonnet 4 (latest)",
      "modalities":{"input":["text","image","pdf"],"output":["text"]},
      "cost":{"input":2.989,"output":14.945,"cache_read":0.296,"cache_write":3.707},
      "limit":{"context":200000,"output":65000},
      "canonical_model_id":"anthropic/claude-sonnet-4-0"}}},
  "drift": {"name":"Drift","models": {
    "sonnet-4-0": {"id":"sonnet-4-0","name":"Drift Sonnet",
      "modalities":{"input":["text"],"output":["text"]},
      "cost":{"input":99,"output":99},
      "limit":{"context":32000,"output":4000}}}}
}`

// A canonical id is a model as a listed one is, so it is never taken for
// another: claude-sonnet-4-0 starts with a relay's prefix, and what is left
// names a version, but the id is its own model, not Drift's sonnet-4-0.
func TestCanonicalIDIsNeverTakenForAnother(t *testing.T) {
	writeCatalog(t, canonicalPrefixCatalog)

	if p, ok := PricedBy([]string{"drift"}, "claude-sonnet-4-0"); ok {
		t.Errorf("PricedBy(drift, claude-sonnet-4-0) = %+v, want none", p)
	}
	// what it carries is Claude's, as Cortecs's entry says of it, never
	// Drift's
	if got := ContextOf("claude-sonnet-4-0"); got != 200_000 {
		t.Errorf("ContextOf(claude-sonnet-4-0) = %d, want 200000", got)
	}
	if got := NameOf("claude-sonnet-4-0"); got != "Claude Sonnet 4 (latest)" {
		t.Errorf("NameOf(claude-sonnet-4-0) = %q, want Claude Sonnet 4 (latest)", got)
	}
	// the model the id is Drift's for keeps its own price, and a listed id
	// in front of the prefix is priced by its own entry
	if p, ok := PricedBy([]string{"drift"}, "sonnet-4-0"); !ok || p.Input != 99 {
		t.Errorf("PricedBy(drift, sonnet-4-0) = %+v, %v, want 99", p, ok)
	}
	if p, ok := PricedBy([]string{"cortecs"}, "claude-sonnet-4"); !ok || p.Input != 2.989 {
		t.Errorf("PricedBy(cortecs, claude-sonnet-4) = %+v, %v, want 2.989", p, ok)
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
		"anthropic2-deepseek-v4.1-flash", "claude-claude-opus-5x",
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
	// a router's id is no one model: another vendor's "auto" takes none of
	// model-oracle-ai's, whose own list still has its levels
	if e, c, i := EffortsOf("auto"), ContextOf("auto"), SeesImages("auto"); e != nil || c != 0 || i || Thinks("auto") {
		t.Errorf("auto took a router's: efforts %v, context %d, images %v", e, c, i)
	}
	if got, _ := ListedBy([]string{"model-oracle-ai"}, "auto"); !slices.Contains(got, "xhigh") {
		t.Errorf("ListedBy(model-oracle-ai, auto) = %v", got)
	}
	if got := strings.Join(EffortsOf("opus-5"), ","); got != "minimal" {
		t.Errorf("EffortsOf(opus-5) = %q", got)
	}
	if got := ContextOf("glm-5"); got != 200000 {
		t.Errorf("ContextOf(glm-5) = %d", got)
	}
}

// #2157 (sstdgm): a relay names each channel's models with its own word in
// front — nyro-gpt-5.5, nyro-gemini-3.8-flash, nyro-deepseek-v4.1-flash —
// and models.dev knew none of them, so every one was taken for a
// text-only model with no window and no levels, and Codex refused every
// image. The model after the word is the same model.
func TestChannelWordBeforeAModelIsTheModel(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning":true,
	    "reasoning_options":[{"type":"effort","values":["low","medium","high"]}],
	    "modalities":{"input":["text","image"],"output":["text"]},
	    "limit":{"context":400000,"output":128000}}}},
	  "deepseek": {"models": {"deepseek-v4.1-flash": {"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash",
	    "modalities":{"input":["text"],"output":["text"]},
	    "limit":{"context":1000000,"output":65536}},
	    "auto": {"id":"auto","name":"Router","limit":{"context":8000,"output":800}}}},
	  "elsewhere": {"models": {"chan-gpt-5.5": {"id":"chan-gpt-5.5","name":"Its Own",
	    "modalities":{"input":["text"],"output":["text"]},
	    "limit":{"context":32000,"output":4000}}}}}`)

	if !SeesImages("nyro-gpt-5.5") || ContextOf("nyro-gpt-5.5") != 400000 || strings.Join(EffortsOf("nyro-gpt-5.5"), ",") != "low,medium,high" {
		t.Errorf("nyro-gpt-5.5: images %v, window %d, levels %v", SeesImages("nyro-gpt-5.5"), ContextOf("nyro-gpt-5.5"), EffortsOf("nyro-gpt-5.5"))
	}
	if SeesImages("nyro-deepseek-v4.1-flash") || ContextOf("nyro-deepseek-v4.1-flash") != 1000000 {
		t.Errorf("nyro-deepseek-v4.1-flash: images %v (a text-only model), window %d", SeesImages("nyro-deepseek-v4.1-flash"), ContextOf("nyro-deepseek-v4.1-flash"))
	}
	// the name a model is shown by keeps the channel's word, so a relay's
	// nyro-gpt-5.5 and its gpt-5.5 read apart
	if got := NameOf("nyro-gpt-5.5"); got != "" {
		t.Errorf("NameOf(nyro-gpt-5.5) = %q, want its id kept", got)
	}
	// a listed id is its own model, an unversioned rest is no model, and
	// a word with a digit in it is not a channel's
	if ContextOf("chan-gpt-5.5") != 32000 || SeesImages("chan-gpt-5.5") {
		t.Errorf("chan-gpt-5.5 lost its own entry")
	}
	for _, id := range []string{"nyro-auto", "ny2-gpt-5.5", "nyro-gpt-5.5x"} {
		if ContextOf(id) != 0 || SeesImages(id) {
			t.Errorf("%s was taken for a model", id)
		}
	}
}
