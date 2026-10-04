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
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexTitleRequest is a title request as Codex's TUI sends one on its own
// model, signed in to ChatGPT: a hidden thread_title turn, its schema in
// text.format; with subagent, as the app names one in x-openai-subagent.
func codexTitleRequest(t *testing.T, s *Server, subagent string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"gpt-5.6-luna","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Generate a concise title. User prompt:\nfix the login bug"}]}],` +
		`"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("session_id", "title-thread")
	if subagent != "" {
		req.Header.Set("x-openai-subagent", subagent)
	} else {
		req.Header.Set("x-codex-turn-metadata", `{"thread_source":"thread_title","parent_thread_id":"main-chat"}`)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// outputText is the text of the message items in a Responses stream's
// response.completed.
func outputText(t *testing.T, body string) (string, int) {
	t.Helper()
	for _, ev := range events(body) {
		if ev["type"] != "response.completed" {
			continue
		}
		resp, _ := ev["response"].(map[string]any)
		out, _ := resp["output"].([]any)
		var text strings.Builder
		for _, it := range out {
			m, _ := it.(map[string]any)
			cs, _ := m["content"].([]any)
			for _, c := range cs {
				cm, _ := c.(map[string]any)
				s, _ := cm["text"].(string)
				text.WriteString(s)
			}
		}
		return text.String(), len(out)
	}
	t.Fatalf("no response.completed in %q", body)
	return "", 0
}

// Codex's title requests go where Settings says (#705): as they came to
// Codex's ChatGPT sign-in by default; turned off, answered here with no
// title and sent nowhere; or to a model of magpie's, its plain answer
// handed back as the {"title": …} Codex reads. Each stays a title request
// in the Usage and Routing views.
func TestCodexTitlesSetting(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Fix login bug"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	chatgptCalls := 0
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		chatgptCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"title\":\"From ChatGPT\"}"}]}],"usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	save := func(v string) {
		t.Helper()
		st := settings.Load()
		st.CodexTitles = v
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
	}

	// default: as before, to Codex's own sign-in
	s := New()
	rec := codexTitleRequest(t, s, "")
	if text, _ := outputText(t, rec.Body.String()); chatgptCalls != 1 || f.calls != 0 || text != `{"title":"From ChatGPT"}` {
		t.Fatalf("default: chatgpt %d, magpie model %d, %q", chatgptCalls, f.calls, text)
	}

	// off: nothing goes out, a finished reply with no title in it
	save("off")
	for _, sub := range []string{"", "thread_title", "thread_title_reconsideration"} {
		rec = codexTitleRequest(t, s, sub)
		text, n := outputText(t, rec.Body.String())
		if rec.Code != 200 || n != 0 || text != "" || chatgptCalls != 1 || f.calls != 0 {
			t.Fatalf("off (%q): %d %q, %d items, chatgpt %d, magpie model %d", sub, rec.Code, rec.Body.String(), n, chatgptCalls, f.calls)
		}
	}

	// a model of magpie's: it writes the title, in the shape Codex reads
	save("fake/m1")
	rec = codexTitleRequest(t, s, "thread_title")
	if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != `{"title":"Fix login bug"}` || f.calls != 1 || chatgptCalls != 1 {
		t.Fatalf("routed: %d %q, magpie model %d, chatgpt %d", rec.Code, rec.Body.String(), f.calls, chatgptCalls)
	}
	var sent struct {
		Model string `json:"model"`
	}
	json.Unmarshal(f.got, &sent)
	if sent.Model != "m1" {
		t.Errorf("the magpie model was asked for %q", sent.Model)
	}

	// a turn of the conversation is not a title: it goes as before
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	if chatgptCalls != 2 || f.calls != 1 {
		t.Errorf("a plain turn: chatgpt %d, magpie model %d", chatgptCalls, f.calls)
	}

	// every title request is in the ledger as one: ChatGPT's, the three
	// answered here, the magpie model's
	var kinds []string
	for _, u := range usage.Load(time.Time{}) {
		kinds = append(kinds, u.Provider+":"+u.Kind)
		if u.Provider == "magpie" && (!u.IsRejected() || u.Status != 200) {
			t.Errorf("an answered-here title %+v", u)
		}
	}
	want := "openai:thread_title magpie:thread_title magpie:thread_title magpie:thread_title_reconsideration fake:thread_title openai:"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("ledger %s\nwant   %s", got, want)
	}
	st := s.Trace(t.Context(), 0, 0)
	titles := 0
	for _, r := range st.Routes {
		if isTitleKind(r.Kind) {
			titles++
		}
	}
	if titles != 5 {
		t.Errorf("Routing view titles %d, want 5: %+v", titles, st.Routes)
	}
}

// codexProviderTitle is a title request as Codex's TUI sends one with
// magpie as its model provider (model_provider = "magpie", #743): to
// magpie's own /v1/responses, on the conversation's model, a magpie id —
// or, with backend, to the ChatGPT backend path on that magpie model.
func codexProviderTitle(t *testing.T, s *Server, backend bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"fake/m1","stream":true,"reasoning":{"effort":"low"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Generate a concise title. User prompt:\nfix the login bug"}]}],` +
		`"text":{"format":{"type":"json_schema","name":"codex_output_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":36}},"required":["title"],"additionalProperties":false}}}}`
	path := "/v1/responses"
	if backend {
		path = CodexPath + "/responses"
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("session_id", "title-thread")
	req.Header.Set("x-codex-turn-metadata", `{"thread_source":"thread_title","parent_thread_id":"main-chat"}`)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A title Codex asks one of magpie's models for is one it can read
// (#743): with magpie as Codex's provider the request comes to /v1, where
// the Codex titles setting is heeded too, and with no model set there the
// conversation's model's plain answer — a Chat model is never shown the
// schema — is handed back as {"title": …}, as it is on the ChatGPT path.
// An answer with no title in it is in the Routing and Usage views as the
// failure it is to Codex.
func TestCodexTitleOnMagpieProvider(t *testing.T) {
	answer := "Fix login bug"
	f := &fake{t: t}
	setup(t, provider.Chat, f)
	reply := func() string {
		return sse(
			`data: {"id":"c1","choices":[{"index":0,"delta":{"content":`+strconvQuote(answer)+`}}]}`,
			`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30,"completion_tokens":4}}`,
			`data: [DONE]`)
	}
	save := func(v string) {
		t.Helper()
		st := settings.Load()
		st.CodexTitles = v
		if err := settings.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	for _, backend := range []bool{false, true} {
		f.reply = reply()
		rec := codexProviderTitle(t, s, backend)
		if text, _ := outputText(t, rec.Body.String()); rec.Code != 200 || text != `{"title":"Fix login bug"}` {
			t.Fatalf("backend %v: %d %q", backend, rec.Code, rec.Body.String())
		}
	}
	calls := f.calls

	// the setting holds on /v1 as well
	save("off")
	rec := codexProviderTitle(t, s, false)
	if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 0 || text != "" || f.calls != calls {
		t.Fatalf("off on /v1: %d %q, upstream calls %d", rec.Code, rec.Body.String(), f.calls-calls)
	}
	save("")

	// a turn of the conversation on /v1 is left as it was
	f.reply = reply()
	if code, body := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`); code != 200 || strings.Contains(body, `{\"title\"`) {
		t.Fatalf("a plain turn: %d %q", code, body)
	}

	// no title in the answer: none for Codex, and the call says why
	answer = ""
	f.reply = reply()
	rec = codexProviderTitle(t, s, false)
	if text, n := outputText(t, rec.Body.String()); rec.Code != 200 || n != 0 || text != "" {
		t.Fatalf("no title: %d %q", rec.Code, rec.Body.String())
	}
	var last usage.Record
	for _, u := range usage.Load(time.Time{}) {
		last = u
	}
	if last.Kind != "thread_title" || !strings.HasPrefix(last.Error, "title: ") || !last.Failed() {
		t.Errorf("the ledger's no-title call %+v", last)
	}
	st := s.Trace(t.Context(), 0, 0)
	var r Route
	for _, c := range st.Routes {
		if c.Seq > r.Seq {
			r = c
		}
	}
	if !isTitleKind(r.Kind) || !strings.HasPrefix(r.Error, "title: ") {
		t.Errorf("the Routing view's no-title call %+v", r)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestTitleJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"title":"Fix login bug"}`:                      `{"title":"Fix login bug"}`,
		"```json\n{\"title\": \"Fix login\"}\n```":       `{"title":"Fix login"}`,
		`"Fix login bug"`:                                `{"title":"Fix login bug"}`,
		"Title: Fix login\n\nmore words":                 `{"title":"Fix login"}`,
		"**修复登录问题**":                                     `{"title":"修复登录问题"}`,
		"<think>\nthe user wants\n</think>\n\nFix login": `{"title":"Fix login"}`,
		"":             "",
		`{"name":"x"}`: "",
	} {
		if got := titleJSON(in, titleShape{}); got != want {
			t.Errorf("titleJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// #743: a Codex whose title schema requires a description beside the
// title, no other field allowed, is given both, each in its bounds; the
// model's own description when its JSON has one.
func TestTitleJSONFillsTheSchema(t *testing.T) {
	body := `{"text":{"format":{"type":"json_schema","strict":true,"schema":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":36},"description":{"type":"string","minLength":1}},"required":["title","description"],"additionalProperties":false}}}}`
	shape := titleShapeOf([]byte(body))
	for in, want := range map[string]string{
		"Fix login bug": `{"description":"Fix login bug","title":"Fix login bug"}`,
		`{"title":"Fix login","description":"The login form rejects valid passwords"}`: `{"description":"The login form rejects valid passwords","title":"Fix login"}`,
		"Fix the login bug that rejects every valid password on mobile": `{"description":"Fix the login bug that rejects every valid password on mobile","title":"Fix the login bug that rejects every"}`,
		"": "",
	} {
		if got := titleJSON(in, shape); got != want {
			t.Errorf("titleJSON(%q) = %s, want %s", in, got, want)
		}
	}
	if got := titleJSON("Fix login", titleShapeOf([]byte(`{}`))); got != `{"title":"Fix login"}` {
		t.Errorf("no schema: %s", got)
	}
}
