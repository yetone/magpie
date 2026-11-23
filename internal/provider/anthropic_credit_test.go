package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// anthropicCreditTooLow is Anthropic's refusal of a key whose Console
// organization has no credit for the request, as its API sends it: 400,
// invalid_request_error, a request_id beside the error.
const anthropicCreditTooLow = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},"request_id":"req_011CXexample"}`

// anthropicAt is a server answering a Messages request with status and
// body, and a model list to anything else, counted as Anthropic's own API
// while the test runs.
func anthropicAt(t *testing.T, own bool, status int, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = rw.Write([]byte(`{"data":[{"type":"model","id":"claude-sonnet-4-5"}],"has_more":false}`))
			return
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	if own {
		host := HostOf(srv.URL)
		anthropicOwnHosts[host] = true
		t.Cleanup(func() { delete(anthropicOwnHosts, host) })
	}
	return srv.URL
}

// avenger on X: an Anthropic API key's test, and Claude Code through it
// refused "Credit balance too low", with no word that the key is paid from
// its Console organization's API credit, or that a Claude Max plan is
// added as the Claude subscription instead. The test's refusal says both,
// after Anthropic's own words.
func TestAnthropicKeyNoCreditTestSaysWhere(t *testing.T) {
	url := anthropicAt(t, true, http.StatusBadRequest, anthropicCreditTooLow)
	p := Provider{ID: "anthropic", Name: "Anthropic", Preset: "anthropic", Anthropic: url, Key: "sk-ant-api03-x"}
	for name, rs := range map[string][]Result{
		"test":        p.Test(context.Background()),
		"test models": p.TestModels(context.Background(), []string{"claude-sonnet-4-5"}),
	} {
		if len(rs) == 0 {
			t.Fatalf("%s: no result", name)
		}
		for _, r := range rs {
			if r.OK {
				t.Fatalf("%s: %+v passed", name, r)
			}
			if !strings.HasPrefix(r.Error, "Your credit balance is too low to access the Anthropic API") {
				t.Fatalf("%s: Anthropic's words not first: %q", name, r.Error)
			}
			if !strings.HasSuffix(r.Error, " — "+AnthropicNoCreditHint) {
				t.Fatalf("%s: no word of where credit comes from: %q", name, r.Error)
			}
		}
	}
}

// The hint is Anthropic's, for a key at Anthropic's own API: a relay that
// passes the same words on speaks of its own upstream, a Claude
// subscription's account isn't paid from a Console organization, and
// another refusal says nothing of credit.
func TestNoCreditHintScopedToAnthropicKeys(t *testing.T) {
	body := []byte(anthropicCreditTooLow)
	key := Provider{ID: "anthropic", Preset: "anthropic", Anthropic: "https://api.anthropic.com", Key: "sk-ant-api03-x"}
	if got := key.NoCreditHint(http.StatusBadRequest, body); got != AnthropicNoCreditHint {
		t.Fatalf("Anthropic key: %q", got)
	}
	if got := key.Explain("Anthropic: x", http.StatusBadRequest, body); got != "Anthropic: x — "+AnthropicNoCreditHint {
		t.Fatalf("Explain: %q", got)
	}
	for name, c := range map[string]struct {
		p      Provider
		status int
		body   string
	}{
		"relay":         {Provider{ID: "relay", Anthropic: "https://relay.example.com", Key: "sk-x"}, http.StatusBadRequest, anthropicCreditTooLow},
		"account":       {Provider{ID: "claude", Anthropic: "https://api.anthropic.com", Account: &Account{User: "a@b.c"}}, http.StatusBadRequest, anthropicCreditTooLow},
		"other refusal": {key, http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: Field required"}}`},
		"other status":  {key, http.StatusTooManyRequests, anthropicCreditTooLow},
		"chat only":     {Provider{ID: "o", Chat: "https://api.anthropic.com/v1", Key: "sk-x"}, http.StatusBadRequest, anthropicCreditTooLow},
	} {
		if got := c.p.NoCreditHint(c.status, []byte(c.body)); got != "" {
			t.Fatalf("%s: hinted %q", name, got)
		}
		if got := c.p.Explain("m", c.status, []byte(c.body)); strings.Contains(got, AnthropicNoCreditHint) {
			t.Fatalf("%s: Explain hinted %q", name, got)
		}
	}
	// a relay's test passes Anthropic's words on as they are
	url := anthropicAt(t, false, http.StatusBadRequest, anthropicCreditTooLow)
	relay := Provider{ID: "relay", Name: "Relay", Anthropic: url, Key: "sk-x"}
	for _, r := range relay.TestModels(context.Background(), []string{"claude-sonnet-4-5"}) {
		if r.OK || strings.Contains(r.Error, AnthropicNoCreditHint) || !strings.Contains(r.Error, "credit balance is too low") {
			t.Fatalf("relay: %+v", r)
		}
	}
}

// The hint names where a Claude plan goes, and makes no promise of what
// it includes.
func TestNoCreditHintPromisesNothing(t *testing.T) {
	for _, w := range []string{"$", "include", "monthly", "free", "claim"} {
		if strings.Contains(strings.ToLower(AnthropicNoCreditHint), w) {
			t.Fatalf("hint says %q: %s", w, AnthropicNoCreditHint)
		}
	}
}
