package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A sign-in nothing uses is renewed by KeepSignedIn as it comes due, as a
// request would renew it (#5080: OrcaTerm's two-hour WeChat token lapsed
// for good on an idle gateway): through the same renewal, so a request
// arriving meanwhile waits for it and the vendor is asked once; one with
// time left isn't renewed; one the vendor can't be reached for says why,
// isn't marked and isn't tried again at once; one the vendor turned away
// is marked as a request's renewal marks it. The providers say which
// plugins keep alive and how early, as the host reckons it, and a plugin
// that says keepAlive: false is left alone.
func TestKeepSignedInRenewsIdleSignIns(t *testing.T) {
	sandbox(t)
	var mu sync.Mutex
	var seen []string
	srv := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	logf := filepath.Join(t.TempDir(), "renewals")
	t.Setenv("RENEW_LOG", logf)
	said := make(chan string, 16)
	OnSignIn(func(provider, account, s string) { said <- provider + " " + account + " " + s })
	t.Cleanup(func() { OnSignIn(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, f := range []string{"testdata/renew/index.js", "testdata/renew-nokeep/index.js"} {
		abs, _ := filepath.Abs(f)
		if _, err := Add(ctx, abs); err != nil {
			t.Fatal(err)
		}
	}
	soon := time.Now().Add(time.Minute).UnixMilli() // inside the 5 minutes
	later := time.Now().Add(time.Hour).UnixMilli()
	account := func(provider, who, refresh string, expires int64) string {
		t.Helper()
		k, err := Import(ctx, provider, map[string]any{"type": "oauth", "refresh": refresh, "access": "old-" + who, "expires": expires, "accountId": who})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	due := account("renewco", "due@renew", "r-ok", soon)
	busy := account("renewco", "busy@renew", "r-ok-busy", soon)
	notYet := account("renewco", "later@renew", "r-ok-later", later)
	never := account("renewco", "never@renew", "r-ok-never", 0)
	down := account("renewco", "down@renew", "r-down", soon)
	gone := account("renewco", "gone@renew", "r-gone", soon)
	stay := account("stayco", "stay@renew", "s-1", soon)
	renewals := func() []string {
		b, _ := os.ReadFile(logf)
		return strings.Fields(string(b))
	}
	stored := func(key string) map[string]any {
		var all map[string]map[string]any
		b, _ := os.ReadFile(AuthPath())
		json.Unmarshal(b, &all)
		return all[key]
	}
	keep := func(provider, key string) KeptSignedIn {
		t.Helper()
		k, err := KeepSignedIn(ctx, provider, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return k
	}

	// what the providers say of renewing
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Provider{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	if p := byID["renewco"]; !p.Renews || p.RenewLead != 5*60*1000 {
		t.Fatalf("renewco: renews %v, lead %d; want true, 5 minutes", p.Renews, p.RenewLead)
	}
	if p := byID["stayco"]; p.Renews {
		t.Fatal("stayco said keepAlive false and is listed as renewed in the background")
	}
	if at, ok := RenewAt(byID["renewco"], due); !ok || at.UnixMilli() != soon-5*60*1000 {
		t.Fatalf("RenewAt(due) = %v, %v; want %v", at, ok, time.UnixMilli(soon-5*60*1000))
	}
	if at, ok := RenewAt(byID["renewco"], notYet); !ok || !at.After(time.Now()) {
		t.Fatalf("RenewAt(later) = %v, %v", at, ok)
	}
	if at, ok := RenewAt(byID["renewco"], never); ok {
		t.Fatalf("RenewAt(never) = %v: a sign-in with no expiry is due", at)
	}
	if _, ok := RenewAt(byID["stayco"], stay); ok {
		t.Fatal("RenewAt(stay): a plugin that keeps nothing alive has a due time")
	}

	// due: renewed with nothing using it, and kept
	if k := keep("renewco", due); !k.Due || !k.Renewed || k.Err != "" {
		t.Fatalf("due: %+v", k)
	}
	if got := renewals(); strings.Join(got, " ") != "r-ok" {
		t.Fatalf("renewals = %v, want r-ok", got)
	}
	if a := stored(due); a["access"] != "a-1" || a["refresh"] != "r-ok+" || a["accountId"] != "due@renew" {
		t.Fatalf("kept %v", a)
	}
	if s := waitSaid(t, said); s != "renewco "+due+" renewed" {
		t.Fatalf("said %q", s)
	}
	// renewed already: not due
	if k := keep("renewco", due); k.Due || k.Renewed {
		t.Fatalf("due again: %+v", k)
	}

	// the keep-alive and requests at once: one renewal, the requests sent
	// with what it gave
	var wg sync.WaitGroup
	var kept KeptSignedIn
	wg.Add(1)
	go func() { defer wg.Done(); kept = keep("renewco", busy) }()
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := Fetch(ctx, FetchRequest{Provider: "renewco", Account: busy, Model: "renew-1", NPM: "@ai-sdk/openai-compatible",
				URL: srv.URL + "/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
			if err != nil {
				t.Error(err)
				return
			}
			io.ReadAll(res.Body)
			res.Body.Close()
		}()
	}
	wg.Wait()
	if got := renewals(); strings.Join(got, " ") != "r-ok r-ok-busy" {
		t.Fatalf("renewals = %v, want r-ok-busy once", got)
	}
	if !kept.Due || !kept.Renewed || stored(busy)["refresh"] != "r-ok-busy+" {
		t.Fatalf("busy: %+v, kept %v", kept, stored(busy))
	}
	mu.Lock()
	for _, h := range seen {
		if h != "Bearer a-2" {
			t.Errorf("a request went with %q, not the renewed token", h)
		}
	}
	mu.Unlock()
	if s := waitSaid(t, said); s != "renewco "+busy+" renewed" {
		t.Fatalf("said %q", s)
	}

	// time left: not renewed
	if k := keep("renewco", notYet); k.Due || k.Renewed {
		t.Fatalf("later: %+v", k)
	}
	if k := keep("renewco", never); k.Due || k.Renewed {
		t.Fatalf("never: %+v", k)
	}
	// keepAlive false: not renewed, though due
	if k := keep("stayco", stay); k.Due || k.Renewed {
		t.Fatalf("stay: %+v", k)
	}

	// can't be reached: why, not marked, kept as it was, not tried again
	// at once
	k := keep("renewco", down)
	if !k.Due || k.Renewed || k.Gone || !strings.Contains(k.Err, "can't be reached") {
		t.Fatalf("down: %+v", k)
	}
	if k := keep("renewco", down); !k.Waiting || k.Renewed || k.Gone {
		t.Fatalf("down again: %+v", k)
	}
	if a := stored(down); a["access"] != "old-down@renew" {
		t.Fatalf("an account that couldn't renew was changed: %v", a)
	}

	// turned away for good: gone, and marked
	if k := keep("renewco", gone); !k.Due || k.Renewed || !k.Gone {
		t.Fatalf("gone: %+v", k)
	}
	if s := waitSaid(t, said); s != "renewco "+gone+" expired" {
		t.Fatalf("said %q", s)
	}
	select {
	case s := <-said:
		t.Fatalf("said %q: the account that couldn't be reached was marked", s)
	default:
	}
	if got := renewals(); strings.Join(got, " ") != "r-ok r-ok-busy r-down r-gone" {
		t.Fatalf("renewals = %v", got)
	}
}

// An expires the host reads as a number is read so by RenewAt too: a
// number, or a string of one.
func TestExpiresOf(t *testing.T) {
	for raw, want := range map[string]int64{
		`1800000000000`:     1800000000000,
		`"1800000000000"`:   1800000000000,
		`" 1800000000000 "`: 1800000000000,
		`1.8e12`:            1800000000000,
		`0`:                 0,
		`-5`:                0,
		`""`:                0,
		`null`:              0,
		`"soon"`:            0,
		``:                  0,
	} {
		got, ok := expiresOf(json.RawMessage(raw))
		if got != want || ok != (want > 0) {
			t.Errorf("expiresOf(%s) = %d, %v; want %d", raw, got, ok, want)
		}
	}
}
