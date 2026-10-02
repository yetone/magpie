package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A model asked for at an effort of its own, "<model>:<level>" — a Claude
// Code tier magpie set one on (#536) — is that model, asked for that effort
// at its nearest level whatever the agent asked, as a group's member fixed
// at one is (#189); the trace says it was fixed. A member's own effort wins
// over it, and a model whose own id ends in a level is taken whole.
func TestModelAskedAtAnEffort(t *testing.T) {
	s, a, b := ruled(t)
	if err := provider.SetModelEfforts("a/small", []string{"low", "medium", "high"}); err != nil {
		t.Fatal(err)
	}
	post := func(model, extra string) (string, Route) {
		t.Helper()
		return postOK(t, s, "", strings.Replace(chat("hi", nil, 0, extra), `"model":"group/r"`, `"model":`+quote(model), 1))
	}
	for _, c := range []struct{ model, asked, want, fixed string }{
		{"a/small:low", "high", "low", "low"},
		{"a/small:low", "", "low", "low"}, // asked for none: given it anyway
		{"a/small:xhigh", "low", "high", "xhigh"},
		{"a/small", "medium", "medium", ""}, // none of its own: as asked
	} {
		extra := ""
		if c.asked != "" {
			extra = `,"reasoning_effort":"` + c.asked + `"`
		}
		out, r := post(c.model, extra)
		sent := sentBody(t, a)
		if !strings.Contains(out, "from ka") || sent["model"] != "small" || sent["reasoning_effort"] != c.want {
			t.Fatalf("%s asked %q: sent %v (%s)", c.model, c.asked, sent, out)
		}
		if r.Model != "a/small" || len(r.Tries) != 1 || r.Tries[0].Fixed != c.fixed || r.Tries[0].Effort != c.want {
			t.Fatalf("%s asked %q: traced %s %+v", c.model, c.asked, r.Model, r.Tries)
		}
	}

	// as Claude Code asks: Anthropic's API, the tier's model marked [1m]
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"a/small:low[1m]","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("anthropic-version", "2023-06-01")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages: %d %s", rec.Code, rec.Body)
	}
	if sent := sentBody(t, a); sent["model"] != "small" || sent["reasoning_effort"] != "low" {
		t.Fatalf("messages sent %v", sent)
	}
	if r := lastRoute(s); r.Model != "a/small[1m]" && r.Model != "a/small" || r.Tries[0].Fixed != "low" {
		t.Fatalf("messages traced %s %+v", r.Model, r.Tries)
	}

	// a group at an effort: its members without one of their own take it,
	// one with its own keeps that
	setMembers(t, []string{"a/small", "b/big"})
	_, r := post("group/r:low", `,"reasoning_effort":"high"`)
	if sent := sentBody(t, a); sent["reasoning_effort"] != "low" || r.Group == nil || r.Tries[0].Fixed != "low" {
		t.Fatalf("group at low: sent %v traced %+v", sent, r.Tries)
	}
	setMembers(t, []string{"a/small:medium", "b/big"})
	_, r = post("group/r:low", `,"reasoning_effort":"high"`)
	if sent := sentBody(t, a); sent["reasoning_effort"] != "medium" || r.Tries[0].Fixed != "medium" {
		t.Fatalf("member's own: sent %v traced %+v", sent, r.Tries)
	}

	// a model whose own id ends in a level is that model, at no effort fixed
	pb, err := provider.Find("b")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "c", Name: "C", Key: "kc", Models: []string{"tiny:high"}, Chat: pb.Chat}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("c", pb.Chat, []catalog.Model{{ID: "tiny:high"}}); err != nil {
		t.Fatal(err)
	}
	_, r = post("c/tiny:high", `,"reasoning_effort":"low"`)
	if sent := sentBody(t, b); sent["model"] != "tiny:high" || sent["reasoning_effort"] != "low" || r.Tries[0].Fixed != "" {
		t.Fatalf("own colon: sent %v traced %+v", sent, r.Tries)
	}
}
