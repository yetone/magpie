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

// A plugin whose models hook can't reach its vendor and gives its
// defaults back keeps the list it told last, for the provider and each
// account, as a built-in whose fetch fails keeps the one it fetched last;
// the vendor back, its list is told again. So too for a hook that throws
// (Cursor's, Grok's, Devin's) rather than give its defaults back, and for
// one handing back a table of its own that it says is a fallback.
func TestFallenBackListKeepsTheLastOne(t *testing.T) {
	for _, throws := range []string{"", "1", "own"} {
		t.Run("throws="+throws, func(t *testing.T) { fallenBack(t, throws) })
	}
}

func fallenBack(t *testing.T, throws string) {
	sandbox(t)
	t.Setenv("FAKE_MODELS_THROW", throws)
	var down atomic.Bool
	var name atomic.Value
	name.Store("extra")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			// the connection dropped, as a vendor gone away drops it
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		fmt.Fprint(w, name.Load())
	}))
	defer srv.Close()
	t.Setenv("FAKE_MODELS", srv.URL+"/models")
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
		if len(ps) != 1 || len(ps[0].Accounts) != 2 {
			t.Fatalf("Providers = %+v", ps)
		}
		in := slices.ContainsFunc(ps[0].Models, func(m Model) bool { return m.ID == id })
		for _, a := range ps[0].Accounts {
			if slices.Contains(a.Models, id) != in {
				t.Fatalf("account %s's models %v, the provider's having %s: %v", a.Key, a.Models, id, in)
			}
		}
		return in
	}
	ps, err := Providers(ctx)
	if err != nil || !has(ps, "fake-extra") {
		t.Fatalf("with the vendor up, Providers = %+v, %v", ps, err)
	}

	down.Store(true)
	ps, err = Providers(ctx)
	if err != nil || !has(ps, "fake-extra") || ps[0].FellBack {
		t.Fatalf("with the vendor down, the list told last went: %+v, %v", ps, err)
	}
	// so after a restart, from the list kept on disk
	Restart()
	provMu.Lock()
	provCache = nil
	provMu.Unlock()
	if ps, err = Providers(ctx); err != nil || !has(ps, "fake-extra") {
		t.Fatalf("after a restart, Providers = %+v, %v", ps, err)
	}

	down.Store(false)
	name.Store("newer")
	if ps, err = Providers(ctx); err != nil || !has(ps, "fake-newer") || has(ps, "fake-extra") {
		t.Fatalf("with the vendor back, Providers = %+v, %v", ps, err)
	}
}
