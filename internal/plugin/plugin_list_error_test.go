package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// A plugin that never got its vendor's list says why, for the provider
// and each account: the editor shows it over the plugin's short defaults
// (gnayiab on X: Cursor in WSL listing Auto alone, with nothing saying
// its list had failed). Its list come in, nothing is said.
func TestFallenBackListSaysWhy(t *testing.T) {
	for _, throws := range []string{"", "1", "own"} {
		t.Run("throws="+throws, func(t *testing.T) { listSaysWhy(t, throws) })
	}
}

func listSaysWhy(t *testing.T, throws string) {
	sandbox(t)
	t.Setenv("FAKE_MODELS_THROW", throws)
	// no list told before this one, in memory or on disk
	Restart()
	provMu.Lock()
	provCache = nil
	provMu.Unlock()
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		w.Write([]byte("extra"))
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
	ps, err := Providers(ctx)
	if err != nil || len(ps) != 1 || len(ps[0].Accounts) != 2 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	if !ps[0].FellBack || ps[0].ListError == "" {
		t.Fatalf("the provider fell back saying %q (fellBack %v)", ps[0].ListError, ps[0].FellBack)
	}
	for _, a := range ps[0].Accounts {
		if !a.FellBack || a.ListError == "" {
			t.Fatalf("account %s fell back saying %q (fellBack %v)", a.Key, a.ListError, a.FellBack)
		}
	}
	// and so as magpie reads it, its accounts read again from the sign-ins
	for _, a := range Cached()[0].Accounts {
		if !a.FellBack || a.ListError == "" {
			t.Fatalf("Cached: account %s fell back saying %q (fellBack %v)", a.Key, a.ListError, a.FellBack)
		}
	}

	up.Store(true)
	if ps, err = Providers(ctx); err != nil || ps[0].FellBack || ps[0].ListError != "" {
		t.Fatalf("with the list in, Providers = %+v, %v", ps, err)
	}
	for _, a := range ps[0].Accounts {
		if a.FellBack || a.ListError != "" {
			t.Fatalf("with the list in, account %s says %q", a.Key, a.ListError)
		}
	}
}

// A lapsed account kept first in the sign-ins (#4917) fails its own list,
// but one it stands beside lists fine: the provider's list is the accounts'
// union, so the provider hasn't fallen back, and the list it told goes
// when its vendor's changes — the last one told is kept for a provider
// whose every account fell back, not for one whose list was told.
func TestLapsedFirstAccountKeepsTheProvidersList(t *testing.T) {
	sandbox(t)
	Restart()
	provMu.Lock()
	provCache = nil
	provMu.Unlock()
	var name atomic.Value
	name.Store("extra")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(name.Load().(string)))
	}))
	defer srv.Close()
	t.Setenv("FAKE_MODELS", srv.URL+"/models")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	// a lapsed sign-in under the provider's own key, a live one beside it,
	// as a Kiro oauth the vendor turned away kept first beside an API key
	if _, err := Import(ctx, "fakeco", map[string]any{"type": "oauth", "refresh": "r-dead", "access": "stale", "expires": time.Now().Add(time.Hour).UnixMilli(), "accountId": "Lapsed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "live", NewAccount); err != nil {
		t.Fatal(err)
	}
	has := func(ps []Provider, id string) bool {
		t.Helper()
		if len(ps) != 1 || len(ps[0].Accounts) != 2 {
			t.Fatalf("Providers = %+v", ps)
		}
		return slices.ContainsFunc(ps[0].Models, func(m Model) bool { return m.ID == id })
	}
	ps, err := Providers(ctx)
	if err != nil || !has(ps, "fake-extra") {
		t.Fatalf("with the vendor up, Providers = %+v, %v", ps, err)
	}
	// the lapsed account fell back; the provider, listing by the live one,
	// didn't
	if ps[0].FellBack || ps[0].ListError != "" {
		t.Fatalf("a lapsed first account said the provider fell back saying %q (fellBack %v)", ps[0].ListError, ps[0].FellBack)
	}
	var lapsed, live *Account
	for i := range ps[0].Accounts {
		if ps[0].Accounts[i].Type == "oauth" {
			lapsed = &ps[0].Accounts[i]
		} else {
			live = &ps[0].Accounts[i]
		}
	}
	if lapsed == nil || !lapsed.FellBack || lapsed.ListError == "" {
		t.Fatalf("the lapsed account says %+v", lapsed)
	}
	if live == nil || live.FellBack || live.ListError != "" {
		t.Fatalf("the live account says %+v", live)
	}

	// the vendor's list changes: the provider's follows it, though its
	// first account stays lapsed
	name.Store("newer")
	if ps, err = Providers(ctx); err != nil || !has(ps, "fake-newer") || has(ps, "fake-extra") {
		t.Fatalf("with the vendor's list changed, Providers = %+v, %v", ps, err)
	}
	if ps[0].FellBack {
		t.Fatalf("a lapsed first account says the provider fell back")
	}
}
