package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
	"github.com/yetone/magpie/internal/usageapi"
)

// rdsaltbo on X (#6060): GET /v1/magpie/usage/requests?cursor= reads the
// requests as changes. Over HTTP: ids, the vendor's request id apart from
// magpie's own, a cursor that resumes with exactly the new rows, the
// refusals that are not a silent gap, and a held key's own calls alone.
func TestUsageRequestsFeedOverGateway(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	usage.ResetFeedForTest()
	t.Cleanup(usage.ResetFeedForTest)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "req_vendor_1")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":5}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Laptop", "Held")
	if _, err := access.Update("limit-key", access.Change{Key: keys[1].ID, Limit: &access.Limit{Period: "day", Tokens: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
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
	chat := func(secret string) {
		t.Helper()
		if c, b := do("POST", "/v1/chat/completions", "127.0.0.1:5000", strings.NewReader(chatReq), "Authorization", "Bearer "+secret); c != 200 {
			t.Fatal(c, b)
		}
	}
	read := func(q, from string, hdr ...string) (int, usageapi.FeedResult, string) {
		t.Helper()
		c, b := do("GET", "/v1/magpie/usage/requests?"+q, from, nil, hdr...)
		var out usageapi.FeedResult
		if c == 200 {
			if err := json.Unmarshal([]byte(b), &out); err != nil {
				t.Fatal(err, b)
			}
		}
		return c, out, b
	}
	chat(secrets[0])
	chat(secrets[1])

	c, p, b := read("cursor=start&limit=1", "127.0.0.1:5000")
	if c != 200 || len(p.Rows) != 1 || !p.HasMore || p.NextCursor == "" {
		t.Fatal("first page:", c, b)
	}
	if strings.Contains(b, `"client_ip"`) {
		t.Fatal("the address a call came from leaves this computer:", b)
	}
	row := p.Rows[0]
	if row.ID == "" || row.Origin != "gateway" || row.Seq == 0 || row.Granularity() != "request" {
		t.Fatalf("row: %+v", row)
	}
	c, p2, b := read("cursor="+p.NextCursor+"&limit=10", "127.0.0.1:5000")
	if c != 200 || len(p2.Rows) != 1 || p2.HasMore || p2.Rows[0].ID == row.ID {
		t.Fatal("second page:", c, b)
	}
	// the page of a client that crashed before it committed: the same again
	if c, again, _ := read("cursor="+p.NextCursor+"&limit=10", "127.0.0.1:5000"); c != 200 || len(again.Rows) != 1 || again.Rows[0].ID != p2.Rows[0].ID {
		t.Fatal("replay:", c, again)
	}

	// a new call arrives: the resumed read has it and only it
	chat(secrets[0])
	c, p3, b := read("cursor="+p2.NextCursor, "127.0.0.1:5000")
	if c != 200 || len(p3.Rows) != 1 || len(p3.Deleted) != 0 || p3.Rows[0].ID == row.ID || p3.Rows[0].ID == p2.Rows[0].ID {
		t.Fatal("the new call alone:", c, b)
	}

	// the old shape is unchanged without ?cursor=
	var old usageapi.Ledger
	if c, b := do("GET", "/v1/magpie/usage/requests?period=all", "127.0.0.1:5000", nil); c != 200 || json.Unmarshal([]byte(b), &old) != nil || old.Total != 3 || strings.Contains(b, "next_cursor") {
		t.Fatal("offset mode:", c, b)
	}

	// refusals say why
	for _, tc := range []struct {
		q    string
		code int
		word string
	}{
		{"cursor=nonsense", 400, "cursor_invalid"},
		{"cursor=" + p3.NextCursor + "&agent=claude", 400, "cursor_filter_changed"},
	} {
		if c, b := do("GET", "/v1/magpie/usage/requests?"+tc.q, "127.0.0.1:5000", nil); c != tc.code || !strings.Contains(b, tc.word) {
			t.Fatal(tc.q, c, b)
		}
	}
	// another computer needs a key
	if c, _ := do("GET", "/v1/magpie/usage/requests?cursor=start", "192.168.1.9:5000", nil); c != http.StatusUnauthorized {
		t.Fatal("no key:", c)
	}

	// the held key: its own call alone, and a cursor of everyone's is refused
	c, held, b := read("cursor=start", "192.168.1.9:5000", "x-api-key", secrets[1])
	if c != 200 || len(held.Rows) != 1 || held.Rows[0].CallerKeyID != keys[1].ID {
		t.Fatal("held key's feed:", c, b)
	}
	if c, _ := do("GET", "/v1/magpie/usage/requests?cursor="+p3.NextCursor, "192.168.1.9:5000", nil, "x-api-key", secrets[1]); c != 400 {
		t.Fatal("a cursor of the whole ledger must not resume as a held key's:", c)
	}
}
