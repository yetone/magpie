package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// plugins#349 through a real plugin host: the fake plugin as Factory, two
// accounts in order, each capped at 90%, their usage read from the
// plugin's usage call as it says it now (FAKE_WINDOWS). With Check usage
// near a cap on, a usage call that fails holds both accounts: the agent is
// answered 429 and nothing reaches the vendor; off, the last reading is
// used as before; once usage reads again, A serves. With Stop at the caps
// on, both at their caps, the fallback isn't sent the request either.
// With no fallback, a routing group of the user's that has the plugin's
// model first goes on to its next member, Stop at the caps off.
func TestPluginCarefulPool(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", "factory")
	windows := filepath.Join(t.TempDir(), "windows.json")
	t.Setenv("FAKE_WINDOWS", windows)
	t.Cleanup(plugin.Settle)
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	carefulTried.Lock()
	carefulTried.at = nil
	carefulTried.Unlock()

	var mu sync.Mutex
	var tried []string
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
		mu.Lock()
		tried = append(tried, who)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mig, _ := json.Marshal(map[string]any{"factory": map[string]any{"state": provider.MovePlugin}})
	if err := os.WriteFile(filepath.Join(dir, "migrations.json"), mig, 0o600); err != nil {
		t.Fatal(err)
	}
	resets := time.Now().Add(2 * time.Hour)
	five := func(used float64) []map[string]any {
		return []map[string]any{{"name": "5 hours", "used": used, "span": 18000, "resetsAt": resets.UTC().Format(time.RFC3339)}}
	}
	write := func(t *testing.T, raw []byte) {
		t.Helper()
		if err := os.WriteFile(windows+".tmp", raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(windows+".tmp", windows); err != nil {
			t.Fatal(err)
		}
	}
	// says writes what the plugin says of each account, and waits for
	// routing to have read it: a's share, or with broken, a failed read
	says := func(t *testing.T, a, b float64, broken bool) {
		t.Helper()
		if broken {
			write(t, []byte("{not json"))
		} else {
			raw, _ := json.Marshal(map[string]any{"a@fake": five(a), "b@fake": five(b)})
			write(t, raw)
		}
		for _, u := range []string{"a@fake", "b@fake"} {
			provider.StaleAllowance("plugin:factory", u)
		}
		deadline := time.Now().Add(time.Minute)
		for {
			got := provider.Allowances("plugin:factory")
			_, failed, ok := provider.UsageRead("plugin:factory", "a@fake")
			if broken && ok && failed || !broken && ok && !failed && len(got["a@fake"]) > 0 && got["a@fake"][0].Used == a && len(got["b@fake"]) > 0 && got["b@fake"][0].Used == b {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("routing never read the windows (broken %v): %+v", broken, got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	raw, _ := json.Marshal(map[string]any{"a@fake": five(40), "b@fake": five(10)})
	write(t, raw)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"a", "b"} {
		st, err := provider.StartPluginSignIn("factory", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if ls := provider.Logins("factory"); len(ls) != 2 {
		t.Fatalf("accounts %+v", ls)
	}
	if err := provider.SwitchLogin("factory", "a@fake"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("factory", "b@fake", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("factory", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"a@fake", "b@fake"} {
		if err := provider.SetAccountCap("factory", u, 90); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := provider.Find("factory"); p.CapCareful || p.CapStop {
		t.Fatalf("on by default: careful %v stop %v", p.CapCareful, p.CapStop)
	}

	s := New()
	ask := func(t *testing.T) (int, string, []string) {
		t.Helper()
		mu.Lock()
		tried = nil
		mu.Unlock()
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"factory/fake-1","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
		s.Handler().ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		return rec.Code, rec.Body.String(), append([]string(nil), tried...)
	}
	served := func(t *testing.T, want string) {
		t.Helper()
		code, body, seen := ask(t)
		if code != 200 || !strings.Contains(body, "from "+want) || strings.Join(seen, " ") != want {
			t.Fatalf("%d %s (tried %v), want %s alone", code, body, seen, want)
		}
	}
	careful := func(t *testing.T, on bool) {
		t.Helper()
		if err := provider.SetCapCareful("factory", on); err != nil {
			t.Fatal(err)
		}
		carefulTried.Lock()
		carefulTried.at = nil
		carefulTried.Unlock()
	}

	says(t, 40, 10, false)
	served(t, "a")

	// usage can't be read: careful holds both, nothing reaches Factory
	says(t, 0, 0, true)
	careful(t, true)
	code, body, seen := ask(t)
	if code != 429 || len(seen) != 0 {
		t.Fatalf("careful, unread: %d %s (tried %v)", code, body, seen)
	}
	for _, want := range []string{"rate_limit", "couldn't be read", "a@fake", "b@fake", "--careful off"} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q in %s", want, body)
		}
	}
	// off: the last reading (A at 40%) is used, as before
	careful(t, false)
	served(t, "a")

	// read again: careful sends to A
	careful(t, true)
	raw, _ = json.Marshal(map[string]any{"a@fake": five(40), "b@fake": five(10)})
	write(t, raw)
	served(t, "a")

	// both at their caps with a fallback: it takes the request, unless
	// Stop at the caps is on
	careful(t, false)
	fb := &keyed{}
	serveOn(t, "spare", "kf", []string{"m2"}, fb)
	p, _ := provider.Find("factory")
	p.Fallback = []string{"spare/m2"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	says(t, 95, 92, false)
	if code, body, seen := ask(t); code != 200 || !strings.Contains(body, "from kf") || len(seen) != 0 {
		t.Fatalf("stop off: %d %s (tried %v)", code, body, seen)
	}
	if err := provider.SetCapStop("factory", true); err != nil {
		t.Fatal(err)
	}
	fb.tried = nil
	if code, body, seen := ask(t); code != 429 || len(seen) != 0 || len(fb.tried) != 0 || !strings.Contains(body, "usage cap reached") {
		t.Fatalf("stop on: %d %s (tried %v, fallback %v)", code, body, seen, fb.tried)
	}

	// no fallback, but a routing group of the user's with the plugin's
	// model first and an API after it: asked for the plugin's own model,
	// both accounts at their caps, the API takes it; Stop at the caps
	// still ends there
	p, _ = provider.Find("factory")
	p.Fallback = nil
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "Pool", Members: []string{"factory/fake-1", "spare/m2"}}); err != nil {
		t.Fatal(err)
	}
	fb.tried = nil
	if code, body, seen := ask(t); code != 429 || len(seen) != 0 || len(fb.tried) != 0 {
		t.Fatalf("group, stop on: %d %s (tried %v, group's API %v)", code, body, seen, fb.tried)
	}
	if err := provider.SetCapStop("factory", false); err != nil {
		t.Fatal(err)
	}
	if code, body, seen := ask(t); code != 200 || !strings.Contains(body, "from kf") || len(seen) != 0 {
		t.Fatalf("group, stop off: %d %s (tried %v)", code, body, seen)
	}
}
