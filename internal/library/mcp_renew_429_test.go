package library

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// routerCall asks the gateway's MCP router at gw what an agent asks it.
func routerCall(t *testing.T, gw, agent, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, gw+"/mcp/magpie/hub/"+agent, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", method, resp.StatusCode, b)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %s: %v", method, b, err)
	}
	return out
}

// #7451 (Asher-S-Wu): Canva's token endpoint answered a renewal 429, and
// every request through the router after it, a call to another server's
// tool too, asked Canva to renew again. Canva, whose token is due and
// whose tools aren't listed yet, is asked once; calls to the other server
// go through without asking it again.
func TestRouter429RenewsOnce(t *testing.T) {
	hubSandbox(t)
	f := mcpauthtest.New(t)
	f.ExpiresIn = 60 // within renewLead: due at once
	f.Tools = []string{"design"}
	var seen http.Header
	other := httptest.NewServer(mcpHandler(t, false, &seen))
	t.Cleanup(other.Close)
	ok(t)(SaveServer("", Server{Name: "canva", Transport: "http", URL: f.URL, Agents: []string{"codex"}}))
	ok(t)(SaveServer("", Server{Name: "other", Transport: "http", URL: other.URL, Agents: []string{"codex"}}))
	f.SignIn(t, "canva")
	ok(t)(SetMCPHub(true))
	f.Throttle("60")
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)

	if r := routerCall(t, gw.URL, "codex", "tools/list", map[string]any{}); r["error"] != nil {
		t.Fatalf("tools/list: %v", r["error"])
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("tools/list asked Canva to renew %d times, want 1", n)
	}
	for i := range 3 {
		r := routerCall(t, gw.URL, "codex", "tools/call", map[string]any{"name": "other__a", "arguments": map[string]any{}})
		if r["error"] != nil {
			t.Fatalf("call %d to the other server: %v", i, r["error"])
		}
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("after 3 calls to the other server Canva was asked to renew %d times, want 1 (Retry-After: 60)", n)
	}
}

// The same with Canva's token run out, as the reporter had it: Canva's
// tools are left out while its renewal waits, the other server's are
// listed and called, and Canva is asked to renew once in the wait, not on
// every request.
func TestRouter429RunOutToken(t *testing.T) {
	hubSandbox(t)
	now := time.Now()
	var skew atomic.Int64
	t.Cleanup(mcpauth.SetClockForTest(func() time.Time { return now.Add(time.Duration(skew.Load())) }))
	f := mcpauthtest.New(t)
	f.ExpiresIn = 60
	f.Tools = []string{"design"}
	var seen http.Header
	other := httptest.NewServer(mcpHandler(t, false, &seen))
	t.Cleanup(other.Close)
	ok(t)(SaveServer("", Server{Name: "canva", Transport: "http", URL: f.URL, Agents: []string{"codex"}}))
	ok(t)(SaveServer("", Server{Name: "other", Transport: "http", URL: other.URL, Agents: []string{"codex"}}))
	f.SignIn(t, "canva")
	ok(t)(SetMCPHub(true))
	skew.Store(int64(61 * time.Second)) // the token has run out
	f.Throttle("600")
	gw := httptest.NewServer(gateway.New().Handler())
	t.Cleanup(gw.Close)

	r := routerCall(t, gw.URL, "codex", "tools/list", map[string]any{})
	b, _ := json.Marshal(r["result"])
	if !strings.Contains(string(b), `"other__a"`) || strings.Contains(string(b), "canva__design") {
		t.Fatalf("tools/list in the wait: %s", b)
	}
	for i := range 3 {
		r := routerCall(t, gw.URL, "codex", "tools/call", map[string]any{"name": "other__a", "arguments": map[string]any{}})
		if r["error"] != nil {
			t.Fatalf("call %d to the other server: %v", i, r["error"])
		}
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("Canva was asked to renew %d times in its wait, want 1", n)
	}
	// the wait over, Canva is renewed and its tools come back
	skew.Store(int64(661 * time.Second))
	f.ExpiresIn = 3600
	f.Unthrottle()
	r = routerCall(t, gw.URL, "codex", "tools/list", map[string]any{})
	b, _ = json.Marshal(r["result"])
	if !strings.Contains(string(b), "canva__design") || f.RefreshAsked() != 2 {
		t.Fatalf("after the wait: asked %d times, tools %s", f.RefreshAsked(), b)
	}
}
