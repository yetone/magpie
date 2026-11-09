package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// keySlotFree are the routes, other than a GET's or an OPTIONS' (never
// held), that a key's MaxConcurrency doesn't hold: magpie's own, MCP (a
// tool call of a turn already held) and count_tokens. A route in neither
// this nor keySlotRoutes fails TestKeySlotEveryRoute, so a new one is
// judged.
var keySlotFree = []string{
	"POST /api/show", "POST /v1/magpie/usage/requests", "POST " + provider.RemoteRefreshPath,
	"POST " + provider.RemoteCodexResetPath, "POST " + StatuslinePath, "POST " + KeysPath,
	"DELETE " + KeysPath + "/{id}", "POST " + OIDCKeysPath, "POST " + provider.RemoteProvePath,
	"POST " + RestartPath, "POST " + provider.RemoteUpdatePath, "POST /v1/messages/count_tokens",
	"POST " + claudeMCPPath + "{token}", "/mcp/{name}", "/mcp/run/{name}", SearchMCPPath, VisionMCPPath,
	ImageMCPPath, BrowserMCPPath, MemoryMCPPath, ImageMCPFiles + "{name}", ToolsMCPPath, ToolsMCPOne + "{plugin}",
	HubMCPPath + "{agent}", HubMCPAll, "/",
}

// Every route is judged for a key's MaxConcurrency: held (keySlotRoutes)
// or not (keySlotFree, or a GET's), never both, and keySlotRoutes names
// only routes the router has.
func TestKeySlotEveryRoute(t *testing.T) {
	fresh(t)
	mux := New().routes()
	for _, p := range mux.patterns {
		_, held := keySlotRoutes[p]
		free := slices.Contains(keySlotFree, p) || strings.HasPrefix(p, "GET ") || strings.HasPrefix(p, "OPTIONS ")
		if held == free {
			t.Errorf("route %q is in neither keySlotRoutes nor keySlotFree, or in both: say whether a gateway key's MaxConcurrency holds it", p)
		}
	}
	for p := range keySlotRoutes {
		if !slices.Contains(mux.patterns, p) {
			t.Errorf("keySlotRoutes names %q, which the router doesn't have", p)
		}
	}
	for _, p := range keySlotFree {
		if !slices.Contains(mux.patterns, p) {
			t.Errorf("keySlotFree names %q, which the router doesn't have", p)
		}
	}
}

// lanServer serves s as it is to another computer on the LAN.
func lanServer(t *testing.T, s *Server) *httptest.Server {
	h := lanGuard(s.Handler())
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.168.1.9:5000"
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(gw.Close)
	return gw
}

// keyCap gives the key id a MaxConcurrency of n.
func keyCap(t *testing.T, id string, n int) {
	t.Helper()
	if _, err := access.Update("concurrency-key", access.Change{Key: id, MaxConcurrency: n}); err != nil {
		t.Fatal(err)
	}
}

// young02036 on Discord: a gateway key with a MaxConcurrency of 2 has no
// more than two requests at the vendor at once, from another computer on
// the LAN. A third waits in the key's queue, shown as waiting for a slot,
// and goes once one before it is read to its end; one whose agent hangs
// up while it waits leaves the queue and never reaches the vendor; another
// key isn't held by it; and the wait is the try's Queued.
func TestKeyConcurrency(t *testing.T) {
	fresh(t)
	v := &slowVendor{release: map[string]chan struct{}{}, quit: make(chan struct{})}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "slow", Name: "Slow", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Team", "Other")
	team, other := keys[0], keys[1]
	keyCap(t, team.ID, 2)
	s := New()
	gw := lanServer(t, s)
	t.Cleanup(func() { close(v.quit) })

	lane := func() Lane { return s.KeyLanes()[team.ID] }
	type reply struct {
		tag, body string
		err       error
	}
	replies := make(chan reply, 10)
	send := func(ctx context.Context, secret, tag string) {
		body := `{"model":"slow/m","stream":true,"messages":[{"role":"user","content":"` + tag + `"}]}`
		go func() {
			req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+secret)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				replies <- reply{tag: tag, err: err}
				return
			}
			b, err := io.ReadAll(res.Body)
			res.Body.Close()
			replies <- reply{tag, string(b), err}
		}()
	}

	send(context.Background(), secrets[0], "a")
	within(t, "a at the vendor", func() bool { return len(v.seen()) == 1 })
	send(context.Background(), secrets[0], "b")
	within(t, "b at the vendor", func() bool { return len(v.seen()) == 2 })
	send(context.Background(), secrets[0], "c")
	within(t, "c queued", func() bool { return lane().Waiting == 1 })
	if l := lane(); l.Busy != 2 || l.Limit != 2 {
		t.Fatalf("key lane = %+v, want 2 out, 1 waiting", l)
	}
	if !slices.ContainsFunc(s.Active(), func(a Active) bool { return a.Phase == "slot" }) {
		t.Fatalf("no request shows waiting for a slot: %+v", s.Active())
	}

	// another key isn't held by this one's
	send(context.Background(), secrets[1], "other")
	within(t, "the other key's at the vendor", func() bool { return slices.Contains(v.seen(), "other") })
	close(v.gate("other"))
	if r := <-replies; r.tag != "other" || !strings.Contains(r.body, "[DONE]") {
		t.Fatalf("the other key's reply = %+v", r)
	}
	if _, ok := s.KeyLanes()[other.ID]; ok {
		t.Fatal("a key with no cap has a lane")
	}

	// gone while it waits: out of the queue, never sent
	ctx, cancel := context.WithCancel(context.Background())
	send(ctx, secrets[0], "gone")
	within(t, "gone queued", func() bool { return lane().Waiting == 2 })
	cancel()
	within(t, "gone out of the queue", func() bool { return lane().Waiting == 1 })
	if r := <-replies; r.tag != "gone" || r.err == nil {
		t.Fatalf("the canceled request answered: %+v", r)
	}

	// c goes only once a is read to its end, and then the slot of the
	// stream it holds is the key's until it ends
	time.Sleep(50 * time.Millisecond)
	if slices.Contains(v.seen(), "c") {
		t.Fatal("c reached the vendor with two out")
	}
	close(v.gate("a"))
	if r := <-replies; r.tag != "a" || !strings.Contains(r.body, "[DONE]") {
		t.Fatalf("a = %+v", r)
	}
	within(t, "c at the vendor", func() bool { return slices.Contains(v.seen(), "c") })
	if l := lane(); l.Busy != 2 || l.Waiting != 0 {
		t.Fatalf("key lane after a = %+v, want 2 out", l)
	}
	for _, tag := range []string{"b", "c"} {
		close(v.gate(tag))
		if r := <-replies; r.err != nil || !strings.Contains(r.body, "[DONE]") {
			t.Fatalf("%s = %+v", tag, r)
		}
	}
	if got := v.seen(); slices.Contains(got, "gone") {
		t.Fatalf("the canceled request reached the vendor: %v", got)
	}
	within(t, "every key slot given back", func() bool { return len(s.KeyLanes()) == 0 })

	// c's wait is its try's Queued
	var queued int64
	for _, rt := range s.Trace(context.Background(), 0, 0).Routes {
		for _, tr := range rt.Tries {
			queued = max(queued, tr.Queued)
		}
	}
	if queued < 40 {
		t.Fatalf("the queued try's Queued = %dms, want its wait", queued)
	}
}

// A stream waiting for its key's slot past keepHeldAfter is sent its 200
// and SSE comments meanwhile, as one waiting for a provider's slot is
// (keepQueued): its agent's timeout for headers doesn't end it.
func TestKeyConcurrencyKeepsStreamAlive(t *testing.T) {
	fresh(t)
	k, e := keepHeldAfter, watchEvery
	keepHeldAfter, watchEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, watchEvery = k, e })
	v := &slowVendor{release: map[string]chan struct{}{}, quit: make(chan struct{})}
	up := httptest.NewServer(v)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "slow", Name: "Slow", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Team")
	keyCap(t, keys[0].ID, 1)
	s := New()
	gw := lanServer(t, s)
	t.Cleanup(func() { close(v.quit) })
	post := func(tag string) *http.Response {
		body := `{"model":"slow/m","stream":true,"messages":[{"role":"user","content":"` + tag + `"}]}`
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	first := post("first")
	if line, _ := bufio.NewReader(first.Body).ReadString('\n'); !strings.Contains(line, "from first") {
		t.Fatalf("first = %q", line)
	}
	// the second has its headers while it waits, nothing sent upstream
	second := post("second")
	if second.StatusCode != 200 || !strings.Contains(second.Header.Get("Content-Type"), "event-stream") {
		t.Fatalf("waiting stream: %d %s", second.StatusCode, second.Header.Get("Content-Type"))
	}
	if slices.Contains(v.seen(), "second") {
		t.Fatal("second reached the vendor with first out")
	}
	close(v.gate("first"))
	close(v.gate("second"))
	b, _ := io.ReadAll(second.Body)
	if !strings.Contains(string(b), "from second") || !strings.Contains(string(b), "[DONE]") {
		t.Fatalf("second's stream = %.300s", b)
	}
}

// The routes that wait before their handler (keySlotEager) hold a key to
// its cap as well: with 1, the second waits for the first's handler to
// return, one gone while waiting never runs, and a key issued under it
// waits for a slot of its issuer's too.
func TestKeyConcurrencyEager(t *testing.T) {
	fresh(t)
	var in, most atomic.Int32
	var mu sync.Mutex
	ran := []string{}
	gates := map[string]chan struct{}{}
	gate := func(tag string) chan struct{} {
		mu.Lock()
		defer mu.Unlock()
		if gates[tag] == nil {
			gates[tag] = make(chan struct{})
		}
		return gates[tag]
	}
	mux := &routeMux{ServeMux: http.NewServeMux()}
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		n := in.Add(1)
		defer in.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		tag := r.URL.Query().Get("t")
		mu.Lock()
		ran = append(ran, tag)
		mu.Unlock()
		<-gate(tag)
	})
	s := New()
	h := s.keySlots(mux, mux)
	parent := access.Identity{KeyID: "p", KeyName: "Parent", MaxConcurrency: 1}
	child := access.Identity{KeyID: "c", KeyName: "Child", Issuer: &parent}
	done := make(chan string, 8)
	send := func(ctx context.Context, who access.Identity, tag string) {
		go func() {
			r := httptest.NewRequestWithContext(access.WithIdentity(ctx, who), "POST", "/v1/embeddings?t="+tag, strings.NewReader("{}"))
			h.ServeHTTP(httptest.NewRecorder(), r)
			done <- tag
		}()
	}
	seen := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(ran) }
	send(context.Background(), parent, "one")
	within(t, "one runs", func() bool { return len(seen()) == 1 })
	send(context.Background(), child, "issued")
	within(t, "the issued key's waits for its issuer's slot", func() bool { return s.KeyLanes()["p"].Waiting == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	send(ctx, parent, "gone")
	within(t, "gone queued", func() bool { return s.KeyLanes()["p"].Waiting == 2 })
	cancel()
	if tag := <-done; tag != "gone" {
		t.Fatalf("%s returned first", tag)
	}
	close(gate("one"))
	within(t, "the issued key's runs", func() bool { return slices.Contains(seen(), "issued") })
	close(gate("issued"))
	<-done
	<-done
	if got := seen(); !slices.Equal(got, []string{"one", "issued"}) || most.Load() != 1 {
		t.Fatalf("ran %v, %d at once", got, most.Load())
	}
	within(t, "every key slot given back", func() bool { return len(s.KeyLanes()) == 0 })
	// a GET, or a key with no cap, is never held
	r := httptest.NewRequest("GET", "/v1/embeddings", nil)
	h.ServeHTTP(httptest.NewRecorder(), r.WithContext(access.WithIdentity(r.Context(), access.Identity{KeyID: "p", MaxConcurrency: 1})))
	if len(s.KeyLanes()) != 0 {
		t.Fatal("a GET took a slot")
	}
}
