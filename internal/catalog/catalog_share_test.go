package catalog

import (
	"testing"
	"unsafe"
)

// The loaded catalog holds what its models repeat once: two models with
// the same modalities, family, release date, levels and SDK share them.
func TestLoadedCatalogSharesRepeats(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {"models": {
	    "a": {"id":"a","name":"A","family":"gpt","release_date":"2026-01-01",
	          "modalities":{"input":["text","image"],"output":["text"]},
	          "reasoning_options":[{"type":"effort","values":["low","high"]}],
	          "provider":{"npm":"@ai-sdk/openai"}},
	    "b": {"id":"b","name":"B","family":"gpt","release_date":"2026-01-01",
	          "modalities":{"input":["text","image"],"output":["text"]},
	          "reasoning_options":[{"type":"effort","values":["low","high"]}],
	          "provider":{"npm":"@ai-sdk/openai"}}
	  }},
	  "other": {"models": {
	    "c": {"id":"c","name":"C","family":"gpt","modalities":{"input":["text"],"output":["text"]}}
	  }}
	}`)
	m, _ := loadGen()
	a, b, c := m["openai"].Models["a"], m["openai"].Models["b"], m["other"].Models["c"]
	same := func(x, y string) bool { return unsafe.StringData(x) == unsafe.StringData(y) }
	if !same(a.Family, b.Family) || !same(a.Family, c.Family) || !same(a.ReleaseDate, b.ReleaseDate) {
		t.Error("each model has its own copy of its family and release date")
	}
	if &a.Modalities.Input[0] != &b.Modalities.Input[0] || &a.Modalities.Output[0] != &b.Modalities.Output[0] {
		t.Error("each model has its own modalities list")
	}
	if !same(a.Modalities.Output[0], c.Modalities.Input[0]) {
		t.Error("each list has its own copy of a modality")
	}
	if &a.Reasoning[0].Values[0] != &b.Reasoning[0].Values[0] || !same(a.Reasoning[0].Type, b.Reasoning[0].Type) {
		t.Error("each model has its own levels")
	}
	if a.Provider != b.Provider {
		t.Error("each model has its own SDK")
	}
	// a shared list is clipped: an append copies it
	in := a.Modalities.Input
	if cap(in) != len(in) {
		t.Fatalf("shared list has room past its end: len %d cap %d", len(in), cap(in))
	}
	_ = append(in, "pdf")
	if len(b.Modalities.Input) != 2 {
		t.Error("an append to one model's list changed another's")
	}
	// what is read through it is as before
	if a.Name != "A" || b.Name != "B" || c.Modalities.Input[0] != "text" || a.Reasoning[0].Values[1] != "high" || a.Provider.NPM != "@ai-sdk/openai" {
		t.Errorf("values changed: %+v %+v %+v", a, b, c)
	}
}
