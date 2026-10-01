package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A call Codex makes on a hidden thread of its own — the title of a new
// chat — comes without x-openai-subagent; its turn metadata names the
// thread's source (#314). magpie's own web search says so too.
func TestCallKindThreadSource(t *testing.T) {
	for want, h := range map[string]map[string]string{
		"thread_title":    {"x-codex-turn-metadata": `{"session_id":"s","thread_source":"thread_title","turn_id":"t"}`},
		"guardian_review": {"x-codex-turn-metadata": `{"thread_source":"guardian_review"}`},
		"guardian":        {"x-openai-subagent": "guardian", "x-codex-turn-metadata": `{"thread_source":"guardian_review"}`},
		"web_search":      {"User-Agent": SearchAgent},
		"":                {"x-codex-turn-metadata": `{"thread_source":"user","turn_id":"t"}`, "User-Agent": "codex_cli_rs/0.159.2"},
	} {
		hh := http.Header{}
		for k, v := range h {
			hh.Set(k, v)
		}
		if got := callKind(hh); got != want {
			t.Errorf("%v: %q, want %q", h, got, want)
		}
	}
	for _, meta := range []string{"", "{", `{"thread_source":"subagent"}`, `{"turn_id":"t"}`} {
		if got := threadSource(meta); got != "" {
			t.Errorf("threadSource(%q) = %q", meta, got)
		}
	}
}

// The searches magpie runs for a model that can't search are its own
// calls, on the model it searches with: the Routing view is told they are
// web searches, and for which agent's model (a DeepSeek chat in Codex
// showed Codex's GPT answering, #314).
func TestWebSearchCallSaysWhose(t *testing.T) {
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5"}]}`)
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"s","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[
				{"type":"text","text":"Sunny in Changsha."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer search.Close()
	n := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		n++
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"changsha weather\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`, `data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Sunny."}}]}`,
			`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	defer model.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	setHome(t, t.TempDir())
	hosts := searchHosts[provider.Anthropic]
	searchHosts[provider.Anthropic] = append(hosts, provider.HostOf(search.URL))
	defer func() { searchHosts[provider.Anthropic] = hosts }()
	for _, p := range []provider.Provider{
		{ID: "srch", Name: "Search", Key: "k", Anthropic: search.URL},
		{ID: "deep", Name: "Deep", Key: "k", Chat: model.URL, Models: []string{"deep-chat"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := provider.Find("srch")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	s := New()
	body := `{"model":"deep/deep-chat","messages":[{"role":"user","content":"长沙最近天气"}],"web_search_options":{}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("User-Agent", "codex_cli_rs/0.159.2")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Sunny.") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var turn, searched *Route
	for _, r := range s.Trace(context.Background(), 0, 0).Routes {
		switch r.Model {
		case "deep/deep-chat":
			turn = &r
		case "srch/claude-haiku-4-5":
			searched = &r
		}
	}
	if turn == nil || searched == nil {
		t.Fatalf("routes: turn %v, search %v", turn, searched)
	}
	if turn.Kind != "" || turn.For != nil {
		t.Errorf("the turn is the agent's own: kind %q for %+v", turn.Kind, turn.For)
	}
	if searched.Kind != "web_search" || searched.For == nil || searched.For.Model != "deep/deep-chat" || searched.For.Agent != agentOf(req) {
		t.Errorf("the search: kind %q for %+v, want web_search for %s's deep/deep-chat", searched.Kind, searched.For, agentOf(req))
	}
}
