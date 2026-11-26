package redact

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// withStandIn makes the stand-in the issue's test made (#7837): a made-up
// key beginning with an ordinary letter, so a text ending in d may be the
// start of one, and the Writer holds that d for the next event.
func withStandIn(t *testing.T) {
	t.Helper()
	mu.Lock()
	prevValues, prevShapes := values, shapes
	values = map[string]string{"dummy_standin": "real_secret"}
	shapes = map[string]string{"dummy_standin": "SECRET"}
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		values, shapes = prevValues, prevShapes
		mu.Unlock()
	})
}

func throughWriter(evs ...string) string {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)
	for _, e := range evs {
		w.Write([]byte(e))
	}
	w.Finish()
	return rec.Body.String()
}

// Gemini's :streamGenerateContent?alt=sse sends each of a turn's parallel
// functionCalls whole, in an event of its own, at the same path
// (candidates.0.content.parts.0.functionCall.args.Path). The d at the end
// of one call's path was held for a placeholder's start and moved to the
// start of the next call's: /tmp/foo1.m, d/tmp/foo2.m (DeliciousBuding,
// #7837; the issue's bytes as Gemini sent them). Each call goes out as it
// came, and a placeholder in each is put back in that call.
func TestWriterGeminiParallelCallsKeepTheirArgs(t *testing.T) {
	withStandIn(t)
	healthy := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"Path\":\"/tmp/foo1.md\"}}}]},\"finishReason\":null,\"index\":0}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"Path\":\"/tmp/foo2.md\"}}}]},\"finishReason\":null,\"index\":0}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"Path\":\"/tmp/foo3.py\"}}}]},\"finishReason\":null,\"index\":0}]}\n\n"
	evs := strings.SplitAfter(healthy, "\n\n")
	if got := throughWriter(evs...); got != healthy {
		t.Fatalf("parallel functionCalls changed:\n got %s\nwant %s", got, healthy)
	}
	// Code Assist's wrapper, snake_case, nested args, a functionResponse
	wrapped := []string{
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"function_call":{"name":"edit","args":{"meta":{"file":"/tmp/ca1.md"},"paths":["/tmp/a.md","/tmp/b.md"]}}}]},"index":0}]}}` + "\n\n",
		`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"function_call":{"name":"edit","args":{"meta":{"file":"/tmp/ca2.md"},"paths":["/tmp/c.md","/tmp/d.md"]}}}]},"index":0}]}}` + "\n\n",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionResponse":{"name":"edit","response":{"out":"wrote /tmp/x.md"}}}]},"index":0}]}` + "\n\n",
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionResponse":{"name":"edit","response":{"out":"wrote /tmp/y.md"}}}]},"index":0}]}` + "\n\n",
	}
	if got, want := throughWriter(wrapped...), strings.Join(wrapped, ""); got != want {
		t.Fatalf("Code Assist calls changed:\n got %s\nwant %s", got, want)
	}
	// a placeholder in each call is put back in that call
	const v1, v2 = "Hunter2Alpha-77", "Hunter2Bravo-88"
	p1, p2 := placeholder("SECRET", v1), placeholder("SECRET", v2)
	call := func(path string) string {
		return `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"Path":"` + path + `"}}}]},"index":0}]}` + "\n\n"
	}
	got := throughWriter(call("/tmp/"+p1+"/a.md"), call("/tmp/"+p2+"/b.md"))
	if want := call("/tmp/"+v1+"/a.md") + call("/tmp/"+v2+"/b.md"); got != want {
		t.Fatalf("placeholders in calls:\n got %s\nwant %s", got, want)
	}
}

// A stream's protocol, as a test reads it back: each string of an event
// under the text it is a piece of, as the protocol says which that is.
type protocol struct {
	name  string
	event func(text, piece string) string
	// texts puts each piece of an event's JSON under its text
	texts func(data map[string]any, n int, into map[string]*strings.Builder)
}

func add(into map[string]*strings.Builder, k, s string) {
	if into[k] == nil {
		into[k] = &strings.Builder{}
	}
	into[k].WriteString(s)
}

func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

var protocols = []protocol{
	{
		// Chat Completions: one call's pieces by its index; a call's
		// first piece sits at tool_calls.0 as the call before it did
		name: "chat tool_calls",
		event: func(text, piece string) string {
			// in the order OpenAI writes a chunk's keys: the choice's
			// index before its delta
			a, _ := json.Marshal(piece)
			return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":` + text +
				`,"id":"call_` + text + `","type":"function","function":{"name":"read_file","arguments":` + string(a) + `}}]}}]}` + "\n\n"
		},
		texts: func(d map[string]any, _ int, into map[string]*strings.Builder) {
			c := get(d, "choices", 0, "delta", "tool_calls", 0)
			add(into, fmt.Sprint(get(c, "index")), str(get(c, "function", "arguments")))
		},
	},
	{
		// a vendor's Chat Completions that names a call by its id alone
		name: "chat tool_calls by id",
		event: func(text, piece string) string {
			a, _ := json.Marshal(piece)
			return `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_` + text +
				`","type":"function","function":{"name":"read_file","arguments":` + string(a) + `}}]}}]}` + "\n\n"
		},
		texts: func(d map[string]any, _ int, into map[string]*strings.Builder) {
			c := get(d, "choices", 0, "delta", "tool_calls", 0)
			add(into, str(get(c, "id")), str(get(c, "function", "arguments")))
		},
	},
	{
		name: "responses function_call_arguments.delta",
		event: func(text, piece string) string {
			b, _ := json.Marshal(map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_" + text, "output_index": json.Number(text), "delta": piece})
			return "event: response.function_call_arguments.delta\ndata: " + string(b) + "\n\n"
		},
		texts: func(d map[string]any, _ int, into map[string]*strings.Builder) {
			add(into, str(d["item_id"]), str(d["delta"]))
		},
	},
	{
		name: "anthropic input_json_delta",
		event: func(text, piece string) string {
			b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": json.Number(text), "delta": map[string]any{"type": "input_json_delta", "partial_json": piece}})
			return "event: content_block_delta\ndata: " + string(b) + "\n\n"
		},
		texts: func(d map[string]any, _ int, into map[string]*strings.Builder) {
			add(into, fmt.Sprint(d["index"]), str(get(d, "delta", "partial_json")))
		},
	},
	{
		// Gemini: the thoughts, then the answer, both at parts.0.text
		name: "gemini thought and text",
		event: func(text, piece string) string {
			part := map[string]any{"text": piece}
			if text == "0" {
				part["thought"] = true
			}
			b, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "index": 0}}})
			return "data: " + string(b) + "\n\n"
		},
		texts: func(d map[string]any, _ int, into map[string]*strings.Builder) {
			p := get(d, "candidates", 0, "content", "parts", 0)
			k := "text"
			if get(p, "thought") == true {
				k = "thought"
			}
			add(into, k, str(get(p, "text")))
		},
	},
	{
		// Gemini's streamed arguments (streamFunctionCallArguments): a
		// value's pieces by its jsonPath while willContinue; each call
		// here has one, of the same name, and its last piece ends it
		name: "gemini partialArgs",
		event: func(text, piece string) string {
			arg := map[string]any{"jsonPath": "$.Path", "stringValue": piece}
			if strings.HasSuffix(text, "+") {
				arg["willContinue"] = true
			}
			b, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{
				map[string]any{"functionCall": map[string]any{"name": "read_file", "partialArgs": []any{arg}, "willContinue": true}}}}, "index": 0}}})
			return "data: " + string(b) + "\n\n"
		},
		texts: func(d map[string]any, n int, into map[string]*strings.Builder) {
			a := get(d, "candidates", 0, "content", "parts", 0, "functionCall", "partialArgs", 0)
			add(into, fmt.Sprint(get(a, "jsonPath"), "#", into["#calls"].Len()), str(get(a, "stringValue")))
			if get(a, "willContinue") != true {
				add(into, "#calls", "x") // the next piece is a call of its own
			}
		},
	},
}

// readBack puts each piece of a stream under its text, and counts the
// events.
func readBack(t *testing.T, p protocol, stream string) (map[string]string, int) {
	t.Helper()
	into := map[string]*strings.Builder{"#calls": {}}
	n := 0
	for _, ev := range strings.Split(stream, "\n\n") {
		_, d, ok := strings.Cut(ev, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("%s: event %q: %v", p.name, d, err)
		}
		p.texts(m, n, into)
		n++
	}
	out := map[string]string{}
	for k, b := range into {
		if k != "#calls" {
			out[k] = b.String()
		}
	}
	return out, n
}

// Each text a stream holds a tail of goes on with that tail only: never
// with another tool call's, another block's, or the thoughts' before the
// answer (#7837). In every protocol the Writer reads, a text ending in a
// stand-in's first letter is followed by another text's first piece, and
// each text the agent puts together is the one the vendor sent.
func TestWriterTailStaysWithItsText(t *testing.T) {
	withStandIn(t)
	for _, p := range protocols {
		var evs []string
		texts := [][2]string{{"0", `{"Path":"/tmp/foo1.md`}, {"1", `{"Path":"/tmp/foo2.md`}, {"1", `"}`}, {"0", `"}`}}
		if p.name == "gemini thought and text" {
			texts = [][2]string{{"0", "I should read"}, {"1", "Here it is, read"}, {"1", " and done"}}
		}
		if p.name == "gemini partialArgs" {
			texts = [][2]string{{"0", "/tmp/foo1.md"}, {"1+", "/tmp/foo2.md"}, {"1", ""}, {"2", "/tmp/foo3.md"}}
		}
		for _, x := range texts {
			evs = append(evs, p.event(x[0], x[1]))
		}
		in := strings.Join(evs, "")
		out := throughWriter(evs...)
		want, _ := readBack(t, p, in)
		got, n := readBack(t, p, out)
		if n != len(evs) || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: texts changed\n got %q\nwant %q\n%s", p.name, got, want, out)
		}
	}
}

// A placeholder split between two pieces of one text is still put back
// whole, with another text's piece, ending in what may be a placeholder's
// start, just before it.
func TestWriterSplitPlaceholderBesideAnotherText(t *testing.T) {
	withStandIn(t)
	const v = "Hunter2Charlie-99"
	ph := placeholder("SECRET", v)
	for _, p := range protocols {
		other, mine, rest := "0", "1", "1"
		text := `{"Path":"/tmp/` + ph + `/notes.md"}`
		before := `{"Path":"/tmp/foo1.md`
		switch p.name {
		case "gemini thought and text":
			text, before = "the key is "+ph+", read", "I should read"
		case "gemini partialArgs":
			mine, text, before = "1+", "/tmp/"+ph+"/notes.md", "/tmp/foo1.md"
		}
		at := strings.Index(text, ph)
		for cut := at + 1; cut < at+len(ph); cut++ {
			evs := []string{p.event(other, before), p.event(mine, text[:cut]), p.event(rest, text[cut:])}
			got, n := readBack(t, p, throughWriter(evs...))
			want, _ := readBack(t, p, strings.Join(evs, ""))
			for k := range want {
				want[k] = strings.ReplaceAll(want[k], ph, v)
			}
			if n != len(evs) || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s cut at %d:\n got %q\nwant %q", p.name, cut, got, want)
			}
		}
	}
}
