package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// #3850 (prgrmrwy): magpie runs on a home server, and the office's remote
// magpie card (or DSH's, reading the cards) said "1 reset" with no way to
// use it but walking to the server. POST /v1/magpie/quotas/codex-reset
// spends it there, through the Usage page's own code. A reset can't be
// undone, so only a gateway key with full access spends one: never a
// request with no key, from this computer or a page or app the gateway
// lets in on loopback without one, nor a read-only key or a held one.
func TestCodexResetNeedsAFullKey(t *testing.T) {
	codexSignedIn(t)
	t.Setenv("MAGPIE_ADDR", "")
	b := newResetBackend(t, "acct-1")
	keys, secrets := newCaller(t, "Full", "Read only", "Held")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("limit-key", access.Change{Key: keys[2].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	path := provider.RemoteCodexResetPath + "?user=" + url.QueryEscape("me@example.com")
	do := func(path, from, host, origin, secret string) (int, string) {
		t.Helper()
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = from
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.2 Safari/605.1.15")
		}
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const loop, lan, gw = "127.0.0.1:50123", "192.168.1.9:5000", "127.0.0.1:3425"
	refused := []struct {
		why                        string
		from, host, origin, secret string
	}{
		{"this computer, no key", loop, gw, "", ""},
		{"a sandboxed frame (Origin null), no key", loop, gw, "null", ""},
		{"an app's scheme, no key", loop, gw, "tauri://localhost", ""},
		{"a *.localhost page, no key", loop, gw, "http://evil.localhost:8080", ""},
		{"a rebound page (its own Host), no key", loop, "evil.example:3425", "", ""},
		{"a foreign page, no key", loop, gw, "https://evil.example", ""},
		{"a foreign page with the full key", loop, gw, "https://evil.example", secrets[0]},
		{"another machine, no key", lan, gw, "", ""},
		{"another machine, a wrong key", lan, gw, "", "sk-magpie-wrong"},
		{"a read-only key", lan, gw, "", secrets[1]},
		{"a read-only key, this computer", loop, gw, "", secrets[1]},
		{"a held key", lan, gw, "", secrets[2]},
	}
	for _, c := range refused {
		if code, body := do(path, c.from, c.host, c.origin, c.secret); code < 400 || code == http.StatusNotFound || strings.Contains(body, `"code":"reset"`) {
			t.Errorf("%s: %d %s", c.why, code, body)
		}
		if _, spent := b.seen(); spent != "" {
			t.Fatalf("%s spent a reset of %s", c.why, spent)
		}
	}
	if code, body := do(provider.RemoteCodexResetPath, lan, gw, "", secrets[0]); code != http.StatusBadRequest || !strings.Contains(body, "name the Codex account") {
		t.Fatalf("no account named: %d %s", code, body)
	}
	if code, body := do(path, lan, gw, "", secrets[1]); code != http.StatusForbidden || !strings.Contains(body, ReadOnlyCode) {
		t.Fatalf("read-only key: %d %s", code, body)
	}
	if code, body := do(path, lan, gw, "", secrets[2]); code != http.StatusForbidden || !strings.Contains(body, provider.KeyHeldCode) {
		t.Fatalf("held key: %d %s", code, body)
	}
	if _, spent := b.seen(); spent != "" {
		t.Fatalf("spent before the full key asked: %s", spent)
	}
	// the full key, from another machine: one reset, of the account named
	code, body := do(path, lan, gw, "", secrets[0])
	var out provider.ResetOutcome
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Code != "reset" || out.Windows != 2 {
		t.Fatalf("full key: %d %s", code, body)
	}
	if _, spent := b.seen(); spent != "acct-1" {
		t.Fatalf("spent %q, want acct-1 once", spent)
	}
	// none left: said so, nothing more spent
	code, body = do(path, loop, gw, "", secrets[0])
	if code != 200 || json.Unmarshal([]byte(body), &out) != nil || out.Code != "no_credit" {
		t.Fatalf("none left: %d %s", code, body)
	}
	if _, spent := b.seen(); spent != "acct-1" {
		t.Fatalf("spent %q after none was left", spent)
	}
}

// The office's magpie spends the home server's reset through its remote
// magpie provider (#3850): the card here keeps the remote's resets, and
// using one asks the remote with the provider's key. A read-only key there
// says so in words the card can show.
func TestRemoteCodexResetThroughTheGateway(t *testing.T) {
	codexSignedIn(t)
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetRemoteCardsForTest()
	t.Cleanup(provider.ForgetRemoteCardsForTest)
	b := newResetBackend(t, "acct-1")
	keys, secrets := newCaller(t, "Office", "Viewer")
	if _, err := access.Update("access-key", access.Change{Key: keys[1].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(lanGuard(New().Handler()))
	defer srv.Close()
	ctx := context.Background()
	save := func(secret string) {
		t.Helper()
		if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: secret, Chat: srv.URL}); err != nil {
			t.Fatal(err)
		}
		provider.ForgetRemoteCardsForTest()
	}
	save(secrets[1])
	if _, err := provider.UseRemoteCodexReset(ctx, "office/codex", "me@example.com", ""); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only key: %v", err)
	}
	if _, err := provider.UseRemoteCodexReset(ctx, "office/codex", "", ""); err == nil {
		t.Fatal("no account named was asked")
	}
	if _, err := provider.UseRemoteCodexReset(ctx, "office/bal", "me@example.com", ""); err == nil {
		t.Fatal("a card not Codex's was asked")
	}
	if _, spent := b.seen(); spent != "" {
		t.Fatalf("spent %q before a full key asked", spent)
	}
	save(secrets[0])
	// the remote's card, read there, carries its reset here
	provider.RefreshUsage(ctx, "office", "")
	var card *provider.SubscriptionQuota
	for _, q := range provider.RemoteCards(ctx) {
		if q.Provider == "office/codex" {
			card = &q
		}
	}
	if card == nil || card.Resets == nil || card.Resets.Count != 1 || card.User != "me@example.com" || card.Checkins {
		t.Fatalf("office/codex: %+v", card)
	}
	out, err := provider.UseRemoteCodexReset(ctx, "office/codex", card.User, "")
	if err != nil || out.Code != "reset" {
		t.Fatalf("full key: %+v %v", out, err)
	}
	if _, spent := b.seen(); spent != "acct-1" {
		t.Fatalf("spent %q, want acct-1 once", spent)
	}
	// read again after: the card shows none left
	for _, q := range provider.RemoteCards(ctx) {
		if q.Provider == "office/codex" && q.Resets != nil && q.Resets.Count != 0 {
			t.Fatalf("after the reset: %+v", q.Resets)
		}
	}
}
