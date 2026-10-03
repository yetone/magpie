package provider

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A rollout can give the first account 4.6 and another enabled account
// 5.5. Refresh must discover both, and routing must know who lists each.
func TestAntigravityModelsAcrossAccounts(t *testing.T) {
	replies := map[string]string{
		"project-old": `{"models":{"gemini-3.8-flash":{},"claude-opus-4-6-thinking":{"displayName":"Claude Opus 4.6 (Thinking)"},"gpt-oss-120b-medium":{}},
			"agentModelSorts":[{"groups":[{"modelIds":["gemini-3.8-flash","claude-opus-4-6-thinking","gpt-oss-120b-medium"]}]}]}`,
		"project-new": `{"models":{"gemini-3.8-flash":{},"claude-opus-5-5-low":{"displayName":"Claude Opus 5.5 (Low)"},"claude-opus-5-5-high":{"displayName":"Claude Opus 5.5 (High)"},"gpt-oss-120b-medium":{}},
			"agentModelSorts":[{"groups":[{"modelIds":["gemini-3.8-flash","claude-opus-5-5-high","claude-opus-5-5-low","gpt-oss-120b-medium"]}]}]}`,
		"project-off": `{"models":{"off-only":{}}}`,
	}
	f := &fakeGoogle{}
	f.modelsFor = func(body map[string]any) (int, string) {
		project, _ := body["project"].(string)
		if reply, ok := replies[project]; ok && reply != "error" {
			return 200, reply
		}
		return 503, `{"error":{"code":503,"message":"unavailable","status":"UNAVAILABLE"}}`
	}
	googleSandbox(t, f)
	for _, user := range []string{"old", "new", "off"} {
		if err := addGoogleLogin("antigravity", user+"@example.com", "", googleAuth{
			AccessToken: user, RefreshToken: "rt-" + user, Project: "project-" + user,
			Expiry: time.Now().Add(time.Hour).UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchGoogleLogin("antigravity", "old@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := setGoogleLoginOn("antigravity", "new@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := setGoogleLoginOn("antigravity", "off@example.com", false); err != nil {
		t.Fatal(err)
	}
	setReply := func(project, reply string) {
		f.mu.Lock()
		replies[project] = reply
		f.mu.Unlock()
	}
	p, _ := googleAccountOf("antigravity")
	check := func() {
		t.Helper()
		ms, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		// A model only a later account lists stays where that account's
		// picker has it, ahead of the first account's older models.
		want := []string{"gemini-3.8-flash", "claude-opus-5-5-high", "claude-opus-5-5-low", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}
		if !slices.Equal(ids, want) {
			t.Errorf("models %v, want %v", ids, want)
		}
		f.mu.Lock()
		for _, b := range f.fetches {
			if b["project"] == "project-off" {
				t.Error("queried a disabled account")
			}
		}
		f.mu.Unlock()
		if p.Account.Lists("claude-opus-5-5") {
			t.Error("first account does not list 5.5")
		}
		also := p.AlsoOn()
		if len(also) != 1 || !also[0].Account.Lists("claude-opus-5-5") || !also[0].Account.Lists("claude-opus-5-5-high") || also[0].Account.Lists("claude-opus-4-6-thinking") {
			t.Error("per-account model lists do not reflect the rollout")
		}
	}
	check()
	// A transient failure keeps the last known list of that enabled account.
	setReply("project-new", "error")
	check()
	// An unreachable first account with no cache must not hide a healthy
	// secondary account, nor should a completely failed refresh erase it.
	if err := catalog.SaveLive(accountModels("antigravity", "old@example.com"), "", nil); err != nil {
		t.Fatal(err)
	}
	oldReply := replies["project-old"]
	setReply("project-old", "error")
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 4 {
		t.Fatalf("cached secondary account: %v, %v", ms, err)
	}
	if err := catalog.SaveLive(accountModels("antigravity", "new@example.com"), "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("every account failed without a cache, but refresh succeeded")
	}
	if kept, _, ok := catalog.Live("antigravity"); !ok || len(kept) != 4 {
		t.Fatal("failed refresh erased the provider catalog")
	}
	setReply("project-old", oldReply)
	// Once disabled, its cached models no longer join the provider's list.
	if err := setGoogleLoginOn("antigravity", "new@example.com", false); err != nil {
		t.Fatal(err)
	}
	ms, err = p.Fetch(context.Background())
	if err != nil || len(ms) != 3 || slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "claude-opus-5-5-high" }) {
		t.Fatalf("disabled: %v, %v", ms, err)
	}
}

// The merge keeps each account's own order: a model only a later account
// has goes right after the model before it in that account's list.
func TestMergeAntigravityOrder(t *testing.T) {
	ids := func(ss ...string) []catalog.Model {
		var out []catalog.Model
		for _, s := range ss {
			out = append(out, catalog.Model{ID: s})
		}
		return out
	}
	for n, c := range []struct {
		lists [][]catalog.Model
		want  []string
	}{
		// the review's example: a 4.6-only first account, then one whose
		// picker shows 5.5-high, 5.5-low, 4.6
		{[][]catalog.Model{ids("4.6"), ids("5.5-high", "5.5-low", "4.6")}, []string{"5.5-high", "5.5-low", "4.6"}},
		// the shape seen on real accounts: 5.5 where the other has 4.6
		{[][]catalog.Model{ids("g", "s4.6", "o4.6", "oss"), ids("g", "o5.5", "s5.5", "oss")}, []string{"g", "o5.5", "s5.5", "s4.6", "o4.6", "oss"}},
	} {
		var out []catalog.Model
		for _, l := range c.lists {
			out = mergeAntigravityModels(out, l)
		}
		var got []string
		for _, m := range out {
			got = append(got, m.ID)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("case %d: merged %v, want %v", n, got, c.want)
		}
	}
}
