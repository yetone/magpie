package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// OoopsHh on X: an OpenAI key's GPT-6 Astra had Fast and no Ultrafast. OpenAI's
// API serves service_tier "ultrafast" on its Responses for the models its
// Ultrafast mode names, so a pick sent Ultrafast is asked for it there,
// from its Responses or Claude's API; Chat Completions turns the tier away,
// so it goes as Fast there. A model it doesn't serve so (GPT-6 Sol) can't be picked
// Ultrafast, and another vendor's (Claude on an Anthropic key) keeps its own
// fast mode, with no service_tier.
func TestOpenAIKeyUltrafast(t *testing.T) {
	s, up := fasted(t)
	oa := provider.Provider{ID: "ok", Name: "OpenAI", Preset: "openai", Key: "ko", Chat: "https://api.openai.com/v1", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6-astra", "gpt-6-sol"}}
	if err := provider.Save(oa); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("ok")
	if !provider.CanUltra(*p, "gpt-6-astra") || !provider.CanUltra(*p, "gpt-6-astra-2026-09-14") {
		t.Fatal("OpenAI's API offers no Ultrafast on GPT-6 Astra")
	}
	if provider.CanUltra(*p, "gpt-6-sol") {
		t.Fatal("Ultrafast offered on GPT-6 Sol, which OpenAI's API turns away")
	}
	if err := provider.SetFastTier("codex", "ok/gpt-6-sol", "ultrafast"); err == nil {
		t.Fatal("GPT-6 Sol picked Ultrafast")
	}
	if err := provider.SetFastTier("codex", "ok/gpt-6-astra", "ultrafast"); err != nil {
		t.Fatal(err)
	}
	// and its list says so, to another magpie that has this one as its
	// provider: in Docker it said Fast alone (OoopsHh on X)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Data []struct {
			ID    string `json:"id"`
			Tiers []struct {
				ID string `json:"id"`
			} `json:"service_tiers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	listed := map[string]string{}
	for _, m := range list.Data {
		for _, x := range m.Tiers {
			listed[m.ID] += x.ID + " "
		}
	}
	for id, want := range map[string]string{"ok/gpt-6-astra": "priority ultrafast ", "ok/gpt-6-sol": "priority "} {
		if listed[id] != want {
			t.Errorf("%s listed with tiers %q, want %q", id, listed[id], want)
		}
	}
	for _, a := range []struct{ name, path, body string }{
		{"responses", "/v1/responses", `{"model":"ok/gpt-6-astra","input":"hi"}`},
		{"anthropic", "/v1/messages", `{"model":"ok/gpt-6-astra","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		r := postFrom(t, s, "codex", a.path, a.body)
		host, path, _, body := up.last(t)
		if host != "api.openai.com" || path != "/v1/responses" || body["service_tier"] != "ultrafast" || body["model"] != "gpt-6-astra" {
			t.Errorf("%s: sent to %s%s %v", a.name, host, path, body)
		}
		if len(r.Tries) != 1 || !r.Tries[0].Fast || len(r.Order) == 0 || !r.Order[0].Ultra {
			t.Errorf("%s: traced %+v, order %+v", a.name, r.Tries, r.Order)
		}
		// another agent's request goes at the usual speed
		postFrom(t, s, "opencode", a.path, a.body)
		if _, _, _, body := up.last(t); body["service_tier"] != nil {
			t.Errorf("%s: another agent's request: %v", a.name, body)
		}
	}
	// Chat Completions turns Ultrafast away, so an agent speaking it is
	// sent Fast there, its own ask for Ultrafast too
	for _, body := range []string{
		`{"model":"ok/gpt-6-astra","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"ok/gpt-6-astra","messages":[{"role":"user","content":"hi"}],"service_tier":"ultrafast"}`,
	} {
		postFrom(t, s, "codex", "/v1/chat/completions", body)
		if host, path, _, sent := up.last(t); host != "api.openai.com" || path != "/v1/chat/completions" || sent["service_tier"] != "priority" {
			t.Errorf("chat %s: sent to %s%s %v", body, host, path, sent)
		}
	}
	// a client's own Ultrafast on OpenAI's Responses goes as asked
	postFrom(t, s, "opencode", "/v1/responses", `{"model":"ok/gpt-6-astra","input":"hi","service_tier":"ultrafast"}`)
	if _, _, _, body := up.last(t); body["service_tier"] != "ultrafast" {
		t.Errorf("client's own ultrafast: %v", body)
	}

	// another vendor's model is unchanged: Claude on an Anthropic key has
	// no Ultrafast, and its fast pick goes in Claude's words
	an, _ := provider.Find("an")
	if provider.CanUltra(*an, "claude-opus-5-5") {
		t.Fatal("Ultrafast offered on an Anthropic key's Claude")
	}
	if err := provider.SetFastTier("claude", "an/claude-opus-5-5", "ultrafast"); err == nil {
		t.Fatal("Claude picked Ultrafast")
	}
	if err := provider.SetFastPick("claude", "an/claude-opus-5-5", true); err != nil {
		t.Fatal(err)
	}
	postFrom(t, s, "claude", "/v1/messages", `{"model":"an/claude-opus-5-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	if host, _, _, body := up.last(t); host != "api.anthropic.com" || body["service_tier"] != nil || body["speed"] != "fast" {
		t.Errorf("Claude's fast pick: %s %v", host, body)
	}
}
