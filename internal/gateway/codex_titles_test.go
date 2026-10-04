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

func TestTitleJSON(t *testing.T) {
	for in, want := range map[string]string{
		`{"title":"Fix login bug"}`:                `{"title":"Fix login bug"}`,
		"```json\n{\"title\": \"Fix login\"}\n```": `{"title":"Fix login"}`,
		`"Fix login bug"`:                          `{"title":"Fix login bug"}`,
		"Title: Fix login\n\nmore words":           `{"title":"Fix login"}`,
		"**修复登录问题**":                               `{"title":"修复登录问题"}`,
		"":                                         "",
		`{"name":"x"}`:                             "",
	} {
		if got := titleJSON(in); got != want {
			t.Errorf("titleJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
