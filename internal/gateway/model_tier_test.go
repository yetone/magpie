package gateway

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kdajenks on X: the speed given to a model in its Model settings is sent
// on every agent's requests for it that have no fast pick of their own,
// Ultrafast first to the account that offers it and priority to one that
// doesn't; a group's member is sent as the group says.
func TestModelTierSentToEveryAgent(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	send := func(agent string) string {
		t.Helper()
		postFrom(t, s, agent, "/v1/responses", `{"model":"codex/gpt-6-luna","input":"ping"}`)
		return strings.Join(tried(), ",")
	}
	if got := send("omp"); got != "acct-plus:" {
		t.Fatalf("no tier set: tried %s", got)
	}
	if err := provider.SetModelTier("codex/gpt-6-luna", "ultrafast"); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"omp", "opencode"} {
		if got := send(agent); got != "acct-pro:ultrafast" {
			t.Errorf("%s: ultrafast tried %s", agent, got)
		}
	}
	proOut = true
	if got := send("omp"); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Errorf("pro out: tried %s", got)
	}
	proOut = false
	if err := provider.SetModelTier("codex/gpt-6-luna", "fast"); err != nil {
		t.Fatal(err)
	}
	if got := send("omp"); got != "acct-plus:priority" {
		t.Errorf("fast tried %s", got)
	}
	if err := provider.SetModelTier("codex/gpt-6-luna", "usual"); err != nil {
		t.Fatal(err)
	}
	if got := send("omp"); got != "acct-plus:" {
		t.Errorf("usual again tried %s", got)
	}
}

// A group's member is sent as the group says: Ultrafast to the account that
// offers it, whatever tier the model's own settings give it.
func TestGroupMemberUltrafast(t *testing.T) {
	proOut := false
	tried := tierAccounts(t, &proOut, nil)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	if err := provider.SaveGroup(provider.Group{ID: "u", Name: "U", Members: []string{"codex/gpt-6-luna"}, Fast: []string{"codex/gpt-6-luna"}, Ultra: []string{"codex/gpt-6-luna"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	send := func() string {
		t.Helper()
		postFrom(t, s, "omp", "/v1/responses", `{"model":"group/u","input":"ping"}`)
		return strings.Join(tried(), ",")
	}
	if got := send(); got != "acct-pro:ultrafast" {
		t.Errorf("ultra member tried %s", got)
	}
	proOut = true
	if got := send(); got != "acct-pro:ultrafast,acct-plus:priority" {
		t.Errorf("pro out: tried %s", got)
	}
	proOut = false
	if err := provider.SaveGroup(provider.Group{ID: "u", Name: "U", Members: []string{"codex/gpt-6-luna"}, Fast: []string{"codex/gpt-6-luna"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if got := send(); got != "acct-plus:priority" {
		t.Errorf("fast member tried %s", got)
	}
}
