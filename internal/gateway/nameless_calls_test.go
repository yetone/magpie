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

	"github.com/yetone/magpie/internal/provider"
)

// namelessAsk is Claude Code's turn after a model made a call naming no
// function (GLM-5.3 on Command Code, #7565): the bad call beside a good
// one, both answered, and the user's next words.
const namelessAsk = `{"model":"m","max_tokens":16,"messages":[
	{"role":"user","content":"look at /x"},
	{"role":"assistant","content":[
		{"type":"tool_use","id":"call_bad","name":"","input":{}},
		{"type":"tool_use","id":"call_ok","name":"Read","input":{"file_path":"/x"}}
	]},
	{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"call_bad","content":"bad output"},
		{"type":"tool_result","tool_use_id":"call_ok","content":"ok output"},
		{"type":"text","text":"continue"}
	]}
],"tools":[{"name":"Read","description":"Reads a file.","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}`

// TestResponsesBuildDropsNamelessCall is k0ngk0ng's test (#7565, their
// branch fix/drop-nameless-responses-call), read the way the gateway reads
// a client's request (parseRequest): a call naming no function runs
// nowhere, so it is no call. It and the answer to it are left out of the
// Responses build; the call that names its function stays, and so does the
// turn's text.
func TestResponsesBuildDropsNamelessCall(t *testing.T) {
	r, err := parseRequest(provider.Anthropic, []byte(namelessAsk), false, false)
	if err != nil {
		t.Fatal(err)
	}
	out := buildResponses(r, "m", "api.commandcode.ai", false)
	var built struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(out, &built); err != nil {
		t.Fatal(err)
	}
	var named []string
	badOutput, okOutput := false, false
	for _, it := range built.Input {
		switch it["type"] {
		case "function_call":
			name, _ := it["name"].(string)
			named = append(named, name)
		case "function_call_output":
			badOutput = badOutput || it["call_id"] == "call_bad"
			okOutput = okOutput || it["call_id"] == "call_ok"
		}
	}
	if len(named) != 1 || named[0] != "Read" {
		t.Errorf("calls = %q, want [Read]: a call naming no function is no call", named)
	}
	if badOutput {
		t.Error("the answer to the nameless call is left in, with no call to answer")
	}
	if !okOutput {
		t.Error("the named call's answer was lost with the nameless one")
	}
	if !bodyHas(out, `"text":"continue"`) {
		t.Error("the turn's text was lost with the nameless call")
	}
}

// Every API magpie builds a request on leaves the nameless call and its
// answer out, not only Responses: each turns a call naming nothing away,
// or runs it nowhere.
func TestEveryBuildDropsNamelessCall(t *testing.T) {
	for _, from := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses} {
		ask := namelessAsk
		switch from {
		case provider.Chat:
			ask = `{"model":"m","messages":[
				{"role":"user","content":"look at /x"},
				{"role":"assistant","content":null,"tool_calls":[
					{"id":"call_bad","type":"function","function":{"name":"","arguments":"{}"}},
					{"id":"call_ok","type":"function","function":{"name":"Read","arguments":"{\"file_path\":\"/x\"}"}}]},
				{"role":"tool","tool_call_id":"call_bad","content":"bad output"},
				{"role":"tool","tool_call_id":"call_ok","content":"ok output"},
				{"role":"user","content":"continue"}],
				"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}]}`
		case provider.Responses:
			ask = `{"model":"m","input":[
				{"role":"user","content":"look at /x"},
				{"type":"function_call","call_id":"call_bad","name":"","arguments":"{}"},
				{"type":"function_call","call_id":"call_ok","name":"Read","arguments":"{\"file_path\":\"/x\"}"},
				{"type":"function_call_output","call_id":"call_bad","output":"bad output"},
				{"type":"function_call_output","call_id":"call_ok","output":"ok output"},
				{"role":"user","content":"continue"}],
				"tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}]}`
		}
		r, err := parseRequest(from, []byte(ask), false, false)
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		builds := map[string][]byte{
			"responses":   buildResponses(r, "m", "api.commandcode.ai", false),
			"chat":        buildChat(r, "m", "api.commandcode.ai", false),
			"anthropic":   buildAnthropic(r, "m"),
			"code assist": buildCodeAssist(r, "gemini-3-pro", "gemini"),
		}
		g, err := buildGemini(r, "m")
		if err != nil {
			t.Fatal(err)
		}
		builds["gemini"] = g
		for name, out := range builds {
			for _, gone := range []string{"call_bad", "bad output"} {
				if bytes.Contains(out, []byte(gone)) {
					t.Errorf("%s → %s: %q is still in the request: %s", from, name, gone, out)
				}
			}
			for _, kept := range []string{"Read", "ok output", "continue"} {
				if !bytes.Contains(out, []byte(kept)) {
					t.Errorf("%s → %s: %q was lost with the nameless call: %s", from, name, kept, out)
				}
			}
		}
	}
}

// A request with no nameless call is read as it was.
func TestNamedCallsUntouched(t *testing.T) {
	ask := strings.Replace(namelessAsk, `"name":""`, `"name":"Grep"`, 1)
	r, err := parseRequest(provider.Anthropic, []byte(ask), false, false)
	if err != nil {
		t.Fatal(err)
	}
	if out := buildResponses(r, "m", "h", false); !bytes.Contains(out, []byte("call_bad")) || !bytes.Contains(out, []byte("bad output")) {
		t.Errorf("a named call was dropped: %s", out)
	}
}

// namelessVendor is an upstream on one API that keeps the last request it
// got, and answers with a call naming no function and nothing else, as
// GLM-5.3 on Command Code did: on Responses as an output item, on Chat as a
// tool call, on Messages as a tool_use block.
type namelessVendor struct {
	proto provider.Protocol
	mu    sync.Mutex
	got   []byte
}

func (v *namelessVendor) last() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.got
}

func (v *namelessVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	v.got = b
	v.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	switch v.proto {
	case provider.Responses:
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"m"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_new","name":"","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{}"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_new","name":"","arguments":"{}","status":"completed"}}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"function_call","call_id":"call_new","name":"","arguments":"{}"}],"usage":{"input_tokens":5,"output_tokens":5}}}`))
	case provider.Chat:
		io.WriteString(w, sse(`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_new","type":"function","function":{"name":"","arguments":"{}"}}]}}]}`,
			`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`,
			`data: [DONE]`))
	default:
		io.WriteString(w, sse(`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_new","name":"","input":{}}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			`data: {"type":"message_stop"}`))
	}
}

// Claude Code on a provider serving Responses alone, as Command Code's GLM
// was (#7565): the history's nameless call and its answer never reach the
// upstream, and the upstream's new nameless call never reaches Claude Code,
// whose turn ends instead of waiting on a call that runs nowhere.
func TestNamelessCallTranslatedBothWays(t *testing.T) {
	fresh(t)
	up := &namelessVendor{proto: provider.Responses}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	if err := provider.Save(provider.Provider{ID: "cc", Name: "CC", Key: "k", Responses: vendor.URL + "/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	ask := strings.Replace(namelessAsk, `"model":"m"`, `"model":"cc/m","stream":true`, 1)
	res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(ask))
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, reply)
	}
	sent := up.last()
	if bytes.Contains(sent, []byte("call_bad")) || bytes.Contains(sent, []byte("bad output")) {
		t.Errorf("the nameless call or its answer reached the upstream: %s", sent)
	}
	if !bytes.Contains(sent, []byte("call_ok")) || !bytes.Contains(sent, []byte("ok output")) {
		t.Errorf("the named call was lost: %s", sent)
	}
	if bytes.Contains(reply, []byte(`"tool_use"`)) {
		t.Errorf("Claude Code was handed a call naming nothing: %s", reply)
	}
	if !bytes.Contains(reply, []byte(`"stop_reason":"end_turn"`)) {
		t.Errorf("the turn didn't end: %s", reply)
	}
}

// A translated reply on each API a client speaks hands it no call naming
// nothing, whichever API the upstream answered on.
func TestNamelessCallNeverReachesClient(t *testing.T) {
	for _, upAPI := range []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic} {
		for _, client := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
			if upAPI == provider.Anthropic && client == "/v1/messages" || upAPI == provider.Chat && client == "/v1/chat/completions" || upAPI == provider.Responses && client == "/v1/responses" {
				continue // relayed, not translated
			}
			for _, stream := range []bool{true, false} {
				t.Run(string(upAPI)+client+map[bool]string{true: "/stream", false: "/whole"}[stream], func(t *testing.T) {
					fresh(t)
					up := &namelessVendor{proto: upAPI}
					vendor := httptest.NewServer(up)
					t.Cleanup(vendor.Close)
					p := provider.Provider{ID: "cc", Name: "CC", Key: "k", Models: []string{"m"}}
					switch upAPI {
					case provider.Responses:
						p.Responses = vendor.URL + "/v1"
					case provider.Chat:
						p.Chat = vendor.URL + "/v1"
					default:
						p.Anthropic = vendor.URL
					}
					if err := provider.Save(p); err != nil {
						t.Fatal(err)
					}
					srv := httptest.NewServer(New().Handler())
					t.Cleanup(srv.Close)
					var ask string
					switch client {
					case "/v1/messages":
						ask = `{"model":"cc/m","max_tokens":16,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`
					case "/v1/chat/completions":
						ask = `{"model":"cc/m","stream":%v,"messages":[{"role":"user","content":"hi"}]}`
					default:
						ask = `{"model":"cc/m","stream":%v,"input":"hi"}`
					}
					ask = strings.Replace(ask, "%v", map[bool]string{true: "true", false: "false"}[stream], 1)
					res, err := http.Post(srv.URL+client, "application/json", strings.NewReader(ask))
					if err != nil {
						t.Fatal(err)
					}
					reply, _ := io.ReadAll(res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						t.Fatalf("stream %v: %d %s", stream, res.StatusCode, reply)
					}
					for _, call := range []string{`"tool_use"`, `"tool_calls"`, `"function_call"`, "call_new"} {
						if bytes.Contains(reply, []byte(call)) {
							t.Errorf("stream %v: the client was handed a call naming nothing (%s): %s", stream, call, reply)
						}
					}
				})
			}
		}
	}
}

// A request relayed as the client wrote it, on Messages, Responses and
// Chat, goes without the nameless call and its answer; the rest of it, and
// a request with none, go as they came.
func TestNamelessCallRelayedOut(t *testing.T) {
	asks := map[provider.Protocol]string{
		provider.Anthropic: namelessAsk,
		provider.Chat: `{"model":"m","messages":[
			{"role":"user","content":"look at /x"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_bad","type":"function","function":{"name":"","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_bad","content":"bad output"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_ok","type":"function","function":{"name":"Read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_ok","content":"ok output"},
			{"role":"user","content":"continue"}]}`,
		provider.Responses: `{"model":"m","input":[
			{"role":"user","content":"look at /x"},
			{"type":"function_call","call_id":"call_bad","name":"","arguments":"{}"},
			{"type":"function_call","call_id":"call_ok","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_bad","output":"bad output"},
			{"type":"function_call_output","call_id":"call_ok","output":"ok output"},
			{"role":"user","content":"continue"}]}`,
	}
	out := map[provider.Protocol]func([]byte) []byte{
		provider.Anthropic: namelessBlocksOut,
		provider.Chat:      namelessChatCallsOut,
		provider.Responses: namelessItemsOut,
	}
	for proto, ask := range asks {
		got := out[proto]([]byte(ask))
		if !json.Valid(got) {
			t.Fatalf("%s: not JSON: %s", proto, got)
		}
		for _, gone := range []string{"call_bad", "bad output"} {
			if bytes.Contains(got, []byte(gone)) {
				t.Errorf("%s: %q is still in the relayed request: %s", proto, gone, got)
			}
		}
		for _, kept := range []string{"call_ok", "ok output", "continue", "look at /x"} {
			if !bytes.Contains(got, []byte(kept)) {
				t.Errorf("%s: %q was lost: %s", proto, kept, got)
			}
		}
		named := []byte(strings.Replace(ask, `"name":""`, `"name":"Grep"`, 1))
		if again := out[proto](named); !bytes.Equal(again, named) {
			t.Errorf("%s: a request with no nameless call changed: %s", proto, again)
		}
	}
}

// End to end on a Messages relay, as Claude Code to Command Code's
// /provider/v1/messages: the nameless call and its answer never reach the
// upstream.
func TestNamelessCallMessagesRelay(t *testing.T) {
	fresh(t)
	up := &namelessVendor{proto: provider.Anthropic}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	if err := provider.Save(provider.Provider{ID: "cc", Name: "CC", Key: "k", Anthropic: vendor.URL, Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New().Handler())
	t.Cleanup(srv.Close)
	ask := strings.Replace(namelessAsk, `"model":"m"`, `"model":"cc/m","stream":true`, 1)
	res, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(ask))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	sent := up.last()
	if len(sent) == 0 {
		t.Fatalf("no request reached the upstream: %d", res.StatusCode)
	}
	if bytes.Contains(sent, []byte("call_bad")) || bytes.Contains(sent, []byte("bad output")) {
		t.Errorf("the nameless call or its answer reached the upstream: %s", sent)
	}
	if !bytes.Contains(sent, []byte("call_ok")) {
		t.Errorf("the named call was lost: %s", sent)
	}
}
