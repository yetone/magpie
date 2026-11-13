package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// advisedUpstream is a Chat Completions upstream serving two models: the
// executor, which calls advisor() once it is offered and answers once the
// advice is back, and the advisor model (adv-pro), which answers with
// advice, or fails with fail when that is set.
type advisedUpstream struct {
	mu      sync.Mutex
	exec    []string // the executor's requests
	advisor []string // the advisor model's
	fail    int      // the advisor model's status, 0 = answers
}

func (u *advisedUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	q := gjson.ParseBytes(b)
	u.mu.Lock()
	defer u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if q.Get("model").String() == "adv-pro" {
		u.advisor = append(u.advisor, string(b))
		if u.fail != 0 {
			w.WriteHeader(u.fail)
			io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, chatSSE("Check the tests first.", "", ""))
		return
	}
	u.exec = append(u.exec, string(b))
	msgs := q.Get("messages").Array()
	offered := false
	for _, t := range q.Get("tools").Array() {
		offered = offered || t.Get("function.name").String() == advisorTool
	}
	switch {
	case len(msgs) > 0 && msgs[len(msgs)-1].Get("role").String() == "tool":
		io.WriteString(w, chatSSE("Done.", "", ""))
	case offered:
		io.WriteString(w, chatSSE("Let me ask.", advisorTool, "{}"))
	default:
		io.WriteString(w, chatSSE("ok", "", ""))
	}
}

// advisedSetup saves an executor provider (v, glm-5.1) and an advisor's
// (adv, adv-pro) at one advisedUpstream.
func advisedSetup(t *testing.T) *advisedUpstream {
	t.Helper()
	fresh(t)
	u := &advisedUpstream{}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	for _, p := range []provider.Provider{
		{ID: "v", Name: "V", Key: "k", Chat: srv.URL + "/v1", Models: []string{"glm-5.1"}},
		{ID: "adv", Name: "Adv", Key: "k", Chat: srv.URL + "/v1", Models: []string{"adv-pro"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

// sendAdvised is Claude Code's advised turn, body, streamed or not, and
// the reply's content blocks.
func sendAdvised(t *testing.T, s *Server, body string, stream bool) []map[string]any {
	t.Helper()
	if !stream {
		body = strings.Replace(body, `"stream":true`, `"stream":false`, 1)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
	req.Header.Set("Authorization", "Bearer "+Token)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !stream {
		var out struct {
			Content []map[string]any `json:"content"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
		return out.Content
	}
	// the blocks as a client puts them together from the stream
	var blocks []map[string]any
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		ev := gjson.Parse(data)
		switch ev.Get("type").String() {
		case "content_block_start":
			var b map[string]any
			json.Unmarshal([]byte(ev.Get("content_block").Raw), &b)
			blocks = append(blocks, b)
		case "content_block_delta":
			if d := ev.Get("delta"); d.Get("type").String() == "text_delta" && len(blocks) > 0 {
				blocks[len(blocks)-1]["text"] = blocks[len(blocks)-1]["text"].(string) + d.Get("text").String()
			}
		}
	}
	return blocks
}

func blockTypes(blocks []map[string]any) string {
	var out []string
	for _, b := range blocks {
		out = append(out, b["type"].(string))
	}
	return strings.Join(out, ",")
}

// Claude Code's advisor on a model whose upstream has none (Awad_Yoo on
// X): magpie offers the model an advisor tool, asks the advisor model when
// it is called, and tells Claude Code of it as Anthropic's server does, a
// server_tool_use and an advisor_tool_result with the advice, before the
// model's answer. Before, the advisor was left out and Claude Code's
// instructions for it said to be void.
func TestAdvisorServedForAVendorModel(t *testing.T) {
	for _, stream := range []bool{true, false} {
		u := advisedSetup(t)
		s := New()
		blocks := sendAdvised(t, s, advisorTurn("v/glm-5.1", "adv/adv-pro"), stream)
		if got := blockTypes(blocks); got != "text,server_tool_use,advisor_tool_result,text" {
			t.Fatalf("stream=%t: blocks %s: %v", stream, got, blocks)
		}
		call, result := blocks[1], blocks[2]
		id, _ := call["id"].(string)
		if call["name"] != advisorTool || !strings.HasPrefix(id, "srvtoolu_") || result["tool_use_id"] != id {
			t.Fatalf("stream=%t: call %v, result %v", stream, call, result)
		}
		content, _ := result["content"].(map[string]any)
		if content["type"] != "advisor_result" || content["text"] != "Check the tests first." {
			t.Fatalf("stream=%t: advice %v", stream, content)
		}
		// what it says after goes on from what it said before, as after a
		// search
		if after, _ := blocks[3]["text"].(string); strings.TrimSpace(after) != "Done." {
			t.Fatalf("stream=%t: answer %v", stream, blocks[3])
		}
		u.mu.Lock()
		exec, adv := u.exec, u.advisor
		u.mu.Unlock()
		if len(exec) != 2 || len(adv) != 1 {
			t.Fatalf("stream=%t: %d executor requests, %d advisor's", stream, len(exec), len(adv))
		}
		// the executor is offered the tool, Claude Code's instructions for
		// it as written, none said to be void
		if !strings.Contains(exec[0], `"name":"advisor"`) || !strings.Contains(exec[0], "You have access to an `advisor` tool") || strings.Contains(exec[0], advisorGone) {
			t.Fatalf("stream=%t: executor sent %s", stream, exec[0])
		}
		if strings.Contains(exec[0], "advisor_2026") {
			t.Fatalf("stream=%t: the server tool went upstream: %s", stream, exec[0])
		}
		// the advisor is asked the conversation, with no tool to call
		a := gjson.Parse(adv[0])
		if !strings.Contains(adv[0], `"hi"`) || !strings.Contains(adv[0], "Let me ask.") || a.Get("tool_choice").String() != "none" {
			t.Fatalf("stream=%t: advisor asked %s", stream, adv[0])
		}
		for _, tl := range a.Get("tools").Array() {
			if tl.Get("function.name").String() == advisorTool {
				t.Fatalf("stream=%t: the advisor was offered itself: %s", stream, adv[0])
			}
		}
		// the advice reaches the executor as the call's result
		if !strings.Contains(exec[1], "Check the tests first.") {
			t.Fatalf("stream=%t: the advice never reached the model: %s", stream, exec[1])
		}
	}
}

// The next turn has the advisor's blocks in the assistant's message, as
// Claude Code echoes them back: they go to the model as its call and the
// call's result, the assistant's message split around them.
func TestAdvisorHistoryGoesBackAsACall(t *testing.T) {
	u := advisedSetup(t)
	body := advisorTurn("v/glm-5.1", "adv/adv-pro")
	turn := `{"role":"assistant","content":[{"type":"text","text":"Let me ask."},` +
		`{"type":"server_tool_use","id":"srvtoolu_abc","name":"advisor","input":{}},` +
		`{"type":"advisor_tool_result","tool_use_id":"srvtoolu_abc","content":{"type":"advisor_result","text":"Check the tests first."}},` +
		`{"type":"text","text":"Done."}]},{"role":"user","content":[{"type":"text","text":"thanks"}]}`
	body = strings.Replace(body, `"output_config":{"effort":"medium"}}]`, `"output_config":{"effort":"medium"}},`+turn+`]`, 1)
	if !strings.Contains(body, "srvtoolu_abc") {
		t.Fatal("fixture not changed")
	}
	sendAdvised(t, New(), body, true)
	u.mu.Lock()
	sent := u.exec[0]
	u.mu.Unlock()
	var shape []string
	for _, m := range gjson.Get(sent, "messages").Array() {
		s := m.Get("role").String()
		if m.Get("tool_calls.0.function.name").String() == advisorTool {
			s += "+advisor()"
		}
		if m.Get("role").String() == "tool" && !strings.Contains(m.Get("content").Raw, "Check the tests first.") {
			t.Fatalf("result lost: %s", sent)
		}
		shape = append(shape, s)
	}
	got := strings.Join(shape, ",")
	if !strings.Contains(got, "assistant+advisor(),tool,assistant,user") {
		t.Fatalf("messages %s: %s", got, sent)
	}
	call := gjson.Get(sent, `messages.#(role=="assistant").tool_calls.0.id`).String()
	res := gjson.Get(sent, `messages.#(role=="tool").tool_call_id`).String()
	if call == "" || call != res {
		t.Fatalf("call %q, result %q: %s", call, res, sent)
	}
}

// An advisor that fails is told as Anthropic's server tells it, an
// advisor_tool_result_error with its code, and the model goes on.
func TestAdvisorFailureIsAnErrorCode(t *testing.T) {
	u := advisedSetup(t)
	u.fail = 429
	blocks := sendAdvised(t, New(), advisorTurn("v/glm-5.1", "adv/adv-pro"), false)
	if got := blockTypes(blocks); got != "text,server_tool_use,advisor_tool_result,text" {
		t.Fatalf("blocks %s: %v", got, blocks)
	}
	content, _ := blocks[2]["content"].(map[string]any)
	if content["type"] != "advisor_tool_result_error" || content["error_code"] == "" {
		t.Fatalf("result %v", blocks[2])
	}
}

// Who runs the advisor: Anthropic's server for a Claude model with a Claude
// advisor of the same provider, magpie for a model magpie can ask the
// advisor beside, nobody for an advisor model magpie doesn't serve or a
// tool of the client's own by the name.
func TestAdvisedHere(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}},
		{ID: "v", Name: "V", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-5.1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	rp, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	vp, err := provider.Find("v")
	if err != nil {
		t.Fatal(err)
	}
	relay, v := *rp, *vp
	for _, c := range []struct {
		name  string
		p     provider.Provider
		to    provider.Protocol
		model string
		body  string
		want  bool
	}{
		{"native", relay, provider.Anthropic, "claude-sonnet-5-5", advisorTurn("relay/claude-sonnet-5-5", "relay/claude-opus-5-5"), false},
		{"vendor model", v, provider.Chat, "glm-5.1", advisorTurn("v/glm-5.1", "relay/claude-opus-5-5"), true},
		{"another provider's advisor", relay, provider.Anthropic, "claude-sonnet-5-5", advisorTurn("relay/claude-sonnet-5-5", "v/glm-5.1"), true},
		{"unknown advisor", v, provider.Chat, "glm-5.1", advisorTurn("v/glm-5.1", "nobody/x"), false},
		{"no advisor", v, provider.Chat, "glm-5.1", claudeCodeTurn("v/glm-5.1", "x"), false},
		{"client's own advisor", v, provider.Chat, "glm-5.1", strings.Replace(advisorTurn("v/glm-5.1", "relay/claude-opus-5-5"), `{"name":"Read"`, `{"name":"advisor","input_schema":{"type":"object"}},{"name":"Read"`, 1), false},
	} {
		if got := advisedHere(c.p, provider.Anthropic, c.to, c.model, []byte(c.body), nil); got != c.want {
			t.Errorf("%s: advisedHere %t, want %t", c.name, got, c.want)
		}
	}
}
