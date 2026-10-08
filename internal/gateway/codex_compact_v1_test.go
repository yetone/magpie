package gateway

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #1301: Codex compacts remotely (compaction v2: a compaction_trigger on
// /responses, exactly one compaction item back) for any provider table
// named "OpenAI", as CC Switch names its relay tables. magpie points those
// tables at /v1 while a magpie model is on, so the trigger reached /v1, went
// on to the relay as it came, and the relay's message came back: "remote
// compaction v2 expected exactly one compaction output item, got 0 from 1
// output items". On /v1 the summary is magpie's to make, as on Codex's
// backend path, and magpie's summary goes back to the model as text.
func TestCodexCompactionTriggerOnV1(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_r1","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_r1","status":"completed","output":[]}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","instructions":"You are Codex","stream":true,"store":false,
	  "include":["reasoning.encrypted_content"],"prompt_cache_key":"thread-1","tool_choice":"auto","parallel_tool_calls":true,
	  "tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},{"type":"compaction_trigger"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(f.got), "compaction_trigger") || !strings.Contains(string(f.got), "CONTEXT CHECKPOINT COMPACTION") {
		t.Errorf("relay got %s", f.got)
	}
	// what Codex's collect_compaction_output counts: output_item.done items
	var done []map[string]any
	for _, e := range events(body) {
		if e["type"] == "response.output_item.done" {
			done = append(done, e["item"].(map[string]any))
		}
	}
	if len(done) != 1 || done[0]["type"] != "compaction" {
		t.Fatalf("Codex wants exactly one compaction output item, got %v\n%s", done, body)
	}
	enc, _ := done[0]["encrypted_content"].(string)
	if b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, magpieCompaction)); string(b) != "SUMMARY" {
		t.Errorf("compaction holds %q", enc)
	}

	// the next turn carries the compaction item; the relay gets its text
	f.reply = sse(`data: {"type":"response.completed","response":{"id":"resp_r2","status":"completed","output":[]}}`)
	code, body = post(t, "/v1/responses", `{"model":"fake/m1","stream":true,
	  "input":[{"type":"compaction","encrypted_content":"`+enc+`"},{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(string(f.got), magpieCompaction) || !strings.Contains(string(f.got), "SUMMARY") {
		t.Errorf("relay got %s", f.got)
	}
}
