package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Wing8Aqua on X: a Claude subscription routed By weight, a Max account
// weighing 3 beside Pro ones weighing 1, Stays left at Auto. By weight
// shares out the conversations nobody has answered yet; a conversation
// already answered stays on its account turn after turn while Anthropic
// holds its cache there, as under every other routing.
func TestClaudeWeightedConversationKeepsItsAccount(t *testing.T) {
	claudeCaching(t)
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	p, err := provider.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	p.Routing = provider.Weighted
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountWeight("claude", "b@example.com", 3); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.subscription.abortAll)
	h := s.Handler()
	send := func(session, msgs string) (string, *Affinity) {
		t.Helper()
		body := `{"model":"claude/claude-opus-5-5","max_tokens":100,"tools":[{"name":"read","input_schema":{"type":"object"}}],"messages":` + msgs + `}`
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		r.Header.Set("x-claude-code-session-id", session)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var res struct {
			Content []struct{ Text string } `json:"content"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if rec.Code != 200 || len(res.Content) == 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		tok, _, _ := strings.Cut(res.Content[0].Text, " pid")
		return tok, s.trace.routes[len(s.trace.routes)-1].Affinity
	}
	msg := func(role, text string) string { return `{"role":"` + role + `","content":` + strconv.Quote(text) + `}` }

	began := map[string]int{}
	for i := range 4 {
		ses := "s-" + strconv.Itoa(i)
		conv := msg("user", "task "+ses)
		first, _ := send(ses, `[`+conv+`]`)
		began[first]++
		for turn := range 4 {
			conv += `,` + msg("assistant", "done") + `,` + msg("user", "and "+strconv.Itoa(turn))
			if got, a := send(ses, `[`+conv+`]`); got != first {
				t.Errorf("%s turn %d went to %s, the conversation began on %s (affinity %+v)", ses, turn+2, got, first, a)
			}
		}
	}
	// b weighs 3, a 1: three of four new conversations begin on b
	if began["tok-b"] != 3 || began["tok-a"] != 1 {
		t.Errorf("conversations began %v, want tok-b 3 and tok-a 1", began)
	}
}
