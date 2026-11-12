package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// #4374 (RoyShen12): Codex Desktop through Copilot — the Gemini models all
// came back 400 "Copilot: invalid request body" on a turn whose only
// content was "hi", while a tool-less provider test came back 200, and
// Grok 4.7 came back 422 "Copilot: 422 Unprocessable Entity: Unprocessable
// Entity", six gateway requests in a row, all on the first turn. The two
// speak different wires: a Copilot Gemini model is served on chat
// completions alone, so Codex's Responses turn is translated for it, and
// Grok is served on /responses, so its turn is relayed as it came.

// copilotUnionsRequest is Codex's first turn with create_thread, its
// target the disjoint object union #3931's comparisons pinned as the
// refusal (each branch requires a "type" whose value no other branch
// takes), as Codex Desktop declares it.
const copilotUnionsRequest = `{"model":"copilot/gemini-3.8-flash","stream":true,"store":false,
  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
  "tools":[{"type":"function","name":"create_thread","description":"Create a thread.",
    "parameters":{"type":"object","properties":{"target":{"description":"Where the thread lives.",
      "anyOf":[{"type":"object","properties":{"type":{"type":"string","enum":["project"]},"project_id":{"type":"string"}},
               "required":["type","project_id"],"additionalProperties":false},
              {"type":"object","properties":{"type":{"type":"string","enum":["projectless"]}},
               "required":["type"],"additionalProperties":false}]}},
      "required":["target"],"additionalProperties":false}}]}`

// TestCopilotGeminiTakesCodexUnionsAsOneOf: Copilot's Gemini turned every
// tool-bearing turn of Codex's away 400 "invalid request body" (#4374,
// #3931's comparisons: the same request with the disjoint unions as oneOf
// came back 200). What Copilot's Gemini rejects is the anyOf, so a
// provably disjoint one goes as oneOf, which says the same to a model
// choosing what to call.
func TestCopilotGeminiTakesCodexUnionsAsOneOf(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"gemini-3.8-flash": `["/chat/completions"]`,
	}, nil, func(m string, b []byte) bool {
		// as #3931's live comparisons show: a tool whose schema carries an
		// object anyOf turns the whole request away, whatever else it says
		return bytes.Contains(b, []byte(`"anyOf"`))
	}, "invalid request body", http.StatusBadRequest, &sent)

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(copilotUnionsRequest)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("Copilot was asked %d times", len(sent))
	}
	if strings.Contains(sent[0], `"anyOf"`) {
		t.Errorf("Codex's disjoint union went to Copilot's Gemini as anyOf, which it turns away: %s", sent[0])
	}
	if !strings.Contains(sent[0], `"oneOf"`) {
		t.Errorf("the union went as neither anyOf nor oneOf: %s", sent[0])
	}
}

// copilotOverlappingRequest carries a union no one can prove disjoint
// (neither branch requires anything, neither forbids a field): an anyOf
// to every vendor, oneOf or not.
const copilotOverlappingRequest = `{"model":"copilot/gemini-3.8-flash","stream":true,"store":false,
  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
  "tools":[{"type":"function","name":"draw","description":"Draw a shape.",
    "parameters":{"type":"object","properties":{"shape":{"description":"The shape.",
      "anyOf":[{"type":"object","properties":{"round":{"type":"boolean"}}},
              {"type":"object","properties":{"square":{"type":"boolean"}}}]}}}}]}`

// TestCopilotGeminiKeepsUnprovenUnions: only what is provably disjoint
// goes as oneOf. A union whose branches can't be told apart stays an
// anyOf (one of mixed types: same-type branches are merged,
// TestCopilotGeminiTakesToolShapes), and Copilot's refusal of it still reaches the client, as it did
// before (#4374's Gemini turns were all refused; this union is one the
// conversion leaves as it came).
func TestCopilotGeminiKeepsUnprovenUnions(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"gemini-3.8-flash": `["/chat/completions"]`,
	}, nil, func(m string, b []byte) bool {
		return bytes.Contains(b, []byte(`"anyOf"`))
	}, "invalid request body", http.StatusBadRequest, &sent)

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(strings.Replace(copilotOverlappingRequest,
		`{"type":"object","properties":{"square":{"type":"boolean"}}}`, `{"type":"array","items":{"type":"string"}}`, 1))))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 0 {
		t.Errorf("a refused turn was recorded as taken: %q", sent)
	}
}

// TestCopilotGrokTakesCustomToolsAsFunctions: Grok on Copilot is served on
// /responses, and Codex's turn went to it as it came — with apply_patch, a
// custom tool, which xAI behind Copilot takes none of (#3392). Copilot's
// answer in front of Grok says no more than the status (#378's bare 400),
// so nothing in "422 Unprocessable Entity: Unprocessable Entity" (#2951,
// #4374: six requests in a row, all refused) tells magpie what to learn,
// and every turn failed. The custom tools go as functions from the first
// ask, as a refusal already taught magpie to do for the vendors whose
// answer names them (#2656, #3392); a model Copilot serves on the same
// /responses that takes them (GPT) is relayed as it came.
func TestCopilotGrokTakesCustomToolsAsFunctions(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"grok-4.7": `["/responses"]`,
		"gpt-6.1":  `["/responses"]`,
	}, func(m string, b []byte) (int, string) {
		if m == "grok-4.7" && hasCustomTool(b) {
			return http.StatusUnprocessableEntity, `{"error":{"message":"422 Unprocessable Entity: Unprocessable Entity","type":"invalid_request_error"}}`
		}
		return 0, ""
	}, nil, "", 0, &sent)

	turn := func(model string) (int, string) {
		body := `{"model":"` + model + `","stream":true,"store":false,"reasoning":{"effort":"low"},
		  "input":[{"role":"user","content":"hi check current time"}],
		  "tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"text"}},
		           {"type":"function","name":"shell","description":"Run a command.","strict":false,
		            "parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]}`
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	code, got := turn("copilot/grok-4.7")
	if code != 200 || !strings.Contains(got, "DONE") {
		t.Fatalf("grok-4.7: %d %s", code, got)
	}
	code, got = turn("copilot/gpt-6.1")
	if code != 200 || !strings.Contains(got, "DONE") {
		t.Fatalf("gpt-6.1: %d %s", code, got)
	}
	mu.Lock()
	defer mu.Unlock()
	var grok, gpt string
	for _, s := range sent {
		if strings.HasPrefix(s, "grok-4.7 ") && grok == "" {
			grok = s
		}
		if strings.HasPrefix(s, "gpt-6.1 ") && gpt == "" {
			gpt = s
		}
	}
	if grok == "" || gpt == "" {
		t.Fatalf("Copilot was asked: %q", sent)
	}
	if strings.Contains(grok, `"type":"custom"`) {
		t.Errorf("Grok was handed Codex's custom tool, which it turns every turn away over: %s", grok)
	}
	if !strings.Contains(grok, `"name":"apply_patch"`) {
		t.Errorf("apply_patch not offered Grok as a function: %s", grok)
	}
	if !strings.Contains(gpt, `"type":"custom"`) {
		t.Errorf("GPT was not handed Codex's custom tool as it came: %s", gpt)
	}
}

// copilotTurnFake is a Copilot account whose api serves the models named,
// on the endpoints their lists give (a "/responses" path and a
// "/chat/completions" one). refuse says whether Copilot's /responses turns
// a turn away, and with what; chatRefuse says the same of its chat
// completions, answered with chatStatus and chatMessage. The bodies of the
// turns Copilot takes are appended to sent as "<model> <body>".
func copilotTurnFake(t *testing.T, models map[string]string, refuse func(model string, body []byte) (int, string),
	chatRefuse func(model string, body []byte) bool, chatMessage string, chatStatus int, sent *[]string,
) {
	t.Helper()
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "echo", "oauth_token": "gho_echo"},
	}), 0o600)

	var mu sync.Mutex
	note := func(m string, b []byte) {
		mu.Lock()
		*sent = append(*sent, m+" "+string(b))
		mu.Unlock()
	}
	// the account's model list, as Copilot's would have it: each model on
	// the APIs its endpoints give
	var live []catalog.Model
	for id, endpoints := range models {
		var apis []string
		for _, e := range strings.Split(strings.Trim(strings.ReplaceAll(endpoints, "/v1/messages", "/chat/completions"), `[]"`), ",") {
			switch strings.TrimSpace(e) {
			case "/responses":
				apis = append(apis, "responses")
			default:
				apis = append(apis, "chat")
			}
		}
		live = append(live, catalog.Model{ID: id, Name: id, APIs: apis, Efforts: []string{"low", "medium", "high"}})
	}
	if err := catalog.SaveLive("copilot", "https://api.githubcopilot.com", live); err != nil {
		t.Fatalf("save live: %v", err)
	}
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[]}`)
		case "/responses":
			m := modelOf(b)
			if refuse != nil {
				if status, message := refuse(m, b); status != 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					io.WriteString(w, message)
					return
				}
			}
			note(m, b)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
				`data: {"type":"response.output_text.delta","delta":"DONE"}`,
				`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":1}}}`))
		case "/chat/completions":
			m := modelOf(b)
			if chatRefuse != nil && chatRefuse(m, b) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(chatStatus)
				io.WriteString(w, `{"error":{"code":null,"message":"`+chatMessage+`","param":null,"type":"invalid_request_error"}}`)
				return
			}
			note(m, b)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","model":"`+m+`","choices":[{"delta":{"content":"hi"}}]}`,
				`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(apiSrv.Close)
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": apiSrv.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	t.Cleanup(ghSrv.Close)
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = ghSrv.URL+"/token", ghSrv.URL+"/user"
	t.Cleanup(func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser })
}

// copilotNullableRequest carries Codex Desktop's real move_thread_to_project
// (#4374, RoyShen12's isolation and yuushabu_buyinn's report): projectId a
// nullable string as anyOf, and the other shapes a nullable takes — oneOf,
// inside an array's items, behind a $def.
const copilotNullableRequest = `{"model":"copilot/gemini-3.8-flash","stream":true,"store":false,
  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
  "tools":[{"type":"function","name":"move_thread_to_project","description":"Move a thread.",
    "parameters":{"type":"object","properties":{
      "projectId":{"anyOf":[{"type":"string"},{"type":"null"}],"description":"Destination, or null."},
      "alt":{"oneOf":[{"type":"integer"},{"type":"null"}]},
      "many":{"type":"array","items":{"anyOf":[{"type":"string"},{"type":"null"}]}},
      "ref":{"$ref":"#/$defs/maybe"}},
      "$defs":{"maybe":{"anyOf":[{"type":"number"},{"type":"null"}]}},
      "required":["projectId"],"additionalProperties":false}}]}`

// TestCopilotGeminiKeepsNullableUnionsUntyped: the type written beside a
// nullable union (Vertex wants one) is what Copilot's Gemini turns away
// 400 "invalid request body": removing that one field made the live
// request 200 (#4374). Every union goes as its client wrote it.
func TestCopilotGeminiKeepsNullableUnionsUntyped(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"gemini-3.8-flash": `["/chat/completions"]`,
	}, nil, func(m string, b []byte) bool {
		bad := false
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				_, a := x["anyOf"]
				_, o := x["oneOf"]
				if _, ty := x["type"]; (a || o) && ty {
					bad = true
				}
				for _, c := range x {
					walk(c)
				}
			case []any:
				for _, c := range x {
					walk(c)
				}
			}
		}
		var req map[string]any
		json.Unmarshal(b, &req)
		walk(req["tools"])
		return bad
	}, "invalid request body", http.StatusBadRequest, &sent)

	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(copilotNullableRequest)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], `"anyOf"`) {
		t.Fatalf("Copilot was asked %q", sent)
	}
}

// TestGeminiTypesOtherVendorsKeepTheirs: only Copilot's Gemini leaves a
// union untyped; a Vertex-bound Gemini behind any other provider is still
// given the type Vertex requires.
func TestGeminiTypesOtherVendorsKeepTheirs(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"p":{"anyOf":[{"type":"string"},{"type":"null"}]}}}}}]}`)
	other := provider.Provider{Account: &provider.Account{Agent: "claude"}}
	if copilotGemini(other, body) {
		t.Fatal("another provider counted as Copilot")
	}
	if got := geminiTypes(body, copilotGemini(other, body)); !strings.Contains(string(got), `"p":{"type":"string","anyOf"`) {
		t.Errorf("another vendor's union lost its type: %s", got)
	}
	cp := provider.Provider{Account: &provider.Account{Agent: "copilot"}}
	if got := geminiTypes(body, copilotGemini(cp, body)); strings.Contains(string(got), `"type":"string","anyOf"`) {
		t.Errorf("Copilot's union was typed: %s", got)
	}
}

// copilotNamespaceRequest is Codex Desktop's turn with an ordinary
// namespace (an MCP server's tools) and a function whose parameters are a
// root union.
const copilotNamespaceRequest = `{"model":"copilot/%s","stream":true,"store":false,
  "input":[{"role":"user","content":"hi"}],
  "tools":[{"type":"namespace","name":"mcp__codex_app","description":"Codex app.",
     "tools":[{"type":"function","name":"move_thread_to_project","description":"Move.","parameters":{"type":"object","properties":{"projectId":{"type":"string"}}}}]},
    {"type":"function","name":"automation_update","description":"Update.","parameters":{"oneOf":[
      {"type":"object","properties":{"mode":{"type":"string","enum":["a"]}},"required":["mode"]},
      {"type":"object","properties":{"mode":{"type":"string","enum":["b"]}},"required":["mode"]}]}}]}`

// TestCopilotGrokTakesNamespacesFlat: Grok behind Copilot turns a request
// with a namespace tool, or a function whose parameters are a root union,
// away; flat names and object roots gave 200 (#4374). GPT on the same
// /responses is handed the tools as they came.
func TestCopilotGrokTakesNamespacesFlat(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"grok-4.7": `["/responses"]`,
		"gpt-6.1":  `["/responses"]`,
	}, func(m string, b []byte) (int, string) {
		if m == "grok-4.7" && (bytes.Contains(b, []byte(`"type":"namespace"`)) || bytes.Contains(b, []byte(`"oneOf"`))) {
			return http.StatusBadRequest, `{"error":{"message":"Bad Request","type":"invalid_request_error"}}`
		}
		return 0, ""
	}, nil, "", 0, &sent)
	turn := func(model string) {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(copilotNamespaceRequest, model))))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body.String())
		}
	}
	turn("grok-4.7")
	turn("gpt-6.1")
	mu.Lock()
	defer mu.Unlock()
	for _, s := range sent {
		switch {
		case strings.HasPrefix(s, "grok-4.7 "):
			if !strings.Contains(s, `"name":"mcp__codex_app__move_thread_to_project"`) || strings.Contains(s, `"namespace"`) {
				t.Errorf("Grok's namespace not flat: %s", s)
			}
			if !strings.Contains(s, `"name":"automation_update"`) {
				t.Errorf("a plain function was lost: %s", s)
			}
		case strings.HasPrefix(s, "gpt-6.1 "):
			if !strings.Contains(s, `"type":"namespace"`) || !strings.Contains(s, `"oneOf"`) {
				t.Errorf("GPT's tools were changed: %s", s)
			}
		}
	}
	if len(sent) != 2 {
		t.Fatalf("Copilot took %d turns", len(sent))
	}
}
