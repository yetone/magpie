package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// JackyFill on X: Codex's settings in magpie set the service tier every
// request of Codex's goes at, whatever Codex asked: as Codex asks it
// (unset), none (standard), priority (Fast) or ultrafast (Ultrafast).
// Another agent's request goes as it asked, and Ultrafast on a model no
// account lists it on goes as priority. Both of Codex's ways in: with
// magpie as its provider (/v1/responses) and signed in, on the ChatGPT
// backend's route.
func TestCodexServiceTierSet(t *testing.T) {
	tried := tierAccounts(t, nil, nil)
	s := New()
	last := func() string {
		t.Helper()
		got := tried()
		if len(got) == 0 {
			t.Fatal("the ChatGPT backend was asked nothing")
		}
		return got[len(got)-1]
	}
	body := func(model, tier string) string {
		b := `{"model":"` + model + `","stream":true,"input":"ping"`
		if tier != "" {
			b += `,"service_tier":"` + tier + `"`
		}
		return b + "}"
	}
	// through magpie's Codex provider, whose accounts' lists magpie knows
	viaProvider := func(agent, tier string) string {
		t.Helper()
		postFrom(t, s, agent, "/v1/responses", body("codex/gpt-6-luna", tier))
		return last()
	}
	// on the backend's route, as Codex signed in to ChatGPT sends
	viaBackend := func(model, tier string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body(model, tier)))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-plus")
		req.Header.Set("User-Agent", "codex_cli_rs/0.170.0 (Mac OS 26.6.0; arm64) kitty")
		req.Header.Set("originator", "codex_cli_rs")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("backend route: %d %s", rec.Code, rec.Body)
		}
		return strings.SplitN(last(), ":", 2)[1]
	}
	for _, c := range []struct {
		set, asked string
		provider   string // account:tier tried last
		luna, sol  string // the tier sent for a model a plan offers Ultrafast on, and one none does
	}{
		{"", "priority", "acct-plus:priority", "priority", "priority"},
		{"", "", "acct-plus:", "", ""},
		{settings.CodexTierStandard, "priority", "acct-plus:", "", ""},
		{settings.CodexTierStandard, "ultrafast", "acct-plus:", "", ""},
		{"priority", "", "acct-plus:priority", "priority", "priority"},
		{"priority", "ultrafast", "acct-plus:priority", "priority", "priority"},
		{"ultrafast", "", "acct-pro:ultrafast", "ultrafast", "priority"},
		{"ultrafast", "priority", "acct-pro:ultrafast", "ultrafast", "priority"},
	} {
		if err := provider.SetCodexServiceTier(c.set); err != nil {
			t.Fatal(err)
		}
		if got := viaProvider("codex", c.asked); got != c.provider {
			t.Errorf("set %q, Codex asked %q through /v1/responses: tried %s, want %s", c.set, c.asked, got, c.provider)
		}
		if got := viaBackend("gpt-6-luna", c.asked); got != c.luna {
			t.Errorf("set %q, Codex asked %q for gpt-6-luna on the backend's route: sent %q, want %q", c.set, c.asked, got, c.luna)
		}
		if got := viaBackend("gpt-6-sol", c.asked); got != c.sol {
			t.Errorf("set %q, Codex asked %q for gpt-6-sol on the backend's route: sent %q, want %q", c.set, c.asked, got, c.sol)
		}
	}
	// the rule is Codex's alone
	provider.SetCodexServiceTier(settings.CodexTierStandard)
	if got := viaProvider("opencode", "priority"); got != "acct-plus:priority" {
		t.Errorf("OpenCode's priority tried %s", got)
	}
	// a value magpie doesn't know is refused
	if err := provider.SetCodexServiceTier("flex"); err == nil {
		t.Error("flex was taken")
	}
}

// withServiceTier sets, replaces or takes out the top-level tier alone,
// every other byte as it came; a nested service_tier is not the request's.
func TestWithServiceTier(t *testing.T) {
	for _, c := range []struct{ in, tier, want string }{
		{`{"model":"m"}`, "priority", `{"service_tier":"priority","model":"m"}`},
		{`{}`, "priority", `{"service_tier":"priority"}`},
		{` { } `, "priority", ` {"service_tier":"priority" } `},
		{`{"model":"m","service_tier":"ultrafast"}`, "priority", `{"service_tier":"priority","model":"m"}`},
		{`{"model":"m","service_tier":"priority"}`, "priority", `{"model":"m","service_tier":"priority"}`},
		{`{"model":"m","service_tier":"priority"}`, "", `{"model":"m"}`},
		{`{"model":"m"}`, "", `{"model":"m"}`},
		{`{"model":"m","metadata":{"service_tier":"flex"}}`, "", `{"model":"m","metadata":{"service_tier":"flex"}}`},
		{`not json`, "priority", `not json`},
	} {
		if got := string(withServiceTier([]byte(c.in), c.tier)); got != c.want {
			t.Errorf("withServiceTier(%s, %q) = %s, want %s", c.in, c.tier, got, c.want)
		}
	}
}
