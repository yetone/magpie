package redact

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A placeholder split between two pieces of a tool call's arguments comes
// back whole when a keepalive event comes between the pieces: Anthropic's
// ping, which the API and magpie's own encoder send while the model is
// slow, and Responses' response.in_progress, magpie's keepalive on that
// protocol. The ping released the first piece as it was, and the agent
// wrote {{SECRET_…}} into a file where the model had written German
// (magpie-community/plugins#738, sensenova's prune_old_keys texts).
func TestWriterHeldAcrossKeepalive(t *testing.T) {
	const v = `Alte magpie-*-Keys dieses Kontos werden gelöscht; manuelle Keys und Keys anderer Konten bleiben „erhalten".`
	p := placeholder("SECRET", v)
	anthropic := func(s string) string {
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": s}})
		return "event: content_block_delta\ndata: " + string(b) + "\n\n"
	}
	responses := func(typ string) func(s string) string {
		return func(s string) string {
			b, _ := json.Marshal(map[string]any{"type": typ, "item_id": "fc_1", "output_index": 1, "delta": s})
			return "event: " + typ + "\ndata: " + string(b) + "\n\n"
		}
	}
	const (
		ping       = "event: ping\ndata: {\"type\": \"ping\"}\n\n"
		inProgress = `event: response.in_progress` + "\n" + `data: {"type":"response.in_progress","sequence_number":7,"response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.5","output":[]}}` + "\n\n"
	)
	// what the agent puts together from the events' pieces
	partial := func(d string) string {
		var x struct {
			Delta json.RawMessage
		}
		json.Unmarshal([]byte(d), &x)
		var s string
		if json.Unmarshal(x.Delta, &s) == nil {
			return s
		}
		var o struct {
			PartialJSON string `json:"partial_json"`
		}
		json.Unmarshal(x.Delta, &o)
		return o.PartialJSON
	}
	args := `{"file_path":"/x/index.mjs","new_string":"de: \"` + p + `\","}`
	patch := "*** Update File: index.mjs\n+            de: \"" + p + "\",\n"
	for _, c := range []struct {
		name, keep, text string
		ev               func(string) string
		json             bool
	}{
		{"anthropic ping", ping, args, anthropic, true},
		{"responses in_progress", inProgress, args, responses("response.function_call_arguments.delta"), true},
		{"responses custom tool in_progress", inProgress, patch, responses("response.custom_tool_call_input.delta"), false},
	} {
		at := strings.Index(c.text, p)
		for cut := at + 1; cut < at+len(p); cut++ {
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "text/event-stream")
			w := NewWriter(rec)
			w.WriteHeader(200)
			for _, e := range []string{c.ev(c.text[:cut]), c.keep, c.ev(c.text[cut:])} {
				w.Write([]byte(e))
			}
			w.Finish()
			var got strings.Builder
			n := 0
			for _, e := range strings.Split(rec.Body.String(), "\n\n") {
				_, d, ok := strings.Cut(e, "data: ")
				if !ok {
					continue
				}
				n++
				if !strings.Contains(d, `"delta"`) {
					continue
				}
				got.WriteString(partial(d))
			}
			if n != 3 {
				t.Fatalf("%s at %d: %d events, want 3:\n%s", c.name, cut, n, rec.Body.String())
			}
			if c.json {
				var in map[string]string
				if err := json.Unmarshal([]byte(got.String()), &in); err != nil || in["new_string"] != `de: "`+v+`",` {
					t.Fatalf("%s cut at %d: args %q: %v", c.name, cut, got.String(), err)
				}
			} else if want := strings.Replace(c.text, p, v, 1); got.String() != want {
				t.Fatalf("%s cut at %d:\n got %q\nwant %q", c.name, cut, got.String(), want)
			}
		}
	}
}
