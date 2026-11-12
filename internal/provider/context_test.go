package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestContextSetByUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// set, not empty: a group saved lists what Devin's CLI reports, and
	// Devin given an empty one writes its config into the working directory
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	p := Provider{ID: "a", Contexts: map[string]int{"*": 272000, "big": 1000000}}
	if got := p.ContextOf("big"); got != 1000000 {
		t.Errorf("big: %d", got)
	}
	if got := p.ContextOf("other"); got != 272000 {
		t.Errorf("other: %d", got)
	}
	if got := (Provider{ID: "b"}).ContextOf("x"); got != 0 {
		t.Errorf("unset: %d", got)
	}

	e := func(p, m string, ctx int) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: &Provider{ID: p}, Context: ctx}
	}
	entries := []Entry{e("a", "m1", 128000), e("b", "m2", 200000)}
	of := func() int {
		for _, g := range groupEntries(entries) {
			if g.ID == GroupPrefix+"g" {
				return g.Context
			}
		}
		t.Fatal("no group g")
		return 0
	}
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 200000 {
		t.Errorf("largest member's: %d", got)
	}
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, Context: 400000}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 400000 {
		t.Errorf("set: %d", got)
	}
	// one set below the members' still wins
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, Context: 100000}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 100000 {
		t.Errorf("set lower: %d", got)
	}
	// Mikan on Discord: told the smallest member's, kept as smallest when
	// the members change
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"a/m1", "b/m2"}, Context: ContextSmallest}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 128000 {
		t.Errorf("smallest member's: %d", got)
	}
	if err := SaveGroup(Group{ID: "g", Name: "g", Members: []string{"b/m2"}, Context: -7}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 200000 {
		t.Errorf("smallest after a member left: %d", got)
	}
}

// #712: a group of DeepSeek V4.1 Flash (1M) at WorkBuddy and OpenCode Go,
// with OpenCode Zen's free DeepSeek V4 Flash (200k, models.dev) and a
// model whose window nothing says among them, is told the largest window:
// a conversation too long for the free one goes on to a member with room
// (#700), so Pi was made to compact at 200k for nothing.
func TestGroupToldItsLargestWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	e := func(p, m string, ctx int) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: m, Provider: &Provider{ID: p}, Context: ctx}
	}
	entries := []Entry{
		e("workbuddy-ai", "deepseek-v4.1-flash", 1000000),
		e("opencode-go", "deepseek-v4.1-flash", 1000000),
		e("opencode-zen", "deepseek-v4-flash-free", 200000),
		e("opencode-go", "muse-spark-1.3-contributor", 1048576),
		e("zcode", "GLM-5.3-Flash", 0),
	}
	members := []string{"workbuddy-ai/deepseek-v4.1-flash:max", "opencode-go/deepseek-v4.1-flash:high",
		"opencode-zen/deepseek-v4-flash-free:high", "opencode-go/muse-spark-1.3-contributor:xhigh", "zcode/GLM-5.3-Flash"}
	if err := SaveGroup(Group{ID: "deepseek", Name: "腾讯DeepSeek", Members: members}); err != nil {
		t.Fatal(err)
	}
	for _, g := range groupEntries(entries) {
		if g.ID == GroupPrefix+"deepseek" {
			if g.Context != 1048576 {
				t.Errorf("context %d, want the largest member's 1048576", g.Context)
			}
			return
		}
	}
	t.Fatal("no group deepseek")
}

// #120: a subscription's models take the window the user sets on it, over
// the one its backend says (Codex: 272K) — kept through the account's save.
func TestContextSetOnAccount(t *testing.T) {
	signIn(t)
	of := func() int {
		for _, e := range Served() {
			if e.ID == "codex/gpt-5.5" {
				return e.Context
			}
		}
		t.Fatal("no codex/gpt-5.5")
		return 0
	}
	before := of()
	if err := Save(Provider{ID: "codex", Contexts: map[string]int{"*": 872000}}); err != nil {
		t.Fatal(err)
	}
	if got := of(); got != 872000 {
		t.Errorf("set: %d, was %d", got, before)
	}
}

// #2112: OpenCode Go set to 512K for all of its models, to spend less.
// cat-jam-free, which models.dev says takes 262144, is told 262144, not
// 512K; DeepSeek V4.1 Flash (1M) is told the 512K set, and so is a model
// whose window nothing says. A window set for the model itself is the
// user's word on it, larger than its own or not.
func TestProviderWindowNoLargerThanTheModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"opencode-go":{"models":{
	  "cat-jam-free":{"id":"cat-jam-free","name":"Cat Jam Free","limit":{"context":262144,"output":131072}},
	  "deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash","limit":{"context":1048576,"output":393216}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	of := func(id string) int {
		for _, e := range Served() {
			if e.ID == id {
				return e.Context
			}
		}
		t.Fatalf("no %s", id)
		return 0
	}
	if err := Save(Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "https://opencode.test/v1", Catalog: "opencode-go",
		Models: []string{"cat-jam-free", "deepseek-v4.1-flash", "unheard-of"}, Contexts: map[string]int{"*": 512000}}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"opencode-go/cat-jam-free": 262144, "opencode-go/deepseek-v4.1-flash": 512000, "opencode-go/unheard-of": 512000} {
		if got := of(id); got != want {
			t.Errorf("%s: told %d, want %d", id, got, want)
		}
	}
	if err := Save(Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "https://opencode.test/v1", Catalog: "opencode-go",
		Models: []string{"cat-jam-free"}, Contexts: map[string]int{"*": 512000, "cat-jam-free": 400000}}); err != nil {
		t.Fatal(err)
	}
	if got := of("opencode-go/cat-jam-free"); got != 400000 {
		t.Errorf("set for the model: told %d, want 400000", got)
	}
	// a window asked for above the listed one, Codex's max_context_window,
	// is the model's own too: Codex raised to 600K stays raised (#120)
	p := Provider{ID: "codex", Contexts: map[string]int{"*": 600000}}
	if got := p.WindowOf(catalog.Model{ID: "gpt-6-sol", Context: 272000, MaxContext: 872000}); got != 600000 {
		t.Errorf("codex raised: %d", got)
	}
	if got := p.WindowOf(catalog.Model{ID: "gpt-6-sol", Context: 272000, MaxContext: 400000}); got != 400000 {
		t.Errorf("codex past its max: %d", got)
	}
}

// MisakaNatsuxun on Discord (10-23): a custom OpenAI-compatible provider set
// to 1M for all of its models (Advanced → Context window) still told its
// claude-sonnet-4-5 200K, the window models.dev gives that id under other
// vendors. A relay's list says nothing of its window, and a guess from
// another vendor's entry is magpie's default, not the provider's own word,
// so the user's 1M is told. The models.dev bytes are anthropic's own entry
// for the model, as it has it today. A window set lower than the guess
// still holds, and a vendor's own list still caps "*" (#2112, above).
func TestProviderWindowSetOverAnotherVendorsGuess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"models":{
	  "claude-sonnet-4-5":{"id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5 (latest)","attachment":true,"reasoning":true,"tool_call":true,
	    "modalities":{"input":["text","image","pdf"],"output":["text"]},"limit":{"context":200000,"output":64000}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	of := func(id string) int {
		for _, e := range Served() {
			if e.ID == id {
				return e.Context
			}
		}
		t.Fatalf("no %s", id)
		return 0
	}
	relay := func(cx map[string]int) {
		t.Helper()
		if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.test/v1",
			Models: []string{"claude-sonnet-4-5", "my-own-model-x"}, Contexts: cx}); err != nil {
			t.Fatal(err)
		}
	}
	relay(nil)
	if got := of("relay/claude-sonnet-4-5"); got != 200000 {
		t.Fatalf("nothing set: told %d, want models.dev's 200000", got)
	}
	for _, c := range []struct {
		cx        map[string]int
		sonnet, x int
	}{
		{map[string]int{"*": 1000000}, 1000000, 1000000},
		{map[string]int{"*": 128000}, 128000, 128000},
		{map[string]int{"*": 128000, "claude-sonnet-4-5": 1000000}, 1000000, 128000},
		{map[string]int{"claude-sonnet-4-5": 1000000}, 1000000, 0},
	} {
		relay(c.cx)
		if got := of("relay/claude-sonnet-4-5"); got != c.sonnet {
			t.Errorf("%v: claude-sonnet-4-5 told %d, want %d", c.cx, got, c.sonnet)
		}
		if got := of("relay/my-own-model-x"); got != c.x {
			t.Errorf("%v: my-own-model-x told %d, want %d", c.cx, got, c.x)
		}
	}
	// a window the provider's own list gives is its word, and "*" stays
	// within it, as for OpenCode Go's catalog (#2112)
	p := Provider{ID: "relay", Contexts: map[string]int{"*": 1000000}}
	if got := p.WindowOf(catalog.Model{ID: "claude-sonnet-4-5", Context: 200000}); got != 200000 {
		t.Errorf("listed by the vendor: %d", got)
	}
}
