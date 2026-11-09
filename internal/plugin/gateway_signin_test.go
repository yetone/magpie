package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitLines waits for path to hold n lines, and fails past ten seconds.
func waitLines(t *testing.T, path string, n int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(path)
		if strings.Count(string(b), "\n") >= n {
			return string(b)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lines not written: %q", n, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A request hook's onSignIn hears of an account to sign in again
// (shannnnnks on X): a hook that exports only onSignIn loads, gets the
// event with its own options, and one that throws holds up none after it.
// A hook with onRequest only is untouched, and requests still pass the
// sign-in-only hook by.
func TestGatewaySignInHook(t *testing.T) {
	sandbox(t)
	out := filepath.Join(t.TempDir(), "signins.jsonl")
	gatewayFixture(t, `export function onSignIn(){ throw new Error("first fails") }`)
	only := gatewayFixture(t, `import fs from "node:fs";
export function onSignIn(ev, ctx) {
  fs.appendFileSync(ctx.options.out, JSON.stringify({ev, keys: Object.keys(ctx).sort(), aborted: ctx.signal.aborted}) + "\n");
}`)
	if err := SetOptions(only, map[string]any{"out": out}); err != nil {
		t.Fatal(err)
	}
	gatewayFixture(t, `export function onRequest(body){ return {...body, note: "kept"} }`)
	loaded, err := Plugins(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range loaded {
		if p.Error != "" {
			t.Fatalf("a hook failed to load: %+v", p)
		}
	}
	GatewaySignIn(t.Context(), SignInEvent{Provider: "codex", ProviderName: "Codex", Account: "me@example.com", State: "signed-out"})
	got := waitLines(t, out, 1)
	want := `{"ev":{"provider":"codex","providerName":"Codex","account":"me@example.com","state":"signed-out"},"keys":["options","signal"],"aborted":false}`
	if strings.TrimSpace(got) != want {
		t.Fatalf("onSignIn got\n%s\nnot\n%s", got, want)
	}
	body := gatewayRequest(t.Context(), []byte(`{"note":"original"}`), GatewayContext{})
	if !strings.Contains(string(body), `"kept"`) {
		t.Fatalf("a sign-in-only hook stopped the request hooks: %s", body)
	}
}
