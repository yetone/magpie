package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// codexSideRequest is one of the Codex app's hidden side turns as it sends
// one beside its ChatGPT sign-in: on gpt-6-luna, its own model, whatever
// the conversation is on, with the thread's source in its turn metadata
// and the schema its answer must fill in text.format.
func codexSideRequest(t *testing.T, s *Server, source, schema string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"gpt-6-luna","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Latest message: fixed the login bug"}]}],` +
		`"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":` + schema + `}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "side-thread")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"`+source+`"}`)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const (
	sideTitleSchema       = `{"type":"object","properties":{"title":{"type":"string","maxLength":36},"description":{"type":"string"}},"required":["title","description"],"additionalProperties":false}`
	sideDescriptionSchema = `{"type":"object","properties":{"description":{"type":"string"}},"required":["description"],"additionalProperties":false}`
	sideSummarySchema     = `{"type":"object","properties":{"summary":{"type":"string"},"compactSummary":{"type":"string","maxLength":60}},"required":["summary","compactSummary"],"additionalProperties":false}`
)

// With one of magpie's models picked beside Codex's ChatGPT sign-in, the
// Codex app's titles, descriptions and summaries, which it asks for on
// gpt-6-luna whatever the conversation is on, are written by the model
// picked, not by ChatGPT, so a conversation on a third-party model spends
// none of the ChatGPT account's allowance (atom_Ray on X: an hour took 2%
// of the 5-hour window, and at 0% they failed). Each answer goes back in
// the shape its schema asks for. On Codex's own model (no side model) they
// go to ChatGPT as before, and so does a turn of the conversation.
func TestCodexSideTurnsOnThePickedModel(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Fixed the login bug"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgptCalls := 0
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		chatgptCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"summary\":\"From ChatGPT\"}"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	side := ""
	CodexSideModel = func() string { return side }
	// no turn comes before these side turns: each takes the config's
	// model once its wait for one is over (codexLeads.sideOf)
	wasWait := codexSideWait
	codexSideWait = 0
	t.Cleanup(func() { CodexSideModel, codexSideWait = nil, wasWait })
	s := New()

	// on Codex's own model: every side turn to ChatGPT, as before
	for _, src := range []string{"thread_title", "thread_description", "thread_summary"} {
		codexSideRequest(t, s, src, sideSummarySchema)
	}
	if chatgptCalls != 3 || f.calls != 0 {
		t.Fatalf("own model: chatgpt %d, magpie model %d", chatgptCalls, f.calls)
	}

	side = "fake/m1"
	for _, c := range []struct{ source, schema, want string }{
		{"thread_title", sideTitleSchema, `{"description":"Fixed the login bug","title":"Fixed the login bug"}`},
		{"thread_description", sideDescriptionSchema, `{"description":"Fixed the login bug"}`},
		{"thread_summary", sideSummarySchema, `{"compactSummary":"Fixed the login bug","summary":"Fixed the login bug"}`},
	} {
		before := f.calls
		rec := codexSideRequest(t, s, c.source, c.schema)
		if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != c.want || f.calls != before+1 || chatgptCalls != 3 {
			t.Fatalf("%s on the picked model: %d %q, magpie model %d, chatgpt %d", c.source, rec.Code, rec.Body.String(), f.calls, chatgptCalls)
		}
		var sent struct {
			Model string `json:"model"`
		}
		json.Unmarshal(f.got, &sent)
		if sent.Model != "m1" {
			t.Errorf("%s: the magpie model was asked for %q", c.source, sent.Model)
		}
	}

	// a turn of the conversation on Codex's own model is the user's pick:
	// it goes to ChatGPT, side model or not
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if chatgptCalls != 4 || f.calls != 3 {
		t.Errorf("a plain turn: chatgpt %d, magpie model %d", chatgptCalls, f.calls)
	}

	var kinds []string
	for _, u := range usage.Load(time.Time{}) {
		kinds = append(kinds, u.Provider+":"+u.Kind)
	}
	want := "openai:thread_title openai:thread_description openai:thread_summary fake:thread_title fake:thread_description fake:thread_summary openai:"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("ledger %s\nwant   %s", got, want)
	}
}

// A summary on one of magpie's models, with magpie Codex's provider, is
// handed back in the shape its schema asks for too: a model behind the
// Chat API never sees the schema, and the app drops a plain answer.
func TestCodexSummaryOnMagpieProvider(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Summary: \"Ran the tests\""}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	s := New()
	body := `{"model":"fake/m1","stream":true,"input":"Summarize","text":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"]}}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"thread_summary"}`)
	s.Handler().ServeHTTP(rec, req)
	if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != `{"summary":"Ran the tests"}` || f.calls != 1 {
		t.Fatalf("summary: %d %q, calls %d", rec.Code, rec.Body.String(), f.calls)
	}
}

func TestSummaryJSON(t *testing.T) {
	long := `{"type":"object","properties":{"summary":{"type":"string"},"compactSummary":{"type":"string","maxLength":10}},"required":["summary","compactSummary"]}`
	var shape titleShape
	json.Unmarshal([]byte(long), &shape)
	for in, want := range map[string]string{
		"Fixed it":                               `{"compactSummary":"Fixed it","summary":"Fixed it"}`,
		"Fixed the\nlogin bug":                   `{"compactSummary":"Fixed the","summary":"Fixed the login bug"}`,
		`{"summary":"A","compactSummary":"B"}`:   `{"compactSummary":"B","summary":"A"}`,
		"```json\n{\"summary\":\"Fenced\"}\n```": `{"compactSummary":"Fenced","summary":"Fenced"}`,
		"":                                       "",
		"{broken":                                "",
	} {
		if got := summaryJSON(in, shape); got != want {
			t.Errorf("summaryJSON(%q) = %s, want %s", in, got, want)
		}
	}
	if got := summaryJSON("Ran the tests", titleShape{}); got != `{"summary":"Ran the tests"}` {
		t.Errorf("no shape: %s", got)
	}
}
