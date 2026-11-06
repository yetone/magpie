package cli

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
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
		{"limit", id, "day", "--cost"}, {"limit", id, "day", "--what"}, {"limit", "missing", "day", "--tokens", "5"}, {"limit", id, "off", "extra"},
		{"limit", id, "0d", "--tokens", "5"}, {"limit", id, "3651d", "--tokens", "5"}, {"limit", id, "xd", "--tokens", "5"}, {"limit", id, "days", "--tokens", "5"}, {"limit", id, "reset", "extra"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	// every 10 days (#1509), and Reset
	if got, err = call("limit", id, "10d", "--cost", "800"); err != nil || !strings.Contains(got, "this 10-day cycle") {
		t.Fatal(got, err)
	}
	keys, _ = access.List()
	if l := keys[0].Limit; l == nil || l.Period != "days" || l.Days != 10 || l.Cost != 800 || l.Since.IsZero() {
		t.Fatalf("limit kept as %+v", keys[0].Limit)
	}
	if got, _ := call("list"); !strings.Contains(got, "$0.00/$800.00 every 10 days") {
		t.Fatal(got)
	}
	since := keys[0].Limit.Since
	if got, err = call("limit", id, "reset"); err != nil || !strings.Contains(got, "this 10-day cycle") {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Limit.Since.Before(since) || keys[0].Limit.Cost != 800 {
		t.Fatalf("reset kept %+v", keys[0].Limit)
	}
	// every 5 hours (#2102): a new cycle from now, then Reset starts another
	if got, err = call("limit", id, "5h", "--tokens", "200k"); err != nil || !strings.Contains(got, "this 5-hour cycle") {
		t.Fatal(got, err)
	}
	keys, _ = access.List()
	if l := keys[0].Limit; l == nil || l.Period != "hours" || l.Hours != 5 || l.Days != 0 || l.Tokens != 200_000 || l.Since.IsZero() || l.Since.Before(since) {
		t.Fatalf("limit kept as %+v", keys[0].Limit)
	}
	if st := budget.Of(keys[0], keys[0].Limit.Since.Add(time.Minute)); st == nil || st.Reset.Sub(st.Start) != 5*time.Hour || st.Hours != 5 {
		t.Fatalf("a 5-hour key's window: %+v", st)
	}
	if got, _ := call("list"); !strings.Contains(got, "0/200000 tokens every 5 hours") {
		t.Fatal(got)
	}
	if got, err = call("limit", id, "reset"); err != nil || !strings.Contains(got, "this 5-hour cycle") {
		t.Fatal(got, err)
	}
	for _, args := range [][]string{{"limit", id, "0h", "--tokens", "5"}, {"limit", id, "8761h", "--tokens", "5"}, {"limit", id, "xh", "--tokens", "5"},
		{"limit", id, "1.5h", "--tokens", "5"}, {"limit", id, "hours", "--tokens", "5"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	// month ends in an h, and is the month, not a count of hours
	if got, err = call("limit", id, "month", "--cost", "20"); err != nil || !strings.Contains(got, "this month") {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Limit == nil || keys[0].Limit.Period != "month" || keys[0].Limit.Cost != 20 {
		t.Fatalf("limit kept as %+v", keys[0].Limit)
	}
	if got, err = call("limit", id, "add", "week", "--tokens", "1m"); err != nil || !strings.Contains(got, "Limit 2") {
		t.Fatal(got, err)
	}
	if got, err := call("limit", id, "off"); err != nil || !strings.Contains(got, "no limit") {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Limit != nil {
		t.Fatal("off kept", keys[0].Limit)
	}
	if _, err := call("limit", id, "reset"); err == nil {
		t.Fatal("reset a key with no limit")
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

// magpie gateway-key limit adds, lists, changes and removes a key's
// several limits (#2227); the single-limit form still sets an only limit.
func TestGatewayKeySeveralLimitsCLI(t *testing.T) {
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
	if _, err := call("add", "Team"); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[0].ID
	limits := func() []*access.Limit {
		keys, _ := access.List()
		return keys[0].Limits()
	}
	if _, err := call("limit", id, "add", "5h", "--cost", "10"); err != nil {
		t.Fatal(err)
	}
	got, err := call("limit", id, "add", "week", "--cost", "50")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Limit 1", "every 5 hours", "Limit 2", "per week", "this 5-hour cycle", "this week", "$10.00", "$50.00"} {
		if !strings.Contains(got, want) {
			t.Fatalf("two limits show %q, without %q", got, want)
		}
	}
	if list, err := call("limit", id, "list"); err != nil || list != got {
		t.Fatal("list:", list, err)
	}
	// the plain form changes the one with that window
	if _, err := call("limit", id, "week", "--cost", "60"); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); len(ls) != 2 || ls[1].Cost != 60 || ls[0].Cost != 10 {
		t.Fatalf("after changing the week: %+v %+v", ls[0], ls[1])
	}
	for _, args := range [][]string{{"limit", id, "day", "--tokens", "5"}, {"limit", id, "add", "week", "--tokens", "5"}, {"limit", id, "remove", "3"},
		{"limit", id, "remove"}, {"limit", id, "remove", "x"}, {"limit", id, "reset", "0"}, {"limit", id, "list", "extra"}, {"limit", id, "add"}} {
		if _, err := call(args...); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if ls := limits(); len(ls) != 2 {
		t.Fatalf("a refused command left %d limits", len(ls))
	}
	if _, err := call("limit", id, "reset", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := call("limit", id, "remove", "1"); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); len(ls) != 1 || ls[0].Period != "week" {
		t.Fatalf("after removing the first: %+v", ls)
	}
	if got, err := call("limit", id); err != nil || strings.Contains(got, "Limit 1") || !strings.Contains(got, "this week") {
		t.Fatal("one limit shows", got, err)
	}
	// the single form, with one limit, replaces it as before
	if _, err := call("limit", id, "day", "--tokens", "5"); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); len(ls) != 1 || ls[0].Period != "day" {
		t.Fatalf("the single form: %+v", ls)
	}
	if _, err := call("limit", id, "remove", "1"); err != nil {
		t.Fatal(err)
	}
	if got, err := call("limit", id); err != nil || !strings.Contains(got, "no limit") {
		t.Fatal(got, err)
	}
}

// #2216: whether a key held to a budget, models or accounts is told the
// quotas of what it may use; off, as every key was (#1981), it is told none.
func TestGatewayKeyQuotasCLI(t *testing.T) {
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
	off, on := "Phone: told no quotas when held to a budget, models or accounts\n", "Phone: told the quotas of the providers and accounts it may use\n"
	if got, err := call("quotas", id); err != nil || got != off {
		t.Fatal(got, err)
	}
	if got, err := call("quotas", id, "on"); err != nil || got != on {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); !keys[0].Quotas {
		t.Fatal("not kept on")
	}
	for _, args := range [][]string{{"quotas"}, {"quotas", id, "yes"}, {"quotas", id, "on", "off"}, {"quotas", "missing", "on"}, {"quotas", "missing"}} {
		if _, err := call(args...); err == nil {
			t.Error("accepted", args)
		}
	}
	if keys, _ = access.List(); !keys[0].Quotas {
		t.Fatal("a refused call turned it off")
	}
	if got, err := call("quotas", id, "off"); err != nil || got != off {
		t.Fatal(got, err)
	}
	if keys, _ = access.List(); keys[0].Quotas {
		t.Fatal("not kept off")
	}
}

// magpie gateway-key move puts a key at a place, counted from 1 as list
// shows them, and list shows the keys in that order (alextjy on Discord).
func TestGatewayKeyCLIMove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return out.String(), err
	}
	for _, name := range []string{"Laptop", "Phone", "CI"} {
		if _, err := call("add", name); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := access.List()
	if _, err := call("move", keys[2].ID, "1"); err != nil {
		t.Fatal(err)
	}
	list, err := call("list")
	if err != nil {
		t.Fatal(err)
	}
	ci, laptop, phone := strings.Index(list, "CI"), strings.Index(list, "Laptop"), strings.Index(list, "Phone")
	if ci < 0 || !(ci < laptop && laptop < phone) {
		t.Fatal("list after moving CI to 1:\n" + list)
	}
	for _, bad := range []string{"0", "4", "top"} {
		if _, err := call("move", keys[0].ID, bad); err == nil {
			t.Fatal("place", bad)
		}
	}
	if _, err := call("move", "nope", "1"); err == nil {
		t.Fatal("no such key")
	}
	if _, err := call("move", keys[0].ID); err == nil {
		t.Fatal("no place")
	}
}

// --model makes a limit count only some models (zola_xynb on X), beside
// one of the whole key in the same window; the plain form changes the one
// of those models, and the list says which models each counts.
func TestGatewayKeyScopedLimitCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	call := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return out.String(), err
	}
	if _, err := call("add", "Team"); err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[0].ID
	limits := func() []*access.Limit {
		keys, _ := access.List()
		return keys[0].Limits()
	}
	if _, err := call("limit", id, "month", "--cost", "100"); err != nil {
		t.Fatal(err)
	}
	got, err := call("limit", id, "add", "month", "--cost", "20", "--model", "openai/*")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "openai/*") {
		t.Fatalf("the list says %q", got)
	}
	if _, err := call("limit", id, "add", "month", "--cost", "5", "--models", "anthropic/claude-opus,zai/*", "--model", "kimi/k2"); err != nil {
		t.Fatal(err)
	}
	ls := limits()
	if len(ls) != 3 || ls[0].Scoped() || strings.Join(ls[1].Models, " ") != "openai/*" || strings.Join(ls[2].Models, " ") != "anthropic/claude-opus zai/* kimi/k2" {
		t.Fatalf("kept as %+v %+v %+v", ls[0], ls[1], ls[2])
	}
	// the plain form changes the month of OpenAI, not the key's
	if _, err := call("limit", id, "month", "--cost", "30", "--model", "openai/*"); err != nil {
		t.Fatal(err)
	}
	if ls := limits(); ls[0].Cost != 100 || ls[1].Cost != 30 {
		t.Fatalf("after changing OpenAI's month: %+v %+v", ls[0], ls[1])
	}
	for _, args := range [][]string{
		{"limit", id, "add", "month", "--cost", "5", "--model", "openai/*"}, // the same window and models again
		{"limit", id, "add", "day", "--cost", "5", "--model", "group/coding"},
		{"limit", id, "add", "day", "--cost", "5", "--model"},
		{"limit", id, "add", "day", "--cost", "5", "--model", "gpt-5"},
	} {
		if _, err := call(args...); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if ls := limits(); len(ls) != 3 {
		t.Fatalf("a refused command left %d limits", len(ls))
	}
}
