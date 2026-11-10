package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// anthropicAPI is a provider on Anthropic's own API, whose review results
// come back (gateway.AutoModeAnswered).
const anthropicAPI = "https://api.anthropic.com"

// TestClaudeAutoModeReview: Claude Code in auto mode behind magpie on a
// model Anthropic doesn't review (1EFLOPS on X, on Codex's gpt-6.1-sol)
// said "this session isn't eligible because your requests go through
// 127.0.0.1:3425…", and 2.1.322 with its telemetry off refuses every
// action there instead, on DeepSeek and on a relay's Claude model alike
// (JoyYoung2Lucky on X). Anthropic's page says to turn the review off
// (CLAUDE_CODE_AUTO_MODE_SERVER=0) for a gateway that can't run it: magpie
// does unless every model Claude Code can reach through it is reviewed
// with the results coming back, shown or behind a group or a fallback. A
// value of the user's is theirs, and magpie's goes when Claude Code is
// taken off magpie.
func TestClaudeAutoModeReview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"theme":"dark"}`)
	a := claude(home)
	review := func() string { v, _ := edit.GetJSON(path, "env."+claudeReviewEnv); return v }
	set := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatal(err)
		}
	}
	sync := func() {
		t.Helper()
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}

	// only a model no one reviews: off
	set("model", "v/gpt-x")
	if review() != "0" {
		t.Fatalf("on v/gpt-x alone: %q, want 0", review())
	}
	sync()
	if review() != "0" {
		t.Fatalf("Sync: %q", review())
	}

	// a Claude model on Anthropic's API joins, v/gpt-x still shown: off
	// still, as /model can take the session to v/gpt-x, where 2.1.322 with
	// its telemetry off would have no check at all
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("a/claude-sonnet-5 beside v/gpt-x: %q, want 0", review())
	}

	// the Claude model alone: on
	set("model", "a/claude-sonnet-5")
	if err := provider.SetHiddenModels("claude", []string{"v/gpt-x"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "" {
		t.Fatalf("a/claude-sonnet-5 alone: %q, want unset", review())
	}

	// reachable through a group shown to it, or a provider's fallback: off
	if err := provider.SaveGroup(provider.Group{Name: "mix", Members: []string{"v/gpt-x", "a/claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("a group with v/gpt-x: %q, want 0", review())
	}
	// a group that keeps its models out of the lists (#1783) still routes
	// to them, so its other member counts
	if err := provider.SaveGroup(provider.Group{Name: "mix", Members: []string{"v/gpt-x", "a/claude-sonnet-5"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("a group unlisting v/gpt-x: %q, want 0", review())
	}
	if err := provider.DeleteGroup("mix"); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "" {
		t.Fatalf("group deleted: %q, want unset", review())
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}, Fallback: []string{"v/gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("falling back to v/gpt-x: %q, want 0", review())
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "" {
		t.Fatalf("fallback gone: %q, want unset", review())
	}

	// off magpie, onto Claude Code's own model, and reset: magpie's goes
	set("model", "v/gpt-x")
	if review() != "0" {
		t.Fatalf("set to v/gpt-x: %q, want 0", review())
	}
	set("model", "claude-opus-4-8")
	if review() != "" {
		t.Fatalf("Claude's own model kept magpie's value: %q", review())
	}
	set("model", "v/gpt-x")
	if review() != "0" {
		t.Fatalf("back on v/gpt-x: %q", review())
	}
	set("model", "")
	if review() != "" {
		t.Fatalf("reset kept magpie's value: %q", review())
	}

	// the user's own value is theirs, through a switch, Sync and reset
	if err := provider.SetHiddenModels("claude", nil); err != nil {
		t.Fatal(err)
	}
	for _, mine := range []string{"1", "0"} {
		if err := edit.SetJSON(path, edit.KV{Path: "env." + claudeReviewEnv, Value: mine}); err != nil {
			t.Fatal(err)
		}
		set("model", "v/gpt-x")
		sync()
		if err := provider.SetHiddenModels("claude", []string{"v/gpt-x"}); err != nil {
			t.Fatal(err)
		}
		set("model", "a/claude-sonnet-5")
		sync()
		if review() != mine {
			t.Fatalf("the user's %q became %q", mine, review())
		}
		set("model", "")
		if review() != mine {
			t.Fatalf("reset took the user's %q away: %q", mine, review())
		}
		if err := edit.DelJSON(path, "env."+claudeReviewEnv); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetHiddenModels("claude", nil); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatal("theme lost")
	}
}

// TestT3CodeAutoModeReview: T3 Code's magpie instance runs Claude Code on
// the gateway through its own environment, so it is told the same: the
// review off while a model it lists isn't reviewed with its results
// coming back, and on (not set) once each is.
func TestT3CodeAutoModeReview(t *testing.T) {
	home := syncHome(t)
	t.Setenv("T3CODE_HOME", "")
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	writeFile(t, path, `{}`)
	a := t3code(home)
	if err := a.Field("provider").Set(magpieID); err != nil {
		t.Fatal(err)
	}
	review := func() (string, bool) {
		t.Helper()
		var file map[string]map[string]json.RawMessage
		if err := json.Unmarshal([]byte(readFile(path)), &file); err != nil {
			t.Fatal(err)
		}
		for _, v := range t3Format(t, file["providerInstances"][magpieID]).Environment {
			if v.Name == claudeReviewEnv {
				return v.Value, true
			}
		}
		return "", false
	}
	sync := func() {
		t.Helper()
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok := review(); v != "0" || !ok {
		t.Fatalf("relay/glm-4.6 alone: %q %v, want 0", v, ok)
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	sync()
	if v, ok := review(); v != "0" || !ok {
		t.Fatalf("a/claude-sonnet-5 beside relay/glm-4.6: %q %v, want 0", v, ok)
	}
	if err := provider.SetHiddenModels("t3code", []string{"relay/glm-4.6"}); err != nil {
		t.Fatal(err)
	}
	sync()
	if v, ok := review(); ok {
		t.Fatalf("a/claude-sonnet-5 listed alone: %q, want none", v)
	}
}

// TestClaudeReviewedJudgesTheSetModelWhereItGoes: a model Claude Code is
// set to is judged on the provider it resolves to, as the gateway judges
// its requests, not by its name alone. A Claude model on Anthropic's API
// counts; one falling back to another vendor's model, or a group with
// such a member, doesn't; a name magpie doesn't know is judged by the name.
func TestClaudeReviewedJudgesTheSetModelWhereItGoes(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	if claudeReviewed(nil, "v/gpt-x") {
		t.Fatal("v/gpt-x alone counted as reviewed")
	}
	if !claudeReviewed(nil, "a/claude-sonnet-5") || !claudeReviewed(nil, "claude-opus-4-8") {
		t.Fatal("a Claude model not reviewed")
	}
	if claudeReviewed(nil, "a/claude-sonnet-5", "v/gpt-x") {
		t.Fatal("a/claude-sonnet-5 beside v/gpt-x counted as reviewed")
	}
	// an empty slot (no small model set, say) is no model at all: Sync
	// passes one, and judged by its name it would turn the review off
	if !claudeReviewed(nil, "a/claude-sonnet-5", "") {
		t.Error("an empty slot beside a/claude-sonnet-5 turned the review off")
	}
	if claudeReviewed(nil, "") {
		t.Error("only an empty slot is reviewed")
	}
	if claudeReviewed(nil) {
		t.Fatal("no model at all counted as reviewed")
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}, Fallback: []string{"v/gpt-x"}}); err != nil {
		t.Fatal(err)
	}
	if claudeReviewed(nil, "a/claude-sonnet-5") {
		t.Fatal("a/claude-sonnet-5 falling back to v/gpt-x counted as reviewed")
	}
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Anthropic: anthropicAPI, Key: "k", Models: []string{"claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "mix", Members: []string{"v/gpt-x", "a/claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := provider.FindGroup(provider.GroupPrefix + "mix"); !ok {
		t.Fatal("group mix not found")
	}
	if claudeReviewed(nil, provider.GroupPrefix+"mix") {
		t.Fatal("group mix with a v/gpt-x member counted as reviewed")
	}
	if err := provider.SaveGroup(provider.Group{Name: "claudes", Members: []string{"a/claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	if !claudeReviewed(nil, provider.GroupPrefix+"claudes") {
		t.Fatal("group of a/claude-sonnet-5 alone not reviewed")
	}
}

// TestClaudeReviewedOnlyWhereResultsComeBack: a Claude model on a relay's
// Anthropic endpoint is sent the safeguards, but the relay may drop the
// results, and Claude Code 2.1.322 with its telemetry off then refuses
// every action ("Auto mode can't check actions in this session … your
// requests go through 127.0.0.1:3425, which doesn't pass it through",
// JoyYoung2Lucky on X, on a Claude model too). So a relay's Claude model
// leaves the review off, as one it serves only on Chat (nexnull on
// Discord, translated, with no safeguards at all) does; Anthropic's own
// API turns it on.
func TestClaudeReviewedOnlyWhereResultsComeBack(t *testing.T) {
	home := syncHome(t)
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{}`)
	review := func() string { v, _ := edit.GetJSON(path, "env."+claudeReviewEnv); return v }
	a := claude(home)
	if err := provider.SetHiddenModels("claude", []string{"relay/glm-4.6"}); err != nil {
		t.Fatal(err)
	}
	sync := func() {
		t.Helper()
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	relay := provider.Provider{ID: "r", Name: "R", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"claude-sonnet-5"}}
	if err := provider.Save(relay); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("r/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	if review() != "0" {
		t.Fatalf("a relay's Claude on chat: %q, want 0", review())
	}

	// the relay's Anthropic endpoint: sent the safeguards, its results
	// not sure to come back: off
	relay.Anthropic = "https://relay.example"
	if err := provider.Save(relay); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("Claude on a relay's Anthropic endpoint: %q, want 0", review())
	}

	// Anthropic's own API: on
	relay.Anthropic = anthropicAPI
	if err := provider.Save(relay); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "" {
		t.Fatalf("Claude on Anthropic's API: %q, want unset", review())
	}

	// set to be asked on chat there: off again
	if err := provider.SetModelAPI("r/claude-sonnet-5", "chat"); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "0" {
		t.Fatalf("Claude set to be asked on chat: %q, want 0", review())
	}
	if err := provider.SetModelAPI("r/claude-sonnet-5", ""); err != nil {
		t.Fatal(err)
	}
	sync()
	if review() != "" {
		t.Fatalf("back to the vendor's list: %q, want unset", review())
	}
}
