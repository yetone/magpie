package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Hermes Agent's tool_call bridge (tools/tool_search.py), as Hermes sends
// it on chat completions: calls[].arguments is an object of any fields.
const hermesToolCall = `{"type":"object","properties":{"calls":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string","description":"Exact tool name to invoke."},"arguments":{"type":"object","description":"Arguments matching the tool schema."}},"required":["name","arguments"]},"description":"One local invocation, or one or more connector invocations. Never mix local and connector tools."}},"required":["calls"]}`

// #7363's call: the arguments the reporter's mcp_tool was to get, nested
// objects and all. Through a relay to gemini-4-hydrogen Hermes was handed
// {"boundary": "{strategy:", "evaluation": "{type:", …} and, on the next
// try, {"boundary": true, …, "workspace" bool}.
const hermesWant = `{"calls":[{"name":"mcp_tool","arguments":{"project":{"boundary":{"strategy":"rolling"},"data_source":{"type":"local"},"evaluation":{"type":"holdout"},"mode":"rolling_trajectory","schema_version":1,"workload_source":{"type":"trace"}},"replace":true,"workspace":"/abs/path/to/workspace"}}]}`

// hermesSaid is hermesWant as a Gemini model writes it when arguments is
// declared a string of JSON text.
func hermesSaid(t *testing.T) string {
	t.Helper()
	inner := gjson.Get(hermesWant, "calls.0.arguments").Raw
	q, _ := json.Marshal(inner)
	return `{"calls":[{"name":"mcp_tool","arguments":` + string(q) + `}]}`
}

func freeSameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var a, b any
	if json.Unmarshal([]byte(got), &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// A Gemini model is sent Hermes's calls[].arguments as a string of JSON
// text, which Google's Schema can say, on the chat route, the Anthropic
// route and from a Responses body; the rest of the schema goes as it
// came. A GPT model on the same provider is sent the schema as it came.
func TestGeminiFreeObjectsSentAsText(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Models: []string{"gemini-4-hydrogen", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		got, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"no","type":"invalid_request_error"}}`))}, nil
	})}
	routes := []struct{ path, tools string }{
		{"/v1/chat/completions", `"tools":[{"type":"function","function":{"name":"tool_call","parameters":%s}}]`},
		{"/v1/messages", `"tools":[{"name":"tool_call","input_schema":%s}]`},
		{"/v1/responses", `"tools":[{"type":"function","name":"tool_call","parameters":%s}]`},
	}
	for _, r := range routes {
		for _, model := range []string{"gemini-4-hydrogen", "gpt-5.5"} {
			got = nil
			msgs := `"messages":[{"role":"user","content":"hi"}],"max_tokens":16,`
			if r.path == "/v1/responses" {
				msgs = `"input":"hi",`
			}
			body := `{"model":"relay/` + model + `",` + msgs + fmt.Sprintf(r.tools, hermesToolCall) + `}`
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", r.path, strings.NewReader(body)))
			if got == nil || !json.Valid(got) {
				t.Fatalf("%s %s: upstream got %q (%d %s)", r.path, model, got, rec.Code, rec.Body)
			}
			sent := ""
			for _, at := range []string{"tools.0.function.parameters", "tools.0.input_schema", "tools.0.parameters"} {
				if v := gjson.GetBytes(got, at); v.Exists() {
					sent = v.Raw
					break
				}
			}
			args := gjson.Get(sent, "properties.calls.items.properties.arguments")
			if model == "gpt-5.5" {
				if sent != hermesToolCall {
					t.Errorf("%s: GPT was sent\n%s\nwant it as it came", r.path, sent)
				}
				continue
			}
			if args.Get("type").String() != "string" || !strings.HasPrefix(args.Get("description").String(), "Arguments matching the tool schema.") {
				t.Errorf("%s: Gemini was sent arguments as %s, want a string of JSON text", r.path, args.Raw)
			}
			if n := gjson.Get(sent, "properties.calls.items.properties.name").Raw; n != `{"type":"string","description":"Exact tool name to invoke."}` {
				t.Errorf("%s: name changed: %s", r.path, n)
			}
			if gjson.Get(sent, "properties.calls.items.required").Raw != `["name","arguments"]` {
				t.Errorf("%s: required changed: %s", r.path, sent)
			}
		}
	}
}

// What is and isn't an object of any fields; the root is never one.
func TestGeminiFreeObjectsWhich(t *testing.T) {
	params := `{"type":"object","properties":{` +
		`"free":{"type":"object"},"emptied":{"type":"object","properties":{}},"more":{"type":"object","additionalProperties":true},` +
		`"none":{"type":"object","additionalProperties":false},"shaped":{"type":"object","properties":{"k":{"type":"object"}}},` +
		`"pat":{"type":"object","patternProperties":{"^x":{"type":"string"}}},"union":{"type":"object","anyOf":[{"type":"object"}]},` +
		`"list":{"type":"array","items":{"type":"object"}},"untyped":{"description":"d"}}}`
	got := freeObjectSpots(gjson.Parse(params))
	var paths []string
	for _, sp := range got {
		paths = append(paths, strings.Join(sp.path, "."))
	}
	want := []string{"free", "emptied", "more", "shaped.k", "list.[]"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("free objects at %v, want %v", paths, want)
	}
	if spots := freeObjectSpots(gjson.Parse(`{"type":"object"}`)); len(spots) != 0 {
		t.Fatalf("a free root counted: %v", spots)
	}
	// only a string that is a JSON object is read back
	paths2 := [][]string{{"free"}, {"list", "[]"}}
	in := `{"free":"{\"a\":1}","list":["{\"b\":[2]}","plain",{"c":3},"{bad"],"other":"{\"x\":1}"}`
	out := freeArgs(in, paths2)
	if wantOut := `{"free":{"a":1},"list":[{"b":[2]},"plain",{"c":3},"{bad"],"other":"{\"x\":1}"}`; out != wantOut {
		t.Fatalf("freeArgs:\n%s\nwant\n%s", out, wantOut)
	}
}

// A Gemini model's call, its arguments written as JSON text as it was
// asked, reaches the client with them read back into #7363's nested
// objects: relayed on Chat (streamed, cut mid-escape, and whole),
// Responses and Messages, and translated from Messages to a Chat client.
// A GPT model's identical reply reaches its client as it came.
func TestGeminiFreeObjectsReadBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "cc", Name: "CC", Key: "k", Chat: "https://cc.test/v1", Models: []string{"gemini-4-hydrogen", "gpt-5.5"}},
		{ID: "rs", Name: "RS", Key: "k", Responses: "https://rs.test/v1", Models: []string{"gemini-4-hydrogen", "gpt-5.5"}},
		{ID: "an", Name: "AN", Key: "k", Anthropic: "https://an.test", Models: []string{"gemini-4-hydrogen", "gpt-5.5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	said := hermesSaid(t)
	q, _ := json.Marshal(said)
	// said cut in three, the middle cut inside an escape
	cut1, cut2 := len(said)/3, 2*len(said)/3
	for said[cut2-1] != '\\' {
		cut2++
	}
	pieces := []string{said[:cut1], said[cut1:cut2], said[cut2:]}
	piece := func(i int) string { b, _ := json.Marshal(pieces[i]); return string(b) }
	chatSSE := `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"tool_call","arguments":""}}]}}]}` + "\n\n"
	for i := range pieces {
		chatSSE += `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + piece(i) + `}}]}}]}` + "\n\n"
	}
	chatSSE += `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}` + "\n\ndata: [DONE]\n\n"
	chatWhole := `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"tool_call","arguments":` + string(q) + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`
	item := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"tool_call","arguments":` + string(q) + `,"status":"completed"}`
	respSSE := `event: response.created` + "\n" + `data: {"type":"response.created","response":{"id":"r","object":"response","status":"in_progress","model":"m","output":[]}}` + "\n\n" +
		`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"tool_call","arguments":"","status":"in_progress"}}` + "\n\n"
	for i := range pieces {
		respSSE += `event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":` + piece(i) + `}` + "\n\n"
	}
	respSSE += `event: response.function_call_arguments.done` + "\n" + `data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":` + string(q) + `}` + "\n\n" +
		`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":0,"item":` + item + `}` + "\n\n" +
		`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","model":"m","output":[` + item + `],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}` + "\n\n"
	anSSE := `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n" +
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"tool_call","input":{}}}` + "\n\n"
	for i := range pieces {
		anSSE += `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + piece(i) + `}}` + "\n\n"
	}
	anSSE += `event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}` + "\n\n" +
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n"
	anWhole := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"tool_call","input":` + said + `}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":5}}`

	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		stream := gjson.GetBytes(body, "stream").Bool()
		reply, ct := "", "application/json"
		switch {
		case r.URL.Host == "cc.test" && stream:
			reply, ct = chatSSE, "text/event-stream"
		case r.URL.Host == "cc.test":
			reply = chatWhole
		case r.URL.Host == "rs.test":
			reply, ct = respSSE, "text/event-stream"
		case r.URL.Host == "an.test" && stream:
			reply, ct = anSSE, "text/event-stream"
		default:
			reply = anWhole
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}

	chatTools := `"tools":[{"type":"function","function":{"name":"tool_call","parameters":` + hermesToolCall + `}}]`
	cases := []struct {
		name, path, model, body string
		args                    func(string) []string // the call's arguments, as the client was told them
	}{
		{"chat relayed stream", "/v1/chat/completions", "cc", `"messages":[{"role":"user","content":"go"}],"stream":true,` + chatTools, chatStreamArgs},
		{"chat relayed whole", "/v1/chat/completions", "cc", `"messages":[{"role":"user","content":"go"}],` + chatTools, func(b string) []string {
			return []string{gjson.Get(b, "choices.0.message.tool_calls.0.function.arguments").String()}
		}},
		{"responses relayed stream", "/v1/responses", "rs", `"input":"go","stream":true,"tools":[{"type":"function","name":"tool_call","parameters":` + hermesToolCall + `}]`, respStreamArgs},
		{"messages relayed stream", "/v1/messages", "an", `"messages":[{"role":"user","content":"go"}],"max_tokens":64,"stream":true,"tools":[{"name":"tool_call","input_schema":` + hermesToolCall + `}]`, anStreamArgs},
		{"messages relayed whole", "/v1/messages", "an", `"messages":[{"role":"user","content":"go"}],"max_tokens":64,"tools":[{"name":"tool_call","input_schema":` + hermesToolCall + `}]`, func(b string) []string {
			return []string{gjson.Get(b, "content.0.input").Raw}
		}},
		{"chat translated stream", "/v1/chat/completions", "an", `"messages":[{"role":"user","content":"go"}],"stream":true,` + chatTools, chatStreamArgs},
		{"chat translated whole", "/v1/chat/completions", "an", `"messages":[{"role":"user","content":"go"}],` + chatTools, func(b string) []string {
			return []string{gjson.Get(b, "choices.0.message.tool_calls.0.function.arguments").String()}
		}},
	}
	for _, c := range cases {
		for _, model := range []string{"gemini-4-hydrogen", "gpt-5.5"} {
			body := `{"model":"` + c.model + `/` + model + `",` + c.body + `}`
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
			if rec.Code != 200 {
				t.Fatalf("%s %s: %d %s", c.name, model, rec.Code, rec.Body)
			}
			got := c.args(rec.Body.String())
			want := hermesWant
			if model == "gpt-5.5" {
				want = said
			}
			for _, g := range got {
				if !freeSameJSON(t, g, want) {
					t.Errorf("%s %s: the client was told the arguments\n%s\nwant\n%s\nreply:\n%s", c.name, model, g, want, rec.Body)
				}
			}
			if len(got) == 0 {
				t.Errorf("%s %s: no call in the reply:\n%s", c.name, model, rec.Body)
			}
		}
	}
}

// freeSSEData is each data line's JSON in a stream.
func freeSSEData(b string) []gjson.Result {
	var out []gjson.Result
	for _, line := range strings.Split(b, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(d, "{") {
			out = append(out, gjson.Parse(d))
		}
	}
	return out
}

// chatStreamArgs is a Chat stream's first call's arguments, its pieces
// joined.
func chatStreamArgs(b string) []string {
	var args strings.Builder
	seen := false
	for _, ev := range freeSSEData(b) {
		ev.Get("choices.0.delta.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if tc.Get("index").Int() == 0 {
				seen = true
				args.WriteString(tc.Get("function.arguments").String())
			}
			return true
		})
	}
	if !seen {
		return nil
	}
	return []string{args.String()}
}

// respStreamArgs is a Responses stream's call's arguments: its deltas
// joined, its .done, its item's done and the completed response's.
func respStreamArgs(b string) []string {
	var deltas strings.Builder
	var out []string
	for _, ev := range freeSSEData(b) {
		switch ev.Get("type").String() {
		case "response.function_call_arguments.delta":
			deltas.WriteString(ev.Get("delta").String())
		case "response.function_call_arguments.done":
			out = append(out, ev.Get("arguments").String())
		case "response.output_item.done":
			out = append(out, ev.Get("item.arguments").String())
		case "response.completed":
			out = append(out, ev.Get("response.output.0.arguments").String())
		}
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, deltas.String())
}

// anStreamArgs is a Messages stream's tool_use input, its deltas joined.
func anStreamArgs(b string) []string {
	var args strings.Builder
	seen := false
	for _, ev := range freeSSEData(b) {
		if ev.Get("delta.type").String() == "input_json_delta" {
			seen = true
			args.WriteString(ev.Get("delta.partial_json").String())
		}
	}
	if !seen {
		return nil
	}
	return []string{args.String()}
}
