package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// vertexModelLast is Vertex AI's refusal of contents whose last turn is the
// model's, as yycsyu was shown it (Codex compacting on gemini-3.8-flash),
// in the shape of Vertex AI's other 400s (vertexBadSig).
const vertexModelLast = "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"Requests ending with a model turn are not supported.\",\n    \"status\": \"INVALID_ARGUMENT\"\n  }\n}\n"

// lastTurn is the role and text of a Gemini request's last content.
func lastTurn(t *testing.T, contents any) (role, text string) {
	t.Helper()
	b, _ := json.Marshal(contents)
	var cs []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(b, &cs); err != nil || len(cs) == 0 {
		t.Fatalf("contents %s", b)
	}
	last := cs[len(cs)-1]
	for _, p := range last.Parts {
		text += p.Text
	}
	return last.Role, text
}

// Every builder of Gemini contents — Vertex AI, Gemini's own API (Factory),
// Code Assist for Gemini CLI and Antigravity, a Claude model on
// Antigravity too — ends a conversation that ends with the assistant's
// message with a user turn: Gemini turns away contents ending with the
// model's. An agent compacting with its ask in the system prompt is asked
// for the summary, not told to go on with the task.
func TestGeminiContentsNeverEndWithModel(t *testing.T) {
	said := []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "fix the bug"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "looking"}, {Kind: Text, Text: "Fixed it in main.go."}}},
	}
	builders := map[string]func(r *Request) any{
		"vertex": func(r *Request) any {
			b, err := buildVertex(r, "gemini-3.8-flash")
			if err != nil {
				t.Fatal(err)
			}
			var q map[string]any
			json.Unmarshal(b, &q)
			return q["contents"]
		},
		"gemini": func(r *Request) any {
			b, err := buildGemini(r, "gemini-3.8-flash")
			if err != nil {
				t.Fatal(err)
			}
			var q map[string]any
			json.Unmarshal(b, &q)
			return q["contents"]
		},
	}
	for _, c := range []struct{ agent, model string }{{"gemini", "gemini-3.8-flash"}, {"antigravity", "gemini-3.8-flash"}, {"antigravity", "claude-sonnet-4-6"}} {
		builders["code assist "+c.agent+" "+c.model] = func(r *Request) any {
			var q struct {
				Request map[string]any `json:"request"`
			}
			json.Unmarshal(buildCodeAssist(r, c.model, c.agent), &q)
			return q.Request["contents"]
		}
	}
	for name, build := range builders {
		for _, c := range []struct {
			name, system, want string
		}{
			{"a turn", "You are Codex.", continueText},
			{"a compaction asked in the system prompt", "You are a helpful AI assistant tasked with summarizing conversations.", compactLastText},
		} {
			role, text := lastTurn(t, build(&Request{System: c.system, Messages: said}))
			if role != "user" || text != c.want {
				t.Errorf("%s, %s: ends with %s %q, want user %q", name, c.name, role, text, c.want)
			}
		}
		// a conversation that already ends with the user's turn is sent as it is
		asked := append(said[:2:2], Message{Role: "user", Parts: []Part{{Kind: Text, Text: "and the test?"}}})
		if role, text := lastTurn(t, build(&Request{Messages: asked})); role != "user" || text != "and the test?" {
			t.Errorf("%s: a user-ended conversation ends with %s %q", name, role, text)
		}
	}
}

// End to end on Vertex AI, which answers 400 vertexModelLast to contents
// ending with the model's turn: Codex's compaction (its compaction_trigger
// after a reply, as compact_remote_v2_attempt.rs pushes it), a Codex turn
// whose input ends with the assistant's message, and an Anthropic client
// compacting with the ask in its system prompt all get an answer.
func TestVertexNeverAskedEndingWithModel(t *testing.T) {
	v := &vertexUp{}
	v.reply = vertexTurnsAway
	s := vertexServe(t, v, vertexLevels)
	const history = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>sandbox</permissions instructions>"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]},` +
		`{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"call_1"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"main.go"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed it in main.go."}]}`
	for _, c := range []struct {
		name, path, body, want string
	}{
		{"codex compaction", CodexPath + "/responses",
			`{"model":"google-vertex/gemini-3.8-flash","instructions":"You are Codex.","stream":true,"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}],"input":[` + history + `,{"type":"compaction_trigger"}]}`,
			codexCompactPrompt},
		{"codex turn ending with the assistant's message", CodexPath + "/responses",
			`{"model":"google-vertex/gemini-3.8-flash","instructions":"You are Codex.","stream":true,"input":[` + history + `]}`,
			continueText},
		// #1533's own request, as zhhe17 posted it: the compaction_trigger
		// right after a tool's output, which went out as one user turn
		// holding the functionResponse and the compaction prompt
		{"#1533: codex compaction after a tool's output", "/v1/responses", issue1533, codexCompactPrompt},
		{"#1533 on codex's own path", CodexPath + "/responses", issue1533, codexCompactPrompt},
		// an Anthropic client's tool_result beside its words in one
		// message, which goes to Gemini the same way (ump45nose's case)
		{"anthropic tool result and words", "/v1/messages", `{"model":"google-vertex/gemini-3.8-flash","max_tokens":64,"stream":true,
			"tools":[{"name":"read_sample","input_schema":{"type":"object"}}],"messages":[
			{"role":"user","content":"Read a sample file."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_sample","input":{"path":"sample.txt"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"Sample file content: hello."},
				{"type":"text","text":"Summarize the sample conversation briefly."}]}]}`,
			"Summarize the sample conversation briefly."},
		{"anthropic compaction asked in the system prompt", "/v1/messages",
			`{"model":"google-vertex/gemini-3.8-flash","max_tokens":2000,"stream":true,"system":"You are a helpful AI assistant tasked with summarizing conversations.","messages":[{"role":"user","content":"fix the bug"},{"role":"assistant","content":"Fixed it in main.go."}]}`,
			compactLastText},
	} {
		rec := codexPostTo(s, c.body)
		if c.path != CodexPath+"/responses" {
			rec = vertexPost(s, c.path, c.body)
		}
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "model turn") {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
			continue
		}
		role, text := lastTurn(t, v.last(t).body["contents"])
		if role != "user" || !strings.Contains(text, c.want) {
			t.Errorf("%s: Vertex AI was asked ending with %s %q, want user %q", c.name, role, text, c.want)
		}
	}
}

func vertexPost(s *Server, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

// issue1533 is the Responses request #1533 reproduced the refusal with.
const issue1533 = `{
    "model": "google-vertex/gemini-3.8-flash",
    "stream": true,
    "store": false,
    "max_output_tokens": 64,
    "reasoning": {"effort": "none"},
    "input": [
      {
        "type": "message",
        "role": "user",
        "content": [{"type": "input_text", "text": "Read a sample file."}]
      },
      {
        "type": "function_call",
        "name": "read_sample",
        "call_id": "call_diagnostic_1",
        "arguments": "{\"path\":\"sample.txt\"}"
      },
      {
        "type": "function_call_output",
        "call_id": "call_diagnostic_1",
        "output": "Sample file content: hello."
      },
      {"type": "compaction_trigger"}
    ]
  }`

// vertexTurnsAway answers as Vertex AI answered #1533: 400 vertexModelLast
// to contents ending with the model's turn, and to a user turn mixing a
// tool's functionResponse with text, which it takes as two turns.
func vertexTurnsAway(_ int, body map[string]any) (int, string) {
	contents, _ := body["contents"].([]any)
	for i, c := range contents {
		turn, _ := c.(map[string]any)
		if i == len(contents)-1 && turn["role"] == "model" {
			return http.StatusBadRequest, vertexModelLast
		}
		parts, _ := turn["parts"].([]any)
		response, text := false, false
		for _, p := range parts {
			part, _ := p.(map[string]any)
			response = response || part["functionResponse"] != nil
			// an image beside the response, as Codex's view_image
			// gives it, is refused the same way (Voo_Ler on X)
			text = text || part["text"] != nil || part["inlineData"] != nil || part["fileData"] != nil
		}
		if response && text {
			return http.StatusBadRequest, vertexModelLast
		}
	}
	return http.StatusOK, vertexPong
}

// Every builder of Gemini contents but a Claude model's on Antigravity
// (an Anthropic tool_result turn) puts what the user said after a tool's
// response in a user turn of its own (#1533), and keeps parallel calls'
// responses together in one.
func TestGeminiToolResponseTurnHoldsNoText(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "Read a sample file."}}},
		{Role: "assistant", Parts: []Part{
			{Kind: ToolCall, ID: "call_diagnostic_1", Name: "read_sample", Args: json.RawMessage(`{"path":"sample.txt"}`)},
			{Kind: ToolCall, ID: "call_2", Name: "read_sample", Args: json.RawMessage(`{"path":"other.txt"}`)},
		}},
		{Role: "user", Parts: []Part{
			{Kind: ToolResult, CallID: "call_diagnostic_1", Text: "Sample file content: hello.", Images: []Part{{Kind: Image, MediaType: "image/png", Data: "iVBORw0KGgo="}}},
			{Kind: ToolResult, CallID: "call_2", Text: "other"},
			{Kind: Text, Text: codexCompactPrompt},
		}},
	}}
	sent := map[string][]byte{}
	sent["vertex"], _ = buildVertex(r, "gemini-3.8-flash")
	sent["gemini"], _ = buildGemini(r, "gemini-3.8-flash")
	for _, agent := range []string{"gemini", "antigravity"} {
		var q struct {
			Request json.RawMessage `json:"request"`
		}
		json.Unmarshal(buildCodeAssist(r, "gemini-3.8-flash", agent), &q)
		sent["code assist "+agent] = q.Request
	}
	for name, b := range sent {
		var q struct {
			Contents []struct {
				Role  string           `json:"role"`
				Parts []map[string]any `json:"parts"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(b, &q); err != nil || len(q.Contents) != 4 {
			t.Errorf("%s: want 4 turns, got %s", name, b)
			continue
		}
		tools, said := q.Contents[2], q.Contents[3]
		if name == "vertex" {
			// Vertex AI takes no image in a response turn: it goes with
			// what the user said, in the turn after
			if len(tools.Parts) != 2 || tools.Parts[0]["functionResponse"] == nil || tools.Parts[1]["functionResponse"] == nil {
				t.Errorf("vertex: the tools' turn is %v", tools)
			}
			if said.Role != "user" || len(said.Parts) != 2 || said.Parts[0]["inlineData"] == nil || said.Parts[1]["text"] != codexCompactPrompt {
				t.Errorf("vertex: the user's turn after it is %v", said)
			}
			continue
		}
		if tools.Role != "user" || len(tools.Parts) != 3 || tools.Parts[0]["functionResponse"] == nil || tools.Parts[1]["inlineData"] == nil || tools.Parts[2]["functionResponse"] == nil {
			t.Errorf("%s: the tools' turn is %v", name, tools)
		}
		if said.Role != "user" || len(said.Parts) != 1 || said.Parts[0]["text"] != codexCompactPrompt {
			t.Errorf("%s: the user's turn after it is %v", name, said)
		}
	}
}
