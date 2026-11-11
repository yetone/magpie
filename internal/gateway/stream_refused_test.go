package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A claude-relay-service Responses endpoint answers a request for one body
// 400 "Stream must be set to true" (Cyrus_4433 on X). An agent that sent
// stream:false gets its whole reply all the same: the request is asked
// again streamed, and the next one is asked so at once.
func TestWholeReplyFromAStreamOnlyBackend(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if !gjson.GetBytes(b, "stream").Bool() {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Stream must be set to true","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"item_id":"m1","content_index":0,"delta":"pong"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong"}]}],"usage":{"input_tokens":3,"output_tokens":1}}}`,
		))
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "My Relay", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	for i := range 2 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"relay/gpt-6-sol","stream":false,"input":"hi"}`)))
		if rec.Code != 200 || gjson.Get(rec.Body.String(), "output.0.content.0.text").String() != "pong" {
			t.Fatalf("ask %d: HTTP %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// the first is refused once, then asked streamed; the second is asked
	// streamed at once
	if len(bodies) != 3 || gjson.Get(bodies[0], "stream").Bool() || !gjson.Get(bodies[1], "stream").Bool() || !gjson.Get(bodies[2], "stream").Bool() {
		t.Fatalf("asked %d times: %q", len(bodies), bodies)
	}
}

// Another 400 is the client's, as before: a request for one body that is
// refused for something else is asked once.
func TestOtherRefusalsAreNotStreamOnly(t *testing.T) {
	fresh(t)
	n := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"input is too long"}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "My Relay", Key: "k", Responses: up.URL + "/v1", Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"relay/gpt-6-sol","input":"hi"}`)))
	if rec.Code != 400 || n != 1 {
		t.Fatalf("HTTP %d after %d asks: %s", rec.Code, n, rec.Body.String())
	}
}
