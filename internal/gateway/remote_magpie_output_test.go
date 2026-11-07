package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// Codex offers web_search on every turn and names no reply limit. A turn
// for a Remote magpie's model is translated here when the search is
// magpie's to do (searchedForIt), and the other magpie, given a body it
// can pass whole, relays it — its own filling is translation work it never
// does. So the model's published output has to go out on the first hop,
// or the vendor's own short default is all the cascade gets (#5566):
// Codex stopped at 1,024 output tokens two magpies deep where the same
// model asked direct of the far one completed.
func TestRemoteMagpieCascadeAsksItsOutputLimit(t *testing.T) {
	var mu sync.Mutex
	var hop, vendor []string // the bodies the far magpie, and its vendor, were sent
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"resp_1","model":"m1"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"mc1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"mc1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_1","model":"m1","usage":{"input_tokens":5,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	// the far magpie publishes the model's reply limit beside its window,
	// as the user's Max output there does
	if err := provider.SetModelOutput("relay/m1", 393216); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			mu.Lock()
			hop = append(hop, string(b))
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Codex's turn: the search tool declared, no reply limit named
	ask := `{"model":"office/relay/m1","input":"Output the integers from 1 through 600, then END600.","tools":[{"type":"web_search"}],"stream":true}`
	code, reply := post(t, "/v1/responses", ask)
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, reply)
	}
	mu.Lock()
	if len(hop) != 1 || len(vendor) != 1 {
		t.Fatalf("hops %d, vendor asks %d", len(hop), len(vendor))
	}
	body, sent := hop[0], vendor[0]
	mu.Unlock()
	// the first hop translated the turn (the search tool is gone) and
	// named the model's published output; the second relayed the body it
	// got, its ceiling only lowering a larger one
	if got := gjson.Get(body, "max_output_tokens").Int(); got != 393216 {
		t.Errorf("the far magpie was sent max_output_tokens %v, want 393216: %s", got, body)
	}
	if got := gjson.Get(sent, "max_output_tokens").Int(); got != 393216 {
		t.Errorf("the vendor was sent max_output_tokens %v, want 393216: %s", got, sent)
	}
	if gjson.Get(body, "tools.#(type==\"web_search\")").Exists() {
		t.Errorf("the search tool went out converted: %s", body)
	}
	if got := gjson.Get(sent, "model").String(); got != "m1" {
		t.Errorf("the vendor was asked for %q, want m1", got)
	}

	// a limit the client named goes as it is
	code, reply = post(t, "/v1/responses", `{"model":"office/relay/m1","input":"Output the integers from 1 through 600, then END600.","tools":[{"type":"web_search"}],"max_output_tokens":8192,"stream":true}`)
	if code != 200 {
		t.Fatalf("HTTP %d: %s", code, reply)
	}
	mu.Lock()
	if n := len(vendor); n != 2 {
		t.Fatalf("vendor asks %d, want 2", n)
	}
	if got := gjson.Get(vendor[1], "max_output_tokens").Int(); got != 8192 {
		t.Errorf("the vendor was sent max_output_tokens %v, want the client's 8192: %s", got, vendor[1])
	}
	mu.Unlock()
}
