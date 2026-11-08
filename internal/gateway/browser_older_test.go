package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/browserext"
)

// An extension older than magpie's keeps running its old code until it is
// reloaded, though magpie rewrote its folder: the first answer on that
// connection tells the agent to have the user reload it (or, for one
// connected with a key, download it again), and only the first.
func TestBrowserSaysTheExtensionIsOlder(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	s := New()
	const testPeer = "X-Test-Peer"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get(testPeer); p != "" {
			r.RemoteAddr, r.Host = p, "192.0.2.1:3425"
			r.Header.Del(testPeer)
		}
		lanGuard(s.Handler()).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	page := func(_, _, _ string, _ map[string]any) any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "Page: Example"}}}
	}
	h := lanGuard(s.Handler())
	call := func(addr string, hdr ...string) []string {
		t.Helper()
		m := readImageReply(t, browserPost(h, addr, imageToolCall("browser_navigate", map[string]any{"url": "example.com"}), hdr...))
		if m.Result.IsError {
			t.Fatalf("an error: %+v", m.Result.Content)
		}
		var all []string
		for _, c := range m.Result.Content {
			all = append(all, c.Text)
		}
		return all
	}
	const old = "0.0.1"
	if !browserext.Older(old, browserext.Version()) {
		t.Fatalf("%s isn't older than %s", old, browserext.Version())
	}

	fakeExtensionVersion(t, srv.URL, "", nil, "Chrome/141", old, page)
	browserConnected(t, s)
	got := call("127.0.0.1:5000", BrowserSessionHeader, "s1")
	if len(got) != 2 || got[1] != "Page: Example" || !strings.Contains(got[0], old) || !strings.Contains(got[0], browserext.Version()) ||
		!strings.Contains(got[0], "reloaded on chrome://extensions") || strings.Contains(got[0], "downloaded again") {
		t.Fatalf("the first answer: %q", got)
	}
	// once a connection, whichever session asks next
	for _, session := range []string{"s1", "s2"} {
		if got := call("127.0.0.1:5000", BrowserSessionHeader, session); len(got) != 1 || got[0] != "Page: Example" {
			t.Fatalf("%s's next answer: %q", session, got)
		}
	}

	// connected with a key, from elsewhere: downloaded again
	keys, secrets := newCaller(t, "Host browser")
	fakeExtensionVersion(t, srv.URL, secrets[0], http.Header{testPeer: {"172.17.0.1:40000"}}, "Chrome/141", old, page)
	for deadline := time.Now().Add(10 * time.Second); len(s.browser.all(keyedSession(keys[0].ID, "x"))) < 1; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the keyed extension never connected")
		}
	}
	mine := []string{"Authorization", "Bearer " + secrets[0], BrowserSessionHeader, "s1"}
	got = call("172.17.0.1:40000", mine...)
	if len(got) != 2 || !strings.Contains(got[0], "downloaded again from magpie's Library → Magpie Browser") {
		t.Fatalf("the keyed one's first answer: %q", got)
	}
	if got := call("172.17.0.1:40000", mine...); len(got) != 1 {
		t.Fatalf("the keyed one's next answer: %q", got)
	}
}

// One as new as magpie's is never told about.
func TestBrowserSaysNothingOfACurrentExtension(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	fakeExtension(t, srv.URL, func(_, _, _ string, _ map[string]any) any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "Page: Example"}}}
	})
	browserConnected(t, s)
	m := readImageReply(t, browserPost(lanGuard(s.Handler()), "127.0.0.1:5000", imageToolCall("browser_navigate", map[string]any{"url": "example.com"}), BrowserSessionHeader, "s1"))
	if len(m.Result.Content) != 1 || m.Result.Content[0].Text != "Page: Example" {
		t.Fatalf("%+v", m.Result.Content)
	}
}
