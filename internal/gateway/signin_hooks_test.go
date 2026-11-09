package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// A ChatGPT account of several whose sign-in OpenAI revoked (shannnnnks
// on X: in gateway mode one Codex account is signed out now and then) is
// told to the request hooks' onSignIn once, with the provider, the
// account's label and "signed-out", and no token; not before a hook is
// installed to hear it, and not again on the next look.
func TestRevokedAccountToldToHooksOnce(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	t.Setenv("MAGPIE_BUN", testenv.Bun(t))
	t.Cleanup(plugin.Settle)
	access := codexAccessToken(t)
	revokedUpstream(t, access)
	if rec := codexPostTo(New(), `{"model":"gpt-5.5","stream":true,"input":"ping"}`); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := codexLapsed(t, "me@example.com"); got == "" {
		t.Fatal("the revoked account isn't marked")
	}

	var mu sync.Mutex
	var told []plugin.SignInEvent
	was := signInToHooks
	signInToHooks = func(ctx context.Context, ev plugin.SignInEvent) {
		mu.Lock()
		told = append(told, ev)
		mu.Unlock()
		was(ctx, ev)
	}
	t.Cleanup(func() { signInToHooks = was })

	// no hook yet: nothing looked at, nothing marked told
	tellHooksOfSignIns(t.Context())
	if len(told) != 0 {
		t.Fatalf("told with no hook: %+v", told)
	}
	if _, err := os.Stat(provider.SignInHooksPath()); !os.IsNotExist(err) {
		t.Fatalf("marked told with no hook: %v", err)
	}

	out := filepath.Join(t.TempDir(), "signins.jsonl")
	hook := filepath.Join(t.TempDir(), "notify.hooks.js")
	os.WriteFile(hook, []byte(`import fs from "node:fs";
export function onSignIn(ev, ctx) { fs.appendFileSync(ctx.options.out, JSON.stringify(ev) + "\n") }`), 0o644)
	if _, err := plugin.Add(t.Context(), hook); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(hook, map[string]any{"out": out}); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	tellHooksOfSignIns(t.Context())
	tellHooksOfSignIns(t.Context())
	mu.Lock()
	n := len(told)
	var first plugin.SignInEvent
	if n > 0 {
		first = told[0]
	}
	mu.Unlock()
	if n != 1 || first.Provider != "codex" || first.Account != "me@example.com" || first.State != "signed-out" || first.ProviderName == "" {
		t.Fatalf("told %d times: %+v", n, first)
	}
	deadline := time.Now().Add(15 * time.Second)
	var b []byte
	for !strings.Contains(string(b), "\n") {
		if time.Now().After(deadline) {
			t.Fatal("the hook never heard of it")
		}
		time.Sleep(20 * time.Millisecond)
		b, _ = os.ReadFile(out)
	}
	if !strings.Contains(string(b), `"account":"me@example.com"`) || !strings.Contains(string(b), `"state":"signed-out"`) {
		t.Fatalf("the hook heard %s", b)
	}
	for _, private := range []string{access, "r-acct", "token_revoked", "invalidated"} {
		if strings.Contains(string(b), private) {
			t.Fatalf("the event carries %q: %s", private, b)
		}
	}
}
