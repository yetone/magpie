package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// SSNQT on X: OpenBot's requests were listed as OpenCode's. OpenBot hands
// magpie's endpoint to the OpenCode it runs, keyless (its key and headers
// are sealed where magpie can't write), so the request carries OpenCode's
// User-Agent and nothing of OpenBot's. Its address names it instead:
// /agent/openbot/v1/… is served as /v1/… from OpenBot. A path naming no
// agent is left as it is.
func TestAgentInPath(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	// package agent lists the agents (usage.Agents), and isn't linked here
	was := usage.PluginAgents
	usage.PluginAgents = func() []usage.Known {
		return []usage.Known{{ID: "openbot", Names: []string{"openbot"}}, {ID: "opencode", Names: []string{"opencode"}, UA: []string{"opencode"}}}
	}
	t.Cleanup(func() { usage.PluginAgents = was })
	var seen *http.Request
	h := lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r }))
	call := func(path string) *http.Request {
		seen = nil
		r := httptest.NewRequest("POST", "http://127.0.0.1:3425"+path, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("User-Agent", "opencode/1.14.20 ai-sdk/provider-utils/3.0.20 runtime/bun/1.3.13")
		h.ServeHTTP(httptest.NewRecorder(), r)
		if seen == nil {
			t.Fatalf("%s: not served", path)
		}
		return seen
	}
	r := call("/agent/openbot/v1/chat/completions?x=1")
	if r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "x=1" || r.RequestURI != "/v1/chat/completions?x=1" {
		t.Errorf("served at %q (%q)", r.URL.Path, r.RequestURI)
	}
	if got := agentOf(r); got != "openbot" {
		t.Errorf("agent %q, want openbot", got)
	}
	if startedByAgent(r, "opencode") {
		t.Error("a request its path names counts as one an agent started")
	}
	if got := agentOf(call("/v1/chat/completions")); got != "opencode" {
		t.Errorf("without the path: agent %q, want opencode", got)
	}
	for _, p := range []string{"/agent/nobody/v1/models", "/agent//v1/models", "/agent/"} {
		if r := call(p); r.URL.Path != p || agentOf(r) != "opencode" {
			t.Errorf("%s: served at %q as %q", p, r.URL.Path, agentOf(r))
		}
	}
	if got := AgentURL("http://127.0.0.1:3425/", "openbot"); got != "http://127.0.0.1:3425/agent/openbot" {
		t.Errorf("AgentURL: %q", got)
	}
}
