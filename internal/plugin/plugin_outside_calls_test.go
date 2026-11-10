package plugin

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A plugin's auth.calls (Cursor's: the account's calls made outside
// magpie) is said in its provider's Calls and read through AccountCalls:
// since handed to the hook as epoch ms, a call without an rid or a time
// dropped, and a count or cost that isn't a number read as none.
func TestPluginOutsideCalls(t *testing.T) {
	sandbox(t)
	t.Setenv("FAKE_CALLS", "1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, p := range ps {
		if p.ID == "fakeco" {
			listed = p.Calls
		}
	}
	if !listed {
		t.Fatal("fakeco's provider doesn't say it lists its calls")
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	since := time.UnixMilli(1_790_000_000_000)
	calls, err := AccountCalls(ctx, "fakeco", "", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("%d calls, want 2: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.RID != "ev-1" || c.T != since.UnixMilli()+1000 || c.Model != "fake-1" || c.In != 12 || c.Out != 3 || c.CacheRead != 40 || c.CacheWrite != 2 ||
		c.Cost == nil || *c.Cost != 0.004 || c.Session != "conv-1" {
		t.Fatalf("first call %+v", c)
	}
	if c := calls[1]; c.RID != "ev-2" || c.In != 0 || c.Out != 0 || c.Cost != nil {
		t.Fatalf("second call %+v", c)
	}
}
