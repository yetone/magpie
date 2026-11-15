package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// antigravityListed is what Antigravity's fetchAvailableModels said for
// an account (2026-09), as magpie keeps it.
var antigravityListed = []catalog.Model{
	{ID: "claude-opus-4-6-thinking", Name: "Claude Opus 4.6 (Thinking)", Images: true},
	{ID: "gemini-3-flash", Name: "Gemini 3 Flash", Images: true},
	{ID: "gemini-3-flash-agent", Name: "Gemini 3.5 Flash (High)", Images: true},
	{ID: "gemini-3.1-pro-high", Name: "Gemini 3.1 Pro (High)", Images: true},
	{ID: "gemini-3.1-pro-low", Name: "Gemini 3.1 Pro (Low)", Images: true},
	{ID: "gemini-3.5-flash-extra-low", Name: "Gemini 3.5 Flash (Low)", Images: true},
	{ID: "gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite", Images: true},
	{ID: "gemini-3.5-flash-low", Name: "Gemini 3.5 Flash (Medium)", Images: true},
	{ID: "gemini-3.7-flash-high", Name: "Gemini 3.7 Flash (High)", Images: true},
	{ID: "gemini-3.7-flash-low", Name: "Gemini 3.7 Flash (Low)", Images: true},
	{ID: "gemini-3.7-flash-medium", Name: "Gemini 3.7 Flash (Medium)", Images: true},
	{ID: "gemini-3.7-flash-tiered", Images: true},
	{ID: "gemini-pro-agent", Name: "Gemini 3.1 Pro (High)", Images: true},
	{ID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
}

// Antigravity's ids of one model at several levels are one model with
// those levels; a model with one id, or only a name like another's,
// stays as Antigravity lists it.
func TestCollapseAntigravityModels(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	var got []string
	for _, m := range collapseAntigravityModels(antigravityListed) {
		got = append(got, m.ID+"|"+m.Name+"|"+strings.Join(m.Efforts, ","))
	}
	want := []string{
		"claude-opus-4-6-thinking|Claude Opus 4.6 (Thinking)|",
		"gemini-3-flash|Gemini 3 Flash|",
		"gemini-3-flash-agent|Gemini 3.5 Flash (High)|",
		"gemini-3.1-pro|Gemini 3.1 Pro|low,high",
		// the names say the levels: extra-low is Low, low is Medium
		"gemini-3.5-flash|Gemini 3.5 Flash|low,medium",
		"gemini-3.5-flash-lite|Gemini 3.5 Flash Lite|",
		"gemini-3.7-flash|Gemini 3.7 Flash|low,medium,high",
		// Antigravity names no -tiered id: it is named for its model, and
		// thinks at its model's levels (PapainTea on X)
		"gemini-3.7-flash-tiered|Gemini 3.7 Flash (Tiered)|low,medium,high",
		"gemini-pro-agent|Gemini 3.1 Pro (High)|",
		"gpt-oss-120b-medium|GPT-OSS 120B (Medium)|",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for model, want := range map[string]string{
		"gemini-3.7-flash": "=gemini-3.7-flash-high high=gemini-3.7-flash-high low=gemini-3.7-flash-low medium=gemini-3.7-flash-medium",
		"gemini-3.5-flash": "=gemini-3.5-flash-low low=gemini-3.5-flash-extra-low medium=gemini-3.5-flash-low",
		"gemini-3.1-pro":   "=gemini-3.1-pro-high high=gemini-3.1-pro-high low=gemini-3.1-pro-low",
	} {
		vs, ok := antigravityVariantsIn(antigravityListed, model)
		var got []string
		for _, l := range []string{"", "high", "low", "medium"} {
			if vs[l] != "" {
				got = append(got, l+"="+vs[l])
			}
		}
		if !ok || strings.Join(got, " ") != want {
			t.Errorf("%s: %v %v, want %s", model, got, ok, want)
		}
	}
	for _, id := range []string{"gemini-3-flash", "gpt-oss-120b-medium", "gemini-3.7-flash-high", "gemini-pro-agent"} {
		if _, ok := antigravityVariantsIn(antigravityListed, id); ok {
			t.Errorf("%s has variants", id)
		}
	}

	// with an id of no level, it is the default
	vs, _ := antigravityVariantsIn([]catalog.Model{{ID: "m-low"}, {ID: "m"}, {ID: "m-high"}}, "m")
	if vs[""] != "m" || vs["low"] != "m-low" {
		t.Errorf("variants %v", vs)
	}
	// without high, the highest there is
	vs, _ = antigravityVariantsIn([]catalog.Model{{ID: "m-low"}, {ID: "m-medium"}}, "m")
	if vs[""] != "m-medium" {
		t.Errorf("variants %v", vs)
	}
}

// Picks saved before the levels were one model name Antigravity's own
// ids: each is the model magpie offers for it now, once, where it was;
// the rest stay.
func TestAntigravityLegacyPicks(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := catalog.SaveLive("antigravity", "", antigravityListed); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"gemini-3.7-flash-high": "gemini-3.7-flash|high", "gemini-3.5-flash-extra-low": "gemini-3.5-flash|low",
		"gemini-3.5-flash-low": "gemini-3.5-flash|medium", "gemini-3.1-pro-low": "gemini-3.1-pro|low"} {
		if b, e, ok := AntigravityBase(id); !ok || b+"|"+e != want {
			t.Errorf("%s: %s|%s %v, want %s", id, b, e, ok, want)
		}
	}
	for _, id := range []string{"gemini-3.7-flash", "gpt-oss-120b-medium", "gemini-pro-agent", "gemini-3-flash-agent", "nope-low"} {
		if b, _, ok := AntigravityBase(id); ok {
			t.Errorf("%s taken for %s", id, b)
		}
	}
	legacy := []string{"gemini-3.7-flash-high", "gemini-3-flash", "gemini-3.7-flash-low", "gemini-pro-agent", "gemini-3.5-flash-low", "gone-model"}
	want := "gemini-3.7-flash,gemini-3-flash,gemini-pro-agent,gemini-3.5-flash,gone-model"
	if got := strings.Join(antigravityPicks(legacy), ","); got != want {
		t.Fatalf("picks %s, want %s", got, want)
	}

	// what the account lists is the families, with their levels
	p := Provider{ID: "antigravity"}
	var got []string
	for _, m := range p.Available() {
		if strings.HasPrefix(m.ID, "gemini-3.7") {
			got = append(got, m.ID+"|"+strings.Join(p.Efforts(m.ID), ","))
		}
	}
	if strings.Join(got, " ") != "gemini-3.7-flash|low,medium,high gemini-3.7-flash-tiered|low,medium,high" {
		t.Fatalf("available %v", got)
	}
}

// A model Antigravity leaves unnamed has a name all the same: a -tiered
// id its model's with "(Tiered)" (EZN7L2C3, #955: agy's list showed
// magpie/antigravity/gemini-3.6-flash-tiered), any other its id.
func TestAntigravityUnnamedModels(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	raw := []catalog.Model{
		{ID: "gemini-3.6-flash-high", Name: "Gemini 3.6 Flash (High)"},
		{ID: "gemini-3.6-flash-low", Name: "Gemini 3.6 Flash (Low)"},
		{ID: "gemini-3.6-flash-tiered"},
		{ID: "gemini-9-flash-tiered"},
		{ID: "mystery-model"},
	}
	var got []string
	for _, m := range collapseAntigravityModels(raw) {
		got = append(got, m.ID+"|"+m.Name+"|"+strings.Join(m.Efforts, ","))
	}
	// a -tiered id with no model of its own to take levels from has none
	want := "gemini-3.6-flash|Gemini 3.6 Flash|low,high gemini-3.6-flash-tiered|Gemini 3.6 Flash (Tiered)|low,high gemini-9-flash-tiered|gemini-9-flash-tiered| mystery-model|mystery-model|"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %s\nwant %s", strings.Join(got, " "), want)
	}
}
