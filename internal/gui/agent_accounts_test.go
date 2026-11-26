package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The editor's Accounts by agent (nb5p on Discord) writes through
// provider/agentaccount: the agent kept by its id, the provider's view
// listing the choices and what is set, and the provider's own save after
// it keeps what was set. A provider of one key offers no choice.
func TestAgentAccountRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://example.invalid/v1", Key: "sk-0",
		Keys: []provider.KeyAccount{{Name: "Work", Key: "sk-work"}}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "single", Name: "Single", Chat: "https://example.invalid/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(action string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(string(b))))
		return w
	}
	work := provider.KeyID("sk-work")
	w := post("agentaccount", map[string]any{"id": "relay", "agent": "claude", "account": work})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var state providersJSON
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	for _, p := range state.Providers {
		switch p.ID {
		case "relay":
			if p.AgentAccounts["claude"] != work || len(p.AgentChoices) != 2 || p.AgentChoices[1].Name != "Work" {
				t.Fatalf("relay's view: %v %+v", p.AgentAccounts, p.AgentChoices)
			}
		case "single":
			if len(p.AgentChoices) != 0 {
				t.Fatalf("one key offers %+v", p.AgentChoices)
			}
		}
	}
	if w := post("agentaccount", map[string]any{"id": "relay", "agent": "claude", "account": "nobody"}); w.Code == 200 {
		t.Fatal("a key the provider hasn't was taken")
	}
	// the editor's save, which carries no choice, keeps it
	if w := post("save", map[string]any{"id": "relay", "from": "relay", "name": "Relay", "chat": "https://example.invalid/v1"}); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if got := provider.AgentAccounts("claude"); len(got) != 1 || got[0] != "relay/"+work {
		t.Fatalf("after save: %v", got)
	}
	if w := post("agentaccount", map[string]any{"id": "relay", "agent": "claude", "account": ""}); w.Code != 200 {
		t.Fatalf("let go: %d %s", w.Code, w.Body)
	}
	if got := provider.AgentAccounts("claude"); len(got) != 0 {
		t.Fatalf("let go: %v", got)
	}
}
