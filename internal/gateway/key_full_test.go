package gateway

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// GET /v1/magpie/limit says "full" for a caller that may use all of this
// magpie, as its owner does: this computer, the sharing key, a key held to
// nothing. A key that gives someone a part of it is not, whatever the part
// (starballl on X: a magpie added as a Remote magpie with a full key is the
// user's own hub, and takes its MCP servers, skills and prompts at once).
func TestKeyLimitSaysFull(t *testing.T) {
	codexSignedIn(t, "spare@example.com") // an account to hold a key to
	keys, secrets := newCaller(t, "Mine", "Budget", "Models", "Accounts", "MCP", "Reader", "Capped", "Issuer")
	byName := map[string]string{}
	for _, k := range keys {
		byName[k.Name] = k.ID
	}
	for _, c := range []struct {
		action string
		name   string
		in     access.Change
	}{
		{"limit-key", "Budget", access.Change{Limit: &access.Limit{Period: "day", Tokens: 1000}}},
		{"models-key", "Models", access.Change{Models: []string{"plan/m1"}}},
		{"accounts-key", "Accounts", access.Change{Accounts: []string{"codex/spare@example.com"}}},
		{"mcp-key", "MCP", access.Change{MCP: []string{"github"}}},
		{"access-key", "Reader", access.Change{ReadOnly: true}},
		{"concurrency-key", "Capped", access.Change{MaxConcurrency: 2}},
		{"issuer-key", "Issuer", access.Change{Issues: true}},
	} {
		c.in.Key = byName[c.name]
		if _, err := access.Update(c.action, c.in); err != nil {
			t.Fatal(c.action, err)
		}
	}
	if err := access.ConfigureLAN(true, false); err != nil { // shared, as a hub is
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	issued := issuedFrom(t, keysCall(h, "POST", KeysPath, fromThere, secrets[7], `{"ttl":"1h"}`))

	full := func(from, secret string) (bool, int, string) {
		w := keysCall(h, "GET", "/v1/magpie/limit", from, secret, "")
		var got struct {
			Full bool `json:"full"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		return got.Full, w.Code, w.Body.String()
	}
	for _, c := range []struct {
		what, from, secret string
	}{
		{"a key held to nothing, from another machine", fromThere, secrets[0]},
		{"a key held to nothing, from this computer", fromHere, secrets[0]},
		{"this computer with no key", fromHere, ""},
		{"the sharing key", fromThere, access.LANSecret()},
		{"a key that issues keys", fromThere, secrets[7]},
	} {
		if ok, code, body := full(c.from, c.secret); code != http.StatusOK || !ok {
			t.Errorf("%s: not full: %d %s", c.what, code, body)
		}
	}
	for _, c := range []struct {
		what, secret string
	}{
		{"a key with a budget", secrets[1]},
		{"a key held to models", secrets[2]},
		{"a key held to accounts", secrets[3]},
		{"a key held to MCP servers", secrets[4]},
		{"a read-only key", secrets[5]},
		{"a key with a cap on requests at once", secrets[6]},
		{"an issued key", issued.Key},
	} {
		if ok, code, body := full(fromThere, c.secret); ok {
			t.Errorf("%s: full: %d %s", c.what, code, body)
		}
	}
}
