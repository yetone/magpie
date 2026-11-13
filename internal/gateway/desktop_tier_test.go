package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop's Code tab runs a subagent on a tier only when the
// gateway's /v1/models tags a model with it; untagged, every subagent ran on
// the chat's model (WilianWeng). A model the user picks for a tier is tagged
// with it as its default, and Claude Code's own id of a tier no row carries
// goes to the tier's pick. A Claude model is not tagged with its own tier
// unpicked (cybercaius: another provider's haiku took Claude Code's small
// calls).
func TestClaudeDesktopTiers(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL,
		Models: []string{"m1", "claude-sonnet-5", "m2"}}); err != nil {
		t.Fatal(err)
	}
	desktop := Token + "-claude-desktop"
	type row struct {
		Tier    string `json:"anthropic_family_tier"`
		Default bool   `json:"is_family_default"`
	}
	rows := func() map[string]row {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", desktop)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var list struct {
			Data []struct {
				ID string `json:"id"`
				row
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]row{}
		for _, d := range list.Data {
			// a model picked for two tiers has a row for the later one
			// too (TestDesktopTierSharedPick): the model's own row is first
			if _, seen := out[DesktopCatalogID(d.ID)]; !seen {
				out[DesktopCatalogID(d.ID)] = d.row
			}
		}
		return out
	}
	send := func(model string) string {
		t.Helper()
		f.got = nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+model+`","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-api-key", desktop)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		return modelOf(f.got)
	}

	// nothing picked: nothing is tagged
	got := rows()
	if r := got["fake/claude-sonnet-5"]; r.Tier != "" || r.Default {
		t.Fatalf("claude-sonnet-5 unpicked: %+v", r)
	}
	if r := got["fake/m1"]; r.Tier != "" {
		t.Fatalf("m1 unpicked: %+v", r)
	}

	// m1 picked for opus and sonnet, m2 for haiku
	for tier, id := range map[string]string{"opus": "fake/m1", "sonnet": "fake/m1", "haiku": "fake/m2"} {
		if err := SetDesktopTier(tier, id); err != nil {
			t.Fatal(err)
		}
	}
	got = rows()
	if r := got["fake/m1"]; r.Tier != "opus" || !r.Default {
		t.Fatalf("m1 picked for opus and sonnet: %+v", r)
	}
	if r := got["fake/m2"]; r.Tier != "haiku" || !r.Default {
		t.Fatalf("m2 picked for haiku: %+v", r)
	}
	if r := got["fake/claude-sonnet-5"]; r.Tier != "" {
		t.Fatalf("claude-sonnet-5 with sonnet picked: %+v", r)
	}
	// Claude Code's own ids of a tier go to its pick, sonnet's too, which
	// no row is tagged with; a Claude model magpie serves stays as asked
	for asked, want := range map[string]string{
		"claude-sonnet-4-6": "m1", "claude-opus-4-8[1m]": "m1", "claude-3-5-haiku-20241022": "m2",
		"claude-haiku-4-5-20251001": "m2", "claude-sonnet-5": "claude-sonnet-5",
	} {
		if got := send(asked); got != want {
			t.Errorf("%s: sent %q, want %q", asked, got, want)
		}
	}
	if DesktopCatalogID(aliasFor("fake/m1")) != "fake/m1" || DesktopCatalogID("fake/m2") != "fake/m2" {
		t.Fatal("DesktopCatalogID")
	}

	// unpicked again, the file goes
	for _, tier := range DesktopTierNames {
		if err := SetDesktopTier(tier, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(desktopTiersPath()); !os.IsNotExist(err) {
		t.Fatalf("tiers file left: %v", err)
	}
}

// A short request for the model the user picked for one of Desktop's tiers
// goes to that pick (#3264, Anarcadia): the pick is tagged is_family_default,
// so it is Desktop's own small_fast and its titles ask for it by id — the
// user's choice of what small work runs on, not the first id with haiku in
// it that Desktop picks when no tier is picked, which is the one that
// follows the session's model. A session on the slow model went to it all
// the same, and its first token missed the title's timeout.
func TestClaudeDesktopTierPickKeepsItsModel(t *testing.T) {
	f := &fake{reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	up := setup(t, provider.Anthropic, f)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Anthropic: up.URL,
		Models: []string{"gemini-4-argon-contributor", "anthropic/claude-haiku-5.5", "claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	const pick = "fake/anthropic/claude-haiku-5.5"
	if err := SetDesktopTier("haiku", pick); err != nil {
		t.Fatal(err)
	}
	desktop := Token + "-claude-desktop"
	send := func(model string, tools bool) string {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":200,"stream":true,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`
		if tools {
			body = `{"model":"` + model + `","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		}
		f.got = nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", desktop)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
		}
		return modelOf(f.got)
	}
	// a turn on the slow model the session is on
	if got := send(aliasFor("fake/gemini-4-argon-contributor"), true); got != "gemini-4-argon-contributor" {
		t.Fatalf("turn: sent %q", got)
	}
	// a title for a Claude model no tier is picked for follows the session
	if got := send("fake/claude-sonnet-5", false); got != "gemini-4-argon-contributor" {
		t.Fatalf("title for an unpicked tier: sent %q", got)
	}
	// a title for the haiku tier's pick goes to the pick, by the id Desktop
	// was shown it by
	haiku, ok := entryOf(pick)
	if !ok {
		t.Fatalf("%s is not in the catalog", pick)
	}
	if got := send(DesktopID(haiku), false); got != "anthropic/claude-haiku-5.5" {
		t.Fatalf("title for the pick: sent %q, want anthropic/claude-haiku-5.5", got)
	}
	// a turn on the pick keeps it and makes it the session's model, as any
	// model the user picks for a chat does
	if got := send(DesktopID(haiku), true); got != "anthropic/claude-haiku-5.5" {
		t.Fatalf("turn on the pick: sent %q", got)
	}
	if b, _ := os.ReadFile(desktopPickedPath()); strings.TrimSpace(string(b)) != pick {
		t.Fatalf("picked %q", b)
	}
	// the next title follows that session, and an unserved Claude model of a
	// tier with no pick goes there too
	if got := send("fake/claude-sonnet-5", false); got != "anthropic/claude-haiku-5.5" {
		t.Fatalf("title after a turn on the pick: sent %q", got)
	}
	if got := send("claude-opus-4-8", false); got != "anthropic/claude-haiku-5.5" {
		t.Fatalf("unserved opus: sent %q", got)
	}
}
