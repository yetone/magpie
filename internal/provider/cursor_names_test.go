package provider

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// The context Cursor puts in a model's name is its Context, shown apart:
// the name goes without it, unless it is what tells two models apart.
func TestCursorNamesWithoutCapacity(t *testing.T) {
	in := []catalog.Model{
		{ID: "claude-opus-5-5", Name: "Claude Opus 5.5 1M", Context: 1_000_000},
		{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol (1M) Thinking", Context: 1_000_000},
		{ID: "gpt-5.5", Name: "GPT-5.5", Context: 200_000},
		{ID: "gpt-5.5-1m", Name: "GPT-5.5 1M", Context: 1_000_000},
		{ID: "grok-a", Name: "Grok 1M", Context: 1_000_000},
		{ID: "grok-b", Name: "Grok 2M", Context: 2_000_000},
		{ID: "composer-2.5", Name: "Composer 2.5", Context: 200_000},
		{ID: "only", Name: "1M", Context: 1_000_000},
	}
	var got []string
	for _, m := range withoutCursorCapacity(in) {
		got = append(got, m.ID+"|"+m.Name)
	}
	want := []string{
		"claude-opus-5-5|Claude Opus 5.5",
		"gpt-5.6-sol|GPT-5.6 Sol Thinking",
		"gpt-5.5|GPT-5.5",
		"gpt-5.5-1m|GPT-5.5 1M", // beside a GPT-5.5, the 1M is what tells it apart
		"grok-a|Grok 1M",        // both would be Grok
		"grok-b|Grok 2M",
		"composer-2.5|Composer 2.5",
		"only|1M", // nothing would be left
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if in[0].Name != "Claude Opus 5.5 1M" {
		t.Fatalf("the list given was changed: %q", in[0].Name)
	}
}

// Through Available and the catalog: names go without the 1M, ids,
// efforts, contexts and the ids each effort asks Cursor for stay, and a
// name the user gave a model is still its name.
func TestCursorAvailableNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	was := cursorStatus
	defer func() { cursorStatus = was }()
	cursorStatus = &cliIdentity{name: "cursor-test", exe: func() string { return "/bin/sh" }, ask: func() (string, string, bool, error) { return "me@example.com", "Pro", true, nil }}
	raw := withCursorContexts(parseCursorModels(cursorListed))
	if err := catalog.SaveLive("cursor", "", raw); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "cursor", Models: []string{"claude-opus-4-7", "gpt-5.5", "gpt-5.5-fast", "claude-4.6-sonnet-medium-thinking"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("cursor")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range p.Available() {
		got = append(got, m.ID+"|"+m.Name+"|"+strings.Join(m.Efforts, ","))
	}
	for _, w := range []string{
		"claude-opus-4-7|Claude Opus 4.7|low,xhigh,max",
		"claude-opus-4-7-thinking|Claude Opus 4.7 Thinking|low,xhigh",
		"gpt-5.5|GPT-5.5|none,medium,xhigh",
		"gpt-5.5-fast|GPT-5.5 Fast|medium,xhigh",
		"claude-4.6-sonnet-medium-thinking|Claude Sonnet 4.6 Thinking|",
	} {
		if !strings.Contains(strings.Join(got, "\n"), w) {
			t.Errorf("no %s in\n%s", w, strings.Join(got, "\n"))
		}
	}
	if vs, ok := cursorVariantsIn(raw, "claude-opus-4-7"); !ok || vs[""] != "claude-opus-4-7-xhigh" {
		t.Errorf("claude-opus-4-7 asks for %v", vs)
	}
	if err := SetModelName("cursor/claude-opus-4-7", "Opus Big"); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Entry{}
	for _, e := range Catalog() {
		byID[e.ID] = e
	}
	if e := byID["cursor/claude-opus-4-7"]; e.Name != "Opus Big" || e.Default != "Claude Opus 4.7" || e.Model != "claude-opus-4-7" || e.Context != 1_000_000 {
		t.Errorf("named: %+v", e)
	}
	if e := byID["cursor/gpt-5.5"]; e.Name != "GPT-5.5" || e.Default != "" || e.Context != 1_000_000 {
		t.Errorf("gpt-5.5: %+v", e)
	}
}
