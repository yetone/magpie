package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The rewrites a Codex request goes through on its way (#1532, #2354):
// a turn with nothing for one to do isn't decoded by it, and what each
// writes is what it wrote when it decoded every turn.

// codexSession is a Codex conversation of n exchanges, as Codex sends one to
// a provider named "OpenAI": its messages' metadata, sealed reasoning,
// calls of each kind with OpenAI's ids, and items marked by f.
func codexSession(n int, f func(i int, it map[string]any)) []byte {
	para := strings.Repeat("reads the file <a> & splits it \"reasoning\" on lines é 漢字\n", 8)
	meta := map[string]any{"turn_id": "t1"}
	var items []any
	add := func(it map[string]any) {
		f(len(items), it)
		items = append(items, it)
	}
	for i := range n {
		add(map[string]any{"type": "message", "role": "user", "id": fmt.Sprintf("msg_u%d", i), "content": []any{map[string]any{"type": "input_text", "text": para}}, "internal_chat_message_metadata_passthrough": meta})
		add(map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_%d", i), "summary": []any{map[string]any{"type": "summary_text", "text": para}}, "content": []any{}, "encrypted_content": "gAAAA" + strings.Repeat("x", 200)})
		add(map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": "shell", "arguments": `{"cmd":"ls"}`})
		add(map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("call_%d", i), "output": para})
		add(map[string]any{"type": "custom_tool_call", "id": fmt.Sprintf("ctc_%d", i), "call_id": fmt.Sprintf("cc_%d", i), "name": "apply_patch", "input": para})
		add(map[string]any{"type": "tool_search_call", "id": fmt.Sprintf("tsc_%d", i), "call_id": fmt.Sprintf("ts_%d", i), "execution": "client", "arguments": map[string]any{"q": "x"}})
		add(map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("msg_a%d", i), "content": []any{map[string]any{"type": "output_text", "text": para}}})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "instructions": para, "input": items, "store": false, "stream": true,
		"include": []string{"reasoning.encrypted_content"}, "tools": []any{map[string]any{"type": "function", "name": "shell"}}})
	return b
}

type codexRewrite struct {
	name     string
	now, was func([]byte) []byte
}

// codexRewrites are the rewrites, each beside what it was before.
var codexRewrites = func() []codexRewrite {
	relay := provider.Provider{Responses: "https://relay.example/v1"}
	rs := []codexRewrite{
		{"callItemIDs", callItemIDs, wasCallItemIDs},
		{"plainItemIDs", plainItemIDs, wasPlainItemIDs},
		{"withoutBareReasoning", withoutBareReasoning, wasWithoutBareReasoning},
		{"withoutReasoningText", withoutReasoningText, wasWithoutReasoningText},
		{"withoutOffsetlessCitations", withoutOffsetlessCitations, wasWithoutOffsetlessCitations},
		{"withModel", func(b []byte) []byte { return withModel(b, "gpt-5.5<&>") }, func(b []byte) []byte {
			if string(trimJSON(b)) == "null" {
				return b // was a panic
			}
			return wasWithModel(b, "gpt-5.5<&>")
		}},
		{"forVendor", func(b []byte) []byte { return forVendor(relay, b, false) }, func(b []byte) []byte { return wasForVendor(relay, b) }},
	}
	for _, magpie := range []bool{false, true} {
		for _, native := range []bool{false, true} {
			rs = append(rs, codexRewrite{fmt.Sprintf("codexInputAs(%v,%v)", magpie, native),
				func(b []byte) []byte { b, c := codexInputAs(b, magpie, native); return append(b, fmt.Sprint(c)...) },
				func(b []byte) []byte { b, c := wasCodexInputAs(b, magpie, native); return append(b, fmt.Sprint(c)...) }})
		}
	}
	return rs
}()

// A turn none of the rewrites has anything to do in goes through each
// without its conversation decoded: they decoded it whole, every one,
// every turn, and that was most of a long session's CPU (#1532).
func TestCodexRewritesLeaveTurnUndecoded(t *testing.T) {
	body := codexSession(60, func(int, map[string]any) {})
	for _, r := range codexRewrites {
		if r.name == "withModel" || r.name == "forVendor" {
			continue // these have something to do in this turn
		}
		in := slices.Clone(body)
		if got := r.now(in); !bytes.Equal(bytes.TrimSuffix(got, []byte("false")), body) {
			t.Fatalf("%s changed a turn with nothing to change", r.name)
		}
		in = slices.Clone(body)
		allocs := testing.AllocsPerRun(5, func() { r.now(in) })
		if allocs > 10 {
			t.Errorf("%s: %v allocations for a turn it leaves as it came (%d KB), want the conversation not decoded", r.name, allocs, len(body)>>10)
		}
	}
	// a body that only names Codex's metadata goes past forVendor
	// undecoded too
	plain := codexSession(60, func(_ int, it map[string]any) { delete(it, "internal_chat_message_metadata_passthrough") })
	plain = append(plain[:len(plain)-1], []byte(`,"note":"internal_chat_message_metadata_passthrough"}`)...)
	relay := provider.Provider{Responses: "https://relay.example/v1"}
	if got := forVendor(relay, plain, false); !bytes.Equal(got, plain) {
		t.Fatal("forVendor changed a body with nothing to leave out")
	}
	if a := testing.AllocsPerRun(5, func() { forVendor(relay, plain, false) }); a > 10 {
		t.Errorf("forVendor: %v allocations for a body it leaves as it came", a)
	}
}

// Writing a long body back, split at its items, takes an allocation or
// two: encoding/json grew its buffer a step at a time, after scanning
// each value a byte a call (#1532).
func TestMarshalRawAllocatesOnce(t *testing.T) {
	q, err := rawObject(codexSession(60, func(int, map[string]any) {}))
	if err != nil {
		t.Fatal(err)
	}
	items, _ := rawArray(q["input"])
	if a := testing.AllocsPerRun(5, func() { marshalPlain(items) }); a > 2 {
		t.Errorf("marshalPlain of %d items: %v allocations", len(items), a)
	}
	if a := testing.AllocsPerRun(5, func() { marshalJSON(q) }); a > 3 {
		t.Errorf("marshalJSON of a body's members: %v allocations", a)
	}
}

// codexMarks are the items a rewrite has something to do with, and the
// shapes it has to read as encoding/json read them.
var codexMarks = []func(it map[string]any){
	func(it map[string]any) { it["id"] = "fc_x" },
	// ids of the lengths Copilot hands out, of the characters OpenAI
	// takes (#4525, #4527): only their length turns an upstream that
	// checks ids away, and one of them on a reasoning item is that
	// vendor's own to read back
	func(it map[string]any) { it["id"] = "msg_" + strings.Repeat("a", 440) },
	func(it map[string]any) { it["id"] = strings.Repeat("A", 442) + "==" },
	func(it map[string]any) { it["type"] = "reasoning"; it["id"] = "rs_" + strings.Repeat("b", 417) },
	func(it map[string]any) { it["id"] = "rs_a:rs_b" },
	func(it map[string]any) { it["id"] = "" },
	func(it map[string]any) { it["id"] = 7 },
	func(it map[string]any) { delete(it, "id") },
	func(it map[string]any) { it["type"] = "reasoning" },
	func(it map[string]any) { it["type"] = "reasoning"; it["encrypted_content"] = "" },
	func(it map[string]any) { it["type"] = "reasoning"; it["encrypted_content"] = nil },
	func(it map[string]any) {
		it["type"] = "reasoning"
		it["content"] = []any{map[string]any{"type": "reasoning_text", "text": "t"}}
	},
	// a reply another vendor's Responses endpoint wrote, its citation
	// without the offsets OpenAI's schema wants (#4921), beside one with
	// them
	func(it map[string]any) {
		it["type"] = "message"
		it["role"] = "assistant"
		it["content"] = []any{map[string]any{"type": "output_text", "text": "t", "annotations": []any{
			map[string]any{"type": "url_citation", "url": "https://a", "title": "A"},
			map[string]any{"type": "url_citation", "start_index": 0, "end_index": 1, "url": "https://b"},
		}}}
	},
	func(it map[string]any) {
		it["type"] = "message"
		it["content"] = "text"
	},
	func(it map[string]any) {
		it["type"] = "message"
		it["content"] = []any{map[string]any{"type": "output_text", "text": "t", "annotations": []any{
			map[string]any{"type": "url_citation", "start_index": nil, "end_index": 2},
		}}}
	},
	func(it map[string]any) {
		it["type"] = "message"
		it["content"] = []any{map[string]any{"type": "input_text", "text": "t", "annotations": []any{
			map[string]any{"type": "url_citation", "url": "https://a"},
		}}}
	},
	func(it map[string]any) {
		it["content"] = []any{map[string]any{"type": "output_text", "text": "t", "annotations": []any{
			map[string]any{"type": "url_citation", "url": "https://a"},
		}}}
	},
	func(it map[string]any) { it["type"] = "reasoning"; it["content"] = nil },
	func(it map[string]any) { it["type"] = "reasoning"; it["content"] = "text" },
	func(it map[string]any) { it["type"] = "reasoning"; it["content"] = map[string]any{} },
	func(it map[string]any) {
		it["type"] = "compaction"
		it["encrypted_content"] = magpieCompaction + base64.StdEncoding.EncodeToString([]byte("sum"))
	},
	func(it map[string]any) {
		it["type"] = "compaction_summary"
		it["encrypted_content"] = magpieCompaction + "!!"
	},
	func(it map[string]any) { it["type"] = "compaction"; it["encrypted_content"] = "gAAAA" },
	func(it map[string]any) { it["type"] = "compaction_trigger" },
	func(it map[string]any) { it["type"] = "configuration_update" },
	func(it map[string]any) { it["type"] = 3 },
	func(it map[string]any) { it["type"] = "tool_search_call"; it["id"] = "fc_9" },
	func(it map[string]any) { it["type"] = "custom_tool_call"; it["id"] = "fc_9" },
	func(it map[string]any) { it["type"] = "custom_tool_call"; it["id"] = "ctc" },
	func(it map[string]any) { it["encrypted_function_args"] = "sealed" },
	func(it map[string]any) { it["encrypted_content"] = "gAAAA-output" },
	func(it map[string]any) { it["encrypted_content"] = nil },
	func(it map[string]any) { it["type"] = "context_compaction"; it["encrypted_content"] = "gAAAA" },
	func(it map[string]any) { it["internal_chat_message_metadata_passthrough"] = nil },
}

// codexRawMarks are what a map can't say: duplicate and escaped keys,
// items that aren't objects, an input that isn't a list.
var codexRawMarks = [][2]string{
	{`{"content"`, `{"type":"reasoning","content"`},
	{`{"content"`, `{"type":"reasoning","encrypted_content":"","content"`},
	{`"id":"msg_u1"`, `"id":"msg_u1","id":"a:b"`},
	{`"id":"msg_u1"`, `"id":"a:b"`},
	{`"type":"reasoning"`, `"type":"reasoning"`},
	{`"type":"reasoning"`, `"Type":"reasoning"`},
	{`"type":"function_call_output"`, `"type":"configuration_update"`},
	{`"internal_chat_message_metadata_passthrough"`, `"internal_chat_message_metadata_passthrough"`},
	{`"input":[`, `"input":[null,`},
	{`"include":["reasoning.encrypted_content"]`, `"include":["reasoning.encrypted_content","output.encrypted_content"]`},
	{`"include":["reasoning.encrypted_content"]`, `"include":["output.encrypted_content"]`},
	{`"include":["reasoning.encrypted_content"]`, `"include":"output.encrypted_content"`},
	{`"input":[`, `"input":[1,`},
	{`"input":[`, `"input":["x",[],`},
	{`"input":[`, `"input":null,"x":[`},
	{`"input":[`, `"input":{"a":1},"x":[`},
	{`"input":[`, `"input" : [ `},
	{`"store":false`, `"store":true`},
	{`"store":false`, `"store":false,"stream_options":{"reasoning_summary_delivery":"x"}`},
	{`"store":false`, `"store":false,"stream_options":{"reasoning_summary_delivery":"x","other":1}`},
	{`"store":false`, `"store":false,"stream_options":null`},
	{`"store":false`, `"store":false,"stream_options":7`},
	{`"store":false`, `"store":false,"stream_options":{"other":1}`},
	{`"stream":true`, `"stream":true,"model":"<b>"`},
}

func checkCodexRewrites(t *testing.T, body []byte) {
	t.Helper()
	for _, r := range codexRewrites {
		got, want := r.now(slices.Clone(body)), r.was(slices.Clone(body))
		if !bytes.Equal(got, want) {
			t.Fatalf("%s of %.300q\n now %.300q\n was %.300q", r.name, body, got, want)
		}
	}
}

func TestCodexRewritesAsBefore(t *testing.T) {
	checkCodexRewrites(t, codexSession(3, func(int, map[string]any) {}))
	for m := range codexMarks {
		for _, at := range []int{0, 1, 5, 20} {
			checkCodexRewrites(t, codexSession(3, func(i int, it map[string]any) {
				if i == at {
					codexMarks[m](it)
				}
			}))
		}
	}
	for _, rm := range codexRawMarks {
		checkCodexRewrites(t, bytes.Replace(codexSession(2, func(int, map[string]any) {}), []byte(rm[0]), []byte(rm[1]), 1))
	}
	// marks together, as a session gathers them
	r := rand.New(rand.NewPCG(5, 6))
	for range 300 {
		body := codexSession(2, func(i int, it map[string]any) {
			if r.IntN(4) == 0 {
				codexMarks[r.IntN(len(codexMarks))](it)
			}
		})
		for range r.IntN(3) {
			rm := codexRawMarks[r.IntN(len(codexRawMarks))]
			body = bytes.Replace(body, []byte(rm[0]), []byte(rm[1]), 1)
		}
		checkCodexRewrites(t, body)
	}
	// a body long enough to be remembered (longBody), with each mark
	for m := range codexMarks {
		checkCodexRewrites(t, codexSession(20, func(i int, it map[string]any) {
			if i == 77 {
				codexMarks[m](it)
			}
		}))
	}
	for _, s := range []string{``, `null`, `[]`, `{}`, `{"input":[]}`, `{"input":null}`, `{`, `{"input":[{"type":"reasoning"}]} x`, `"reasoning"`} {
		checkCodexRewrites(t, []byte(s))
	}
}

func FuzzCodexRewritesAsBefore(f *testing.F) {
	f.Add(codexSession(1, func(i int, it map[string]any) {
		if i == 1 {
			it["content"] = []any{"t"}
		}
	}))
	for _, rm := range codexRawMarks {
		f.Add(bytes.Replace(codexSession(1, func(int, map[string]any) {}), []byte(rm[0]), []byte(rm[1]), 1))
	}
	f.Fuzz(func(t *testing.T, body []byte) { checkCodexRewrites(t, body) })
}

// What the rewrites were before they looked before decoding (#1532),
// kept to hold them to it.

// wasMarshalPlain is marshalPlain as encoding/json writes it.
func wasMarshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func wasWithModel(body []byte, model string) []byte {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	q["model"], _ = json.Marshal(model)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

func wasCodexInputAs(body []byte, magpieModel, native bool) (_ []byte, compact bool) {
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body, false
	}
	var items []map[string]any
	if json.Unmarshal(q["input"], &items) != nil {
		return body, false
	}
	changed := false
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		switch it["type"] {
		case "compaction", "compaction_summary":
			enc, _ := it["encrypted_content"].(string)
			sum, ok := strings.CutPrefix(enc, magpieCompaction)
			if !ok {
				out = append(out, it) // OpenAI's own, for OpenAI
				continue
			}
			changed = true
			if b, err := base64.StdEncoding.DecodeString(sum); err == nil {
				out = append(out, userMessage(codexSummaryPrefix+"\n"+string(b)))
			}
		case "compaction_trigger":
			if !magpieModel || native {
				out = append(out, it)
				continue
			}
			changed, compact = true, true
			out = append(out, userMessage(codexCompactPrompt))
		case "reasoning":
			// OpenAI accepts no reasoning content parts in replayed input.
			// Drop the item so its encrypted_content cannot fail there too.
			if !magpieModel {
				if content, present := it["content"]; present && content != nil {
					parts, ok := content.([]any)
					if !ok || len(parts) > 0 {
						changed = true
						continue
					}
				}
				// Nor reasoning with nothing sealed in it — another vendor's,
				// only an id (rs_…): OpenAI, which keeps nothing (store is
				// false), looks the id up and answers 404 "Item … not found".
				if enc, _ := it["encrypted_content"].(string); enc == "" {
					changed = true
					continue
				}
			}
			out = append(out, it)
		default:
			out = append(out, it)
		}
	}
	if !changed {
		return body, false
	}
	b, _ := json.Marshal(out)
	q["input"] = b
	if compact {
		// the summary is text; a tool call would be no summary, so it
		// offers none. Otherwise it goes as Codex asks for its own summary
		// from a provider it reaches itself: "tools": [] (not left out,
		// #1511), streamed with its tool_choice — a relay in front of the
		// ChatGPT backend turns away one that isn't ("invalid codex
		// request", #292)
		q["tools"] = json.RawMessage("[]")
		q["tool_choice"] = json.RawMessage(`"auto"`)
		q["parallel_tool_calls"] = json.RawMessage("false")
		if cm, ok := localCompactionMeta(q["client_metadata"]); ok {
			q["client_metadata"] = cm
		}
	}
	nb, err := json.Marshal(q)
	if err != nil {
		return body, false
	}
	return nb, compact
}

func wasWithoutBareReasoning(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var store bool
	if json.Unmarshal(q["store"], &store) == nil && store {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type string `json:"type"`
			Enc  string `json:"encrypted_content"`
		}
		if json.Unmarshal(it, &t) == nil && t.Type == "reasoning" && t.Enc == "" {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return body
	}
	q["input"], _ = json.Marshal(kept)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

func wasWithoutReasoningText(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"reasoning"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	kept := items[:0:0]
	for _, it := range items {
		var t struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(it, &t) == nil && t.Type == "reasoning" && len(t.Content) > 0 {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return body
	}
	q["input"], _ = json.Marshal(kept)
	b, err := json.Marshal(q)
	if err != nil {
		return body
	}
	return b
}

func wasWithoutOffsetlessCitations(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"annotations"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		var it map[string]json.RawMessage
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(it["content"], &parts) != nil {
			continue
		}
		itemChanged := false
		for j, p := range parts {
			var part map[string]json.RawMessage
			if json.Unmarshal(p, &part) != nil || string(part["type"]) != `"output_text"` {
				continue
			}
			var notes []json.RawMessage
			if json.Unmarshal(part["annotations"], &notes) != nil {
				continue
			}
			kept := notes[:0:0]
			dropped := false
			for _, a := range notes {
				if annotationLacksOffsets(a) {
					dropped = true
					continue
				}
				kept = append(kept, a)
			}
			if dropped {
				part["annotations"], _ = wasMarshalPlain(kept)
				parts[j], _ = wasMarshalPlain(part)
				itemChanged = true
			}
		}
		if itemChanged {
			it["content"], _ = wasMarshalPlain(parts)
			items[i], _ = wasMarshalPlain(it)
			changed = true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = wasMarshalPlain(items)
	nb, err := wasMarshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

func wasCallItemIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_search_call"`)) && !bytes.Contains(body, []byte(`"custom_tool_call"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	for i, raw := range items {
		var it map[string]json.RawMessage
		var typ, id string
		if json.Unmarshal(raw, &it) != nil || json.Unmarshal(it["type"], &typ) != nil {
			continue
		}
		prefix := openaiItemPrefix[typ]
		if prefix == "" || json.Unmarshal(it["id"], &id) != nil || id == "" || strings.HasPrefix(id, prefix) {
			continue
		}
		if _, rest, ok := strings.Cut(id, "_"); ok && rest != "" {
			id = prefix + rest
		} else {
			id = prefix + id
		}
		it["id"], _ = json.Marshal(id)
		if b, err := wasMarshalPlain(it); err == nil {
			items[i], changed = b, true
		}
	}
	if !changed {
		return body
	}
	q["input"], _ = wasMarshalPlain(items)
	nb, err := wasMarshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

func wasPlainItemIDs(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"id"`)) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) != nil {
		return body
	}
	changed := false
	kept := items[:0:0]
	for _, raw := range items {
		var it map[string]json.RawMessage
		var typ, id string
		if json.Unmarshal(raw, &it) != nil || json.Unmarshal(it["id"], &id) != nil || id == "" ||
			len(id) <= itemIDCap && safeIDChars.MatchString(id) {
			kept = append(kept, raw)
			continue
		}
		changed = true
		if json.Unmarshal(it["type"], &typ) == nil && typ == "reasoning" && !safeIDChars.MatchString(id) {
			continue
		}
		switch {
		case len(id) > itemIDCap && idOptional[typ]:
			delete(it, "id")
		case len(id) > itemIDCap:
			it["id"], _ = json.Marshal(boundItemID(id, typ))
		default:
			it["id"], _ = json.Marshal(unsafeToolID.ReplaceAllString(id, "_"))
		}
		b, err := wasMarshalPlain(it)
		if err != nil {
			return body
		}
		kept = append(kept, b)
	}
	if !changed {
		return body
	}
	q["input"], _ = wasMarshalPlain(kept)
	nb, err := wasMarshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}

func wasForVendor(p provider.Provider, body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" || strings.HasSuffix(p.Host(), "openai.com") {
		return body
	}
	if !slices.ContainsFunc(openaiOnly, func(k []byte) bool { return bytes.Contains(body, k) }) {
		return body
	}
	var q map[string]json.RawMessage
	if json.Unmarshal(body, &q) != nil {
		return body
	}
	changed := false
	// Codex's stream_options holds reasoning_summary_delivery alone; another
	// client's other options stay
	var so map[string]json.RawMessage
	if json.Unmarshal(q["stream_options"], &so) == nil {
		if _, ok := so["reasoning_summary_delivery"]; ok {
			changed = true
			delete(so, "reasoning_summary_delivery")
			if q["stream_options"], _ = wasMarshalPlain(so); len(so) == 0 {
				delete(q, "stream_options")
			}
		}
	}
	var include []string
	if json.Unmarshal(q["include"], &include) == nil && slices.Contains(include, "output.encrypted_content") {
		changed = true
		if include = slices.DeleteFunc(include, func(v string) bool { return v == "output.encrypted_content" }); len(include) == 0 {
			delete(q, "include")
		} else {
			q["include"], _ = wasMarshalPlain(include)
		}
	}
	var items []json.RawMessage
	if json.Unmarshal(q["input"], &items) == nil {
		out := items[:0]
		for _, raw := range items {
			var it map[string]json.RawMessage
			if json.Unmarshal(raw, &it) != nil {
				out = append(out, raw)
				continue
			}
			if string(it["type"]) == `"configuration_update"` {
				changed = true
				continue
			}
			_, meta := it["internal_chat_message_metadata_passthrough"]
			_, sealed := it["encrypted_function_args"]
			_, replay := it["encrypted_content"]
			switch string(it["type"]) {
			case `"reasoning"`, `"compaction"`, `"compaction_summary"`, `"context_compaction"`:
				replay = false
			}
			if meta || sealed || replay {
				changed = true
				delete(it, "internal_chat_message_metadata_passthrough")
				delete(it, "encrypted_function_args")
				if replay {
					delete(it, "encrypted_content")
				}
				raw, _ = wasMarshalPlain(it)
			}
			out = append(out, raw)
		}
		if changed {
			q["input"], _ = wasMarshalPlain(out)
		}
	}
	if !changed {
		return body
	}
	nb, err := wasMarshalPlain(q)
	if err != nil {
		return body
	}
	return nb
}
