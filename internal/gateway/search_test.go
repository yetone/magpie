package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A client's web search, offered to a model that can't search: the model
// is given magpie's web_search tool, the search is done by a provider that
// can, and the client gets one reply with what was found.
func TestWebSearchForAModelThatCannot(t *testing.T) {
	var mu sync.Mutex
	var searched []string
	var asked [][]map[string]any
	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"claude-opus-5-5"},{"id":"claude-haiku-4-5"}]}`)
		case "/v1/messages":
			var q struct {
				Model    string `json:"model"`
				Tools    []map[string]any
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			json.Unmarshal(b, &q)
			if q.Model != "claude-haiku-4-5" || len(q.Tools) != 1 || q.Tools[0]["type"] != "web_search_20250305" {
				http.Error(w, `{"error":{"message":"not a search: `+strings.ReplaceAll(string(b), `"`, `'`)+`"}}`, 400)
				return
			}
			mu.Lock()
			searched = append(searched, q.Messages[0].Content)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"s","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[
				{"type":"server_tool_use","id":"srv","name":"web_search","input":{"query":"go"}},
				{"type":"web_search_tool_result","tool_use_id":"srv","content":[{"type":"web_search_result","title":"Go downloads","url":"https://go.dev/dl/"}]},
				{"type":"text","text":"The latest Go is 1.27.1."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer search.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []map[string]any `json:"messages"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		asked = append(asked, q.Messages)
		n := len(asked)
		mu.Unlock()
		if len(q.Tools) != 2 || q.Tools[1].Function.Name != "web_search" {
			http.Error(w, `{"error":{"message":"no search tool"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			io.WriteString(w, sse(`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me check."}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"latest go\"}"}}]}}]}`,
				`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":30,"completion_tokens":7}}`,
				`data: [DONE]`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"d","choices":[{"index":0,"delta":{"role":"assistant","content":"Go 1.27.1 is out."}}]}`,
			`data: {"id":"d","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			`data: [DONE]`))
	}))
	defer model.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no signed-in agent searches first
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
	if sp, m, ok := searcher(); !ok || sp.ID != "srch" || m != "claude-haiku-4-5" {
		t.Fatalf("searcher = %s %s %v", sp.ID, m, ok)
	}

	// Claude Code's WebSearch, streamed
	body := `{"model":"deep/deep-chat","max_tokens":1000,"stream":true,"messages":[{"role":"user","content":"What is the latest Go?"}],
		"tools":[{"name":"Read","description":"read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	out := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, out)
	}
	var text strings.Builder
	var stop, query string
	var found []Hit
	for _, line := range strings.Split(out, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Text        string `json:"text"`
				StopReason  string `json:"stop_reason"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			ContentBlock struct {
				Type    string `json:"type"`
				Content []Hit  `json:"content"`
			} `json:"content_block"`
		}
		json.Unmarshal([]byte(data), &ev)
		if ev.ContentBlock.Type == "tool_use" {
			t.Fatalf("the search reached the client: %s", out)
		}
		// told as Anthropic's own search, which Claude Code counts
		if ev.ContentBlock.Type == "web_search_tool_result" {
			found = ev.ContentBlock.Content
		}
		query += ev.Delta.PartialJSON
		text.WriteString(ev.Delta.Text)
		if ev.Delta.StopReason != "" {
			stop = ev.Delta.StopReason
		}
	}
	if text.String() != "Let me check.\n\nGo 1.27.1 is out." || stop != "end_turn" {
		t.Fatalf("text %q stop %q\n%s", text.String(), stop, out)
	}
	if len(searched) != 1 || !strings.Contains(searched[0], "latest go") {
		t.Fatalf("searched %q", searched)
	}
	if query != `{"query":"latest go"}` || len(found) != 1 || found[0].URL != "https://go.dev/dl/" {
		t.Fatalf("search told as %q %+v\n%s", query, found, out)
	}
	if len(asked) != 2 {
		t.Fatalf("model asked %d times", len(asked))
	}
	last := asked[1][len(asked[1])-1]
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" || !strings.Contains(last["content"].(string), "1.27.1") || !strings.Contains(last["content"].(string), "https://go.dev/dl/") {
		t.Fatalf("tool result %v", last)
	}

	// a Chat client's web_search_options, not streamed
	asked, searched = nil, nil
	body = `{"model":"deep/deep-chat","messages":[{"role":"user","content":"What is the latest Go?"}],"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}],"web_search_options":{}}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	var res struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || len(res.Choices) != 1 || res.Choices[0].Message.Content != "Let me check.\n\nGo 1.27.1 is out." || len(res.Choices[0].Message.ToolCalls) != 0 || res.Choices[0].FinishReason != "stop" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if res.Usage.Prompt != 40 || res.Usage.Completion != 12 {
		t.Fatalf("usage %+v", res.Usage)
	}
}

func TestWebSearchAskedOfEachAPI(t *testing.T) {
	for _, c := range []struct {
		proto provider.Protocol
		body  string
		want  bool
	}{
		{provider.Responses, `{"model":"m","input":"hi","tools":[{"type":"web_search","external_web_access":false}]}`, true},
		{provider.Responses, `{"model":"m","input":"hi","tools":[{"type":"web_search_preview"}]}`, true},
		{provider.Responses, `{"model":"m","input":"hi","tools":[{"type":"function","name":"web_search_x"}]}`, false},
		{provider.Chat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"web_search_options":{}}`, true},
		{provider.Chat, `{"model":"m","messages":[{"role":"user","content":"web_search"}]}`, false},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}}]}`, true},
		{provider.Gemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"google_search":{}}]}`, true},
		{provider.Anthropic, `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, true},
	} {
		if got := searchAsked(c.proto, []byte(c.body)); got != c.want {
			t.Errorf("%s %s: %v", c.proto, c.body, got)
		}
	}
	r := &Request{Model: "m", Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}, WebSearch: true}
	if b := string(buildResponses(r, "m", "", false)); !strings.Contains(b, `"tools":[{"type":"web_search"}]`) {
		t.Errorf("responses: %s", b)
	}
	if b := string(buildChat(r, "m", "openrouter.ai", false)); !strings.Contains(b, `"plugins":[{"id":"web"}]`) {
		t.Errorf("openrouter: %s", b)
	}
	if b := string(buildChat(r, "m", "api.deepseek.com", false)); strings.Contains(b, "plugins") {
		t.Errorf("deepseek: %s", b)
	}
	if b := string(buildAnthropic(r, "m")); !strings.Contains(b, `"web_search_20250305"`) {
		t.Errorf("anthropic: %s", b)
	}
	codex := provider.Provider{ID: "codex", Responses: provider.CodexBase, Account: &provider.Account{Agent: "codex"}}
	if !searchesItself(codex, provider.Responses) || searchesItself(provider.Provider{Chat: "https://api.deepseek.com"}, provider.Chat) ||
		!searchesItself(provider.Provider{Chat: "https://openrouter.ai/api/v1"}, provider.Chat) {
		t.Error("searchesItself")
	}
}

// Anthropic's own search, streamed: its server tool's input is no call of
// the client's.
func TestAnthropicServerToolLeftOut(t *testing.T) {
	dec := decoder(provider.Anthropic)
	var got []string
	for _, d := range []string{
		`{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":3}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"s","name":"web_search"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"s","content":[]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Go 1.27.1"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
	} {
		dec(d, func(ev Event) {
			switch ev.Kind {
			case KToolStart, KToolArgs:
				got = append(got, "tool")
			case KText:
				got = append(got, ev.Text)
			}
		})
	}
	if strings.Join(got, "|") != "Go 1.27.1" {
		t.Fatalf("%q", got)
	}
}

// Claude Code with ENABLE_TOOL_SEARCH defers WebSearch and loads it by a
// ToolSearch whose result is a tool_reference: the model elsewhere is told
// the tool is there, and the placeholder that keeps deferring on is dropped.
func TestToolSearchReferenceIsTold(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"tools":[
		{"name":"ToolSearch","input_schema":{"type":"object"}},
		{"name":"WebSearch","input_schema":{"type":"object"},"defer_loading":true},
		{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"defer_loading":true}],
		"messages":[{"role":"user","content":"search"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ToolSearch","input":{"query":"select:WebSearch"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"WebSearch"}]}]}]}`
	r, err := parseAnthropic([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "ToolSearch,WebSearch" {
		t.Fatalf("tools = %v", names)
	}
	if got := r.Messages[2].Parts[0].Text; !strings.Contains(got, "WebSearch is loaded") {
		t.Fatalf("tool result = %q", got)
	}
}

func TestSearchToldAsAnthropics(t *testing.T) {
	res := Result{Parts: []Part{{Kind: Search, Text: "go", Hits: []Hit{{Title: "Go", URL: "https://go.dev/"}}}, {Kind: Text, Text: "1.27"}}}
	var out struct {
		Content []struct {
			Type      string         `json:"type"`
			ID        string         `json:"id"`
			ToolUseID string         `json:"tool_use_id"`
			Input     map[string]any `json:"input"`
			Content   []Hit          `json:"content"`
		} `json:"content"`
	}
	json.Unmarshal(renderAnthropic(res, "m"), &out)
	c := out.Content
	if len(c) != 3 || c[0].Type != "server_tool_use" || c[0].Input["query"] != "go" || c[1].Type != "web_search_tool_result" ||
		c[1].ToolUseID != c[0].ID || len(c[1].Content) != 1 || c[1].Content[0].URL != "https://go.dev/" || c[2].Type != "text" {
		t.Fatalf("%+v", out)
	}

	// a Responses provider's own search, with the pages it found
	var events []Event
	d := &responsesDecoder{}
	d.decode(`{"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"bun","sources":[{"type":"url","url":"https://bun.com/"}]}}}`,
		func(ev Event) { events = append(events, ev) })
	if len(events) != 1 || events[0].Kind != KSearch || events[0].Text != "bun" || len(events[0].Hits) != 1 || events[0].Hits[0].URL != "https://bun.com/" {
		t.Fatalf("%+v", events)
	}
}

// Grok moved to its plugin searches by itself as the built-in did: it is
// known by its id, its account being the plugin's.
func TestMovedGrokSearchesItself(t *testing.T) {
	for _, agent := range []string{"grok", "plugin"} {
		p := provider.Provider{ID: "grok", Responses: "plugin://grok/v1", Account: &provider.Account{Agent: agent}}
		if !searchesItself(p, provider.Responses) {
			t.Errorf("Grok as %q's account doesn't search by itself", agent)
		}
		if searchesItself(p, provider.Chat) {
			t.Errorf("Grok as %q's account searches by itself on chat", agent)
		}
	}
	if searchesItself(provider.Provider{ID: "fakeco", Responses: "plugin://fakeco/v1", Account: &provider.Account{Agent: "plugin"}}, provider.Responses) {
		t.Error("another plugin's provider searches by itself")
	}
}

// A relay in front of Anthropic's API searches as Anthropic does, which
// magpie can't tell from its host (#359). One that serves only Claude Code
// refuses a request without Claude Code's metadata.user_id. Set as
// searching by itself, it is sent Claude Code's WebSearch as it was sent;
// not set, the request built again for it keeps the metadata.
func TestWebSearchOfARelayThatSearches(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]json.RawMessage
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5"}]}`)
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var q map[string]json.RawMessage
		json.Unmarshal(b, &q)
		mu.Lock()
		got = append(got, q)
		mu.Unlock()
		var md struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(q["metadata"], &md); md.UserID == "" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"This group is restricted to the official Claude Code client."}}`)
			return
		}
		if string(q["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":5}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Go 1.27.1 is out."}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"Go 1.27.1 is out."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`)
	}))
	defer relay.Close()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no signed-in agent searches for it
	// Claude Code 2.1.285's WebSearch
	body := `{"model":"relay/claude-haiku-4-5","max_tokens":32000,"stream":false,
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.285.3c7; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"You are an assistant for performing a web search tool use"}],
		"messages":[{"role":"user","content":"Perform a web search for the query: latest go"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}],
		"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"s\"}"}}`
	ask := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("User-Agent", "claude-cli/2.1.285 (external, cli)")
		req.Header.Set("x-app", "cli")
		New().Handler().ServeHTTP(rec, req)
		return rec
	}

	p := provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: relay.URL, Models: []string{"claude-haiku-4-5"}, Searches: true}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	// it searches for Claude Code, not for magpie's other models: it would
	// spend the relay's quota, and refuse magpie's request without metadata
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sp, _, ok := searcher(); ok {
		t.Fatalf("magpie searches with %s", sp.ID)
	}
	if rec := ask(); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Go 1.27.1 is out.") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var tools []map[string]any
	var system []any
	json.Unmarshal(got[0]["tools"], &tools)
	json.Unmarshal(got[0]["system"], &system)
	if len(tools) != 1 || tools[0]["type"] != "web_search_20250305" || tools[0]["max_uses"] != float64(8) || len(system) != 3 {
		t.Fatalf("sent %s %s", got[0]["tools"], got[0]["system"])
	}

	// not said to search, it is given no search of its own: magpie has no
	// searcher here, so the request is built again, without the tool
	got = nil
	p.Searches = false
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if rec := ask(); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Go 1.27.1 is out.") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(string(got[0]["metadata"]), `device_id`) || strings.Contains(string(got[0]["tools"]), "web_search") {
		t.Fatalf("sent %s %s", got[0]["metadata"], got[0]["tools"])
	}

	// said to search with only a Chat address, it has no API to search on
	chat := provider.Provider{ID: "chatrelay", Chat: relay.URL + "/v1", Searches: true}
	for _, proto := range []provider.Protocol{provider.Anthropic, provider.Responses, provider.Chat} {
		if searchesItself(chat, proto) {
			t.Errorf("a Chat-only relay searches by itself on %s", proto)
		}
	}
}
