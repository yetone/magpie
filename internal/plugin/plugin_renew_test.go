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

// A plugin with auth.refresh leaves renewing its sign-ins to magpie: a
// token close to its end is renewed once however many requests find it
// so, each of them sent with the new one; one with time left, or no
// expiry, isn't renewed; one the vendor turns away is marked expired and
// not tried again, and one that can't be renewed goes with the old token
// and isn't tried again at once, nor is one whose refresh gives nothing
// back while it is due (magpie-ima-auth's, JZGiVvkSOA75376 on X).
func TestPluginRenewsSignIns(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	abs, _ := filepath.Abs("testdata/renew/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Minute).UnixMilli() // inside the 5 minutes
	later := time.Now().Add(time.Hour).UnixMilli()
	account := func(who, refresh string, expires int64) string {
		t.Helper()
		k, err := Import(ctx, "renewco", map[string]any{"type": "oauth", "refresh": refresh, "access": "old-" + who, "expires": expires, "accountId": who})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	due := account("due@renew", "r-ok", soon)
	notYet := account("later@renew", "r-ok-later", later)
	never := account("never@renew", "r-ok-never", 0)
	gone := account("gone@renew", "r-gone", soon)
	down := account("down@renew", "r-down", soon)
	none := account("none@renew", "r-none", time.Now().Add(-time.Minute).UnixMilli())
	send := func(account string) string {
		t.Helper()
		res, err := Fetch(ctx, FetchRequest{Provider: "renewco", Account: account, Model: "renew-1", NPM: "@ai-sdk/openai-compatible",
			URL: srv.URL + "/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			t.Fatalf("%s: %v", account, err)
		}
		io.ReadAll(res.Body)
		res.Body.Close()
		mu.Lock()
		defer mu.Unlock()
		return seen[len(seen)-1]
	}
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

	// five requests at once on the account due: one renewal, all sent
	// with what it gave, and it is kept
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); send(due) }()
	}
	wg.Wait()
	if got := renewals(); len(got) != 1 || got[0] != "r-ok" {
		t.Fatalf("renewals = %v, want r-ok once", got)
	}
	mu.Lock()
	for _, h := range seen {
		if h != "Bearer a-1" {
			t.Errorf("a request went with %q, not the renewed token", h)
		}
	}
	mu.Unlock()
	if a := stored(due); a["access"] != "a-1" || a["refresh"] != "r-ok+" || a["accountId"] != "due@renew" {
		t.Fatalf("kept %v", a)
	}
	if h := send(due); h != "Bearer a-1" || len(renewals()) != 1 {
		t.Fatalf("renewed again: %q, %v", h, renewals())
	}
	if s := waitSaid(t, said); s != "renewco "+due+" renewed" {
		t.Fatalf("said %q", s)
	}

	// time left, or no expiry: not renewed
	if h := send(notYet); h != "Bearer old-later@renew" {
		t.Fatalf("an account with time left went with %q", h)
	}
	if h := send(never); h != "Bearer old-never@renew" {
		t.Fatalf("an account with no expiry went with %q", h)
	}
	if got := renewals(); len(got) != 1 {
		t.Fatalf("renewals = %v", got)
	}

	// turned away for good: marked, sent as it is, and not tried again
	if h := send(gone); h != "Bearer old-gone@renew" {
		t.Fatalf("a refused account went with %q", h)
	}
	if s := waitSaid(t, said); s != "renewco "+gone+" expired" {
		t.Fatalf("said %q", s)
	}
	send(gone)
	// can't be reached: sent as it is, not tried again at once
	if h := send(down); h != "Bearer old-down@renew" {
		t.Fatalf("an account that couldn't renew went with %q", h)
	}
	send(down)
	// renewed nothing, its token past its end: sent as it is, and the
	// next request doesn't ask the plugin again
	if h := send(none); h != "Bearer old-none@renew" {
		t.Fatalf("an account its plugin renewed nothing for went with %q", h)
	}
	send(none)
	send(none)
	if got := renewals(); strings.Join(got, " ") != "r-ok r-gone r-down r-none" {
		t.Fatalf("renewals = %v", got)
	}
	if a := stored(gone); a["access"] != "old-gone@renew" {
		t.Fatalf("a refused account was changed: %v", a)
	}
}

func waitSaid(t *testing.T, said chan string) string {
	t.Helper()
	select {
	case s := <-said:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("nothing said of the sign-in")
		return ""
	}
}
