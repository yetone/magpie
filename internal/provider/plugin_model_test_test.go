package provider

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// testingPlugin installs the fake plugin with one signed-in account and
// gives its provider; the vendor is vendor.
func testingPlugin(t *testing.T, ctx context.Context, vendor http.Handler) Provider {
	t.Helper()
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	testenv.FakeVendor(t, vendor)
	t.Cleanup(plugin.Settle)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	k, err := plugin.Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-x", "access": "a", "expires": 9e15, "accountId": "a@fake"})
	if err != nil {
		t.Fatal(err)
	}
	saveLogins(t, savedLogin{Agent: "plugin:fakeco", User: "a@fake", Home: k, On: true})
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := Find(PluginID("fakeco"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsPlugin() {
		t.Fatal("not a plugin's provider")
	}
	return *p
}

// answers is a vendor that streams one word on chat completions, and
// keeps the X-Magpie-Fallback and X-Saw-Fallback each request came with.
type answers struct {
	mu   sync.Mutex
	seen []string
}

func (a *answers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.seen = append(a.seen, fmt.Sprintf("fallback=%q saw=%q", r.Header.Get(FallbackHeader), r.Header.Get("X-Saw-Fallback")))
	a.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
}

// A plugin's model its vendor has queued (Qoder's free models) is said
// queued at once by the models' test, not waited out until the test's
// time ran out ("qfmodel: context deadline exceeded", mingxia_16024);
// the model beside it answers; and the header that said so reaches the
// plugin only, never its vendor.
func TestPluginModelTestSaysQueued(t *testing.T) {
	t.Setenv("FAKE_QUEUE", "fake-1")
	defer func(w, pw time.Duration) { testWait, pluginTestWait = w, pw }(testWait, pluginTestWait)
	testWait, pluginTestWait = 5*time.Second, 5*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	v := &answers{}
	p := testingPlugin(t, ctx, v)
	start := time.Now()
	res := p.TestModels(ctx, []string{"fake-1", "fake-claude"})
	took := time.Since(start)
	got := map[string]Result{}
	for _, r := range res {
		got[r.Model] = r
	}
	q := got["fake-1"]
	if !q.OK || q.Error != "" || !strings.Contains(q.Queued, "queued the request for fake-1") {
		t.Errorf("the queued model's test: ok %v, error %q, queued %q (%v); want OK with the plugin's queue message", q.OK, q.Error, q.Queued, took)
	}
	if strings.Contains(q.Error, "deadline") {
		t.Errorf("the queued model's test ran out of time: %q", q.Error)
	}
	if r := got["fake-claude"]; !r.OK {
		t.Errorf("fake-claude's test: %+v", r)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.seen) == 0 {
		t.Fatal("the vendor was never asked")
	}
	for _, s := range v.seen {
		if s != `fallback="" saw="1"` {
			t.Errorf("the vendor got %s; want the plugin to have seen it and the vendor not", s)
		}
	}
}

// A plugin whose host starts cold and whose loader is slow (a sign-in
// renewed) has its model's test answered when the vendor answers: the
// host's start and the loader don't spend the probe's time.
func TestPluginModelTestColdHost(t *testing.T) {
	t.Setenv("FAKE_LOAD_WAIT", "2500")
	defer func(w, pw time.Duration) { testWait, pluginTestWait = w, pw }(testWait, pluginTestWait)
	testWait, pluginTestWait = 2*time.Second, 2*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := testingPlugin(t, ctx, &answers{})
	plugin.Restart() // cold: the next call starts a host, whose loader hasn't run
	res := p.TestModels(ctx, []string{"fake-claude"})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("a cold host's model test: %+v", res)
	}
}
