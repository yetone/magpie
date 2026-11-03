package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// displayKey is a read-only gateway key, magpie shared on the LAN, as a
// display on another machine is given (#3387).
func displayKey(t *testing.T) (access.Key, string) {
	t.Helper()
	keys, secrets := newCaller(t, "Display")
	if _, err := access.Update("access-key", access.Change{Key: keys[0].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	return keys[0], secrets[0]
}

// The feed is told as the usage log is: to this computer, and to another
// machine with an enabled gateway key while magpie is shared; a
// read-only key reads it and nothing it may not; a key held to a budget,
// models or accounts is told no one else's routing; and a page reaching
// the gateway (Origin null, a *.localhost name, a rebound name, a
// foreign site) or a proxy's client is refused it without a key, as
// every other route is.
func TestRouteEventsTold(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	_, display := displayKey(t)
	keys, secrets := newCaller(t, "Laptop", "Held")
	full, held := secrets[0], secrets[1]
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"plan/m1"}}); err != nil {
		t.Fatal(err)
	}
	h := lanGuard(New().Handler())
	do := func(method, path, from, host, key string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(chatReq))
		r.RemoteAddr, r.Host = from, host
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const lan, here, gw = "192.168.1.9:1234", "127.0.0.1:50123", "127.0.0.1:3425"
	// no cursor: a stream is told to take a snapshot, 409, before it begins
	for _, tc := range []struct {
		name, from, host, key string
		path                  string
		want                  int
	}{
		{"read-only key, LAN", lan, "192.168.1.10:3425", display, routeSnapshotPath, 200},
		{"read-only key, LAN, stream", lan, "192.168.1.10:3425", display, routeEventsPath, 409},
		{"full key, LAN", lan, "192.168.1.10:3425", full, routeSnapshotPath, 200},
		{"this computer, no key", here, gw, "", routeSnapshotPath, 200},
		{"this computer, read-only key", here, gw, display, routeEventsPath, 409},
		{"another machine, no key", lan, "192.168.1.10:3425", "", routeSnapshotPath, 401},
		{"another machine, no key, stream", lan, "192.168.1.10:3425", "", routeEventsPath, 401},
		{"another machine, a wrong key", lan, "192.168.1.10:3425", "sk-magpie-wrong", routeSnapshotPath, 401},
		{"held key, LAN", lan, "192.168.1.10:3425", held, routeSnapshotPath, 403},
		{"held key, LAN, stream", lan, "192.168.1.10:3425", held, routeEventsPath, 403},
		{"held key, this computer", here, gw, held, routeSnapshotPath, 403},
	} {
		if w := do("GET", tc.path, tc.from, tc.host, tc.key); w.Code != tc.want {
			t.Errorf("%s: %d, want %d: %.300s", tc.name, w.Code, tc.want, w.Body)
		}
	}
	safari := []string{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/27.2 Safari/605.1.15", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"}
	// a rebound page's own GET: its name now answers at loopback, so it
	// is same-origin and sends no Origin
	rebound := []string{"User-Agent", safari[1], "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors"}
	for _, path := range []string{routeSnapshotPath, routeEventsPath} {
		for _, tc := range []struct {
			name, host string
			hdr        []string
		}{
			{"a foreign page", gw, append([]string{"Origin", "https://evil.example"}, safari...)},
			{"Origin null", gw, append([]string{"Origin", "null"}, safari...)},
			{"a localhost subdomain", gw, append([]string{"Origin", "http://evil.localhost"}, safari...)},
			{"a rebound page's GET", "rebind.attacker.example:3425", rebound},
			{"a page rebound at a localhost subdomain", "evil.localhost:3425", rebound},
			{"a proxy's client", gw, []string{"X-Forwarded-For", "203.0.113.9"}},
		} {
			if w := do("GET", path, here, tc.host, "", tc.hdr...); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with no key: %d %.300s", tc.name, path, w.Code, w.Body)
			}
		}
	}
	// the read-only key reaches nothing else it didn't before
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/chat/completions"}, {"POST", "/v1/responses"}, {"POST", "/v1/messages"},
		{"GET", "/v1/models"}, {"GET", "/v1/magpie/trace"}, {"POST", "/mcp/magpie/web-search"},
	} {
		if w := do(tc.method, tc.path, lan, "192.168.1.10:3425", display); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), ReadOnlyCode) {
			t.Errorf("read-only key: %s %s: %d %.200s", tc.method, tc.path, w.Code, w.Body)
		}
	}
	// no longer shared: another machine is refused it, even with a key
	if err := settings.Save(settings.Settings{LAN: false}); err != nil {
		t.Fatal(err)
	}
	if w := do("GET", routeSnapshotPath, lan, "192.168.1.10:3425", display); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
		t.Errorf("not shared: the display's key from the LAN: %d %.300s", w.Code, w.Body)
	}
}

type routeSnap struct {
	Cursor string          `json:"cursor"`
	Active int             `json:"active"`
	Routes []routeActivity `json:"routes"`
}

func activitySnapshot(t *testing.T, base, key string) routeSnap {
	t.Helper()
	req, _ := http.NewRequest("GET", base+routeSnapshotPath, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got routeSnap
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil || res.StatusCode != 200 || got.Active != len(got.Routes) || got.Routes == nil {
		t.Fatalf("snapshot: status %d, %+v, %v", res.StatusCode, got, err)
	}
	return got
}

func activityStream(t *testing.T, base, key, cursor string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", base+routeEventsPath, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Last-Event-ID", cursor)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// nextActivity is the stream's next change, false once it ended.
func nextActivity(t *testing.T, scan *bufio.Scanner) (routeActivityEvent, bool) {
	t.Helper()
	for scan.Scan() {
		if data, ok := strings.CutPrefix(scan.Text(), "data: "); ok {
			var ev routeActivityEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Cursor != "" {
				return ev, true
			}
		}
	}
	return routeActivityEvent{}, false
}

func mustActivity(t *testing.T, scan *bufio.Scanner) routeActivityEvent {
	t.Helper()
	ev, ok := nextActivity(t, scan)
	if !ok {
		t.Fatalf("event stream ended: %v", scan.Err())
	}
	return ev
}

// A display reconnecting with its cursor is told each change it missed,
// two requests of one session apart, and nothing of their session,
// folder, account or error; a key turned off ends its stream; a cursor
// too old, or from before a restart, is told to take a snapshot.
func TestRouteEventsReplayAndRevocation(t *testing.T) {
	fresh(t)
	was := routeEventsRecheck
	routeEventsRecheck = 0
	t.Cleanup(func() { routeEventsRecheck = was })
	k, key := displayKey(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	cursor := activitySnapshot(t, srv.URL, key).Cursor
	for i := 0; i < 2; i++ {
		r := s.trace.begin(Route{Time: time.Now(), Agent: "codex", Session: "private-session", Folder: "private-folder",
			Model: "m", Order: []Weighed{{ID: "private-account", Who: "private-email", Provider: "p", Model: "m"}}})
		s.trace.update(r, func(r *Route) { r.Tries = []Try{{ID: "private-account", Model: "m", Start: time.Now()}} })
		s.trace.update(r, func(r *Route) { r.Done, r.Status, r.Error = true, 500, "private-error" })
	}
	res := activityStream(t, srv.URL, key, cursor)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(res.Status, res.Header)
	}
	scan := bufio.NewScanner(res.Body)
	ids := map[string]bool{}
	for i := 0; i < 6; i++ {
		ev := mustActivity(t, scan)
		b, _ := json.Marshal(ev)
		if strings.Contains(string(b), "private-") {
			t.Fatalf("private metadata in event: %s", b)
		}
		if ev.Kind == "request_finished" {
			ids[ev.Route.ID] = true
			if !ev.Route.Failed || ev.Route.Status != 500 || ev.Active != 0 || ev.Route.Provider != "p" {
				t.Fatal(ev)
			}
		}
	}
	if len(ids) != 2 {
		t.Fatal("two requests of one session were told as one", ids)
	}
	if _, err := access.Update("off-key", access.Change{Key: k.ID}); err != nil {
		t.Fatal(err)
	}
	s.trace.begin(Route{Time: time.Now(), Agent: "codex"}) // wakes the stream
	if ev, ok := nextActivity(t, scan); ok {
		t.Fatal("a key turned off was told another change", ev)
	}
	res.Body.Close()
	if _, err := access.Update("on-key", access.Change{Key: k.ID}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < routeEventsKeep; i++ {
		r := s.trace.begin(Route{Time: time.Now()})
		s.trace.update(r, func(r *Route) { r.Done = true })
	}
	if res := activityStream(t, srv.URL, key, cursor); res.StatusCode != 409 {
		t.Fatal("a cursor too old was taken", res.Status)
	}
	restarted := httptest.NewServer(lanGuard(New().Handler()))
	t.Cleanup(restarted.Close)
	if res := activityStream(t, restarted.URL, key, cursor); res.StatusCode != 409 {
		t.Fatal("a cursor from the gateway before was taken", res.Status)
	}
}

// A route that leaves the trace unfinished, as an unrouted request's
// stand-in does once its own route begins (drop), is told gone, and
// isn't in the next snapshot: a display would show it going forever.
func TestRouteEventsDroppedRouteGoes(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	cursor := activitySnapshot(t, srv.URL, "").Cursor
	stand := s.trace.begin(Route{Time: time.Now(), Agent: "codex"})
	if snap := activitySnapshot(t, srv.URL, ""); snap.Active != 1 {
		t.Fatal("the stand-in isn't going", snap)
	}
	s.trace.drop(stand)
	// written to after it went, as a request holding it may: not told
	s.trace.update(stand, func(r *Route) { r.Done, r.Status = true, 200 })
	if snap := activitySnapshot(t, srv.URL, ""); snap.Active != 0 || len(snap.Routes) != 0 {
		t.Fatal("a dropped route is still going", snap)
	}
	res := activityStream(t, srv.URL, "", cursor)
	scan := bufio.NewScanner(res.Body)
	if ev := mustActivity(t, scan); ev.Kind != "request_started" {
		t.Fatal(ev)
	}
	ev := mustActivity(t, scan)
	if ev.Kind != "request_dropped" || ev.Route.Phase != phaseDropped || ev.Active != 0 {
		t.Fatal("not told the route went:", ev)
	}
	s.trace.begin(Route{Time: time.Now(), Agent: "next"})
	if ev := mustActivity(t, scan); ev.Kind != "request_started" || ev.Route.Agent != "next" {
		t.Fatal("told of the dropped route after it went:", ev)
	}
}

// A stream ends once the gateway stops serving, for Shutdown not to
// wait its whole drain (15 minutes when handed over) for a display.
func TestRouteEventsEndWithTheGateway(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	res := activityStream(t, srv.URL, "", activitySnapshot(t, srv.URL, "").Cursor)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		io.Copy(io.Discard, res.Body)
	}()
	s.endStreams()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived the gateway's end")
	}
}

// A request that falls back from a member refusing it to one that
// streams is told live, its first content as it comes, with the
// provider and model that answered, while another request goes beside
// it; and none of their prompts or replies.
func TestRouteEventsLiveFallback(t *testing.T) {
	fresh(t)
	_, key := displayKey(t)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	slowOn(t, "a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quota exhausted", 429)
	}))
	slowOn(t, "b", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private-reply\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close) // after each stream's body is closed
	defer finish()
	stream := activityStream(t, srv.URL, key, activitySnapshot(t, srv.URL, key).Cursor)
	scan := bufio.NewScanner(stream.Body)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"private-prompt"}]}`))
			req.Header.Set("Authorization", "Bearer "+Token)
			req.Header.Set(SessionHeader, "private-session")
			res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
			if err == nil {
				_, err = io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
			done <- err
		}()
	}
	first := map[string]bool{}
	fallback := false
	for len(first) < 2 {
		ev := mustActivity(t, scan)
		if ev.Kind == "first_content" {
			if ev.Route.Provider != "b" || ev.Route.Model != "m" || ev.Route.TTFT <= 0 || ev.Route.Phase != phaseResponding {
				t.Fatal(ev)
			}
			first[ev.Route.ID] = true
			fallback = fallback || ev.Route.Fallback
		}
		if b, _ := json.Marshal(ev); strings.Contains(string(b), "private-") {
			t.Fatalf("content leaked: %s", b)
		}
	}
	if snap := activitySnapshot(t, srv.URL, key); len(snap.Routes) != 2 || !fallback {
		t.Fatalf("live requests: %d, fallback: %v", len(snap.Routes), fallback)
	}
	finish()
	for n := 0; n < 2; {
		if ev := mustActivity(t, scan); ev.Kind == "request_finished" {
			n++
		}
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if snap := activitySnapshot(t, srv.URL, key); len(snap.Routes) != 0 {
		t.Fatal(snap)
	}
	// the full trace's TTFT is the try's own, told once it is done
	s.trace.mu.Lock()
	defer s.trace.mu.Unlock()
	for _, r := range s.trace.routes {
		for _, tr := range r.Tries {
			if tr.Done && tr.Status == 200 && tr.TTFT <= 0 {
				t.Errorf("a streamed try's TTFT: %+v", tr)
			}
		}
	}
}
