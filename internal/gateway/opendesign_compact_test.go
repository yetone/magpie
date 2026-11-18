package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// amrRefusal is how Open Design's amr-link turns a request away for the
// vendor behind it (#5219, #4852): a 400 upstream_error that names no
// field.
const amrRefusal = `{"error":{"message":"[code=upstream_error] Provider returned error","type":"upstream_error","code":"upstream_error"}}`

var amrSummary = sse(
	`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"OD SUMMARY"}]}}`,
	`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)

// openDesignCompaction posts Codex Desktop 26.1007's real compaction
// (compaction2352) at Open Design's plugin, its model on chat completions
// and served on Responses too, as amr-link serves every model (FAKE_ALSO),
// to a vendor that refuses what refuse says. It gives Codex's answer and
// the bodies the vendor got.
func openDesignCompaction(t *testing.T, refuse func(b []byte) bool) (*httptest.ResponseRecorder, [][]byte) {
	t.Helper()
	t.Setenv("FAKE_ALSO", "1")
	var mu sync.Mutex
	var got [][]byte
	pid := besideFake(t, "opendesign", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("asked at %s", r.URL.Path)
		}
		if refuse(b) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, amrRefusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, amrSummary)
	}))
	body, turn := compaction2352(t)
	body = strings.Replace(body, `"model":"fake/m1"`, `"model":"`+pid+`/fake-also"`, 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("User-Agent", "Codex Desktop/26.1007 (Mac OS 26.2.0; arm64)")
	req.Header.Set("originator", "Codex Desktop")
	req.Header.Set(turnMetadataHeader, turn)
	New().Handler().ServeHTTP(rec, req)
	mu.Lock()
	defer mu.Unlock()
	return rec, got
}

// Mugenrikka on X: Codex compacting on any Open Design model got 400
// upstream_error, while the turns around it went through. Codex's
// compaction is its one request with no tools ("tools": []), and magpie's
// summary ask added tool_choice and parallel_tool_calls beside them. With
// nothing to call, amr-link is sent none of the three, and the summary
// comes back.
func TestOpenDesignCompactionSendsNoEmptyTools(t *testing.T) {
	lone := func(b []byte) bool {
		var q map[string]json.RawMessage
		json.Unmarshal(b, &q)
		var tools []any
		json.Unmarshal(q["tools"], &tools)
		_, choice := q["tool_choice"]
		_, parallel := q["parallel_tool_calls"]
		_, listed := q["tools"]
		return len(tools) == 0 && (listed || choice || parallel)
	}
	rec, got := openDesignCompaction(t, lone)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"compaction"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(got) != 1 {
		t.Errorf("asked %d times, want once", len(got))
	}
	if len(got) > 0 && (lone(got[0]) || !bytes.Contains(got[0], []byte("CONTEXT CHECKPOINT COMPACTION"))) {
		t.Errorf("amr-link got %.600s", got[0])
	}
}

// A summary amr-link refuses in its unsaid words anyway (here over the
// history's tool calls, which Anthropic's API refuses with no tools
// declared) is asked again with the conversation as text, as a relay's
// "Upstream request failed" is, and Codex gets the summary, not the 400.
func TestOpenDesignCompactionRefusedAsksAsText(t *testing.T) {
	calls := func(b []byte) bool { return bytes.Contains(b, []byte(`"function_call"`)) }
	rec, got := openDesignCompaction(t, calls)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"compaction"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if n := len(got); n < 2 || calls(got[n-1]) || !bytes.Contains(got[n-1], []byte("This is the conversation so far")) {
		t.Errorf("asked %d times; last %.600s", n, got[len(got)-1])
	}
	if strings.Contains(rec.Body.String(), "magpie could not") {
		t.Errorf("compacted locally: %s", rec.Body)
	}
}
