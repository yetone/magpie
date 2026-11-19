package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// strictBackend stands in for the ChatGPT backend's compile of tool
// schemas, as it answered on 2026-11-19 and as Anotiaww's request was
// answered: a function tool whose strict is left out is compiled, and a
// oneOf in it is the reporter's 400 word for word, a lookaround in a
// pattern the backend's own; one with strict true is held to strict
// mode's rules (oneOf, allOf and the like refused, every property
// required, additionalProperties false), each refusal as the backend
// words it; one with strict false is taken whatever its schema. It keeps
// every body it was sent and answers a request it takes with text.
type strictBackend struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (s *strictBackend) last(t *testing.T) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("the ChatGPT backend was asked nothing")
	}
	return s.bodies[len(s.bodies)-1]
}

// strictFault is the backend's refusal of the schema at path under s, a
// strict schema's, or "".
func strictFault(s gjson.Result, path string) string {
	if !s.IsObject() {
		return ""
	}
	for _, k := range []string{"oneOf", "allOf", "not", "if", "uniqueItems", "minProperties", "contains", "propertyNames", "dependentRequired"} {
		if s.Get(k).Exists() {
			return fmt.Sprintf("At schema path %s, %s is not supported for strict schemas", path, k)
		}
	}
	if !s.Get("type").Exists() && !s.Get("anyOf").Exists() && !s.Get(`\$ref`).Exists() && !s.Get("enum").Exists() {
		return fmt.Sprintf("At schema path %s, schema must specify a type", path)
	}
	if s.Get("type").String() == "object" {
		if s.Get("additionalProperties").Raw != "false" {
			return fmt.Sprintf("At schema path %s, additionalProperties must be false for a strict schema", path)
		}
		req := map[string]bool{}
		for _, r := range s.Get("required").Array() {
			req[r.String()] = true
		}
		msg := ""
		s.Get("properties").ForEach(func(k, v gjson.Result) bool {
			if !req[k.String()] {
				msg = fmt.Sprintf(`At schema path %s, required must include declared property "%s"`, path, k.String())
			} else {
				msg = strictFault(v, strings.TrimSuffix(path, "/")+"/properties/"+k.String())
			}
			return msg == ""
		})
		if msg != "" {
			return msg
		}
	}
	for _, k := range []string{"items"} {
		if m := strictFault(s.Get(k), strings.TrimSuffix(path, "/")+"/"+k); m != "" {
			return m
		}
	}
	for i, b := range s.Get("anyOf").Array() {
		if m := strictFault(b, fmt.Sprintf("%s/anyOf/%d", strings.TrimSuffix(path, "/"), i)); m != "" {
			return m
		}
	}
	return ""
}

func refusedSchema(b []byte) string {
	bad := func(msg string) string {
		return `{"error":{"message":` + string(mustJSON(msg)) + `,"type":"invalid_request_error","param":"tools","code":"invalid_function_parameters"}}`
	}
	for i, tl := range gjson.GetBytes(b, "tools").Array() {
		if tl.Get("type").String() != "function" {
			continue
		}
		params := tl.Get("parameters")
		switch strict := tl.Get("strict"); {
		case !strict.Exists():
			if strings.Contains(params.Raw, `"oneOf"`) {
				return bad(fmt.Sprintf("Invalid schema for 'tools[%d].parameters': oneOf constraints are not supported. Enable 'coerce_one_of' option to approximate oneOf with anyOf. Compilation limits apply to all strict schemas in the request", i))
			}
			if strings.Contains(params.Raw, `(?!`) || strings.Contains(params.Raw, `(?=`) {
				return bad("Invalid JSON schema: regex lookaround is not supported.")
			}
		case strict.Type == gjson.True:
			if m := strictFault(params, "/"); m != "" {
				return bad("Invalid schema for function '" + tl.Get("name").String() + "': " + m)
			}
		}
	}
	return ""
}

func strictUpstream(t *testing.T) *strictBackend {
	t.Helper()
	s := &strictBackend{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","supported_reasoning_levels":[{"effort":"medium"}],"context_window":272000}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, b)
		s.mu.Unlock()
		if msg := refusedSchema(b); msg != "" {
			// the backend refuses a schema inside its 200 stream
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: error`+"\n"+`data: {"type":"error","error":`+gjson.Get(msg, "error").Raw+`}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"4"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"4"}]}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return s
}

// lobeHubTools is LobeHub's calculator as its OpenAI provider offers it
// on Responses (convertChatCompletionToolToResponseTool: the function's
// fields, no strict), from lobehub/lobe-chat's
// packages/builtin-tool-calculator/src/manifest.ts. defintegrate's bounds
// and limit's point are a oneOf of string and number.
func lobeHubTools(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/lobehub_calculator_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lobeHubChatTools is the same tools as a Chat client offers them, each
// with strict true.
func lobeHubChatTools(t *testing.T) string {
	t.Helper()
	var out []any
	for _, tl := range gjson.Parse(lobeHubTools(t)).Array() {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": tl.Get("name").String(), "description": tl.Get("description").String(),
			"parameters": json.RawMessage(tl.Get("parameters").Raw), "strict": true}})
	}
	return string(mustJSON(out))
}

// Anotiaww (X): LobeHub's OpenAI provider, pointed at magpie, asked a
// Codex model on Responses and got "Codex: Invalid schema for
// 'tools[3].parameters': oneOf constraints are not supported". The
// ChatGPT backend compiles a tool whose strict is left out; Codex says
// false on every tool it sends. Every path to a Codex account sends each
// tool with a strict the backend can compile: left out it is false, as
// Codex says it; asked for, a oneOf goes as anyOf and stays strict, and
// a schema strict mode can't take goes as written with strict false.
func TestCodexToolSchemasTheBackendCompiles(t *testing.T) {
	codexSignedIn(t)
	sb := strictUpstream(t)
	ok := func(name string, code int, out string) []byte {
		t.Helper()
		got := sb.last(t)
		if code != 200 || refusedSchema(got) != "" || strings.Contains(out, "not supported") {
			t.Fatalf("%s: %d %s\nthe backend got %s", name, code, out, got)
		}
		return got
	}
	tool := func(b []byte, name string) gjson.Result {
		t.Helper()
		tl := gjson.GetBytes(b, `tools.#(name=="`+name+`")`)
		if !tl.Exists() {
			t.Fatalf("no tool %s in %s", name, b)
		}
		return tl
	}

	// the reporter's: LobeHub on Responses, strict left out
	code, out := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"integrate x^2 from 0 to 1"}]}],
	  "tools":`+lobeHubTools(t)+`}`)
	got := ok("responses", code, out)
	for _, tl := range gjson.GetBytes(got, "tools").Array() {
		if s := tl.Get("strict"); s.Type != gjson.False {
			t.Errorf("responses: %s went with strict %s", tl.Get("name"), s.Raw)
		}
	}
	// its schema as LobeHub wrote it: false takes the oneOf
	if want := gjson.Get(lobeHubTools(t), `#(name=="lobe-calculator____defintegrate____builtin").parameters`).Raw; !sameJSONText(tool(got, "lobe-calculator____defintegrate____builtin").Get("parameters").Raw, want) {
		t.Errorf("responses: defintegrate's schema changed: %s", tool(got, "lobe-calculator____defintegrate____builtin").Get("parameters").Raw)
	}

	// a Chat client asking for strict: a oneOf is asked as anyOf and the
	// tool stays strict; a schema strict mode can't take (calculate's
	// precision isn't required) goes non-strict as written
	code, out = post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":true,"messages":[{"role":"user","content":"integrate x^2 from 0 to 1"}],
	  "tools":`+lobeHubChatTools(t)+`}`)
	got = ok("chat", code, out)
	def := tool(got, "lobe-calculator____defintegrate____builtin")
	if def.Get("strict").Type != gjson.True || strings.Contains(def.Raw, `"oneOf"`) ||
		def.Get("parameters.properties.lowerBound.anyOf.#").Int() != 2 || def.Get("parameters.properties.upperBound.anyOf.#").Int() != 2 {
		t.Errorf("chat: defintegrate went as %s", def.Raw)
	}
	if calc := tool(got, "lobe-calculator____calculate____builtin"); calc.Get("strict").Type != gjson.False || !calc.Get("parameters.properties.precision").Exists() {
		t.Errorf("chat: calculate went as %s", calc.Raw)
	}

	// a Responses client asking for strict, a oneOf nested in items, in
	// $defs and in a property of a branch, and one with an allOf
	nested := `{"type":"object","additionalProperties":false,"required":["ops","target"],"properties":{
	  "ops":{"type":"array","items":{"oneOf":[{"type":"string"},{"$ref":"#/$defs/op"}]}},
	  "target":{"$ref":"#/$defs/target"}},
	  "$defs":{"op":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"oneOf":[{"type":"string","const":"a"},{"type":"string","const":"b"}]}}},
	    "target":{"oneOf":[{"type":"string"},{"type":"number"}]}}}`
	allOf := `{"type":"object","additionalProperties":false,"required":["q"],"properties":{"q":{"allOf":[{"type":"string"},{"minLength":1}]}}}`
	code, out = post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"go",
	  "tools":[{"type":"function","name":"edit","strict":true,"parameters":`+nested+`},
	    {"type":"function","name":"find","strict":true,"parameters":`+allOf+`},
	    {"type":"function","name":"read","strict":false,"parameters":{"type":"object","properties":{"p":{"type":"string","pattern":"^(?!/etc).*"}}}}]}`)
	got = ok("responses strict", code, out)
	if e := tool(got, "edit"); e.Get("strict").Type != gjson.True || strings.Contains(e.Raw, `"oneOf"`) ||
		e.Get("parameters.properties.ops.items.anyOf.#").Int() != 2 || e.Get("parameters.$defs.target.anyOf.#").Int() != 2 ||
		e.Get("parameters.$defs.op.properties.kind.anyOf.#").Int() != 2 {
		t.Errorf("responses strict: edit went as %s", e.Raw)
	}
	if f := tool(got, "find"); f.Get("strict").Type != gjson.False || !sameJSONText(f.Get("parameters").Raw, allOf) {
		t.Errorf("responses strict: find went as %s", f.Raw)
	}
	if r := tool(got, "read"); r.Get("strict").Type != gjson.False || !strings.Contains(r.Raw, `(?!/etc)`) {
		t.Errorf("responses strict: read went as %s", r.Raw)
	}

	// Claude Code's Messages, a tool asking for strict with a oneOf
	code, out = post(t, "/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":200,"stream":true,"messages":[{"role":"user","content":"go"}],
	  "tools":[{"name":"mcp__calc__defintegrate","strict":true,"input_schema":`+gjson.Get(lobeHubTools(t), `#(name=="lobe-calculator____defintegrate____builtin").parameters`).Raw+`}]}`)
	got = ok("messages", code, out)
	if m := tool(got, "mcp__calc__defintegrate"); m.Get("strict").Type == gjson.True && strings.Contains(m.Raw, `"oneOf"`) {
		t.Errorf("messages: went as %s", m.Raw)
	}
}

func sameJSONText(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return jsonSame(x, y)
}

// Another vendor's Responses or Chat API is sent the tools as the client
// wrote them: OpenAI's own API keeps its strict default and the client's
// oneOf, and a relay or another provider is not given Codex's rule.
func TestToolStrictOnlyForCodex(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	save := func(p provider.Provider, api string) {
		t.Helper()
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		for _, m := range p.Models {
			if err := provider.SetModelAPI(p.ID+"/"+m, api); err != nil {
				t.Fatal(err)
			}
		}
	}
	save(provider.Provider{ID: "oai", Name: "OpenAI", Key: "sk-k",
		Responses: "https://api.openai.com/v1", Models: []string{"gpt-5.5"}}, "responses")
	save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Responses: "https://relay.test/v1", Models: []string{"gpt-5.5"}}, "responses")
	save(provider.Provider{ID: "chatonly", Name: "Chat only", Key: "k",
		Chat: "https://chat.test/v1", Models: []string{"deepseek-chat"}}, "chat")
	up := &programsUpstream{}
	s := New()
	s.client = &http.Client{Transport: up}
	send := func(path, body string) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		_, b := up.last(t)
		return b
	}
	lobe := lobeHubTools(t)
	for _, model := range []string{"oai/gpt-5.5", "relay/gpt-5.5"} {
		got := send("/v1/responses", `{"model":"`+model+`","stream":true,"input":"go","tools":`+lobe+`}`)
		for i, tl := range gjson.GetBytes(got, "tools").Array() {
			want := gjson.Get(lobe, fmt.Sprintf("%d", i))
			if tl.Get("strict").Exists() || !sameJSONText(tl.Get("parameters").Raw, want.Get("parameters").Raw) {
				t.Errorf("%s: %s went as %s", model, want.Get("name"), tl.Raw)
			}
		}
	}
	got := send("/v1/chat/completions", `{"model":"chatonly/deepseek-chat","stream":true,"messages":[{"role":"user","content":"go"}],"tools":`+lobeHubChatTools(t)+`}`)
	def := gjson.GetBytes(got, `tools.#(function.name=="lobe-calculator____defintegrate____builtin").function`)
	if def.Get("strict").Type != gjson.True || def.Get("parameters.properties.lowerBound.oneOf.#").Int() != 2 {
		t.Errorf("chat: defintegrate went as %s", def.Raw)
	}
}
