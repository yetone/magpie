package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexSuggests is one of the Codex app's prompt-suggestion calls (source
// ambient_suggestions, or the safety check, ambient_suggestion_safety) as
// it sends them with magpie as its provider: to /v1/responses, on the model
// its server names, from this computer.
func codexSuggests(t *testing.T, s *Server, key, model, source string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"suggest prompts"}`))
	req.RemoteAddr = "127.0.0.1:51000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	if key == TokenFor("codex") {
		// another agent's call says nothing of Codex
		req.Header.Set("User-Agent", "Codex Desktop/26.1106.0 (Mac OS 26.2.0; arm64)")
		req.Header.Set("Originator", "Codex Desktop")
	}
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// @0xcb198609 on X: the Codex app's prompt suggestions, which read the
// project and connected apps in the background, ran on whatever magpie's
// auto picked (76.9k tokens on deepseek-v4-pro), and Codex has no setting
// for their model. With one set in magpie both calls go there, a bare
// model of OpenAI's included, the Routing page saying why, and Codex's
// own turns stay where they were.
func TestCodexSuggestionsMoved(t *testing.T) {
	for _, tc := range []struct{ source, model string }{
		{"ambient_suggestions", "fake/m1"},
		{"ambient_suggestion_safety", "fake/m1"},
		{"ambient_suggestions", "gpt-6-luna"},
	} {
		t.Run(tc.source+" "+tc.model, func(t *testing.T) {
			u := dshSetup(t)
			if err := provider.SetCodexSuggestionsModel("fake/m2"); err != nil {
				t.Fatal(err)
			}
			s := New()
			if rec := codexSuggests(t, s, TokenFor("codex"), tc.model, tc.source); rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if got, _ := u.seen(); got != "m2" {
				t.Fatalf("suggestions sent %q, want m2", got)
			}
			r := lastRoute(s)
			if r.Kind != tc.source || r.Moved == nil || *r.Moved != (ModelMove{To: "fake/m2", Why: movedCodexSuggestions}) {
				t.Fatalf("trace: kind %q moved %+v", r.Kind, r.Moved)
			}
			if rec := codexCall(t, s, "fake/m1", ""); rec.Code != 200 {
				t.Fatalf("a turn: %d %s", rec.Code, rec.Body.String())
			}
			if got, _ := u.seen(); got != "m1" {
				t.Fatalf("turn sent %q, want m1", got)
			}
		})
	}
}

// Nothing set (the default: as Codex sends them), another agent's call of
// that kind, and a model set that magpie doesn't serve or the user
// disabled: the call goes on as asked.
func TestCodexSuggestionsLeft(t *testing.T) {
	for _, tc := range []struct{ name, target, key string }{
		{"unset", "", TokenFor("codex")},
		{"other agent", "fake/m2", TokenFor("opencode")},
		{"same", "fake/m1", TokenFor("codex")},
		{"unserved", "fake/missing", TokenFor("codex")},
		{"disabled", "fake/m2", TokenFor("codex")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := dshSetup(t)
			if err := provider.SetCodexSuggestionsModel(tc.target); err != nil {
				t.Fatal(err)
			}
			if tc.name == "disabled" {
				if err := provider.DisableModels("fake/m2"); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			if rec := codexSuggests(t, s, tc.key, "fake/m1", "ambient_suggestions"); rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if got, _ := u.seen(); got != "m1" {
				t.Fatalf("sent %q, want m1", got)
			}
			if r := lastRoute(s); r.Moved != nil {
				t.Fatalf("moved %+v", r.Moved)
			}
		})
	}
}

// Codex signed in to ChatGPT sends the suggestions to magpie's ChatGPT
// backend route on OpenAI's model. With a model set for them they go
// there, ChatGPT isn't asked, and the trace keeps the model asked.
func TestCodexSuggestionsMovedOffChatGPT(t *testing.T) {
	up := codexSubagentSetup(t)
	u := fakeServing(t)
	if err := provider.SetCodexSuggestionsModel("fake/m2"); err != nil {
		t.Fatal(err)
	}
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-6-luna","stream":true,"input":"suggest prompts"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	req.Header.Set("User-Agent", "Codex Desktop/26.1106.0 (Mac OS 26.2.0; arm64)")
	req.Header.Set("session_id", "suggestions-1")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"ambient_suggestions"}`)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got, _ := u.seen(); got != "m2" {
		t.Fatalf("suggestions sent %q, want m2", got)
	}
	if got := up.asked(); len(got) != 0 {
		t.Fatalf("ChatGPT was asked for %v", got)
	}
	r := lastRoute(s)
	if r.Model != "gpt-6-luna" || r.Moved == nil || *r.Moved != (ModelMove{To: "fake/m2", Why: movedCodexSuggestions}) {
		t.Fatalf("trace: model %q moved %+v", r.Model, r.Moved)
	}
}

// A bare name names no model of magpie's.
func TestCodexSuggestionsModelRefusesBareName(t *testing.T) {
	fresh(t)
	if err := provider.SetCodexSuggestionsModel("gpt-6-luna"); err == nil {
		t.Fatal("a bare name was taken")
	}
	if err := provider.SetCodexSuggestionsModel("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := provider.CodexSuggestionsModel(); got != "fake/m2" {
		t.Fatalf("read back %q", got)
	}
}
