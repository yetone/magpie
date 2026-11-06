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

// Gemini's OpenAI-compatible API, in Linka's frames (Discord): a whole
// function call, its thought signature and the usage in one chunk, then
// the finish_reason in the next.
const (
	compatCall = `data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"extra_content":{"google":{"thought_signature":"c2ln"}},"function":{"arguments":"{\"code\":\"run()\"}","name":"codemode"},"id":"call_1","type":"function"}]},"index":0}],"model":"models/gemini-4-argon","object":"chat.completion.chunk","usage":{"completion_tokens":1110,"prompt_tokens":2049,"total_tokens":165667}}` + "\n\n"
	compatStop = `data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"model":"models/gemini-4-argon","object":"chat.completion.chunk","usage":{"completion_tokens":1110,"prompt_tokens":2049,"total_tokens":165667}}` + "\n\n"
	compatDone = "data: [DONE]\n\n"
)

// compatServer serves reply as a Chat upstream on this machine, where a
// Gemini model's is taken to be Gemini's OpenAI-compatible API
// (geminiCompat), and asks it for model at path with body.
func compatServer(t *testing.T, model, reply, path, body string) (int, string) {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, reply)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{model}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	return post(t, path, strings.ReplaceAll(body, "MODEL", "up/"+model))
}

// compatBare is compatCall without the usage: the generation wasn't
// counted as done.
var compatBare = strings.Replace(compatCall, `,"usage":{"completion_tokens":1110,"prompt_tokens":2049,"total_tokens":165667}`, "", 1)

// The upstream hung up between the call and its finish_reason. Gemini's
// OpenAI-compatible API counts the usage only once the generation is
// done, so a call chunk carrying it lost only the finish_reason: the
// client relayed to gets it, "tool_calls", where Pi threw the call away
// as "Stream ended without finish_reason". Without the usage the reply
// was cut, and the client is told so, as a Gemini stream's is (#1644).
func TestGeminiCompatRelayEndedShort(t *testing.T) {
	const ask = `{"model":"MODEL","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	const cut = `data: {"error":{"message":"UP: the reply ended before it was complete","type":"api_error"}}`

	code, body := compatServer(t, "gemini-4-argon", compatCall, "/v1/chat/completions", ask)
	if code != 200 || !strings.Contains(body, `"codemode"`) || strings.Contains(body, "ended before") {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) || !strings.HasSuffix(strings.TrimSpace(body), compatDone[:len(compatDone)-2]) {
		t.Errorf("a done Gemini reply that lost its finish_reason didn't end with it: %s", body)
	}

	// text alone, done, ends with "stop"
	text := `data: {"choices":[{"delta":{"role":"assistant","content":"hi"},"index":0}],"model":"models/gemini-4-argon","object":"chat.completion.chunk","usage":{"completion_tokens":1,"prompt_tokens":2049,"total_tokens":2050}}` + "\n\n"
	if _, body := compatServer(t, "gemini-4-argon", text, "/v1/chat/completions", ask); !strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, "ended before") {
		t.Errorf("done text: %s", body)
	}

	code, body = compatServer(t, "gemini-4-argon", compatBare, "/v1/chat/completions", ask)
	if code != 200 || !strings.Contains(body, `"codemode"`) {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), cut) {
		t.Errorf("a cut Gemini reply ended as if whole: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"`) {
		t.Errorf("a cut reply given a finish_reason: %s", body)
	}

	// whole, with [DONE] or without it, it goes as it came, its one
	// finish_reason "tool_calls" as it made a call (chatTidy, 喜欢一个人
	// on Discord), never a second one added
	for what, reply := range map[string]string{
		"whole":            compatCall + compatStop + compatDone,
		"whole, no [DONE]": compatCall + compatStop,
	} {
		code, body := compatServer(t, "gemini-4-argon", reply, "/v1/chat/completions", ask)
		if code != 200 || strings.Contains(body, "ended before") || strings.Count(body, `"finish_reason":"`) != 1 || !strings.Contains(body, `"finish_reason":"tool_calls"`) {
			t.Errorf("%s: %d %s", what, code, body)
		}
	}

	// another vendor's Chat stream may end whole with neither: it goes as
	// it came, as it did
	for _, reply := range []string{compatCall, compatBare} {
		other := strings.ReplaceAll(reply, "models/gemini-4-argon", "kimi-k3")
		code, body = compatServer(t, "kimi-k3", other, "/v1/chat/completions", ask)
		if code != 200 || strings.Contains(body, "ended before") || strings.Contains(body, `"finish_reason":"`) {
			t.Errorf("another vendor's stream: %d %s", code, body)
		}
	}
}

// Translated, a call whose generation was done ends the turn with the
// call, for Claude Code to run; a cut one ends with the error, not with a
// turn that ended with the call never run.
func TestGeminiCompatTranslatedEndedShort(t *testing.T) {
	const ask = `{"model":"MODEL","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_, body := compatServer(t, "gemini-4-argon", compatCall, "/v1/messages", ask)
	if !strings.Contains(body, `"codemode"`) {
		t.Fatalf("the call didn't reach the client: %s", body)
	}
	if _, stops, errs := messageStops(body); errs != 0 || stops != 1 || !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("a done Gemini call didn't end its turn as one (%d stops, %d errors): %s", stops, errs, body)
	}

	_, body = compatServer(t, "gemini-4-argon", compatBare, "/v1/messages", ask)
	if !strings.Contains(body, `"codemode"`) {
		t.Fatalf("the call didn't reach the client: %s", body)
	}
	if _, stops, errs := messageStops(body); errs != 1 || stops != 0 {
		t.Errorf("a cut Gemini reply ended as a turn (%d stops, %d errors): %s", stops, errs, body)
	}

	_, body = compatServer(t, "gemini-4-argon", compatCall+compatStop+compatDone, "/v1/messages", ask)
	if _, stops, errs := messageStops(body); errs != 0 || stops != 1 {
		t.Errorf("a whole Gemini reply (%d stops, %d errors): %s", stops, errs, body)
	}

	// another vendor's Chat stream ends as it did
	other := strings.ReplaceAll(compatBare, "models/gemini-4-argon", "kimi-k3")
	_, body = compatServer(t, "kimi-k3", other, "/v1/messages", ask)
	if _, _, errs := messageStops(body); errs != 0 {
		t.Errorf("another vendor's stream: %s", body)
	}

	// unstreamed: a done call is the answer, a cut reply a 502
	unstreamed := strings.Replace(ask, `"stream":true`, `"stream":false`, 1)
	code, body := compatServer(t, "gemini-4-argon", compatCall, "/v1/messages", unstreamed)
	if code != 200 || !strings.Contains(body, `"tool_use"`) || !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Errorf("unstreamed done call: %d %s", code, body)
	}
	code, body = compatServer(t, "gemini-4-argon", compatBare, "/v1/messages", unstreamed)
	if code != 502 || !strings.Contains(body, "ended before") {
		t.Errorf("unstreamed cut: %d %s", code, body)
	}
}

// A Gemini reply that ended done but inside a line still gets its
// finish_reason as an event of its own, not as more of that line (#2446).
func TestGeminiCompatFinishAfterPartialLine(t *testing.T) {
	const ask = `{"model":"MODEL","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	_, body := compatServer(t, "gemini-4-argon", compatCall+`data: {"choi`, "/v1/chat/completions", ask)
	i := strings.Index(body, `"finish_reason":"tool_calls"`)
	if i < 0 {
		t.Fatalf("no finish_reason filled in: %s", body)
	}
	j := strings.LastIndex(body[:i], "data: ")
	if j < 0 || !strings.HasSuffix(body[:j], "\n\n") {
		t.Errorf("the finish_reason began inside the line still pending: %q", body)
	}
}

// Gemini's OpenAI-compatible API sends a call with no index (Linka's
// frames), where the OpenAI contract has one on every fragment. The AI
// SDK's OpenAI provider, which Cursor's Private Inference reads Chat with,
// turned the chunk away ("expected number, received undefined" at
// tool_calls[0].index), and the call was never made (#3386). Relayed,
// every call has its index: Linka's call 0, a second call in a chunk of
// its own 1, and #3386's chunk, with no id either, 0 and an id.
func TestGeminiCompatRelayedCallsHaveAnIndex(t *testing.T) {
	const ask = `{"model":"MODEL","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	second := strings.NewReplacer(`"call_1"`, `"call_2"`, `run()`, `two()`).Replace(compatCall)
	reported := `data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"extra_content":{"google":{"thought_signature":"c2ln"}},"function":{"arguments":"{\"code\":\"run()\"}","name":"codemode"}}]},"index":0}],"model":"models/gemini-4-argon","object":"chat.completion.chunk"}` + "\n\n"
	for _, c := range []struct {
		name, reply string
		ids         []string // how each call's id starts
	}{
		{"Linka's call", compatCall + compatStop + compatDone, []string{"call_1"}},
		{"a second call", compatCall + second + compatStop + compatDone, []string{"call_1", "call_2"}},
		{"#3386's chunk", reported + compatStop + compatDone, []string{"call_"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := compatServer(t, "gemini-4-argon", c.reply, "/v1/chat/completions", ask)
			if code != 200 {
				t.Fatalf("status %d: %s", code, body)
			}
			var calls []map[string]any
			for _, ev := range events(body) {
				chs, _ := ev["choices"].([]any)
				for _, ch := range chs {
					d, _ := ch.(map[string]any)["delta"].(map[string]any)
					tcs, _ := d["tool_calls"].([]any)
					for _, tc := range tcs {
						calls = append(calls, tc.(map[string]any))
					}
				}
			}
			if len(calls) != len(c.ids) {
				t.Fatalf("%d calls, want %d: %s", len(calls), len(c.ids), body)
			}
			for i, call := range calls {
				if n, ok := call["index"].(float64); !ok || n != float64(i) {
					t.Errorf("call %d went with index %v, want %d: %s", i, call["index"], i, body)
				}
				if id, _ := call["id"].(string); !strings.HasPrefix(id, c.ids[i]) {
					t.Errorf("call %d went with id %q, want one starting %q", i, id, c.ids[i])
				}
			}
		})
	}
}

// A client that took the gateway for an Ollama server (its /api/show is
// answered in Ollama's shape) sends Ollama's per-request options beside
// its Chat fields. Gemini's OpenAI-compatible API reads proto JSON and
// turns the whole request away over a name it doesn't know (#3097), so a
// body relayed to one goes without options, as does another vendor's
// (Mistral's 422 extra_forbidden, TestHermesOllamaOptionsNeverReachMistral):
// no Chat API reads them.
func TestChatRelayLeavesOutOllamaOptions(t *testing.T) {
	// a whole text reply, as Gemini's OpenAI-compatible API streams one
	hi := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hi there"}}],"model":"models/gemini-3.8-flash","object":"chat.completion.chunk"}` + "\n\n"
	whole := hi + compatStop + compatDone
	ask := func(model string) string {
		return `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}],"max_tokens":8,"options":{"num_ctx":1048576}}`
	}
	// served saves the provider (Chat at base) and hands back what its
	// upstream is sent, readable once the call has come back
	served := func(t *testing.T, id, model, base string) func() []byte {
		t.Helper()
		fresh(t)
		var got []byte
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, whole)
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: id, Name: "UP", Key: "k", Models: []string{model}, Chat: up.URL + base}); err != nil {
			t.Fatal(err)
		}
		return func() []byte { return got }
	}

	// as the google preset serves Gemini: an OpenAI-compatible Chat
	// endpoint under generativelanguage.googleapis.com/v1beta/openai,
	// stood in for by this machine's own address, where a Gemini model's
	// Chat API is taken for Gemini's OpenAI-compatible one (geminiCompat)
	read := served(t, "google", "models/gemini-3.8-flash", "/v1beta/openai")
	code, body := post(t, "/v1/chat/completions", ask("google/models/gemini-3.8-flash"))
	if code != 200 || !strings.Contains(body, `"content":"hi there"`) {
		t.Fatalf("status %d: %s", code, body)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(read(), &sent); err != nil {
		t.Fatalf("the upstream was sent %s", read())
	}
	if _, ok := sent["options"]; ok {
		t.Errorf("Ollama's options reached Gemini's OpenAI-compatible API: %s", read())
	}
	if !strings.Contains(string(sent["messages"]), `"hi"`) || string(sent["max_tokens"]) != "8" {
		t.Errorf("the rest of the request didn't go as asked: %s", read())
	}

	// another vendor's Chat upstream, no Gemini model there: without
	// options too, the rest as the client sent it
	read = served(t, "km", "kimi-k3", "/v1")
	if code, body := post(t, "/v1/chat/completions", ask("km/kimi-k3")); code != 200 || !strings.Contains(body, `"content":"hi there"`) {
		t.Fatalf("another vendor: %d %s", code, body)
	}
	if got := string(read()); strings.Contains(got, `"options"`) || !strings.Contains(got, `"max_tokens":8`) {
		t.Errorf("another vendor's Chat upstream: %s", got)
	}
}
