package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A ChatGPT account that runs out of its allowance after a magpie model was
// picked beside its sign-in leaves the Codex app sending nothing, a magpie
// model's turn included (#540): Sync makes magpie Codex's provider then, as
// a pick does for an account already out, and puts magpie back beside the
// sign-in once the allowance is back. A provider the user asked for (api)
// or one for a Codex not signed in to ChatGPT stays.
func TestCodexRunsOutAfterPick(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("beside the sign-in:\n%s", cfg)
	}
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("used up:\n%s", cfg)
	}
	if err := cx.Sync(); err != nil { // still out: stays
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("used up, synced again:\n%s", cfg)
	}
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("allowance back:\n%s", cfg)
	}

	// magpie as the provider because the user asked for it stays so
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("api:\n%s", cfg)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("api, allowance there, synced:\n%s", cfg)
	}
}

// Kept on ChatGPT (Always ChatGPT), a ChatGPT account that runs out leaves
// magpie beside the sign-in: Codex isn't moved to magpie as its provider,
// by a pick or by Sync, and one moved before the user chose it is put back
// beside it at once.
func TestCodexKeptOnChatGPT(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	codexUsedUp = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("ChatGPT, used up: magpie its provider:\n%s", cfg)
	}

	beside := func(when string) {
		t.Helper()
		if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
			!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
			t.Fatalf("%s:\n%s", when, cfg)
		}
	}
	if err := cx.Field("login").Set("chatgpt"); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up, synced")
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up, picked")
	if got := cx.Field("login").Get(); got != "chatgpt" {
		t.Fatalf("login %q", got)
	}

	// back to ChatGPT, it is moved again while the account is out
	if err := cx.Field("login").Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("ChatGPT again, used up:\n%s", cfg)
	}
}

// The same on one of Codex's own models picked beside the sign-in
// (MasterYi on Discord: the Codex app greyed Send on a used-up account,
// and the third-party models it was joined with couldn't be used either).
// Sync makes magpie Codex's provider on that model as magpie serves it,
// and once the account has room puts Codex back on the one picked, beside
// its sign-in.
func TestCodexOwnModelGoesThroughMagpieWhileUsedUp(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	// the Codex subscription falls back to another provider while the
	// account is out: magpie has something to serve the turn on
	if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, `model = "gpt-a"`) {
		t.Fatalf("joined:\n%s", cfg)
	}
	if cx.AppOut() {
		t.Fatal("joined with room: AppOut")
	}
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, `model = "codex/gpt-a"`) {
		t.Fatalf("used up:\n%s", cfg)
	}
	if !cx.AppOut() {
		t.Fatal("used up, magpie its provider: not AppOut")
	}
	if err := cx.Sync(); err != nil { // still out: stays
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model = "codex/gpt-a"`) {
		t.Fatalf("used up, synced again:\n%s", cfg)
	}
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-a"`) {
		t.Fatalf("allowance back:\n%s", cfg)
	}
	if cx.AppOut() || !cx.Joined() {
		t.Fatalf("allowance back: AppOut %v, Joined %v", cx.AppOut(), cx.Joined())
	}

	// a model the user picked while out stays theirs when room comes back
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("picked while out, allowance back:\n%s", cfg)
	}
}

// One ChatGPT account, out, on one of Codex's own models joined beside the
// sign-in, and nothing else in magpie to serve that model on: no fallback
// on the Codex subscription and no other account (xiaoq6285 on Discord:
// a Plus account in the Windows Codex app, the menu down to "magpie" with
// no quota after the five-hour limit, the app erroring and restarting at
// the limit and again at the reset). magpie leaves Codex on its sign-in:
// every turn it took over would be refused on that same account, so the
// app keeps the account, its usage and its reset, and nothing restarts.
func TestCodexOwnModelOneSpentAccountStaysOnItsSignIn(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	if codexOwnViaMagpie("gpt-a") == "" {
		t.Fatal("magpie lists no codex/gpt-a: the test proves nothing")
	}
	route, _, known := codexAppRoute()
	if !known {
		t.Fatal("route not known")
	}
	// the route is what the app's app-server is ended over
	// (KeepCodexAppOnItsProvider): unchanged, the app keeps running
	same := func(when string) {
		t.Helper()
		cfg := read()
		if !strings.Contains(cfg, `model = "gpt-a"`) || strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, "openai_base_url") {
			t.Fatalf("%s: Codex moved off its sign-in:\n%s", when, cfg)
		}
		if r, out, _ := codexAppRoute(); r != route || out || cx.AppOut() {
			t.Fatalf("%s: the app's app-server would be ended (out %v):\n%s\nwas:\n%s", when, out, r, route)
		}
	}
	codexUsedUp = func() bool { return true }
	for _, when := range []string{"used up", "used up, synced again"} {
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		same(when)
	}
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	same("reset")
	if !cx.Joined() {
		t.Fatal("reset: not joined")
	}
}

// Kept on ChatGPT, a used-up account on one of Codex's own models leaves
// Codex as it is: the user chose the sign-in.
func TestCodexOwnModelKeptOnChatGPT(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	if err := cx.Field("login").Set("chatgpt"); err != nil {
		t.Fatal(err)
	}
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, `model = "gpt-a"`) {
		t.Fatalf("kept on ChatGPT, used up:\n%s", cfg)
	}
	if cx.AppOut() {
		t.Fatal("kept on ChatGPT: AppOut")
	}
}

// Moved onto magpie on one of Codex's own models while the account is
// used up, then kept on ChatGPT from the Agents row's takeover line
// (eddie.zhan on Discord: the Codex app's browser use said it had no
// permission): Codex goes back on its sign-in at once, on the model it
// picked, beside it as joined, and no later Sync moves it again.
func TestCodexOwnModelOutThenKeptOnChatGPT(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-a\"\n")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"),
		[]byte(`{"models":[{"slug":"gpt-a","display_name":"A","priority":1}]}`), 0o644)
	cx := codex(home)
	if ok, err := cx.Join(); err != nil || !ok {
		t.Fatalf("join: %v %v", ok, err)
	}
	// a fallback for magpie to serve the turn on (TestCodexOwnModelOneSpentAccountStaysOnItsSignIn)
	if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"fake/m1"}}); err != nil {
		t.Fatal(err)
	}
	codexUsedUp = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if !cx.AppOut() {
		t.Fatalf("used up: not AppOut:\n%s", read())
	}
	if err := cx.Field("login").Set("chatgpt"); err != nil {
		t.Fatal(err)
	}
	onSignIn := func(when string) {
		t.Helper()
		if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") || !strings.Contains(cfg, `model = "gpt-a"`) {
			t.Fatalf("%s:\n%s", when, cfg)
		}
		if cx.AppOut() {
			t.Fatalf("%s: AppOut", when)
		}
	}
	onSignIn("kept on ChatGPT while used up")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	onSignIn("kept on ChatGPT, synced")
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	onSignIn("kept on ChatGPT, allowance back")
}
