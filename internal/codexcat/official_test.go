package codexcat

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// codexDir holds the files of openai/codex rust-v0.166.0 that say what Codex
// sends as a model's instructions, as they are in that release:
// models-manager/models.json (its bundled catalog) and
// models-manager/prompt.md (its prompt for a model it doesn't know).
const codexDir = "testdata/codex-rust-v0.166.0/"

// codexOwn is each bundled model's instructions as Codex reads them from
// its models.json: model_messages.instructions_template, else the legacy
// base_instructions (codex-rs protocol openai_models.rs,
// deserialize_model_infos_with_legacy_base).
func codexOwn(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(codexDir + "models.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Models []struct {
			Slug     string `json:"slug"`
			Base     string `json:"base_instructions"`
			Messages struct {
				Template string `json:"instructions_template"`
			} `json:"model_messages"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range file.Models {
		if m.Messages.Template != "" {
			out[m.Slug] = m.Messages.Template
		} else if m.Base != "" {
			out[m.Slug] = m.Base
		}
	}
	if len(out) == 0 {
		t.Fatal("no instructions in Codex's models.json")
	}
	return out
}

// The embedded copies are Codex's own text, word for word: Prompt is
// prompt.md, and bundled is every model's instructions in models.json.
// When Codex is updated, copy its two files into testdata and rebuild
// codex_instructions.json from models.json.
func TestBundledIsCodexOwn(t *testing.T) {
	p, err := os.ReadFile(codexDir + "prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	if Prompt != string(p) {
		t.Error("Prompt is not Codex's prompt.md")
	}
	if want := codexOwn(t); !reflect.DeepEqual(bundled(), want) {
		t.Errorf("bundled has %d models' instructions, Codex's models.json %d, or a text differs", len(bundled()), len(want))
	}
}

func entryBySlug(t *testing.T, ms []catalog.Model) map[string]map[string]any {
	t.Helper()
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(Catalog(ms), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, e := range got.Models {
		out[e["slug"].(string)] = e
	}
	return out
}

// A magpie model serving one of OpenAI's (#3759: mycpa/gpt-6-astra; #4008:
// the auto group's auto/gpt-6.1-sol) carries the instructions official
// Codex sends that model, matched as Codex matches a slug; any other model
// carries those Codex sends a model it doesn't know (#4783), and a routing
// group, which may hand a turn to any model, does too. With no ChatGPT
// sign-in they come from Codex's bundled catalog.
func TestThirdPartyModelsTakeCodexOwnInstructions(t *testing.T) {
	v1Home(t, `{"models":[]}`)
	own := codexOwn(t)
	unknown, _ := os.ReadFile(codexDir + "prompt.md")
	got := entryBySlug(t, []catalog.Model{
		{ID: "mycpa/gpt-6-astra", Name: "GPT-6-Astra"},
		{ID: "auto/gpt-6.1-sol", Name: "GPT-6.1-Sol"},
		{ID: "s2a/openai/gpt-6-luna-2026-09-14", Name: "GPT-6-Luna"},
		{ID: "relay/GPT-5.5", Name: "GPT-5.5"},
		{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
		{ID: "anthropic/claude-opus-5", Name: "Claude Opus 5"},
		{ID: "group/gpt-6.1-sol", Name: "a group"},
	})
	for slug, want := range map[string]string{
		"mycpa/gpt-6-astra":                own["gpt-6-astra"],
		"auto/gpt-6.1-sol":                 own["gpt-6.1-sol"],
		"s2a/openai/gpt-6-luna-2026-09-14": own["gpt-6-luna"],
		"relay/GPT-5.5":                    own["gpt-5.5"],
		"deepseek/deepseek-v4-pro":         string(unknown),
		"anthropic/claude-opus-5":          string(unknown),
		"group/gpt-6.1-sol":                string(unknown),
	} {
		e := got[slug]
		if e == nil {
			t.Fatalf("%s: no entry", slug)
		}
		if e["base_instructions"] != want {
			s, _ := e["base_instructions"].(string)
			t.Errorf("%s: base_instructions are not Codex's own for it (starts %.60q)", slug, s)
		}
		if _, ok := e["model_messages"]; ok {
			t.Errorf("%s: model_messages from the bundled catalog, which an older Codex may not read", slug)
		}
	}
}

// With a ChatGPT sign-in, the entry Codex itself was handed for the model
// (models_cache.json, this Codex version's) gives its instructions and its
// model_messages, both as sent; a model the account doesn't list still
// takes the bundled catalog's.
func TestThirdPartyModelsTakeCachedEntry(t *testing.T) {
	msgs := map[string]any{"instructions_template": "sol from the account", "persistent_instructions": "keep going", "approvals": map[string]any{"on_request": nil}}
	m, _ := json.Marshal(msgs)
	v1Home(t, `{"etag":"W/\"a\"","models":[{"slug":"gpt-6.1-sol","base_instructions":"sol from the account","model_messages":`+string(m)+`}]}`)
	got := entryBySlug(t, []catalog.Model{
		{ID: "mycpa/gpt-6.1-sol", Name: "GPT-6.1-Sol"},
		{ID: "mycpa/gpt-6-astra", Name: "GPT-6-Astra"},
	})
	sol := got["mycpa/gpt-6.1-sol"]
	if sol["base_instructions"] != "sol from the account" || !reflect.DeepEqual(sol["model_messages"], msgs) {
		t.Errorf("cached entry not taken as sent: %v %v", sol["base_instructions"], sol["model_messages"])
	}
	astra := got["mycpa/gpt-6-astra"]
	if astra["base_instructions"] != codexOwn(t)["gpt-6-astra"] {
		t.Error("a model the account doesn't list lost the bundled catalog's instructions")
	}
}

// An entry Codex was handed with model_messages alone keeps its own
// instructions; one with none takes those official Codex sends its model.
func TestOwnEntryInstructions(t *testing.T) {
	e := ownEntry(map[string]any{"slug": "gpt-6-astra", "model_messages": map[string]any{"instructions_template": "theirs"}}, "codex/gpt-6-astra", "x", 1)
	if e["base_instructions"] != "theirs" {
		t.Errorf("model_messages' instructions: %v", e["base_instructions"])
	}
	e = ownEntry(map[string]any{"slug": "gpt-6-astra"}, "codex/gpt-6-astra", "x", 1)
	if e["base_instructions"] != codexOwn(t)["gpt-6-astra"] {
		t.Error("an entry with none didn't take Codex's own for its model")
	}
}
