package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

// opaqueSeal stands for Copilot's reasoning_opaque, which was 1156
// base64 characters live for 48 reasoning tokens.
const opaqueSeal = "WY6FaAAlekE9Hryej63xF4ok8vf0wtpUKcezFmQKluSFdL7nRf91nGZAwjmGIzWu1grRbjfDpm4/HlAJMez1MNlVCGo4aaZ1NLZsgITysMIJkLTGRe5wtRrmI8OyI5sdw2G3aRvO70cVls0m7hFxw/8V4x=="

// copilotOpaqueUp is Copilot's Chat API as gemini-4-hydrogen answered it
// live: a first turn's two weather calls, the opaque in the first call's
// chunk, then text once the tool results come back. whole answers a
// request that asks for no stream with the reply whole. The bodies it is
// sent are kept in sent.
func copilotOpaqueUp(t *testing.T, sent *[]string) {
	t.Helper()
	var mu sync.Mutex
	copilotAPIFake(t, map[string]string{"gemini-4-hydrogen": `["/chat/completions"]`, "gemini-4-carbon": `["/chat/completions"]`},
		func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/chat/completions" {
				io.WriteString(w, `{"data":[]}`)
				return
			}
			mu.Lock()
			*sent = append(*sent, string(b))
			mu.Unlock()
			m := modelOf(b)
			second := bodyHas(b, `"tool"`) || bodyHas(b, `"tool_call_id"`)
			if !gjson.GetBytes(b, "stream").Bool() {
				w.Header().Set("Content-Type", "application/json")
				if second {
					io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Sunny in both."}}],"id":"h2","model":"`+m+`","usage":{"completion_tokens":4,"prompt_tokens":275,"total_tokens":279}}`)
					return
				}
				io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_opaque":"`+opaqueSeal+`","tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}","name":"get_weather"},"id":"call_MHxMT2VvRVZXajFQQUtHbGY5cGY","type":"function"},{"function":{"arguments":"{\"city\":\"Tokyo\"}","name":"get_weather"},"id":"call_MXxFQ1o5R2tYeVNicnRocjV6WHg","type":"function"}]}}],"id":"h1","model":"`+m+`","usage":{"completion_tokens":88,"prompt_tokens":107,"total_tokens":195,"reasoning_tokens":48}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if second {
				io.WriteString(w, sse(
					`data: {"choices":[{"index":0,"delta":{"content":"Sunny in both.","role":"assistant"}}],"id":"h2","model":"`+m+`"}`,
					`data: {"choices":[{"finish_reason":"stop","index":0,"delta":{"content":null}}],"id":"h2","usage":{"completion_tokens":4,"prompt_tokens":275,"total_tokens":279},"model":"`+m+`"}`,
					`data: [DONE]`))
				return
			}
			io.WriteString(w, sse(
				`data: {"choices":[{"index":0,"delta":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Paris\"}","name":"get_weather"},"id":"call_MHxMT2VvRVZXajFQQUtHbGY5cGY","index":0,"type":"function"}],"reasoning_opaque":"`+opaqueSeal+`"}}],"created":1795448456,"id":"h1","model":"`+m+`"}`,
				`data: {"choices":[{"index":0,"delta":{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"city\":\"Tokyo\"}","name":"get_weather"},"id":"call_MXxFQ1o5R2tYeVNicnRocjV6WHg","index":1,"type":"function"}]}}],"created":1795448456,"id":"h1","model":"`+m+`"}`,
				`data: {"choices":[{"finish_reason":"tool_calls","index":0,"delta":{"content":null,"role":"assistant"}}],"created":1795448456,"id":"h1","usage":{"completion_tokens":88,"prompt_tokens":107,"total_tokens":195,"reasoning_tokens":48},"model":"`+m+`"}`,
				`data: [DONE]`))
		})
}

func postOpaque(t *testing.T, s *Server, path, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

const opaqueTools = `[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]`

// chatSecondTurn is a Chat client's second turn: the first turn's calls
// as a Chat agent keeps them (no reasoning_opaque, which none keeps), and
// their results. extra goes into the assistant message.
func chatSecondTurn(model, extra string, stream bool) string {
	s := "false"
	if stream {
		s = "true"
	}
	return `{"model":"` + model + `","stream":` + s + `,"tools":` + opaqueTools + `,"messages":[
	  {"role":"user","content":"weather in Paris and Tokyo?"},
	  {"role":"assistant","content":null` + extra + `,"tool_calls":[
	    {"id":"call_MHxMT2VvRVZXajFQQUtHbGY5cGY","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
	    {"id":"call_MXxFQ1o5R2tYeVNicnRocjV6WHg","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
	  {"role":"tool","tool_call_id":"call_MHxMT2VvRVZXajFQQUtHbGY5cGY","content":"sunny"},
	  {"role":"tool","tool_call_id":"call_MXxFQ1o5R2tYeVNicnRocjV6WHg","content":"sunny"}]}`
}

// opaqueOn is the reasoning_opaque the assistant message with tool calls
// in a Chat body carries, "" for none.
func opaqueOn(body string) string {
	for _, m := range gjson.Get(body, "messages").Array() {
		if m.Get("role").Str == "assistant" && m.Get("tool_calls").Exists() {
			return m.Get("reasoning_opaque").Str
		}
	}
	return ""
}

// Copilot seals a reply's reasoning in reasoning_opaque beside its tool
// calls, and opens it again when the next turn carries it back, as VS
// Code's Copilot Chat does (live: 275 prompt tokens with it, 227
// without). A Chat agent keeps no field it doesn't know, so the model's
// reasoning was lost on every tool turn (@yuushabu_buyinn on X). It is
// kept by the call ids, on disk, and put back for the same account's
// same model, a restart between the turns or not, streamed or whole.
func TestCopilotOpaqueGoesBackOnTheToolTurn(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "whole"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var sent []string
			copilotOpaqueUp(t, &sent)
			first := `{"model":"copilot/gemini-4-hydrogen","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,"tools":` + opaqueTools + `,"messages":[{"role":"user","content":"weather in Paris and Tokyo?"}]}`
			if out := postOpaque(t, New(), "/v1/chat/completions", first); !strings.Contains(out, "call_MHxMT2VvRVZXajFQQUtHbGY5cGY") {
				t.Fatalf("the first turn's calls didn't reach the client: %s", out)
			}
			// a new magpie: what was kept is on disk
			postOpaque(t, New(), "/v1/chat/completions", chatSecondTurn("copilot/gemini-4-hydrogen", "", stream))
			if len(sent) != 2 {
				t.Fatalf("Copilot was asked %d times", len(sent))
			}
			if got := opaqueOn(sent[1]); got != opaqueSeal {
				t.Fatalf("the tool turn went without its reasoning_opaque (%q):\n%s", got, sent[1])
			}
			if strings.Count(sent[1], opaqueSeal) != 1 {
				t.Errorf("the opaque went more than once:\n%s", sent[1])
			}
		})
	}
}

// What a client sends of its own goes as it is; a model other than the
// one that sealed it, or another account, gets none; nothing kept leaves
// the body byte for byte.
func TestCopilotOpaqueOnlyItsOwn(t *testing.T) {
	var sent []string
	copilotOpaqueUp(t, &sent)
	s := New()
	postOpaque(t, s, "/v1/chat/completions", `{"model":"copilot/gemini-4-hydrogen","stream":true,"tools":`+opaqueTools+`,"messages":[{"role":"user","content":"weather in Paris and Tokyo?"}]}`)

	postOpaque(t, s, "/v1/chat/completions", chatSecondTurn("copilot/gemini-4-hydrogen", `,"reasoning_opaque":"the client's own"`, true))
	if got := opaqueOn(sent[len(sent)-1]); got != "the client's own" {
		t.Errorf("the client's own opaque was replaced: %q", got)
	}
	postOpaque(t, s, "/v1/chat/completions", chatSecondTurn("copilot/gemini-4-carbon", "", true))
	if got := sent[len(sent)-1]; strings.Contains(got, "reasoning_opaque") {
		t.Errorf("another model was sent this one's opaque:\n%s", got)
	}
	if keptOpaque("someone else", "gemini-4-hydrogen", "call_MHxMT2VvRVZXajFQQUtHbGY5cGY") != "" ||
		keptOpaque("echo", "gemini-4-hydrogen", "call_MHxMT2VvRVZXajFQQUtHbGY5cGY") != opaqueSeal {
		t.Error("the opaque isn't kept for its own account alone")
	}
	before := len(sent)
	none := `{"model":"copilot/gemini-4-hydrogen","stream":true,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_other","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_other","content":"x"}]}`
	postOpaque(t, s, "/v1/chat/completions", none)
	if len(sent) != before+1 || strings.Contains(sent[before], "reasoning_opaque") {
		t.Errorf("a call with nothing kept got an opaque:\n%s", sent[before])
	}
}

// A client of another protocol (Claude Code on Messages, Codex on
// Responses) keeps the calls' ids, which magpie gave it from Copilot's,
// so its next turn's calls get their opaque back too.
func TestCopilotOpaqueBackThroughTranslation(t *testing.T) {
	t.Run("messages", func(t *testing.T) {
		var sent []string
		copilotOpaqueUp(t, &sent)
		out := postOpaque(t, New(), "/v1/messages", `{"model":"copilot/gemini-4-hydrogen","max_tokens":1000,"stream":true,"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[{"role":"user","content":"weather in Paris and Tokyo?"}]}`)
		var ids []string
		for _, line := range strings.Split(out, "\n") {
			if d, ok := strings.CutPrefix(line, "data: "); ok {
				if id := gjson.Get(d, "content_block.id").Str; gjson.Get(d, "content_block.type").Str == "tool_use" && id != "" {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) != 2 {
			t.Fatalf("calls told to the client: %v\n%s", ids, out)
		}
		second := `{"model":"copilot/gemini-4-hydrogen","max_tokens":1000,"stream":true,"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[
		  {"role":"user","content":"weather in Paris and Tokyo?"},
		  {"role":"assistant","content":[{"type":"tool_use","id":"` + ids[0] + `","name":"get_weather","input":{"city":"Paris"}},{"type":"tool_use","id":"` + ids[1] + `","name":"get_weather","input":{"city":"Tokyo"}}]},
		  {"role":"user","content":[{"type":"tool_result","tool_use_id":"` + ids[0] + `","content":"sunny"},{"type":"tool_result","tool_use_id":"` + ids[1] + `","content":"sunny"}]}]}`
		postOpaque(t, New(), "/v1/messages", second)
		if got := opaqueOn(sent[len(sent)-1]); got != opaqueSeal {
			t.Fatalf("the translated tool turn went without its opaque (%q):\n%s", got, sent[len(sent)-1])
		}
	})
	t.Run("responses", func(t *testing.T) {
		var sent []string
		copilotOpaqueUp(t, &sent)
		out := postOpaque(t, New(), "/v1/responses", `{"model":"copilot/gemini-4-hydrogen","stream":true,"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"input":"weather in Paris and Tokyo?"}`)
		var ids []string
		for _, line := range strings.Split(out, "\n") {
			if d, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(d, "type").Str == "response.output_item.done" {
				if id := gjson.Get(d, "item.call_id").Str; id != "" {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) != 2 {
			t.Fatalf("calls told to the client: %v\n%s", ids, out)
		}
		second := `{"model":"copilot/gemini-4-hydrogen","stream":true,"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"input":[
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"weather in Paris and Tokyo?"}]},
		  {"type":"function_call","call_id":"` + ids[0] + `","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},
		  {"type":"function_call","call_id":"` + ids[1] + `","name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"},
		  {"type":"function_call_output","call_id":"` + ids[0] + `","output":"sunny"},
		  {"type":"function_call_output","call_id":"` + ids[1] + `","output":"sunny"}]}`
		postOpaque(t, New(), "/v1/responses", second)
		if got := opaqueOn(sent[len(sent)-1]); got != opaqueSeal {
			t.Fatalf("the translated tool turn went without its opaque (%q):\n%s", got, sent[len(sent)-1])
		}
	})
}

// The store is a folder of files written whole: one left half-written by
// a write that died is a temp file, never read, and pruned.
func TestCopilotOpaqueStoreWritesWhole(t *testing.T) {
	fresh(t)
	keepOpaque("u", "m", "c1", "seal")
	if got := keptOpaque("u", "m", "c1"); got != "seal" {
		t.Fatalf("kept %q", got)
	}
	entries, _ := os.ReadDir(copilotOpaqueDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Errorf("a temp file was left: %s", e.Name())
		}
	}
	keepOpaque("u", "m", "c2", strings.Repeat("x", opaqueLargest+1))
	if keptOpaque("u", "m", "c2") != "" {
		t.Error("an opaque over the bound was kept")
	}
}
