package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// An idle OAuth account whose access token the vendor dropped before the
// expiry it was stored with, while its refresh token still renews, is not
// a sign-in gone (plugins#618, jinpy666: three Copilot slots, the two
// with no traffic said "sign-in has expired — sign in again" though their
// refresh tokens still traded at /copilot_internal/v2/token). The bare
// 401 makes the access due and keeps the account; the next request
// renews it through the plugin's loader and goes through. An account
// whose renewed token is turned away too is marked, and so is one whose
// plugin says its refresh was refused; a sign-in that can't be read when
// the 401 comes is unknown, not marked. A marked account is still one a
// request can be routed to, for the loader to renew on use.
func TestPluginStaleAccessRenewsNotLapses(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	var unreadable func()
	testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch r.Header.Get("Authorization") {
		case "Bearer fresh-r-ok", "Bearer fresh-r-unknown":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			return
		case "Bearer a-unknown":
			unreadable()
		}
		// the access token turned away, as Copilot's API answers one it
		// no longer takes, whatever the stored expiry said
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"unauthorized: token expired","code":"token_expired"}}`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	// the reporter's shape: access stored with an expiry still ahead,
	// refresh good; "r-bad"'s renewed token is turned away too
	accts := []struct{ user, refresh, access string }{
		{"idle@fake", "r-ok", "a"},
		{"bad@fake", "r-bad", "a"},
		{"revoked@fake", "r-revoked", "a"},
		{"unknown@fake", "r-unknown", "a-unknown"},
		{"first@fake", "r-first", "fresh-r-first"},
	}
	keys := map[string]string{}
	var rows []savedLogin
	for _, a := range accts {
		k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": a.refresh, "access": a.access, "expires": float64(time.Now().Add(20 * time.Hour).UnixMilli()), "accountId": a.user})
		if err != nil {
			t.Fatal(err)
		}
		keys[a.user] = k
		rows = append(rows, savedLogin{Agent: "plugin:fakeco", User: a.user, Home: k, On: true})
	}
	saveLogins(t, rows...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	authPath := plugin.AuthPath()
	unreadable = func() {
		for _, p := range []string{authPath, authPath + ".bak"} {
			if err := os.WriteFile(p, []byte("{half"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	stored := func(user string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal(err)
		}
		var all map[string]map[string]any
		if err := json.Unmarshal(b, &all); err != nil {
			t.Fatal(err)
		}
		return all[keys[user]]
	}
	lapsed := func(user string) bool {
		for _, l := range pluginLogins(mustPlugin(t)) {
			if l.User == user {
				return l.Lapsed != ""
			}
		}
		t.Fatalf("no row for %s", user)
		return false
	}
	ask := func(user string) int {
		t.Helper()
		pp := mustPlugin(t)
		req, _ := http.NewRequestWithContext(ctx, "POST", pluginBase+pp.ID+"/v1/chat/completions", strings.NewReader(`{"model":"fake-claude","messages":[{"role":"user","content":"hi"}]}`))
		resp, err := pluginFetch(pp, keys[user], req)
		if err != nil {
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.Header.Get(SignInHeader) != "" {
			t.Errorf("%s: %s went on to the client", user, SignInHeader)
		}
		return resp.StatusCode
	}

	// idle: the first request meets the dropped token, the second renews
	if got := ask("idle@fake"); got != http.StatusUnauthorized {
		t.Fatalf("idle@fake's first request: %d", got)
	}
	if lapsed("idle@fake") {
		t.Fatal("idle@fake was marked for a dropped access token its refresh token renews")
	}
	if a := stored("idle@fake"); a["refresh"] != "r-ok" || a["access"] != "a" || a["expires"] != float64(1) {
		t.Fatalf("idle@fake isn't due for renewal: %v", a)
	}
	if got := ask("idle@fake"); got != http.StatusOK {
		t.Fatalf("idle@fake's second request: %d", got)
	}
	if a := stored("idle@fake"); a["access"] != "fresh-r-ok" || a["refresh"] != "r-ok" {
		t.Fatalf("idle@fake wasn't renewed: %v", a)
	}
	if lapsed("idle@fake") {
		t.Fatal("idle@fake is marked after it renewed")
	}

	// bad: renewed, and the renewed token turned away too
	if got := ask("bad@fake"); got != http.StatusUnauthorized || lapsed("bad@fake") {
		t.Fatalf("bad@fake's first request: %d, lapsed %v", got, lapsed("bad@fake"))
	}
	if got := ask("bad@fake"); got != http.StatusUnauthorized {
		t.Fatalf("bad@fake's second request: %d", got)
	}
	if !lapsed("bad@fake") {
		t.Fatal("bad@fake isn't marked though its renewed token was turned away")
	}
	// a lapsed account is still one to route to: the first of the
	// provider, or one beside it
	routed := slices.ContainsFunc(pluginAlsoOn(mustPlugin(t)), func(p Provider) bool { return p.Account != nil && p.Account.pluginKey == keys["bad@fake"] })
	for _, l := range pluginLogins(mustPlugin(t)) {
		routed = routed || l.User == "bad@fake" && l.Active
	}
	if !routed {
		t.Error("bad@fake left the accounts a request is routed to")
	}

	// revoked: the plugin says its refresh was refused, and is believed
	if got := ask("revoked@fake"); got != 0 {
		t.Fatalf("revoked@fake's request went through: %d", got)
	}
	for deadline := time.Now().Add(10 * time.Second); !lapsed("revoked@fake"); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("revoked@fake isn't marked though its plugin said its refresh was refused")
		}
	}

	// unknown: plugin-auth.json can't be read when the 401 comes
	good, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := ask("unknown@fake"); got != http.StatusUnauthorized {
		t.Fatalf("unknown@fake's request: %d", got)
	}
	if err := os.WriteFile(authPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if lapsed("unknown@fake") {
		t.Fatal("unknown@fake was marked while its sign-in couldn't be read")
	}
	if lapsed("first@fake") || lapsed("idle@fake") {
		t.Fatal("an account no request failed was marked")
	}
}
