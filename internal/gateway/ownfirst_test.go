package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// ownFirstHome: Codex signed in to ChatGPT, whose backend serves
// gpt-6-luna (tierAccounts), and a relay first in the Providers order that
// serves gpt-6-luna too, and glm-6 with a second relay. It returns the
// requests each relay answered.
func ownFirstHome(t *testing.T) (codexTried func() []string, relayHits func() int64) {
	t.Helper()
	tried := tierAccounts(t, nil, nil)
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hits.Add(1)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r2","model":"gpt-6-luna"}}`,
			`data: {"type":"response.output_text.delta","delta":"relay"}`,
			`data: {"type":"response.completed","response":{"id":"r2","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-6-luna", "glm-6"}},
		{ID: "relay2", Name: "Relay 2", Key: "k", Responses: up.URL + "/v1", Models: []string{"glm-6"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetOrder([]string{"relay", "relay2"}); err != nil {
		t.Fatal(err)
	}
	return tried, func() int64 { return hits.Swap(0) }
}

func postCodex(s *Server, model string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+TokenFor("codex"))
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// freefcw on X: Codex asked for gpt-6-astra by its bare name, which its
// own ChatGPT subscription serves, and magpie sent it to Devin, through
// the group it found (on) or as the first provider in the order (off). A
// bare name Codex's own subscription serves now goes there, found groups
// on or off.
func TestBareNameGoesToTheAgentsOwnSubscription(t *testing.T) {
	codexTried, relayHits := ownFirstHome(t)
	s := New()
	for _, on := range []bool{true, false} {
		if err := provider.SetAutoGroups(on); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			rec := postCodex(s, "gpt-6-luna")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
				t.Fatalf("groups on %v: %d %s", on, rec.Code, rec.Body)
			}
			if n := relayHits(); n != 0 {
				t.Fatalf("groups on %v: the relay was asked %d times", on, n)
			}
			if len(codexTried()) == 0 {
				t.Fatalf("groups on %v: Codex's own account never asked", on)
			}
		}
	}
}

// Off means off: a bare name two other vendors serve, none of them Codex's
// own subscription, is refused for Codex with what to ask for instead,
// rather than sent to the first of them. Named by its provider, it goes.
func TestBareNameOnSeveralVendorsRefusedWhileGroupsOff(t *testing.T) {
	_, relayHits := ownFirstHome(t)
	s := New()
	if err := provider.SetAutoGroups(false); err != nil {
		t.Fatal(err)
	}
	rec := postCodex(s, "glm-6")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "relay/glm-6") || !strings.Contains(rec.Body.String(), "magpie group auto on") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if n := relayHits(); n != 0 {
		t.Fatalf("sent anyway, %d times", n)
	}
	if rec := postCodex(s, "relay2/glm-6"); rec.Code != 200 || relayHits() != 1 {
		t.Fatalf("named: %d %s", rec.Code, rec.Body)
	}
	// on, the found group has it
	if err := provider.SetAutoGroups(true); err != nil {
		t.Fatal(err)
	}
	if rec := postCodex(s, "glm-6"); rec.Code != 200 || relayHits() != 1 {
		t.Fatalf("on: %d %s", rec.Code, rec.Body)
	}
}

// A found group magpie set the agent to (SetToGroup), which Codex asks
// for by its model's bare name (#750), stays the group's.
func TestBareNameOfAGroupSetOnTheAgentStaysTheGroups(t *testing.T) {
	ownFirstHome(t)
	old := SetToGroup
	t.Cleanup(func() { SetToGroup = old })
	var asked []string
	SetToGroup = func(agent, gid string) bool {
		asked = append(asked, agent+" "+gid)
		return gid == "group/auto-gpt-6-luna"
	}
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	if to, ok := ownFirst(r, "codex", "gpt-6-luna"); ok {
		t.Fatalf("set to the group, sent to %s", to)
	}
	if len(asked) != 1 || asked[0] != "codex group/auto-gpt-6-luna" {
		t.Fatalf("asked %v", asked)
	}
	SetToGroup = func(string, string) bool { return false }
	if to, ok := ownFirst(r, "codex", "gpt-6-luna"); !ok || to != "codex/gpt-6-luna" {
		t.Fatalf("not set to it: %q %v", to, ok)
	}
}
