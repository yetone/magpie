package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// fakeRouter is another magpie's router as this one reaches it: its tools
// are tools, and when stream is set, a GET opens its event stream, on which
// say tells its listeners their tools changed. down has it answer no
// request.
type fakeRouter struct {
	*httptest.Server
	mu        sync.Mutex
	tools     []string
	stream    bool
	down      bool
	lists     int
	listeners []chan struct{}
	opened    chan struct{}
}

func newFakeRouter(t *testing.T, stream bool, tools ...string) *fakeRouter {
	t.Helper()
	f := &fakeRouter{tools: tools, stream: stream, opened: make(chan struct{}, 8)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	// Close waits for the event streams the hub holds open
	t.Cleanup(func() { f.CloseClientConnections(); f.Close() })
	return f
}

func (f *fakeRouter) url() string { return f.URL + gateway.HubMCPAll }

func (f *fakeRouter) set(tools ...string) {
	f.mu.Lock()
	f.tools = tools
	f.mu.Unlock()
}

// say is the router's tools changing: its listeners are told.
func (f *fakeRouter) say() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.listeners {
		c <- struct{}{}
	}
}

func (f *fakeRouter) listed() int { f.mu.Lock(); defer f.mu.Unlock(); return f.lists }

func (f *fakeRouter) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down, stream := f.down, f.stream
	f.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	if r.Method == http.MethodGet {
		if !stream {
			http.Error(w, "no stream", http.StatusMethodNotAllowed)
			return
		}
		c := make(chan struct{}, 8)
		f.mu.Lock()
		f.listeners = append(f.listeners, c)
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.listeners = slices.DeleteFunc(f.listeners, func(x chan struct{}) bool { return x == c })
			f.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, ": magpie\n\n")
		w.(http.Flusher).Flush()
		f.opened <- struct{}{}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-c:
				fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}
	var m struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
	}
	json.NewDecoder(r.Body).Decode(&m)
	w.Header().Set("Content-Type", "application/json")
	switch m.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "s-1")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"magpie","title":"Magpie MCP hub · gateway","version":"0.1.4800"}}}`, *m.ID)
	case "tools/list":
		f.mu.Lock()
		f.lists++
		var ts []string
		for _, n := range f.tools {
			ts = append(ts, fmt.Sprintf(`{"name":%q,"description":"Does %s.","inputSchema":{"type":"object"}}`, n, n))
		}
		f.mu.Unlock()
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[%s]}}`, *m.ID, strings.Join(ts, ","))
	default:
		w.WriteHeader(202)
	}
}

// hubToolsOf is the tools of server the hub gives claude now.
func hubToolsOf(server string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []string
	for _, t := range (theHub{}).Tools(ctx, "claude") {
		if t.Server == server {
			out = append(out, t.Tool)
		}
	}
	slices.Sort(out)
	return out
}

// eventually waits for the hub to give claude want of server.
func eventually(t *testing.T, server string, want []string) {
	t.Helper()
	var got []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if got = hubToolsOf(server); slices.Equal(got, want) {
			return
		}
	}
	t.Fatalf("the hub gives %s %v, want %v", server, got, want)
}

// A tool added at the magpie this one reaches as a Remote magpie (its
// router, here a fake one) is given here as soon as that router says its
// tools changed (jjiabdhhdhjiann on X: it was given only once this magpie
// was restarted, as the router's first list was kept for good), and this
// hub's own clients are told then, once, not before.
func TestHubHearsAnotherRouterChange(t *testing.T) {
	hubSandbox(t)
	gw := newFakeRouter(t, true, "fetch__fetch")
	ok(t)(SaveServer("", Server{Name: "gw", Transport: "http", URL: gw.url(), Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	if got := hubToolsOf("gw"); !slices.Equal(got, []string{"fetch__fetch"}) {
		t.Fatalf("the hub gives gw %v", got)
	}
	select {
	case <-gw.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the router's event stream wasn't opened")
	}
	v := (theHub{}).Version()
	if hubToolsOf("gw"); (theHub{}).Version() != v {
		t.Fatal("the hub's Version changed with nothing changed")
	}

	gw.set("fetch__fetch", "time__now")
	gw.say()
	eventually(t, "gw", []string{"fetch__fetch", "time__now"})
	if (theHub{}).Version() == v {
		t.Error("the hub's Version didn't change with gw's tools")
	}

	// told again with nothing changed: listed again, the Version kept
	v = (theHub{}).Version()
	n := gw.listed()
	gw.say()
	for end := time.Now().Add(5 * time.Second); gw.listed() == n && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
	}
	if gw.listed() == n {
		t.Fatal("told its tools changed, the router wasn't listed again")
	}
	time.Sleep(50 * time.Millisecond)
	if (theHub{}).Version() != v {
		t.Error("the Version changed with the router's tools the same, so two magpies would tell each other round")
	}
}

// A router that offers no event stream (an older magpie) is listed again
// once its list is routerFresh old, and one that doesn't answer then has
// its last list given.
func TestHubAsksAnOlderRouterAgain(t *testing.T) {
	hubSandbox(t)
	was := routerFresh
	routerFresh = 100 * time.Millisecond
	t.Cleanup(func() { routerFresh = was })
	gw := newFakeRouter(t, false, "fetch__fetch")
	ok(t)(SaveServer("", Server{Name: "gw", Transport: "http", URL: gw.url(), Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	if got := hubToolsOf("gw"); !slices.Equal(got, []string{"fetch__fetch"}) {
		t.Fatalf("the hub gives gw %v", got)
	}
	gw.set("fetch__fetch", "time__now")
	eventually(t, "gw", []string{"fetch__fetch", "time__now"})

	gw.mu.Lock()
	gw.down = true
	gw.mu.Unlock()
	time.Sleep(2 * routerFresh)
	if got := hubToolsOf("gw"); !slices.Equal(got, []string{"fetch__fetch", "time__now"}) {
		t.Errorf("the router not answering, the hub gives %v", got)
	}
}

// A server that isn't another magpie's router is listed once, as before:
// it says itself when its tools change.
func TestHubListsOtherServersOnce(t *testing.T) {
	hubSandbox(t)
	was := routerFresh
	routerFresh = 0
	t.Cleanup(func() { routerFresh = was })
	gw := newFakeRouter(t, false, "fetch")
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: gw.URL + "/mcp", Agents: []string{"claude"}}))
	ok(t)(SetMCPHub(true))
	hubToolsOf("web")
	hubToolsOf("web")
	if n := gw.listed(); n != 1 {
		t.Errorf("listed %d times", n)
	}
}
