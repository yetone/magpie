package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin's provider with several accounts fails over as a built-in's
// subscription does: an account out of its allowance, refused or failing
// before its answer starts gives the request to the next, and rests; one
// whose window for the model is known to be used up isn't tried at all;
// and a routing group goes on to its next member when every account of
// the plugin is out.
func TestPluginFailover(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	// the allowances the plugin tells, not routing_test's none
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })

	var mu sync.Mutex
	var tried []string          // each request's account, as its token names it
	fail := map[string]string{} // account → how it fails
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
		mu.Lock()
		tried = append(tried, who)
		how := fail[who]
		mu.Unlock()
		switch how {
		case "quota":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"usage limit reached: your plan's allowance is used up"}}`)
			return
		case "gone":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"token expired"}}`)
			return
		case "stream":
			// the vendor says it only in the stream, before any content
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"error":{"message":"You have exceeded your monthly quota"}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/messages" {
			for _, e := range []string{
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"claude from ` + who + `"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", e)
			}
			return
		}
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	t.Setenv("FAKE_BASE", up.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	// three accounts signed in in magpie, as a built-in's are added
	for _, team := range []string{"full", "a", "b"} {
		st, err := provider.StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
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
	ls := provider.Logins("fakeco")
	if len(ls) != 3 {
		t.Fatalf("accounts %+v", ls)
	}
	for _, l := range ls {
		if !l.On {
			t.Fatalf("%s isn't on: %+v", l.User, ls)
		}
	}

	s := New()
	reset := func(how map[string]string) {
		mu.Lock()
		tried, fail = nil, how
		mu.Unlock()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
		sticks.Lock()
		sticks.m = map[string]stick{}
		sticks.Unlock()
	}
	ask := func(model string) (int, string, []string) {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"`+model+`","messages":[{"role":"user","content":"hello"}]}`)
		mu.Lock()
		defer mu.Unlock()
		return code, body, append([]string(nil), tried...)
	}
	servedBy := func(body string) string {
		for _, u := range []string{"a", "b", "full"} {
			if strings.Contains(body, `"from `+u+`"`) {
				return u
			}
		}
		return ""
	}

	// an account out of its allowance: the request goes on to another,
	// and the next one doesn't try it again
	for _, how := range []string{"quota", "stream", "gone"} {
		hit := false
		for _, first := range []string{"a", "b", "full"} {
			reset(map[string]string{first: how})
			if how == "gone" {
				// the allowances the last round left to be read again are
				// read before the 401: the fake's usage doesn't ask the
				// upstream that refuses, so a reading that asked after the
				// 401 would find the sign-in good and rightly take the
				// mark off (one begun before it doesn't:
				// TestPluginUsageReadOlderThanAnswer)
				provider.LoginUsage(ctx, "plugin:fakeco")
			}
			code, body, seen := ask("fakeco/fake-1")
			if code != 200 || servedBy(body) == "" || servedBy(body) == first {
				t.Fatalf("%s with %s failing: %d %s (tried %v)", how, first, code, body, seen)
			}
			if !contains(seen, first) {
				continue // the router began elsewhere; try the next as first
			}
			hit = true
			t.Logf("%s: %s failed, served by %s, tried %v", how, first, servedBy(body), seen)
			mu.Lock()
			tried = nil
			mu.Unlock()
			code, body, seen = ask("fakeco/fake-1")
			if code != 200 || contains(seen, first) {
				t.Fatalf("%s: the next request tried %s again: %d %s (tried %v)", how, first, code, body, seen)
			}
			if how == "gone" {
				for _, l := range provider.Logins("fakeco") {
					if strings.HasPrefix(l.User, first+"@") && !strings.Contains(l.Lapsed, "sign-in has expired") {
						t.Fatalf("%s refused isn't marked: %+v", first, l)
					}
				}
			}
			break
		}
		if !hit {
			t.Fatalf("%s: no failing account was ever tried", how)
		}
	}

	// full@fake, switched to, is the account in use
	if err := provider.SwitchLogin("fakeco", "full@fake"); err != nil {
		t.Fatal(err)
	}
	reset(map[string]string{})
	if code, body, seen := ask("fakeco/fake-1"); code != 200 || len(seen) == 0 || seen[0] != "full" {
		t.Fatalf("with full@fake switched to: %d %s (tried %v)", code, body, seen)
	}

	// full@fake's five hours, which count fake-claude alone, are used up:
	// in use, it is weighed last for fake-claude, tried only when
	// nothing else can take it, as a built-in's spent account is; for
	// fake-1 it goes first as before
	for i := 0; i < 50; i++ {
		if _, ok := provider.Allowances("plugin:fakeco")["full@fake"]; ok {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		reset(map[string]string{})
		code, body, seen := ask("fakeco/fake-claude")
		if code != 200 || !strings.Contains(body, "claude from") || contains(seen, "full") {
			t.Fatalf("fake-claude with full@fake used up: %d %s (tried %v)", code, body, seen)
		}
	}
	reset(map[string]string{"a": "quota", "b": "quota"})
	if code, body, seen := ask("fakeco/fake-claude"); code != 200 || !strings.Contains(body, "claude from full") || strings.Join(seen, " ") != "a b full" {
		t.Fatalf("fake-claude with the others out: %d %s (tried %v)", code, body, seen)
	}
	reset(map[string]string{})
	if code, body, seen := ask("fakeco/fake-1"); code != 200 || len(seen) == 0 || seen[0] != "full" {
		t.Fatalf("fake-1 is held back on full@fake: %d %s (tried %v)", code, body, seen)
	}

	// a routing group: every account of the plugin out, its next member
	// answers
	other := &keyed{}
	serveOn(t, "other", "ko", []string{"m"}, other)
	if err := provider.SaveGroup(provider.Group{Name: "Mixed", Members: []string{"fakeco/fake-1", "other/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	reset(map[string]string{})
	if code, body, seen := ask("group/mixed"); code != 200 || servedBy(body) == "" || len(other.tried) != 0 {
		t.Fatalf("the group's first member: %d %s (tried %v, other %v)", code, body, seen, other.tried)
	}
	reset(map[string]string{"a": "quota", "b": "stream", "full": "quota"})
	code, body, seen := ask("group/mixed")
	if code != 200 || !strings.Contains(body, "from ko") || len(seen) != 3 {
		t.Fatalf("with the plugin's accounts out: %d %s (tried %v, other %v)", code, body, seen, other.tried)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A plugin answers with the status its built-in did and says apart what
// it means for the sign-in: a 502 of a refused renewal marks the account
// lapsed, as the built-in's did; a 401 the built-in didn't take for a
// refused sign-in leaves it be. The agent never sees the header.
func TestPluginSaysSignIn(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	var mu sync.Mutex
	status, said := 200, ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		st, sa := status, said
		mu.Unlock()
		if sa != "" {
			w.Header().Set(provider.SignInHeader, sa)
		}
		if st != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			fmt.Fprint(w, `{"error":{"message":"the vendor said no"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	t.Setenv("FAKE_BASE", up.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	s := New()
	lapsed := func() string {
		for _, l := range provider.Logins("fakeco") {
			return l.Lapsed
		}
		return "?"
	}
	for _, c := range []struct {
		status int
		said   string
		lapsed bool
	}{
		{502, "expired", true}, // a refused renewal the built-in answered 502
		{200, "", false},       // a request through takes the mark off
		{401, "kept", false},   // a 401 the built-in didn't take for a lapse
		{401, "", true},        // a plugin that says nothing: a 401 is a lapse
		{200, "kept", true},    // kept is kept, the mark too
		{200, "", false},
		{502, "expired", true},
		{429, "renewed", false}, // renewed, though the request then failed
	} {
		mu.Lock()
		status, said = c.status, c.said
		mu.Unlock()
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"fakeco/fake-1","messages":[{"role":"user","content":"hello"}]}`)))
		if rec.Header().Get(provider.SignInHeader) != "" {
			t.Fatalf("%d %s: the agent was handed the plugin's header", c.status, c.said)
		}
		if got := lapsed() != ""; got != c.lapsed {
			t.Fatalf("%d %q: lapsed %v, want %v (%q; answered %d %s)", c.status, c.said, got, c.lapsed, lapsed(), rec.Code, rec.Body.String())
		}
		if c.status != 200 && rec.Code != c.status {
			t.Fatalf("%d %q: the agent got %d", c.status, c.said, rec.Code)
		}
	}
}
