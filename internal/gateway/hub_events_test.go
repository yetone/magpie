package gateway

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hubStream opens the hub's event stream at srv as an MCP client does
// after initialize, and is each event's data line as it comes.
func hubStream(t *testing.T, srv *httptest.Server) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+HubMCPPath+"claude", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", "s-1")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("the stream: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if got := res.Header.Get("Mcp-Session-Id"); got != "s-1" {
		t.Errorf("Mcp-Session-Id %q", got)
	}
	out := make(chan string, 16)
	go func() {
		defer res.Body.Close()
		defer close(out)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				out <- d
			}
		}
	}()
	return out
}

func (f *fakeHub) setVersion(v string) { f.mu.Lock(); f.version = v; f.mu.Unlock() }

// waitData is the next event's data on c, "" when none comes within d.
func waitData(c <-chan string, d time.Duration) string {
	select {
	case s := <-c:
		return s
	case <-time.After(d):
		return ""
	}
}

// A client holding the hub's event stream (another magpie that has this
// one as a Remote magpie, an agent) is told its tools changed whenever the
// hub's Version does: at once when the library says so (HubChanged), at
// the next look when another process changed it, and not otherwise
// (jjiabdhhdhjiann on X: a tool added here reached the other magpie only
// once it was restarted).
func TestHubEventsSayToolsChanged(t *testing.T) {
	fresh(t)
	was := hubWatchEvery
	hubWatchEvery = 50 * time.Millisecond
	t.Cleanup(func() { hubWatchEvery = was })
	f := &fakeHub{on: true, version: "1"}
	withHub(t, f)
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	events := hubStream(t, srv)

	if got := waitData(events, 300*time.Millisecond); got != "" {
		t.Fatalf("nothing changed, yet the stream said %s", got)
	}
	const changed = `{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`
	f.setVersion("2")
	HubChanged()
	if got := waitData(events, 5*time.Second); got != changed {
		t.Fatalf("after a change the stream said %q", got)
	}
	// changed by another process: seen at the next look, told once
	f.setVersion("3")
	if got := waitData(events, 5*time.Second); got != changed {
		t.Fatalf("after a change elsewhere the stream said %q", got)
	}
	if got := waitData(events, 300*time.Millisecond); got != "" {
		t.Fatalf("told twice of one change: %s", got)
	}
}

// The stream is guarded as the hub's POSTs are, a GET that doesn't ask
// for a stream is still refused, and initialize says the hub tells of
// changes.
func TestHubEventsGuarded(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	f := &fakeHub{on: true, version: "1"}
	withHub(t, f)
	h := lanGuard(New().Handler())
	get := func(addr string, hdr ...string) int {
		// a stream let through ends here, to be told apart by its code
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, HubMCPPath+"claude", nil)
		r.RemoteAddr, r.Host = addr, "127.0.0.1:3425"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	const stream = "text/event-stream"
	if c := get("192.0.2.7:4000", "Accept", stream); c != http.StatusForbidden {
		t.Errorf("another machine, not shared: %d", c)
	}
	newCaller(t, "Laptop")
	if c := get("192.0.2.7:4000", "Accept", stream); c != http.StatusUnauthorized {
		t.Errorf("another machine, no key: %d", c)
	}
	if c := get(here, "Accept", stream, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"); c != http.StatusForbidden {
		t.Errorf("a web page: %d", c)
	}
	if c := get(here, "Accept", stream, "Origin", "http://localhost:5173", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"); c != http.StatusForbidden {
		t.Errorf("a page on this computer: %d", c)
	}
	if c := get(here); c != http.StatusMethodNotAllowed {
		t.Errorf("a GET asking for no stream: %d", c)
	}

	w := hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"magpie","version":"1"}}}`)
	if !strings.Contains(w.Body.String(), `"tools":{"listChanged":true}`) {
		t.Errorf("initialize: %s", w.Body)
	}
}
