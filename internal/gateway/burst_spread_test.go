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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/internal/testenv"
)

// #5242 (Moody-Sin, Tericerice): requests sent at the same moment to a
// provider with three accounts, routed to spread load, all went to one
// account, in turn and in pace, with affinity turn and off, and even when
// each request named a session of its own.
func TestABurstSpreadsOverAccounts(t *testing.T) {
	const n = 6
	for _, routing := range []string{provider.Rotate, provider.Pace} {
		for _, aff := range []string{provider.AffinityTurn, provider.AffinityOff, ""} {
			for _, how := range []string{"x-session-id", SessionHeader, "same body"} {
				for _, warm := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/warm=%v", routing, aff, how, warm), func(t *testing.T) {
						fresh(t)
						h := &held{want: n, all: make(chan struct{}), tried: map[string]int{}}
						serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
						if err := provider.SetRouting("ag", routing); err != nil {
							t.Fatal(err)
						}
						if err := provider.SetAffinity("ag", aff); err != nil {
							t.Fatal(err)
						}
						routes.Forget("ag")
						s := New()
						if warm {
							// the reporters' rounds: the same request answered
							// before, so its conversation is held on an account
							h.want = 1
							burstPost(t, s, how, 0)
							h.mu.Lock()
							h.want, h.in, h.all, h.tried = n, 0, make(chan struct{}), map[string]int{}
							h.mu.Unlock()
						}
						var wg sync.WaitGroup
						for i := range n {
							wg.Add(1)
							go func() {
								defer wg.Done()
								burstPost(t, s, how, i)
							}()
						}
						wg.Wait()
						h.mu.Lock()
						defer h.mu.Unlock()
						for _, k := range []string{"k1", "k2", "k3"} {
							if h.tried[k] == 0 {
								t.Errorf("%s got none of %d requests sent at once: %v", k, n, h.tried)
							}
						}
					})
				}
			}
		}
	}
}

// burstPost sends the reporters' request: the same body every time, with a
// session of its own in a header, or none.
func burstPost(t *testing.T, s *Server, how string, i int) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"ag/m1","messages":[{"role":"user","content":"hi"}]}`))
	if how != "same body" {
		req.Header.Set(how, fmt.Sprintf("s-%d", i))
	}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}

// #5242 through a routing group: its members' keys weighed together, and
// a group of one provider's three keys, routed In turn and By pace.
func TestABurstSpreadsOverAGroup(t *testing.T) {
	const n = 6
	for _, routing := range []string{provider.Rotate, provider.Pace} {
		for _, shape := range []string{"three providers", "one provider's keys"} {
			t.Run(routing+"/"+shape, func(t *testing.T) {
				fresh(t)
				h := &held{want: n, all: make(chan struct{}), tried: map[string]int{}}
				members := []string{"ag/m1"}
				if shape == "three providers" {
					serveOn(t, "pa", "k1", []string{"m1"}, h)
					serveOn(t, "pb", "k2", []string{"m1"}, h)
					serveOn(t, "pc", "k3", []string{"m1"}, h)
					members = []string{"pa/m1", "pb/m1", "pc/m1"}
				} else {
					serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
				}
				if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: members, Routing: routing, Affinity: provider.AffinityTurn}); err != nil {
					t.Fatal(err)
				}
				s := New()
				var wg sync.WaitGroup
				for i := range n {
					wg.Add(1)
					go func() {
						defer wg.Done()
						rec := httptest.NewRecorder()
						req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+provider.GroupPrefix+`g","messages":[{"role":"user","content":"hi"}]}`))
						req.Header.Set(SessionHeader, fmt.Sprintf("s-%d", i))
						s.Handler().ServeHTTP(rec, req)
						if rec.Code != 200 {
							t.Errorf("%d %s", rec.Code, rec.Body.String())
						}
					}()
				}
				wg.Wait()
				h.mu.Lock()
				defer h.mu.Unlock()
				for _, k := range []string{"k1", "k2", "k3"} {
					if h.tried[k] == 0 {
						t.Errorf("%s got none of %d requests sent at once: %v", k, n, h.tried)
					}
				}
			})
		}
	}
}

// #5242 through a remote magpie: the sessions each request names go on
// to it, and it spreads them over its provider's keys.
func TestABurstSpreadsBehindARemoteMagpie(t *testing.T) {
	const n = 6
	fresh(t)
	h := &held{want: n, all: make(chan struct{}), tried: map[string]int{}}
	serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
	if err := provider.SetRouting("ag", provider.Pace); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("ag", provider.AffinityTurn); err != nil {
		t.Fatal(err)
	}
	there := New().Handler()
	remote := httptest.NewServer(there)
	t.Cleanup(remote.Close)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Key: "rk", Models: []string{"ag/m1"},
		Chat: remote.URL + "/v1", Responses: remote.URL + "/v1", Anthropic: remote.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"office/ag/m1","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set(SessionHeader, fmt.Sprintf("s-%d", i))
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("%d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range []string{"k1", "k2", "k3"} {
		if h.tried[k] == 0 {
			t.Errorf("%s got none of %d requests sent at once: %v", k, n, h.tried)
		}
	}
}

// #5242 for a provider a plugin serves: three accounts of the fake plugin
// (as Factory) through a real plugin host, By pace, sent six at once.
func TestABurstSpreadsOverAPluginsAccounts(t *testing.T) {
	const n = 6
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", "factory")
	t.Cleanup(plugin.Settle)
	h := &held{want: n, all: make(chan struct{}), tried: map[string]int{}}
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
		h.mu.Lock()
		h.tried[who]++
		if h.in++; h.in == h.want {
			close(h.all)
		}
		h.mu.Unlock()
		select {
		case <-h.all:
		case <-time.After(5 * time.Second):
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	teams := []string{"a", "b", "c"}
	for _, team := range teams {
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
	if ls := provider.Logins("factory"); len(ls) != len(teams) {
		t.Fatalf("accounts %+v", ls)
	}
	for _, team := range teams {
		if err := provider.SetLoginOn("factory", team+"@fake", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetRouting("factory", provider.Pace); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("factory", provider.AffinityTurn); err != nil {
		t.Fatal(err)
	}
	s := New()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"factory/fake-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set(SessionHeader, fmt.Sprintf("s-%d", i))
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("%d %s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, team := range teams {
		if h.tried[team] == 0 {
			t.Errorf("%s@fake got none of %d requests sent at once: %v", team, n, h.tried)
		}
	}
}

// Spreading a burst leaves a conversation its affinity keeps where it is:
// one session, Stays: Session, answered once, then sent six at once.
func TestABurstKeepsAKeptConversation(t *testing.T) {
	const n = 6
	for _, routing := range []string{provider.Rotate, provider.Pace} {
		t.Run(routing, func(t *testing.T) {
			fresh(t)
			h := &held{want: 1, all: make(chan struct{}), tried: map[string]int{}}
			serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
			if err := provider.SetRouting("ag", routing); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetAffinity("ag", provider.AffinitySession); err != nil {
				t.Fatal(err)
			}
			s := New()
			burstPost(t, s, SessionHeader, 0)
			var first string
			for k := range h.tried {
				first = k
			}
			h.mu.Lock()
			h.want, h.in, h.all, h.tried = n, 0, make(chan struct{}), map[string]int{}
			h.mu.Unlock()
			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					burstPost(t, s, SessionHeader, 0)
				}()
			}
			wg.Wait()
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.tried[first] != n {
				t.Errorf("the session's %d requests left %s, the account it is kept on: %v", n, first, h.tried)
			}
		})
	}
}

// By pace a burst goes on to another account only while its pace is alike
// (within a tenth): one with markedly more of its week left per hour
// takes them all, as pace says.
func TestBurstSpreadsOnlyOverAlikePaces(t *testing.T) {
	cs := []candidate{{p: provider.Provider{ID: "ag", Key: "k1"}, rest: "ag#1"}, {p: provider.Provider{ID: "ag", Key: "k2"}, rest: "ag#2"}}
	for _, tc := range []struct {
		paces  [2]float64
		spread bool
	}{{[2]float64{10, 9.5}, true}, {[2]float64{10, 5}, false}, {[2]float64{0, 0}, true}} {
		inFlight.Lock()
		inFlight.m = map[string]int{}
		inFlight.Unlock()
		pl := planned{order: []Weighed{{Pace: tc.paces[0]}, {Pace: tc.paces[1]}}}
		var gone []string
		var releases []func()
		for range 2 {
			got, _, release := burstSpread(provider.Pace, nil, false, true, slices.Clone(cs), pl)
			gone, releases = append(gone, got[0].who()), append(releases, release)
		}
		for _, r := range releases {
			r()
		}
		if spread := gone[0] != gone[1]; spread != tc.spread {
			t.Errorf("paces %v: went to %v, spread %v, want %v", tc.paces, gone, spread, tc.spread)
		}
		inFlight.Lock()
		left := len(inFlight.m)
		inFlight.Unlock()
		if left != 0 {
			t.Errorf("%d still counted in flight after each ended", left)
		}
	}
}

// The session a caller names in X-Magpie-Session is its conversation's, as
// the agent's own session header is: two sessions that open alike are two
// conversations, the second not kept on the first's account (#5242's
// second run: each request named a session of its own, and every one was
// taken for the first's conversation by its first message).
func TestMagpieSessionIsTheConversation(t *testing.T) {
	fresh(t)
	h := &held{want: 1, all: make(chan struct{}), tried: map[string]int{}}
	serveOn(t, "ag", "k1", []string{"m1"}, h, "k2", "k3")
	if err := provider.SetRouting("ag", provider.Pace); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAffinity("ag", provider.AffinitySession); err != nil {
		t.Fatal(err)
	}
	s := New()
	for i := range 3 {
		burstPost(t, s, SessionHeader, i)
	}
	for _, k := range []string{"k1", "k2", "k3"} {
		if h.tried[k] != 1 {
			t.Errorf("three sessions, one after another, kept for the session: %v", h.tried)
			break
		}
	}
	// an agent's own session header still names it first
	in := http.Header{}
	in.Set(SessionHeader, "magpie-s")
	in.Set("session_id", "agent-s")
	if got := affinityID(in, nil); got != "agent-s" {
		t.Errorf("conversation %q, want the agent's own agent-s", got)
	}
	// what a vendor or plugin is sent as the conversation never is the caller's
	in.Del("session_id")
	if got := conversationID(in, nil); got == "magpie-s" {
		t.Errorf("conversationID %q: X-Magpie-Session would go upstream", got)
	}
	if got := affinityID(in, nil); got != "magpie-s" {
		t.Errorf("affinityID %q, want magpie-s", got)
	}
}
