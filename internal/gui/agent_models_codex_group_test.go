package gui

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Codex's own models, a ChatGPT account's, stay in its model picker with a
// routing group switched on and its own models switched off (shiqi_40904 on
// Discord: "开启路由组后 … 模型列表里不会显示 codex 模型"). magpie's list shows
// them Codex as its Codex section, and its pickers offer them as Codex's own
// (codex/… would only add a hop), so the switch for the agent's own models
// never takes them out. It did once the group gave the picker one of
// magpie's models, which is what the switch waits for: the picker was the
// group alone, and with the group off every model was back.
func TestCodexOwnModelsStayWithGroupOn(t *testing.T) {
	ids := codexSignedInSandbox(t)
	if err := provider.SetOwnModelsHidden("codex", true); err != nil {
		t.Fatal(err)
	}
	codex, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	picks := func() (out []string) {
		t.Helper()
		for _, f := range agentFields(codex, codex.Values()) {
			if f.Key == "model" {
				for _, o := range f.Options {
					out = append(out, o.Value)
				}
			}
		}
		return out
	}
	for _, id := range ids {
		if got := picks(); !slices.Contains(got, id) {
			t.Fatalf("group off: %s not in Codex's picker %v", id, got)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mygpt", Name: "MyGPT", Members: []string{"codex/gpt-5.5", "codex/gpt-5.4"}}); err != nil {
		t.Fatal(err)
	}
	got := picks()
	for _, id := range append(slices.Clone(ids), "group/mygpt") {
		if !slices.Contains(got, id) {
			t.Fatalf("group on: %s not in Codex's picker %v", id, got)
		}
	}
	// nothing of Codex's own is left for the switch to take out, so the
	// list has none to show
	if codex.OwnModels(codex.Values()) {
		t.Fatal("Codex's list offers a switch for its own models, every one of them magpie's too")
	}
	// one the list takes out is the switch's again: magpie no longer shows
	// it Codex
	if err := provider.SetHiddenModels("codex", []string{"codex/gpt-5.4"}); err != nil {
		t.Fatal(err)
	}
	if got := picks(); slices.Contains(got, "gpt-5.4") || !slices.Contains(got, "gpt-5.4-mini") {
		t.Fatalf("gpt-5.4 taken out of the list, own models off: picker %v", got)
	}
}

// codexSignedInSandbox signs a ChatGPT account in to a sandbox Codex, with
// the models its backend lists, and returns their ids.
func codexSignedInSandbox(t *testing.T) []string {
	t.Helper()
	home := sandboxHome(t)
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	os.MkdirAll(codexHome, 0o700)
	jwt := func(c any) string {
		b, _ := json.Marshal(c)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"email": "a@x.y", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acc"}}),
		"access_token": jwt(map[string]any{"exp": time.Now().Add(24 * time.Hour).Unix()}), "refresh_token": "r", "account_id": "acc"}})
	os.WriteFile(filepath.Join(codexHome, "auth.json"), auth, 0o600)
	os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o600)
	ids := []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2"}
	var cache []map[string]any
	var live []catalog.Model
	for i, id := range ids {
		cache = append(cache, map[string]any{"slug": id, "display_name": id, "visibility": "list", "priority": i, "context_window": 400000})
		live = append(live, catalog.Model{ID: id, Context: 400000})
	}
	b, _ := json.Marshal(map[string]any{"etag": "x", "models": cache})
	os.WriteFile(filepath.Join(codexHome, "models_cache.json"), b, 0o600)
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := catalog.SaveLive("codex", provider.CodexBase, live); err != nil {
		t.Fatal(err)
	}
	return ids
}

// The codex provider "Only through routing groups" (只通过路由分组使用), or a
// group that lists itself alone, keeps the ChatGPT account's models out of
// other agents' lists but in Codex's own (士奇 on Discord, 10-28: "开启路由组以后，agent 设置页面，模型列表里就是丢失 codex 供应商和对应模型，在下拉
// 选项中虽然能显示 codex 模型但是实际codex 应用中是没有的"). Codex's list
// has its Codex section, so its picker offers them as magpie's and there is
// no own-models switch to show them by.
func TestCodexOwnModelsListedOnlyThroughGroups(t *testing.T) {
	ids := codexSignedInSandbox(t)
	codex, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string, unlisted ...string) {
		t.Helper()
		own := map[string]bool{}
		for _, m := range agentModelList(codex) {
			if m.Own {
				own[m.ID] = true
			}
		}
		for _, id := range ids {
			if !own["codex/"+id] {
				t.Errorf("%s: codex/%s not in Codex's list %v", when, id, own)
			}
		}
		if codex.OwnModels(codex.Values()) {
			t.Errorf("%s: Codex's list offers a switch for its own models", when)
		}
		var picks []string
		for _, f := range agentFields(codex, codex.Values()) {
			if f.Key == "model" {
				for _, o := range f.Options {
					picks = append(picks, o.Value)
				}
			}
		}
		for _, id := range ids {
			if !slices.Contains(picks, id) {
				t.Errorf("%s: %s not in Codex's picker %v", when, id, picks)
			}
		}
		for _, e := range func() []provider.Entry { l, _ := provider.ListedFor("claude"); return l }() {
			if provider.CodexOwn(e) && slices.Contains(unlisted, e.ID) {
				t.Errorf("%s: Claude Code lists %s", when, e.ID)
			}
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mygpt", Name: "MyGPT", Members: []string{"codex/gpt-5.5", "codex/gpt-5.4"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	// a group listing alone keeps them from every other agent
	// (TestGroupAloneKeepsSubscriptionModelsOutOfEveryAgentsList)
	check("group lists itself alone", "codex/gpt-5.5", "codex/gpt-5.4")
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	q := *p
	q.Unlisted = true
	if err := provider.Save(q); err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, id := range ids {
		all = append(all, "codex/"+id)
	}
	check("only through routing groups", all...)
}

// A group that lists itself alone (Group.Unlists, the routing editor's
// the group alone, its card's 成员模型不单独列出) keeps its models out of
// every agent's list, a signed-in subscription's too (shaoyang. on
// Discord, 11-03: with it on, the list fetched still had every model of
// the group). 88e6fb9cf kept a subscription's models in every agent's
// list, so for a group of them, as most groups are, the option changed
// nothing an agent was listed. Codex alone keeps its ChatGPT sign-in's own
// (TestCodexOwnModelsListedOnlyThroughGroups). With its models listed too, every
// agent lists them beside the group (士奇 on Discord: a group switched on
// takes nothing from the lists then). Every agent is checked, its
// Agents-page list, what it is written and /v1/models gives it, and its
// model pickers, so a fix for one agent can't pass for all.
func TestGroupAloneKeepsSubscriptionModelsOutOfEveryAgentsList(t *testing.T) {
	ids := codexSignedInSandbox(t)
	var refs []string
	for _, id := range ids {
		refs = append(refs, "codex/"+id)
	}
	listed := func(a *agent.Agent) map[string]bool {
		out := map[string]bool{}
		for _, m := range agentModelList(a) {
			out[m.ID] = true
		}
		return out
	}
	picks := func(a *agent.Agent) []string {
		var out []string
		for _, f := range agentFields(a, a.Values()) {
			if f.Key != "model" {
				continue
			}
			for _, o := range f.Options {
				out = append(out, o.Value)
			}
		}
		return out
	}
	// a picker's value is the ref, or the ref after a prefix of the agent's
	member := func(v string) bool {
		return slices.ContainsFunc(refs, func(r string) bool { return v == r || strings.HasSuffix(v, "/"+r) })
	}
	// the group names every one of them
	g := provider.Group{ID: "mygpt", Name: "MyGPT", Members: refs, Unlists: true}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, a := range agent.All() {
		got := listed(a)
		if !got["group/mygpt"] {
			continue // an agent shown no models of magpie's (or no groups)
		}
		if strings.EqualFold(a.ListsFor(), "codex") {
			continue // its own sign-in's models stay (catalogOf)
		}
		checked++
		for _, ref := range refs {
			if got[ref] {
				t.Errorf("the group alone: %s in %s's list", ref, a.ID)
			}
		}
		if shown, _ := provider.CatalogFor(a.ListsFor()); slices.ContainsFunc(shown, func(e provider.Entry) bool { return slices.Contains(refs, e.ID) }) {
			t.Errorf("the group alone: a member in what %s is written and /v1/models gives it", a.ID)
		}
		if p := picks(a); slices.ContainsFunc(p, member) {
			t.Errorf("the group alone: %s's picker offers a member: %v", a.ID, p)
		}
	}
	for _, id := range []string{"hermes", "workbuddy", "claude"} {
		a, err := agent.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if !listed(a)["group/mygpt"] {
			t.Errorf("%s's list wasn't checked: it has no group/mygpt", id)
		}
	}
	if checked < 10 {
		t.Errorf("only %d agents' lists checked", checked)
	}
	for _, e := range provider.Catalog() {
		if slices.Contains(refs, e.ID) {
			t.Errorf("the gateway's /v1/models for a client lists %s, kept for the group", e.ID)
		}
	}
	// the picker's "kept for routing groups" note names them
	for _, ref := range refs {
		if !slices.ContainsFunc(unlistedModels(), func(m unlistedJSON) bool { return m.ID == ref }) {
			t.Errorf("%s not said kept for routing groups", ref)
		}
	}

	// its models too: every agent lists them beside the group
	g.Unlists = false
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hermes", "workbuddy", "claude", "codex"} {
		a, _ := agent.Find(id)
		got := listed(a)
		for _, ref := range refs {
			if !got[ref] || !got["group/mygpt"] {
				t.Errorf("its models too: %s's list %v lacks %s or the group", id, got, ref)
			}
		}
	}
}
