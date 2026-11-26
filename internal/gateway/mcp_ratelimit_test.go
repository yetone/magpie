package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// #7451 (Asher-S-Wu): a renewal the server's token endpoint turns away
// with 429 reaches the agent as a 429 with the wait in Retry-After, not
// as a sign-in run out, and requests in the wait don't ask the server to
// renew again.
func TestMCPProxyRenewal429(t *testing.T) {
	fresh(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "canva")
	gw := httptest.NewServer(New().Handler())
	defer gw.Close()
	f.Revoke()
	f.Throttle("60")
	for i := range 3 {
		req, _ := http.NewRequest("POST", gw.URL+"/mcp/canva", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ra := resp.Header.Get("Retry-After")
		if resp.StatusCode != http.StatusTooManyRequests || (ra != "60" && ra != "59") || resp.Header.Get("WWW-Authenticate") != "" {
			t.Fatalf("request %d: %d Retry-After %q %v %s", i, resp.StatusCode, ra, resp.Header, b)
		}
		if !strings.Contains(string(b), "canva") || strings.Contains(string(b), "sign in to it again") {
			t.Fatalf("request %d says %s", i, b)
		}
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("asked the server to renew %d times, want 1", n)
	}
}
