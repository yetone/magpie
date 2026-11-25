package provider

import (
	"path/filepath"
	"slices"
	"testing"
)

// A copy of a provider (#268) is added beside it with its keys, balance
// token, Volcengine access key (#1427) and routing, the same key on the same host being what was asked
// for; a key pasted in the form is the copy's own, and a signed-in
// account has no copy.
func TestAddCopy(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	src := Provider{ID: "relay", Name: "Relay", Chat: "https://api.relay.example/v1", Key: "sk-1", KeyName: "main",
		Keys: []KeyAccount{{Name: "spare", Key: "sk-2"}}, Routing: "rotate", BalanceToken: "tok", AccessKeyID: "AK-1", SecretAccessKey: "SK-1", SessionToken: "ST-1",
		Headers: map[string]string{"X-Org": "acme"}, Fallback: []string{"other/m"},
		BalanceURL: "https://api.relay.example/q?key={key}", BalancePath: "balance"}
	if err := Save(src); err != nil {
		t.Fatal(err)
	}
	// the form: what it shows of the provider, the key left blank
	form := Provider{Name: "Relay copy", Chat: src.Chat, Headers: src.Headers, BalanceURL: src.BalanceURL, BalancePath: src.BalancePath, Models: []string{"m"}}
	if _, err := Add(form); err == nil {
		t.Fatal("a plain Add without a key")
	}
	form.Key = "sk-1"
	if _, err := Add(form); err == nil {
		t.Fatal("a plain Add took the same key on the same host")
	}
	form.Key = ""
	id, err := AddCopy(form, "relay")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if id != "relay-copy" || c.Name != "Relay copy" || c.Key != "sk-1" || c.KeyName != "main" || len(c.Keys) != 1 || c.Keys[0].Key != "sk-2" ||
		c.Routing != "rotate" || c.BalanceToken != "tok" || c.AccessKeyID != "AK-1" || c.SecretAccessKey != "SK-1" || c.SessionToken != "ST-1" || !slices.Equal(c.Fallback, src.Fallback) || c.BalanceURL != src.BalanceURL {
		t.Fatalf("copy: %+v", c)
	}
	// again, under the next free id and name, with a key of its own: a
	// different account, so from's balance token is not inherited (it would
	// ask from's account and show its balance twice, the copy's own never).
	form.Key = "sk-9"
	id, err = AddCopy(form, "relay")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := Find(id); id != "relay-copy-2" || c.Name != "Relay copy 2" || c.Key != "sk-9" || len(c.Keys) != 0 || c.BalanceToken != "" {
		t.Fatalf("second copy %s: %+v", id, c)
	}
	if _, err := AddCopy(form, "nope"); err == nil {
		t.Fatal("a copy of nothing")
	}
}
