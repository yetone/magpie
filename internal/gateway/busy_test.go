package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// What a restart would cut short (#577): a reply streaming counts until
// it has ended, a GET (the model list) never does, and a Claude Code turn
// waiting on its caller's tool results counts until it has them or ends.
func TestBusy(t *testing.T) {
	fresh(t)
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-release
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "up", Name: "Up", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	// deferred after both Closes, so it runs first: each waits for the held
	// stream (the gateway's request on it), which a failed check would
	// leave held (#1808)
	let := sync.OnceFunc(func() { close(release) })
	defer let()
	if b := s.Busy(); b.Any() || !b.Last.IsZero() {
		t.Fatalf("before any request: %+v", b)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	<-started
	if res, err := http.Get(gw.URL + "/v1/models"); err == nil {
		res.Body.Close()
	}
	if b := s.Busy(); b.Requests != 1 || b.Tools != 0 || !b.Any() {
		t.Fatalf("streaming: %+v, want one request", b)
	}
	let()
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for s.Busy().Requests != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b := s.Busy(); b.Any() || time.Since(b.Last) > 5*time.Second {
		t.Fatalf("after the stream: %+v", b)
	}

	// a Claude Code turn parked on its caller's tools
	run := &subscriptionRun{bridge: s.subscription, token: "t", pending: map[string]chan mcpToolResult{}}
	s.subscription.mu.Lock()
	s.subscription.runs["t"] = run
	s.subscription.mu.Unlock()
	if b := s.Busy(); b.Tools != 0 {
		t.Fatalf("a run not waiting: %+v", b)
	}
	run.park()
	if b := s.Busy(); b.Tools != 1 || !b.Any() {
		t.Fatalf("a run waiting on tool results: %+v", b)
	}
	run.finish()
	if b := s.Busy(); b.Tools != 0 {
		t.Fatalf("a run ended: %+v", b)
	}
}

// /v1/magpie/concurrency tells the whole gateway's requests in flight with
// no limit set anywhere (#2132, a status bar polling it): 0 when idle,
// never left out, the poll itself not counted, and the requests waiting
// for a slot counted among them and told apart.
func TestConcurrencyTellsTheWholeGateway(t *testing.T) {
	fresh(t)
	var hold sync.WaitGroup
	started, release := make(chan struct{}, 4), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-release
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	one := 1
	if err := provider.Save(provider.Provider{ID: "free", Name: "Free", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "one", Name: "One", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}, MaxConcurrency: &one}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	let := sync.OnceFunc(func() { close(release) })
	defer hold.Wait()
	defer let()

	read := func() map[string]any {
		t.Helper()
		res, err := http.Get(gw.URL + "/v1/magpie/concurrency")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 200 {
			t.Fatalf("concurrency: %d %v", res.StatusCode, err)
		}
		return out
	}
	want := func(when string, requests, waiting float64) {
		t.Helper()
		out := read()
		for k, v := range map[string]float64{"requests": requests, "waiting": waiting, "tools": 0} {
			if got, ok := out[k].(float64); !ok || got != v {
				t.Fatalf("%s: %s = %v, want %v (%v)", when, k, out[k], v, out)
			}
		}
	}
	want("idle", 0, 0)

	send := func(model string) {
		hold.Add(1)
		go func() {
			defer hold.Done()
			res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			if err == nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
	}
	// one with no limit anywhere, as the reporter's gateway has
	send("free/m")
	<-started
	want("one request, no limit", 1, 0)

	// two on a provider of one slot: one out, one waiting
	send("one/m")
	<-started
	send("one/m")
	deadline := time.Now().Add(5 * time.Second)
	waiting := func() (n int) {
		for _, ln := range s.Lanes() {
			n += ln.Waiting
		}
		return n
	}
	for waiting() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the second never waited: %v", s.Lanes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	want("three in flight, one waiting", 3, 1)

	let()
	hold.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for s.Busy().Requests != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	want("done", 0, 0)
}

// /v1/magpie/concurrency lists each request in flight (#2132, bigbitbox:
// a status bar's hover names which agent is asking): its agent, model,
// session and start, and its phase only where the gateway knows it — out
// at the vendor, or waiting for a slot. Two of one agent are both listed,
// a request with no session too, and the list is [] when idle, never left
// out; the poll itself is never in it.
func TestConcurrencyListsEachRequest(t *testing.T) {
	fresh(t)
	var hold sync.WaitGroup
	started, release := make(chan struct{}, 4), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-release
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	one := 1
	if err := provider.Save(provider.Provider{ID: "one", Name: "One", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}, MaxConcurrency: &one}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	let := sync.OnceFunc(func() { close(release) })
	defer hold.Wait()
	defer let()

	type active struct {
		ID       int64     `json:"id"`
		Agent    string    `json:"agent"`
		Model    string    `json:"model"`
		Provider string    `json:"provider"`
		Session  string    `json:"session"`
		Started  time.Time `json:"started"`
		Phase    string    `json:"phase"`
	}
	read := func() (list []active, raw string) {
		t.Helper()
		res, err := http.Get(gw.URL + "/v1/magpie/concurrency")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out struct {
			Active *[]active `json:"active"`
		}
		if err := json.Unmarshal(b, &out); err != nil || res.StatusCode != 200 {
			t.Fatalf("concurrency: %d %v %s", res.StatusCode, err, b)
		}
		if out.Active == nil {
			t.Fatalf("active left out or null: %s", b)
		}
		return *out.Active, string(b)
	}
	if list, raw := read(); len(list) != 0 {
		t.Fatalf("idle: %s", raw)
	}

	send := func(session string) {
		hold.Add(1)
		go func() {
			defer hold.Done()
			req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"one/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "opencode/1.0")
			if session != "" {
				req.Header.Set(SessionHeader, session)
			}
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
	}
	before := time.Now()
	send("s1")
	<-started
	send("") // the same agent again, no session, waiting for the one slot
	deadline := time.Now().Add(5 * time.Second)
	var list []active
	var raw string
	for {
		list, raw = read()
		if len(list) == 2 && list[1].Phase == "slot" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never one out and one waiting: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, waiting := list[0], list[1]
	if out.Agent != "opencode" || out.Model != "one/m" || out.Provider != "one" || out.Session != "s1" || out.Phase != "upstream" || out.Started.Before(before.Add(-time.Second)) {
		t.Fatalf("the one out: %+v (%s)", out, raw)
	}
	if waiting.Agent != "opencode" || waiting.Session != "" || waiting.ID == out.ID {
		t.Fatalf("the one waiting: %+v (%s)", waiting, raw)
	}
	if strings.Contains(raw, `"hi"`) || strings.Contains(raw, `"k"`) {
		t.Fatalf("a prompt or key told: %s", raw)
	}

	let()
	hold.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for {
		if list, raw = read(); len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still listed when done: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CodexBusy counts Codex's requests alone: a restart of the Codex app's
// app-server waits for a Codex turn, never for another agent's.
func TestCodexBusy(t *testing.T) {
	fresh(t)
	started, release := make(chan struct{}, 2), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-release
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "up", Name: "Up", Key: "k", Chat: up.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	s := New()
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()
	var hold sync.WaitGroup
	let := sync.OnceFunc(func() { close(release) })
	defer hold.Wait()
	defer let()
	send := func(key string) {
		hold.Add(1)
		go func() {
			defer hold.Done()
			req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+key)
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
	}
	if n, last := s.CodexBusy(); n != 0 || !last.IsZero() {
		t.Fatalf("before any request: %d %v", n, last)
	}
	send(Token + "-opencode")
	<-started
	if n, last := s.CodexBusy(); n != 0 || !last.IsZero() {
		t.Fatalf("another agent's request: %d %v", n, last)
	}
	send(Token + "-codex")
	<-started
	if n, _ := s.CodexBusy(); n != 1 {
		t.Fatalf("Codex's request in flight: %d, want 1 (%+v)", n, s.Active())
	}
	let()
	hold.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for s.Busy().Requests != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n, last := s.CodexBusy(); n != 0 || time.Since(last) > 5*time.Second {
		t.Fatalf("after: %d %v", n, last)
	}
}

// Keep awake holds sleep off for as long as other computers reach the
// gateway, idle or not (eachg0 on X: a Mac sharing its gateway slept once
// its agents had been idle past the grace, and a request from another
// computer timed out until someone touched it). Not shared, an idle
// gateway lets it sleep as before.
func TestAwakeStateShared(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("LAZYCAT_APP_DOMAIN", "")
	s := New()
	if st := s.awakeState(); st.Shared || st.Busy {
		t.Fatalf("not shared: %+v", st)
	}
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	if st := s.awakeState(); !st.Shared || st.Busy {
		t.Fatalf("shared on the local network: %+v", st)
	}
	if err := settings.Save(settings.Settings{LAN: false}); err != nil {
		t.Fatal(err)
	}
	if st := s.awakeState(); st.Shared {
		t.Fatalf("sharing turned off: %+v", st)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	if st := s.awakeState(); !st.Shared {
		t.Fatalf("listening on the network: %+v", st)
	}
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.com")
	if st := s.awakeState(); !st.Shared {
		t.Fatalf("at a public address: %+v", st)
	}
}
