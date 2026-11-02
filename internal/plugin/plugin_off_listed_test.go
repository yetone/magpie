package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// ChangQiu on Discord: a provider switched off in magpie long ago was
// still asked. A plugin provider magpie has off (ListingOff) is asked
// nothing by a listing, its models hook (which reaches its vendor) not
// run, and keeps the list it told last; switched on again, it is asked.
func TestOffPluginProviderAskedNoModels(t *testing.T) {
	sandbox(t)
	var hits atomic.Int32
	var name atomic.Value
	name.Store("extra")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, name.Load())
	}))
	defer srv.Close()
	t.Setenv("FAKE_MODELS", srv.URL+"/models")
	was := ListingOff
	t.Cleanup(func() { ListingOff = was })
	ListingOff = nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"k1", "k2"} {
		if _, err := APIKey(ctx, "fakeco", 0, nil, k, NewAccount); err != nil {
			t.Fatal(err)
		}
	}
	has := func(ps []Provider, id string) bool {
		t.Helper()
		if len(ps) != 1 {
			t.Fatalf("Providers = %+v", ps)
		}
		return slices.ContainsFunc(ps[0].Models, func(m Model) bool { return m.ID == id })
	}
	ps, err := Providers(ctx)
	if err != nil || !has(ps, "fake-extra") {
		t.Fatalf("switched on, Providers = %+v, %v", ps, err)
	}
	if hits.Load() == 0 {
		t.Fatal("switched on, the vendor was never asked: the test asks nothing")
	}

	ListingOff = func() []string { return []string{"fakeco"} }
	name.Store("newer")
	before := hits.Load()
	ps, err = Providers(ctx)
	if n := hits.Load() - before; n != 0 {
		t.Fatalf("switched off, the vendor was asked %d times", n)
	}
	if err != nil || !has(ps, "fake-extra") || has(ps, "fake-newer") || ps[0].FellBack {
		t.Fatalf("switched off, the list told last went: %+v, %v", ps, err)
	}

	ListingOff = nil
	if ps, err = Providers(ctx); err != nil || !has(ps, "fake-newer") || hits.Load() == before {
		t.Fatalf("switched on again, Providers = %+v, %v (hits %d)", ps, err, hits.Load())
	}
}
