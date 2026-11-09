package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// A request's row says where it came from (geesehowardt7 on X): the
// connection's address, loopback for this computer, never one a forwarding
// header claims. The Usage page reads it from its own ledger; the
// gateway's usage API, which answers other computers, leaves it out.
func TestUsageRowsKeepTheClientsAddress(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	_, secrets := newCaller(t, "Laptop")
	h := lanGuard(New().Handler())
	do := func(method, path, from string, body io.Reader, hdr ...string) (int, string) {
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if c, b := do("POST", "/v1/chat/completions", "127.0.0.1:5000", strings.NewReader(chatReq)); c != 200 {
		t.Fatal("loopback:", c, b)
	}
	if c, b := do("POST", "/v1/chat/completions", "192.168.1.9:6000", strings.NewReader(chatReq),
		"Authorization", "Bearer "+secrets[0], "X-Forwarded-For", "10.9.9.9"); c != 200 {
		t.Fatal("another machine:", c, b)
	}
	if c, b := do("POST", "/v1/chat/completions", "[fe80::1%en0]:7000", strings.NewReader(chatReq), "Authorization", "Bearer "+secrets[0]); c != 200 {
		t.Fatal("link-local v6:", c, b)
	}
	got := map[string]bool{}
	for _, r := range usage.Load(time.Time{}) {
		got[r.ClientIP] = true
	}
	if len(got) != 3 || !got["127.0.0.1"] || !got["192.168.1.9"] || !got["fe80::1"] {
		t.Fatalf("addresses kept: %v", got)
	}

	// the GUI's own ledger has them
	own := usageapi.LedgerPage(usage.All, usage.Filter{}, 0, 50)
	n := 0
	for _, r := range own.Rows {
		if r.ClientIP != "" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("the Usage page's rows: %d with an address", n)
	}

	// the gateway's API leaves them out, for this computer and another
	for _, from := range []string{"127.0.0.1:5000", "192.168.1.9:6000"} {
		c, b := do("GET", "/v1/magpie/usage/requests?period=all", from, nil, "Authorization", "Bearer "+secrets[0])
		var page usageapi.Ledger
		if c != 200 || json.Unmarshal([]byte(b), &page) != nil || len(page.Rows) != 3 {
			t.Fatal("requests from", from, c, b)
		}
		if strings.Contains(b, "client_ip") || strings.Contains(b, "192.168.1.9") {
			t.Fatal("the gateway's usage API told an address to", from, b)
		}
	}
}
