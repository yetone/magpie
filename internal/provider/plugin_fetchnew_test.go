package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's account listed with the plugin's providers is not asked for
// its list again by FetchNew, as a built-in account fetched once isn't:
// the Providers page built its state through FetchNew, and every ten
// minutes asked every plugin's vendors for their lists again.
func TestFetchNewLeavesListedPlugins(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k-123456"); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("fakeco"); err != nil || !p.IsPlugin() || !p.Ready() {
		t.Fatalf("Find(fakeco) = %+v, %v", p, err)
	}
	// signing in kicked off a refresh of the plugins' providers in the
	// background (Cached, after the auth event); one still running
	// rewrites the list below and reads as FetchNew asking again
	plugin.Refreshed()
	was := newFetchRetry
	newFetchRetry = 0
	t.Cleanup(func() { newFetchRetry = was })
	listed := filepath.Join(filepath.Dir(plugin.AuthPath()), "plugin-providers.json")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(listed, old, old); err != nil {
		t.Fatal(err)
	}
	FetchNew(20 * time.Second)
	if st, err := os.Stat(listed); err != nil || !st.ModTime().Equal(old) {
		t.Fatalf("the plugins were asked for their lists again: %v %v", st.ModTime(), err)
	}
}

// A plugin's usage asked for by the provider's id (the Usage page) and as
// plugin:<id> (routing) is read from the vendor once, as a built-in's,
// asked for by one name, is.
func TestPluginUsageReadOnceWhicheverName(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	var asked atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		fmt.Fprint(w, "Fake Max")
	}))
	defer vendor.Close()
	t.Setenv("FAKE_USAGE", vendor.URL)
	clear := func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
	}
	clear()
	t.Cleanup(clear)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"fakeco", "plugin:fakeco"} {
		if q := LoginUsage(ctx, agent)["a@fake"]; q.Plan != "Fake Max" {
			t.Fatalf("%s: %+v", agent, q)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("the vendor was asked %d times", n)
	}
}
