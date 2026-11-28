package provider

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// What a host says of a sign-in whose provider no listing names yet
// (plugin.OnSignIn's handler) waits for the plugins' listing rather than
// asking for one of its own: it called plugin.Providers, a listing of
// every plugin outside the pace the host's changes are listed at
// (c83193bd4), for each thing said (#8064). Counted in the models hook's
// asks, not in time: with the listing answered nothing is asked, and
// with it not answered yet the callers share one listing. Red on the old
// code: 5 asks, then 6.
func TestSignInSaidOfAnUnlistedProviderIsNotListedAgain(t *testing.T) {
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	spec := filepath.Join(dir, "plugin.mjs")
	js := fmt.Sprintf(`import { appendFileSync } from "node:fs"
export default async () => ({
  config(c) { c.provider ??= {}; c.provider.counted = { name: "Counted", models: { "c-1": { name: "C One" } } } },
  auth: { provider: "counted", methods: [{ type: "api", label: "API key" }] },
  provider: { id: "counted", models: async (p) => { appendFileSync(%s, "x\n"); return p.models } },
})
`, strconv.Quote(asked))
	if err := os.WriteFile(spec, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plugin.AuthPath(), []byte(`{"counted":{"type":"api","key":"sk-counted-123456789"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	plugin.Refreshed()
	plugin.Told()
	count := func() int {
		b, _ := os.ReadFile(asked)
		return bytes.Count(b, []byte("\n"))
	}
	if count() == 0 {
		t.Fatal("the models hook was never asked: nothing here counts listings")
	}

	// the listing kept is the plugins' answer: told five times of a
	// provider it doesn't name, nothing is asked
	before := count()
	for range 5 {
		signInToldByHost("ghost", "ghost", "kept")
	}
	plugin.Refreshed()
	if n := count() - before; n != 0 {
		t.Fatalf("five sign-ins said of a provider not listed asked the models hook %d times, want 0", n)
	}

	// the plugins not answered yet (forgotten, as after a change): five
	// told at once wait for one listing between them
	plugin.UseCached(nil)
	before = count()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signInToldByHost("ghost", "ghost", "kept")
		}()
	}
	wg.Wait()
	plugin.Refreshed()
	if n := count() - before; n != 1 {
		t.Fatalf("five sign-ins said while the plugins were not answered asked the models hook %d times, want 1", n)
	}
	if !plugin.Answered() {
		t.Fatal("the listing they waited for was not kept")
	}
}
