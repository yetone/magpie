package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A Gemini client's own Google tools — googleSearch, urlContext,
// codeExecution — reach Gemini as it sent them, and what googleSearch
// found comes back to it as the candidate's groundingMetadata
// (daoche_wuzui on X: magpie's Gemini dropped URL context and Google
// Search).
func TestGeminiClientKeepsGoogleTools(t *testing.T) {
	s, calls := aiStudio(t, nil)
	code, _, raw, _ := request(t, s, "POST", "/v1beta/models/google/gemini-2.5-flash:generateContent", "application/json",
		`{"contents":[{"role":"user","parts":[{"text":"who won euro 2024? see https://www.uefa.com/euro2024/"}]}],"tools":[{"googleSearch":{}},{"url_context":{}},{"codeExecution":{}}]}`)
	if code != 200 {
		t.Fatalf("generateContent: %d %s", code, raw)
	}
	gem := geminiCalls(calls())
	if len(gem) != 1 {
		t.Fatalf("Gemini calls %d of %d", len(gem), len(calls()))
	}
	if tools := gjson.GetBytes(gem[0].body, "tools").Raw; tools != `[{"googleSearch":{}},{"urlContext":{}},{"codeExecution":{}}]` {
		t.Errorf("tools %s", tools)
	}
	g := gjson.GetBytes(raw, "candidates.0.groundingMetadata")
	if !strings.Contains(g.Get("webSearchQueries").Raw, "who won euro 2024") || g.Get("groundingChunks.#").Int() != 2 ||
		g.Get("groundingChunks.0.web.title").String() != "aljazeera.com" {
		t.Errorf("groundingMetadata %s in %s", g.Raw, raw)
	}
	if !strings.Contains(string(raw), "Spain won Euro 2024") {
		t.Errorf("reply %s", raw)
	}
}

// The same tools in a Chat request, as Gemini's OpenAI-shaped clients send
// them, by name or by type: googleSearch is the web search, the others are
// kept for Gemini, and none goes on as a function with no name.
func TestChatGoogleTools(t *testing.T) {
	r, err := readChat([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[
		{"googleSearch":{}},{"type":"url_context"},{"code_execution":{}},{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.WebSearch {
		t.Error("googleSearch isn't a web search")
	}
	got, _ := json.Marshal(r.GoogleTools)
	if string(got) != `[{"urlContext":{}},{"codeExecution":{}}]` {
		t.Errorf("GoogleTools %s", got)
	}
	var names []string
	for _, tl := range r.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "bash" {
		t.Errorf("tools %q", names)
	}
}

// Code Assist (Gemini CLI, Antigravity) sends a Gemini model the client's
// Google tools with googleSearch, never a Claude model on the same
// account, and never beside function declarations, which Google refuses.
func TestCodeAssistSendsGoogleTools(t *testing.T) {
	r := &Request{WebSearch: true, GoogleTools: []json.RawMessage{json.RawMessage(`{"urlContext":{}}`), json.RawMessage(`{"codeExecution":{}}`)},
		Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}}
	tools := func(model string, r *Request) string {
		return gjson.GetBytes(buildCodeAssist(r, model, "antigravity"), "request.tools").Raw
	}
	if got := tools("gemini-3.8-flash", r); got != `[{"googleSearch":{}},{"urlContext":{}},{"codeExecution":{}}]` {
		t.Errorf("gemini: %s", got)
	}
	noSearch := *r
	noSearch.WebSearch = false
	if got := tools("gemini-3.8-flash", &noSearch); got != `[{"urlContext":{}},{"codeExecution":{}}]` {
		t.Errorf("gemini without search: %s", got)
	}
	if got := tools("claude-sonnet-4-6", &noSearch); got != "" {
		t.Errorf("claude: %s", got)
	}
	fns := *r
	fns.Tools = []Tool{{Name: "bash"}}
	if got := tools("gemini-3.8-flash", &fns); strings.Contains(got, "urlContext") || !strings.Contains(got, "functionDeclarations") {
		t.Errorf("beside functions: %s", got)
	}
}

// A plugin speaking to Google's Code Assist (Antigravity's) searches with
// googleSearch as a Google sign-in does; one at another host doesn't.
func TestGooglePluginSearchesItself(t *testing.T) {
	plain := &Request{WebSearch: true, Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "news?"}}}}}
	ag := provider.PluginAtForTest("antigravity-plugin", "antigravity", "https://daily-cloudcode-pa.googleapis.com/v1beta")
	if !searchesFor(ag, provider.CodeAssist, "gemini-3.8-flash", plain) {
		t.Error("Antigravity's plugin doesn't search by googleSearch")
	}
	other := provider.PluginAtForTest("other", "other", "https://googleapis.com.evil.invalid/v1beta")
	if codeAssistSearches(other, provider.CodeAssist, "gemini-3.8-flash") {
		t.Error("a plugin at another host searches by googleSearch")
	}
}

// Code Gemini ran and what it printed reach the client as text; they were
// dropped.
func TestCodeAssistDecodesRanCode(t *testing.T) {
	var text strings.Builder
	d := &codeAssistDecoder{}
	for _, line := range []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(6*7)\n"}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"42\n"}}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"It is 42."}]},"finishReason":"STOP"}]}}`,
	} {
		d.decode(line, func(ev Event) {
			if ev.Kind == KText {
				text.WriteString(ev.Text)
			}
		})
	}
	if got := text.String(); !strings.Contains(got, "```python\nprint(6*7)\n```") || !strings.Contains(got, "```\n42\n```") || !strings.HasSuffix(got, "It is 42.") {
		t.Errorf("text %q", got)
	}
}

// A search in a stream reaches a Gemini client as groundingMetadata.
func TestGeminiStreamGrounding(t *testing.T) {
	g := grounding([]Part{{Kind: Text, Text: "x"}, {Kind: Search, Text: "a; b", Hits: []Hit{{URL: "https://a.example/", Title: "A"}, {URL: "https://a.example/"}, {URL: "https://b.example/"}}}})
	b, _ := json.Marshal(g)
	if string(b) != `{"groundingChunks":[{"web":{"title":"A","uri":"https://a.example/"}},{"web":{"title":"https://b.example/","uri":"https://b.example/"}}],"webSearchQueries":["a","b"]}` {
		t.Errorf("grounding %s", b)
	}
	if grounding([]Part{{Kind: Text, Text: "x"}}) != nil {
		t.Error("grounding without a search")
	}
}
