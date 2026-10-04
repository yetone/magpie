package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// /mcp/<server> is a remote MCP server magpie signed in to (#615), as the
// agents reach it: a POST's JSON answer and its event stream, a GET's
// stream and a DELETE go to the server with magpie's token in place of the
// gateway's, and the session's headers both ways. A token the server
// refuses is renewed once; one it won't renew is said to need signing in
// again, never with a 401 that would set the agent signing in itself.
func TestMCPProxy(t *testing.T) {
	fresh(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	call := func(method, body string, hdr ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, gw.URL+"/mcp/neon", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+Token)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	token, _ := mcpauth.Token(t.Context(), "neon")

	resp := call("POST", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "X-Team", "acme", "MCP-Protocol-Version", "2025-06-18")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"method":"initialize"`) || resp.Header.Get("Mcp-Session-Id") != "sess-1" {
		t.Fatalf("initialize: %d %s %v", resp.StatusCode, b, resp.Header)
	}
	if got := f.Seen.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("the server got Authorization %q, not magpie's token", got)
	}
	if f.Seen.Get("X-Team") != "acme" || f.Seen.Get("Mcp-Protocol-Version") != "2025-06-18" {
		t.Fatalf("headers not passed on: %v", f.Seen)
	}

	// a call answered as an event stream comes through as one
	resp = call("POST", `{"jsonrpc":"2.0","id":2,"method":"tools/call"}`, "Mcp-Session-Id", "sess-1")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(string(b), "notifications/progress") || !strings.Contains(string(b), `"id":2,"result"`) {
		t.Fatalf("tools/call: %v %s", resp.Header, b)
	}

	// a GET's stream is passed on as it comes, an event at a time
	resp = call("GET", "", "Mcp-Session-Id", "sess-1")
	rd := bufio.NewReader(resp.Body)
	first, err := rd.ReadString('\n')
	if err != nil || first != "id: 1\n" {
		t.Fatalf("stream: %q %v", first, err)
	}
	rest, _ := io.ReadAll(rd)
	resp.Body.Close()
	if !strings.Contains(string(rest), `"n":2`) {
		t.Fatalf("stream ended early: %s", rest)
	}

	resp = call("DELETE", "", "Mcp-Session-Id", "sess-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	if want := "DELETE  sess-1"; f.Calls[len(f.Calls)-1] != want {
		t.Fatalf("calls %q", f.Calls)
	}

	// the server ends the token early: renewed once, and the call goes through
	f.Revoke()
	resp = call("POST", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "tools/list") || f.Refreshed != 1 {
		t.Fatalf("after the server refused the token: %d %s, renewed %d", resp.StatusCode, b, f.Refreshed)
	}

	// it won't renew it: sign in again, and no 401 for the agent to act on
	f.Revoke()
	f.ForgetRefresh()
	resp = call("POST", `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("WWW-Authenticate") != "" || !strings.Contains(string(b), "sign in to it again") {
		t.Fatalf("a sign-in run out: %d %v %s", resp.StatusCode, resp.Header, b)
	}

	// a server magpie isn't signed in to
	req, _ := http.NewRequest("POST", gw.URL+"/mcp/notion", strings.NewReader("{}"))
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown server: %d", r2.StatusCode)
	}
}

// Another machine is lent magpie's sign-ins only through the gateway shared
// on the local network, with its key.
func TestMCPProxyStaysHere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	h := lanGuard(New().Handler())
	r := httptest.NewRequest("POST", "/mcp/neon", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	r.RemoteAddr = "192.168.1.9:5000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(f.Calls) != 0 {
		t.Fatalf("another machine got %d, calls %v", w.Code, f.Calls)
	}
}

// A web page can't borrow magpie's sign-ins through the browser: a request
// with an Origin, or one to a hostname of the page's own pointed at
// 127.0.0.1, is refused before it reaches the server.
func TestMCPProxyNotForWebPages(t *testing.T) {
	fresh(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	h := New().Handler()
	for _, c := range []struct{ host, origin string }{
		{"127.0.0.1:3425", "https://evil.example"},
		{"evil.example:3425", ""},
	} {
		r := httptest.NewRequest("POST", "/mcp/neon", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || len(f.Calls) != 0 {
			t.Fatalf("host %s origin %q: got %d, calls %v", c.host, c.origin, w.Code, f.Calls)
		}
	}
	for _, host := range []string{"localhost:3425", "[::1]:3425", "127.0.0.1"} {
		if !loopbackHost(host) {
			t.Fatalf("%s is this computer", host)
		}
	}
}

// A server signed in to at an address with a query (Exa's
// mcp.exa.ai/mcp?login, where its sign-in is) is reached at that address,
// the query as it was, and the headers the Library gives it (a key) go
// with magpie's token; only the gateway's own key stays here.
func TestMCPProxyKeepsQueryAndHeaders(t *testing.T) {
	fresh(t)
	f := mcpauthtest.New(t)
	mcpauthtest.SignIn(t, "exa", f.URL+"?login")
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	call := func(body string, hdr ...string) {
		t.Helper()
		req, _ := http.NewRequest("POST", gw.URL+"/mcp/exa", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", body, resp.StatusCode, b)
		}
	}
	call(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, "X-Api-Key", "exa-key", "X-Exa-Source", "magpie")
	if f.SeenQuery != "login" {
		t.Fatalf("the server got the query %q, not login", f.SeenQuery)
	}
	if f.Seen.Get("X-Api-Key") != "exa-key" || f.Seen.Get("X-Exa-Source") != "magpie" {
		t.Fatalf("headers not passed on: %v", f.Seen)
	}
	token, _ := mcpauth.Token(t.Context(), "exa")
	if f.Seen.Get("Authorization") != "Bearer "+token {
		t.Fatalf("Authorization %q", f.Seen.Get("Authorization"))
	}

	// the gateway's own key isn't the server's
	call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "X-Api-Key", Token)
	if f.Seen.Get("X-Api-Key") != "" {
		t.Fatalf("the server got the gateway's key %q", f.Seen.Get("X-Api-Key"))
	}
}
