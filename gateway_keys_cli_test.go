package main

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestGatewayKeyCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...)); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	if got := call("list"); !strings.Contains(got, "GATEWAY KEY") {
		t.Fatal(got)
	}
	secret := call("add", "Remote laptop")
	keys, _ := access.List()
	if len(keys) != 1 {
		t.Fatal(keys)
	}
	id := keys[0].ID
	if who, ok := access.Authenticate(secret); !ok || who.KeyID != id || who.KeyName != "Remote laptop" {
		t.Fatal(who, ok)
	}
	if got := call(); strings.Contains(got, secret) || !strings.Contains(got, id) || !strings.Contains(got, "Remote laptop") {
		t.Fatal("list must mask credentials", got)
	}
	next := call("rotate", id)
	if next == secret {
		t.Fatal("rotation reused the secret")
	}
	if _, ok := access.Authenticate(secret); ok {
		t.Fatal("old credential works after rotation")
	}
	if who, ok := access.Authenticate(next); !ok || who.KeyID != id {
		t.Fatal("rotation changed identity", who, ok)
	}
	call("remove", id)
	if _, ok := access.Authenticate(next); ok {
		t.Fatal("removed credential still works")
	}
	for _, args := range [][]string{{"add"}, {"add", ""}, {"list", "extra"}, {"rotate", "missing"}, {"remove", "missing"}, {"unknown", "x"}} {
		if err := gatewayKeysTo(&bytes.Buffer{}, append([]string{"gateway-key"}, args...)); err == nil {
			t.Error("accepted", args)
		}
	}
}

// add --key keeps a value clients already send (love1sbug on X); "-"
// takes it from stdin.
func TestGatewayKeyCLIOwnValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if err := gatewayKeysTo(&out, []string{"gateway-key", "add", "Family", "--key", "cpa-family-key-0042"}); err != nil || strings.TrimSpace(out.String()) != "cpa-family-key-0042" {
		t.Fatal(out.String(), err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("cpa-friend-key-0007\n")
	w.Close()
	stdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = stdin }()
	if err := gatewayKeysTo(&bytes.Buffer{}, []string{"gateway-key", "add", "Friend", "--key", "-"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cpa-family-key-0042", "cpa-friend-key-0007"} {
		if _, ok := access.Authenticate(k); !ok {
			t.Fatal("not a key:", k)
		}
	}
	out.Reset()
	if err := gatewayKeysTo(&out, []string{"gateway-key", "list"}); err != nil || strings.Contains(out.String(), "cpa-family") || !strings.Contains(out.String(), "…0042") {
		t.Fatal("list must mask an own value", out.String(), err)
	}
	for _, args := range [][]string{{"add", "X", "--key"}, {"add", "X", "--key", "short"}, {"add", "X", "--key", "cpa-family-key-0042"}, {"rotate", "x", "--key", "cpa-other-key-0001"}} {
		if err := gatewayKeysTo(&bytes.Buffer{}, append([]string{"gateway-key"}, args...)); err == nil {
			t.Error("accepted", args)
		}
	}
}

func TestGatewayKeyListWithReadOnlyMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-fixture-cli"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
	if err := settings.Save(s); err == nil {
		t.Skip("settings.json remains writable")
	}
	var out bytes.Buffer
	if err := gatewayKeysTo(&out, []string{"gateway-key", "list"}); err != nil || !strings.Contains(out.String(), "Magpie") {
		t.Fatal("read-only migration blocked CLI list", out.String(), err)
	}
}

// magpie gateway-key limit sets, shows and takes off a key's limit, and
// list says what each key has used of its own (#585).
func TestGatewayKeyLimitCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return out.String(), err
	}
	if _, err := call("add", "Phone"); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[0].ID
	if got, err := call("limit", id); err != nil || !strings.Contains(got, "no limit") {
		t.Fatal(got, err)
	}
	got, err := call("limit", id, "week", "--tokens", "2m", "--cost=5", "--cache-reads")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ = access.List()
	if l := keys[0].Limit; l == nil || l.Period != "week" || l.Tokens != 2_000_000 || l.Cost != 5 || !l.CacheReads {
		t.Fatalf("limit kept as %+v", keys[0].Limit)
	}
	for _, want := range []string{"0 used of 2000000, 2000000 left", "cache reads", "$0.00 used of $5.00", "State", "open"} {
		if !strings.Contains(got, want) {
			t.Fatalf("limit shows %q, without %q", got, want)
		}
	}
	if got, _ := call("list"); !strings.Contains(got, "LIMIT") || !strings.Contains(got, "0/2000000 tokens $0.00/$5.00 per week") {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"limit"}, {"limit", id, "hour", "--tokens", "5"}, {"limit", id, "day"}, {"limit", id, "day", "--tokens", "lots"},
		{"limit", id, "day", "--cost"}, {"limit", id, "day", "--what"}, {"limit", "missing", "day", "--tokens", "5"}, {"limit", id, "off", "extra"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	if got, err := call("limit", id, "off"); err != nil || !strings.Contains(got, "no limit") {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Limit != nil {
		t.Fatal("off kept", keys[0].Limit)
	}
}

func TestGatewayKeyModelsCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return out.String(), err
	}
	if _, err := call("add", "Phone"); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[0].ID
	if got, err := call("models", id); err != nil || got != "Phone: every model\n" {
		t.Fatal(got, err)
	}
	if got, err := call("models", id, "openai/gpt-5", "anthropic/*"); err != nil || got != "Phone: only openai/gpt-5, anthropic/*\n" {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); !slices.Equal(keys[0].Models, []string{"openai/gpt-5", "anthropic/*"}) {
		t.Fatal("kept", keys[0].Models)
	}
	if got, _ := call("list"); !strings.Contains(got, "MODELS") || !strings.Contains(got, "openai/gpt-5,anthropic/*") {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"models"}, {"models", id, "gpt-5"}, {"models", "missing", "a/b"}, {"models", "missing"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	if got, err := call("models", id, "all"); err != nil || got != "Phone: every model\n" {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Models != nil {
		t.Fatal("all kept", keys[0].Models)
	}
}

// magpie gateway-key accounts sets, shows and takes off a key's account
// whitelist, and refuses an account no provider has (#905). An account's
// stable id, named by who is signed in, is exercised against a real
// sign-in in the gateway's tests; a key's fingerprint is one here.
func TestGatewayKeyAccountsCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "sk-1", Name: "First"}, {Key: "sk-2", Name: "Second"}}}); err != nil {
		t.Fatal(err)
	}
	k1, k2 := provider.KeyID("sk-1"), provider.KeyID("sk-2")
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return out.String(), err
	}
	if _, err := call("add", "Phone"); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[0].ID
	if got, err := call("accounts", id); err != nil || got != "Phone: every account\n" {
		t.Fatal(got, err)
	}
	// named by the name it is shown by, kept by its fingerprint
	if got, err := call("accounts", id, "relay/First", "relay/"+k2); err != nil || got != "Phone: only relay/First, relay/Second\n" {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); !slices.Equal(keys[0].Accounts, []string{"relay/" + k1, "relay/" + k2}) {
		t.Fatal("kept", keys[0].Accounts)
	}
	if got, _ := call("list"); !strings.Contains(got, "ACCOUNTS") || !strings.Contains(got, "relay/First,relay/Second") {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"accounts"}, {"accounts", id, "sk-1"}, {"accounts", id, "relay/" + provider.KeyID("sk-9")}, {"accounts", "missing", "relay/First"}, {"accounts", "missing"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	if got, err := call("accounts", id, "all"); err != nil || got != "Phone: every account\n" {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Accounts != nil {
		t.Fatal("all kept accounts", keys[0].Accounts)
	}
}
