package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// aiStudioGrounded is a Gemini API stream that searched with googleSearch:
// Google's grounding example's shape (ai.google.dev/gemini-api/docs/
// google-search), webSearchQueries, groundingChunks and groundingSupports
// with the answer.
var aiStudioGrounded = sse(
	`data: {"candidates":[{"content":{"parts":[{"text":"Spain won Euro 2024, defeating England 2-1 in the final."}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-flash"}`,
	`data: {"candidates":[{"content":{"parts":[{"text":" This victory marks Spain's record fourth European Championship title."}],"role":"model"},"finishReason":"STOP","index":0,"groundingMetadata":{"webSearchQueries":["UEFA Euro 2024 winner","who won euro 2024"],"searchEntryPoint":{"renderedContent":"<!-- HTML and CSS for the search widget -->"},"groundingChunks":[{"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/aljazeera","title":"aljazeera.com"}},{"web":{"uri":"https://vertexaisearch.cloud.google.com/grounding-api-redirect/uefa","title":"uefa.com"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":56,"text":"Spain won Euro 2024, defeating England 2-1 in the final."},"groundingChunkIndices":[0]}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":30,"totalTokenCount":42},"modelVersion":"gemini-2.5-flash"}`,
)

type aiStudioCall struct {
	url, key, auth string
	body           []byte
}

// aiStudio saves the Google Gemini preset with an AI Studio key and gives
// a server whose client answers for generativelanguage.googleapis.com:
// the Gemini API with aiStudioGrounded, OpenAI-compatible Chat with chat's
// stream (by the request's body and how many Chat requests came before), and
// anything else with a 404 the test fails on.
func aiStudio(t *testing.T, chat func(n int, body []byte) string) (*Server, func() []aiStudioCall) {
	t.Helper()
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "google", Name: "Google Gemini", Preset: "google",
		Chat: provider.AIStudioChat, Key: "AIza-k", Models: []string{"gemini-2.5-flash"}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []aiStudioCall
	chats := 0
	s := New()
	s.client = &http.Client{Transport: countTransport(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		calls = append(calls, aiStudioCall{r.URL.String(), r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), b})
		n := chats
		mu.Unlock()
		reply := func(ct, body string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		switch {
		case r.URL.Host != "generativelanguage.googleapis.com":
		case strings.HasPrefix(r.URL.Path, "/v1beta/models/") && strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			return reply("text/event-stream", aiStudioGrounded)
		case r.URL.Path == "/v1beta/openai/chat/completions" && chat != nil:
			mu.Lock()
			chats++
			mu.Unlock()
			return reply("text/event-stream", chat(n, b))
		}
		t.Errorf("asked %s %s", r.Method, r.URL)
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
	})}
	return s, func() []aiStudioCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]aiStudioCall(nil), calls...)
	}
}

func geminiCalls(calls []aiStudioCall) []aiStudioCall {
	var out []aiStudioCall
	for _, c := range calls {
		if strings.Contains(c.url, "/v1beta/models/") {
			out = append(out, c)
		}
	}
	return out
}

func checkGoogleSearch(t *testing.T, what string, c aiStudioCall) {
	t.Helper()
	if c.url != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" {
		t.Errorf("%s: asked %s", what, c.url)
	}
	if c.key != "AIza-k" || c.auth != "" {
		t.Errorf("%s: signed key=%q auth=%q", what, c.key, c.auth)
	}
	if tools := gjson.GetBytes(c.body, "tools").Raw; tools != `[{"googleSearch":{}}]` {
		t.Errorf("%s: tools %s", what, tools)
	}
}

// A web search on an AI Studio key, with no tools of the client's own
// (Claude Code's WebSearch), is Gemini's googleSearch on the user's own key
// and credit, at AI Studio's Gemini API, and what it found comes back as
// the client's search result (cocoasoda on Discord).
func TestAIStudioKeySearchesByGoogleSearch(t *testing.T) {
	s, calls := aiStudio(t, nil)
	code, _, raw, _ := request(t, s, "POST", "/v1/messages", "application/json",
		`{"model":"google/gemini-2.5-flash","max_tokens":1024,"messages":[{"role":"user","content":"Perform a web search for the query: who won euro 2024"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`)
	body := string(raw)
	if code != 200 || !strings.Contains(body, "Spain won Euro 2024") {
		t.Fatalf("messages: %d %s", code, body)
	}
	if !strings.Contains(body, `"web_search_tool_result"`) || !strings.Contains(body, "aljazeera.com") || !strings.Contains(body, "grounding-api-redirect/uefa") {
		t.Errorf("the search isn't in the reply: %s", body)
	}
	gem := geminiCalls(calls())
	if len(gem) != 1 || len(calls()) != 1 {
		t.Fatalf("calls %d, Gemini %d", len(calls()), len(gem))
	}
	checkGoogleSearch(t, "search", gem[0])
	if !strings.Contains(gjson.GetBytes(gem[0].body, "contents").Raw, "who won euro 2024") {
		t.Errorf("contents %s", gem[0].body)
	}
}

// A turn with the client's own tools (Codex, Claude Code) stays on Chat,
// where Gemini can't have googleSearch beside function tools; its model is
// given a search tool, which the same key answers by a googleSearch
// request of its own, never another provider and never a silent drop of
// the search.
func TestAIStudioKeySearchesBesideTools(t *testing.T) {
	var asked []string
	s, calls := aiStudio(t, func(n int, body []byte) string {
		var names []string
		for _, tl := range gjson.GetBytes(body, "tools").Array() {
			names = append(names, tl.Get("function.name").String())
		}
		asked = append(asked, strings.Join(names, ","))
		if n == 0 {
			return sse(
				`data: {"id":"c1","object":"chat.completion.chunk","model":"gemini-2.5-flash","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"who won euro 2024\"}"}}]},"finish_reason":null}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","model":"gemini-2.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}}`,
				`data: [DONE]`)
		}
		if !strings.Contains(string(body), "Spain won Euro 2024") {
			t.Errorf("the search's result isn't in the next round: %s", body)
		}
		return sse(
			`data: {"id":"c2","object":"chat.completion.chunk","model":"gemini-2.5-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"Spain won."},"finish_reason":null}]}`,
			`data: {"id":"c2","object":"chat.completion.chunk","model":"gemini-2.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":60,"completion_tokens":3,"total_tokens":63}}`,
			`data: [DONE]`)
	})
	code, _, raw, _ := request(t, s, "POST", "/v1/messages", "application/json",
		`{"model":"google/gemini-2.5-flash","max_tokens":1024,"messages":[{"role":"user","content":"Who won Euro 2024? Search the web."}],"tools":[{"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"type":"web_search_20250305","name":"web_search","max_uses":8}]}`)
	if code != 200 || !strings.Contains(string(raw), "Spain won.") {
		t.Fatalf("messages: %d %s", code, raw)
	}
	if len(asked) < 2 || !strings.Contains(asked[0], "Bash") || !strings.Contains(asked[0], "web_search") {
		t.Errorf("Chat was asked with tools %q", asked)
	}
	gem := geminiCalls(calls())
	if len(gem) != 1 {
		t.Fatalf("Gemini asked %d times: %+v", len(gem), calls())
	}
	checkGoogleSearch(t, "own search", gem[0])
	if strings.Contains(string(gem[0].body), "functionDeclarations") || !strings.Contains(string(gem[0].body), "who won euro 2024") {
		t.Errorf("own search body %s", gem[0].body)
	}
}

// A Gemini client's own request (Gemini CLI) with googleSearch goes to AI
// Studio's Gemini API as it is, tools and all.
func TestAIStudioKeyRelaysGeminiClient(t *testing.T) {
	s, calls := aiStudio(t, nil)
	code, _, raw, _ := request(t, s, "POST", "/v1beta/models/google/gemini-2.5-flash:streamGenerateContent?alt=sse", "application/json",
		`{"contents":[{"role":"user","parts":[{"text":"Who won the euro 2024?"}]}],"tools":[{"googleSearch":{}}]}`)
	if code != 200 || !strings.Contains(string(raw), "Spain won Euro 2024") || !strings.Contains(string(raw), "groundingMetadata") {
		t.Fatalf("gemini stream: %d %s", code, raw)
	}
	gem := geminiCalls(calls())
	if len(gem) != 1 || len(calls()) != 1 {
		t.Fatalf("calls %+v", calls())
	}
	checkGoogleSearch(t, "relay", gem[0])
}

// Only an AI Studio key's own Gemini API searches this way: a Google
// sign-in, Vertex, a relay's Gemini URL and an image model keep what they
// did.
func TestGeminiKeySearchesScope(t *testing.T) {
	plain := &Request{WebSearch: true, Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "news?"}}}}}
	tools := &Request{WebSearch: true, Messages: plain.Messages, Tools: []Tool{{Name: "bash"}}}
	none := &Request{WebSearch: true, Messages: plain.Messages, Tools: tools.Tools, ToolChoice: "none"}
	studio := provider.Provider{ID: "google", Chat: provider.AIStudioChat, Gemini: provider.AIStudioGemini, Key: "k"}
	if !geminiKeySearches(studio) {
		t.Fatal("an AI Studio key doesn't search by its Gemini API")
	}
	if !searchesFor(studio, provider.Gemini, "gemini-2.5-flash", plain) || !searchesFor(studio, provider.Gemini, "gemini-2.5-flash", none) {
		t.Error("an AI Studio key without tools doesn't search by googleSearch")
	}
	if searchesFor(studio, provider.Gemini, "gemini-2.5-flash", tools) {
		t.Error("an AI Studio key with function tools is said to search by googleSearch, which would be dropped")
	}
	if searchesFor(studio, provider.Gemini, "gemini-2.5-flash-image", plain) || searchesFor(studio, provider.Chat, "gemini-2.5-flash", plain) {
		t.Error("an image model, or AI Studio's Chat, is said to search by itself")
	}
	relay := provider.Provider{ID: "relay", Gemini: "https://relay.example/v1beta", Key: "k"}
	if geminiKeySearches(relay) {
		t.Error("a relay's Gemini URL searches by googleSearch")
	}
	signin := provider.Provider{ID: "antigravity", Account: &provider.Account{Agent: "antigravity", User: "u"}, Gemini: provider.AIStudioGemini}
	if geminiKeySearches(signin) {
		t.Error("a Google sign-in is taken for an AI Studio key")
	}
}

// An AI Studio key that searches by googleSearch is a searcher like a
// Google sign-in (cocoasoda on Discord: 代搜索供应商 had no Gemini): Settings
// lists it, its own models first (Own), and magpie picks it by itself to
// search for another provider's model, which can't, on the user's key.
func TestAIStudioKeySearchesForOthers(t *testing.T) {
	s, calls := aiStudio(t, nil)
	other := provider.Provider{ID: "plain", Name: "Plain", Chat: "https://plain.example/v1", Key: "k", Models: []string{"plain-1"}}
	if err := provider.Save(other); err != nil {
		t.Fatal(err)
	}
	google, err := provider.Find("google")
	if err != nil {
		t.Fatal(err)
	}
	if r, manual := namedSearcherRank(*google); r != 5 || manual {
		t.Fatalf("rank %d, by hand only %v", r, manual)
	}
	var found *SearcherChoice
	for _, c := range Searchers() {
		if c.Provider.ID == "google" {
			found = &c
		}
		if c.Provider.ID == "plain" {
			t.Error("a provider that can't search listed")
		}
	}
	if found == nil || !found.Own || found.Small != "gemini-2.5-flash" || found.ManualOnly {
		t.Fatalf("Searchers: %+v", found)
	}
	if p, m, ok := autoSearcher(); !ok || p.ID != "google" || m != "gemini-2.5-flash" {
		t.Fatalf("autoSearcher: %q %q %v", p.ID, m, ok)
	}
	var said string
	var hits []Hit
	said, hits, err = s.webSearch(searchingOn(t.Context(), other), "who won euro 2024")
	if err != nil || !strings.Contains(said, "Spain won Euro 2024") || len(hits) == 0 {
		t.Fatalf("search for plain-1: %q %v %v", said, hits, err)
	}
	gem := geminiCalls(calls())
	if len(gem) != 1 || len(calls()) != 1 {
		t.Fatalf("calls %d, Gemini %d", len(calls()), len(gem))
	}
	checkGoogleSearch(t, "search for another", gem[0])
}
