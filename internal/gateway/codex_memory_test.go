package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// codexCall is a request Codex with magpie as its provider sends to
// /v1/responses: a turn of its own (source "") or a call of a kind of its.
func codexCall(t *testing.T, s *Server, model, source string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+TokenFor("codex"))
	if source != "" {
		req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Codex's memory calls come on models of its own by their bare names
// (gpt-5.6-terra, gpt-5.6-luna), whatever Codex is set to (#742,
// congee949: every one was a 404 "magpie knows no model"). They go to the
// model Codex's own turns go to — the last one answered, or the ledger's
// after a restart — and are recorded as the memory calls they are. Any
// other call on a model magpie doesn't know is still turned away.
func TestCodexMemoryCallsGoWhereItsTurnsGo(t *testing.T) {
	f := &fake{t: t}
	setup(t, provider.Chat, f)
	reply := func() string {
		return sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`,
			`data: [DONE]`)
	}
	s := New()

	// no turn of Codex's answered yet: nothing to go to
	if rec := codexCall(t, s, "gpt-5.6-terra", "memory_consolidation"); rec.Code != 404 {
		t.Fatalf("a memory call before any turn: %d %q", rec.Code, rec.Body.String())
	}

	f.reply = reply()
	if rec := codexCall(t, s, "fake/m1", ""); rec.Code != 200 {
		t.Fatalf("a turn: %d %q", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ model, source string }{
		{"gpt-5.6-terra", "memory_consolidation"},
		{"gpt-5.6-luna", "memgen"},
	} {
		f.reply = reply()
		calls := f.calls
		if rec := codexCall(t, s, c.model, c.source); rec.Code != 200 || f.calls != calls+1 {
			t.Fatalf("%s on %s: %d %q", c.source, c.model, rec.Code, rec.Body.String())
		}
		var sent struct {
			Model string `json:"model"`
		}
		json.Unmarshal(f.got, &sent)
		if sent.Model != "m1" {
			t.Errorf("%s: the vendor was asked for %q", c.source, sent.Model)
		}
	}
	var last usage.Record
	for _, u := range usage.Load(time.Time{}) {
		last = u
	}
	if usage.PurposeOf(last.Kind) != "kind:memory_consolidation" || last.Provider != "fake" || last.Failed() {
		t.Errorf("the ledger's memory call %+v", last)
	}

	// magpie restarted: the ledger has the turn
	f.reply = reply()
	if rec := codexCall(t, New(), "gpt-5.6-terra", "memory_consolidation"); rec.Code != 200 {
		t.Fatalf("after a restart: %d %q", rec.Code, rec.Body.String())
	}

	// any other call on a model magpie doesn't know is turned away as before
	if rec := codexCall(t, s, "gpt-5.6-terra", ""); rec.Code != 404 {
		t.Fatalf("a turn on an unknown model: %d", rec.Code)
	}
	if rec := codexCall(t, s, "gpt-5.6-terra", "review"); rec.Code != 404 {
		t.Fatalf("a review on an unknown model: %d", rec.Code)
	}
}

// #4765 (raphael-y7): Codex's memory consolidation asks for its own bare
// default (gpt-5.6-terra), and a provider of magpie's happened to serve a
// model under that same name: the call went to it — the only one with the
// name — and its account refused the model (400, three tries, the memory
// lost) while the conversation ran on another provider's model. With
// Codex's config naming no model for its memories, the call follows the
// conversation's, as it does on the ChatGPT backend's path (codexFollowTo);
// a model Codex's config names, and a turn of Codex's on the name, go as
// they came.
func TestCodexMemoryOnACollidingBareNameFollowsTheSession(t *testing.T) {
	fresh(t)
	relay := &relayOfChatGPT{}
	wb := httptest.NewServer(relay)
	t.Cleanup(wb.Close)
	if err := provider.Save(provider.Provider{ID: "wb", Name: "WB", Key: "sk-wb", Models: []string{"gpt-5.6-terra"}, Responses: wb.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	session := relayOn(t, "third")
	was := CodexSideSet
	CodexSideSet = func(string) bool { return false }
	t.Cleanup(func() { CodexSideSet = was })
	s := New()
	if rec := codexCall(t, s, "third/gpt-6-astra", ""); rec.Code != 200 {
		t.Fatalf("the turn: %d %s", rec.Code, rec.Body.String())
	}
	if rec := codexCall(t, s, "gpt-5.6-terra", "memory_consolidation"); rec.Code != 200 {
		t.Fatalf("memory: %d %s", rec.Code, rec.Body.String())
	}
	if reqs := relay.requests(); len(reqs) != 0 {
		t.Fatalf("the provider with the name got the memory (%d requests)", len(reqs))
	}
	// and says so: magpie serves the name, so not "magpie doesn't serve it" (#5413)
	if r := lastRoute(s); r.Moved == nil || r.Moved.Why != movedCodexFollow {
		t.Fatalf("the move: %+v", r.Moved)
	}
	reqs := session.requests()
	if len(reqs) != 2 || modelOf([]byte(reqs[1])) != "gpt-6-astra" {
		t.Fatalf("the session's provider got %d requests, the last on %q", len(reqs), modelOf([]byte(reqs[len(reqs)-1])))
	}

	// a model Codex's config names is the user's own pick: as it came
	CodexSideSet = func(string) bool { return true }
	if rec := codexCall(t, s, "gpt-5.6-terra", "memory_consolidation"); rec.Code != 200 {
		t.Fatalf("memory on the config's model: %d %s", rec.Code, rec.Body.String())
	}
	if reqs := relay.requests(); len(reqs) != 1 || modelOf([]byte(reqs[0])) != "gpt-5.6-terra" {
		t.Fatalf("the provider with the name got %d requests on the config's pick", len(reqs))
	}

	// a turn of Codex's on the name is the user's own pick of it: as it resolves
	if rec := codexCall(t, s, "gpt-5.6-terra", ""); rec.Code != 200 {
		t.Fatalf("a turn on the name: %d %s", rec.Code, rec.Body.String())
	}
	if reqs := relay.requests(); len(reqs) != 2 {
		t.Fatalf("the provider with the name got %d requests, the turn among them", len(reqs))
	}
}

// #5413 (MariaMatrix; mahiro233 on Discord): Codex's config, on another
// computer, named sub2api/gpt-6.1-terra for its memories while the
// conversation ran on sub2api/gpt-6.1-sol, and the Remote magpie the call
// reached, with no Codex config of its own to read, put the memory on the
// conversation's model. A "provider/model" id is never one of Codex's bare
// defaults: it is the user's pick, and goes as it came, with no move.
func TestCodexMemoryOnAQualifiedModelKeepsIt(t *testing.T) {
	fresh(t)
	session, picked := relayOn(t, "third"), relayOn(t, "fourth")
	was := CodexSideSet
	CodexSideSet = func(string) bool { return false }
	t.Cleanup(func() { CodexSideSet = was })
	s := New()
	if rec := codexCall(t, s, "third/gpt-6-astra", ""); rec.Code != 200 {
		t.Fatalf("the turn: %d %s", rec.Code, rec.Body.String())
	}
	for _, kind := range []string{"memory_consolidation", "memgen"} {
		if rec := codexCall(t, s, "fourth/gpt-6-astra", kind); rec.Code != 200 {
			t.Fatalf("%s: %d %s", kind, rec.Code, rec.Body.String())
		}
		if r := lastRoute(s); r.Moved != nil {
			t.Fatalf("%s moved to %q (%s)", kind, r.Moved.To, r.Moved.Why)
		}
	}
	if reqs := session.requests(); len(reqs) != 1 {
		t.Fatalf("the conversation's provider got %d requests, want the turn alone", len(reqs))
	}
	if reqs := picked.requests(); len(reqs) != 2 {
		t.Fatalf("the provider picked for the memories got %d requests", len(reqs))
	}
}
