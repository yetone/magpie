package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// lastResponsesEvent is the last data line of a Responses stream, parsed.
func lastResponsesEvent(t *testing.T, body string) map[string]any {
	t.Helper()
	var last string
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(d, "{") {
			last = d
		}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(last), &v); err != nil {
		t.Fatalf("last event %q: %v\n%s", last, err, body)
	}
	return v
}

// wholeFailedResponse checks a response.failed as async-openai's
// ResponseFailedEvent reads it (Grok Build's serde): the event's
// sequence_number, and a Response with id, object, created_at, model,
// output and status. Grok Build 1.0.67 said "missing field `created_at`"
// and then "missing field `sequence_number`" for each one gone.
func wholeFailedResponse(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	if v["type"] != "response.failed" {
		t.Fatalf("last event is %v, not response.failed: %v", v["type"], v)
	}
	if _, ok := v["sequence_number"].(float64); !ok {
		t.Errorf("no sequence_number: %v", v)
	}
	r, _ := v["response"].(map[string]any)
	for _, k := range []string{"id", "object", "status"} {
		if s, _ := r[k].(string); s == "" {
			t.Errorf("response.%s missing: %v", k, r)
		}
	}
	if _, ok := r["model"].(string); !ok {
		t.Errorf("response.model missing: %v", r)
	}
	if c, _ := r["created_at"].(float64); c <= 0 {
		t.Errorf("response.created_at missing: %v", r)
	}
	if _, ok := r["output"].([]any); !ok {
		t.Errorf("response.output not a list: %v", r)
	}
	if e, _ := r["error"].(map[string]any); e["message"] == nil {
		t.Errorf("response.error missing: %v", r)
	}
	return r
}

// A Responses stream that ends short after its output began is ended
// with a response.failed a Responses client can read: the response the
// stream began (its id and created_at), numbered after its last event.
// magpie's own had only object, status and error, and Grok Build failed
// the turn on it with "serialization error: missing field `created_at`"
// instead of saying the error (plugins#852).
func TestResponsesStreamFailureIsAWholeResponse(t *testing.T) {
	fresh(t)
	up := &scripted{replies: []reply{{200, "text/event-stream", sse(
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_up1","object":"response","created_at":1762000000,"status":"in_progress","model":"m","output":[]}}`,
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"pong"}`,
	)}}}
	scriptedOn(t, "a", provider.Responses, up)
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)
	req, _ := http.NewRequest("POST", gw.URL+"/v1/responses", strings.NewReader(`{"model":"a/m","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	if res.StatusCode != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	v := lastResponsesEvent(t, body)
	r := wholeFailedResponse(t, v)
	if r["id"] != "resp_up1" || r["created_at"] != float64(1762000000) {
		t.Errorf("not the response the stream began: %v", r)
	}
	if v["sequence_number"] != float64(3) {
		t.Errorf("sequence_number %v, want 3 (after the stream's last)", v["sequence_number"])
	}
}

// With nothing of the response said yet, the failure is still whole: an
// id and created_at of its own, numbered first.
func TestResponsesStreamFailureBeforeAnyEvent(t *testing.T) {
	b := streamFailure(provider.Responses, "x: connection lost mid-reply (unexpected EOF)")
	r := wholeFailedResponse(t, lastResponsesEvent(t, string(b)))
	if id, _ := r["id"].(string); !strings.HasPrefix(id, "resp_") {
		t.Errorf("id %q", id)
	}
	if !strings.HasPrefix(string(b), "event: response.failed\n") {
		t.Errorf("%s", b)
	}
}
