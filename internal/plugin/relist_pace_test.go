package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// A models hook that saves its account's sign-in each time it is asked
// (a token renewed on use, a field kept beside it) has the plugins
// listed once, not again for each save. Each save told magpie "auth",
// which forgot the providers, stopped the listing under way and began
// the next, which saved again: every plugin was listed again without
// end, ~3 times a second, and a plugin whose models hook downloads its
// vendor's catalog (zen-free's models.opencode.ai/api.json) ran 5 GB in
// minutes (JackChen on Discord). Red on the old code: the listing never
// settles, and the hook is asked hundreds of times.
func TestSaveWhileListedListsOnce(t *testing.T) {
	sandbox(t)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	spec := filepath.Join(dir, "plugin.mjs")
	js := fmt.Sprintf(`import { appendFileSync } from "node:fs"
let n = 0
export default async ({ client }) => ({
  config(c) { c.provider ??= {}; c.provider.saver = { name: "Saver", models: { "s-1": { name: "S One" } } } },
  auth: { provider: "saver", methods: [{ type: "api", label: "API key" }] },
  provider: { id: "saver", models: async (p) => {
    appendFileSync(%s, "x\n")
    await client.auth.set({ path: { id: "saver" }, body: { type: "api", key: "sk-saver-123456789", seen: ++n } })
    return p.models
  } },
})
`, strconv.Quote(asked))
	if err := os.WriteFile(spec, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	refreshWriteList(t, spec)
	if err := os.WriteFile(AuthPath(), []byte(`{"saver":{"type":"api","key":"sk-saver-123456789"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	UseCached(nil)

	count := func() int {
		b, _ := os.ReadFile(asked)
		return bytes.Count(b, []byte("\n"))
	}
	Cached()
	done := make(chan struct{})
	go func() { Refreshed(); close(done) }()
	// the bound is for the old code alone, whose listing never ends: a
	// loaded runner has a minute for one listing and the host's start
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatalf("the listing never settled: the models hook was asked %d times", count())
	}
	Told()
	if n := count(); n != 1 {
		t.Fatalf("the models hook was asked %d times for one listing", n)
	}
	// the save went to disk, and what Cached gives reads it
	if b, _ := os.ReadFile(AuthPath()); !bytes.Contains(b, []byte(`"seen"`)) {
		t.Fatalf("the save is not on disk: %s", b)
	}
	if !refreshCacheGood() {
		t.Fatal("the listing was not kept")
	}
}

// What the host tells starts listings at a pace: a host telling of a
// change as each listing ran (a sign-in it saved that is not only a
// save again: an account added, renamed) began the next at once,
// without end. The first relistFree of a streak go at once, and the next
// waits its turn, which magpie's own change cuts short.
func TestHostChangesListAtAPace(t *testing.T) {
	var asks atomic.Int64
	// set before refreshSandbox, so it is put back after the cleanup's
	// Settle, once no listing loop reads it: cleanups run last first, and
	// put back before it, it raced the loop under -race
	oldStep := relistStep
	relistStep = time.Hour // a turn that never comes in the test
	t.Cleanup(func() { relistStep = oldStep })
	refreshSandbox(t, func(ctx context.Context, epoch uint64) error {
		asks.Add(1)
		// the host tells of a sign-in while the plugins are listed
		forgetProvidersBy(true)
		commitProviders([]Provider{{ID: "fakeco", Spec: refreshAbsPath(t, "testdata/fake/index.js"), Name: "FakeCo"}}, &epoch)
		return nil
	})
	waiting := func() bool {
		provMu.Lock()
		defer provMu.Unlock()
		return provWaiting
	}
	Cached()
	// waits for the state (the loop waiting its turn), never a duration
	deadline := time.Now().Add(time.Minute)
	for !waiting() {
		if time.Now().After(deadline) {
			t.Fatalf("no listing waited its turn after %d asks", asks.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if n := asks.Load(); n != int64(relistFree) {
		t.Fatalf("%d listings before the first wait, want %d", n, relistFree)
	}
	// magpie's own change is listed at once, through the wait
	before := asks.Load()
	forgetProviders()
	for asks.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("magpie's own change waited behind the host's")
		}
		time.Sleep(time.Millisecond)
	}
	// and Settle doesn't wait for the turn (refreshSandbox's cleanup)
}
