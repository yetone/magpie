package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// intentUpstream is one provider's three models: it answers a
// classifier's question of intent by the words of the message it is
// asked about, its question of effort with level, and anything else,
// streamed, with the model's name and a prompt read mostly from the
// vendor's cache, as a warm conversation is.
type intentUpstream struct {
	level  string // the answer to how hard a turn is
	mu     sync.Mutex
	served []string // the model each turn's request went to, not the classifier's
}

var classifiedMessage = regexp.MustCompile(`(?s)<message>\n(.*)\n</message>`)

func (u *intentUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(b, &req)
	var asked string
	for _, m := range req.Messages {
		if c, ok := m.Content.(string); ok {
			asked += c
		}
	}
	if strings.Contains(asked, "Levels:") {
		w.Header().Set("Content-Type", "application/json")
		a, _ := json.Marshal(u.level)
		io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":`+string(a)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":1}}`)
		return
	}
	if m := classifiedMessage.FindStringSubmatch(asked); m != nil && strings.Contains(asked, "Kinds:") {
		said := strings.ToLower(string(m[1]))
		n := "0"
		switch {
		case strings.Contains(said, "refactor"):
			n = "1" // coding
		case strings.Contains(said, "capital"):
			n = "2" // question
		case strings.Contains(said, "haiku"):
			n = "3" // writing
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"`+n+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":1}}`)
		return
	}
	u.mu.Lock()
	u.served = append(u.served, req.Model)
	u.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+req.Model+`"}}]}`+"\n\n"+
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":30000,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":28000}}}`+"\n\n"+
		"data: [DONE]\n\n")
}

func (u *intentUpstream) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.served) == 0 {
		return ""
	}
	return u.served[len(u.served)-1]
}

// intentGroup is @zyh6036's group (X): routed by intent, its classifier a
// flash model that is one of its members too, picking each turn's
// reasoning too, which it answers with level.
func intentGroup(t *testing.T, level string) *intentUpstream {
	t.Helper()
	fresh(t)
	u := &intentUpstream{level: level}
	serveOn(t, "v", "kv", []string{"flash", "pro", "coder"}, u)
	if err := provider.SaveGroup(provider.Group{ID: "intent", Name: "Intent", Members: []string{"v/flash", "v/pro", "v/coder"},
		Classifier: "v/flash", Effort: provider.EffortAuto, Rules: []provider.Rule{
			{Use: "v/coder", Intent: "coding"},
			{Use: "v/pro", Intent: "question"},
			{Use: "v/flash", Intent: "writing"},
		}}); err != nil {
		t.Fatal(err)
	}
	return u
}

// codexFixture is Codex 0.171.0's third request of one session, as it
// came (captured against a recording server, paths and ids made
// anonymous): three turns, each a new intent. Through magpie's wiring
// Codex asks for its catalog's default reasoning too, which it doesn't
// for a provider of its own, as captured.
func codexFixture(t *testing.T) (http.Header, map[string]json.RawMessage, []json.RawMessage) {
	t.Helper()
	raw, err := os.ReadFile("testdata/codex-0.171-three-turns.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Headers map[string]string          `json:"headers"`
		Body    map[string]json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var input []json.RawMessage
	json.Unmarshal(fx.Body["input"], &input)
	h := http.Header{}
	for k, v := range fx.Headers {
		h.Set(k, v)
	}
	fx.Body["model"] = json.RawMessage(`"group/intent"`)
	fx.Body["reasoning"] = json.RawMessage(`{"effort":"medium","summary":"auto"}`)
	return h, fx.Body, input
}

// the conversation as each turn sent it: the opening four items with the
// first message, then each answer and the next message
var codexTurns = []struct {
	items int
	want  string
}{{4, "flash"}, {6, "coder"}, {8, "pro"}}

func withInput(body map[string]json.RawMessage, input []json.RawMessage) []byte {
	b := map[string]json.RawMessage{}
	for k, v := range body {
		b[k] = v
	}
	b["input"], _ = json.Marshal(input)
	out, _ := json.Marshal(b)
	return out
}

// levels are the classifier's answers to how hard a turn is: one it
// reads, and one it can't, which leaves the turn reasoning as the agent
// asked but its intent told all the same
var levels = []string{"2", "This is a moderately complex request."}

// @zyh6036 on X: one group routed by intent, Pi's turns each went to the
// model of their intent, Codex's stayed on the flash model the first one
// went to, "kept for its cache". Codex asks for reasoning and Pi doesn't,
// so with the group's reasoning on auto only Codex's turns asked the
// classifier how hard they are, and an answer to that it couldn't read
// threw its intent away too. Codex's own requests through the group, on
// each route Codex reaches magpie by, go where Pi's do.
func TestCodexTurnsRoutedByIntent(t *testing.T) {
	for _, level := range levels {
		for _, path := range []string{"/v1/responses", CodexPath + "/responses"} {
			t.Run(path+" "+level, func(t *testing.T) {
				u := intentGroup(t, level)
				s := New()
				h, body, input := codexFixture(t)
				for i, turn := range codexTurns {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("POST", path, strings.NewReader(string(withInput(body, input[:turn.items]))))
					req.Header = h.Clone()
					s.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 {
						t.Fatalf("turn %d: %d %s", i+1, rec.Code, rec.Body.String())
					}
					if got := u.last(); got != turn.want {
						t.Errorf("Codex turn %d went to %s, want %s", i+1, got, turn.want)
					}
				}
			})
		}
	}
}

// The same over Codex's WebSocket: each turn names the reply before it
// and sends only the user's next message.
func TestCodexSocketTurnsRoutedByIntent(t *testing.T) {
	for _, level := range levels {
		for _, path := range []string{"/v1/responses", CodexPath + "/responses"} {
			t.Run(path+" "+level, func(t *testing.T) {
				u := intentGroup(t, level)
				_, ts := sockServer(t)
				h, body, input := codexFixture(t)
				h.Set("Openai-Beta", "responses_websockets=2026-02-06")
				c, resp, err := dialSock(t, ts, path, h)
				if err != nil {
					code := 0
					if resp != nil {
						code = resp.StatusCode
					}
					t.Fatalf("%v (%d)", err, code)
				}
				prev := ""
				for i, turn := range codexTurns {
					b := map[string]json.RawMessage{}
					json.Unmarshal(withInput(body, input[:turn.items]), &b)
					if i > 0 {
						// the user's next message alone, after the reply named
						b["input"], _ = json.Marshal(input[turn.items-1 : turn.items])
						b["previous_response_id"], _ = json.Marshal(prev)
					}
					delete(b, "stream")
					b["type"] = json.RawMessage(`"response.create"`)
					f, _ := json.Marshal(b)
					got := c.turn(string(f))
					l := last(got)
					if l.Get("type").String() != "response.completed" {
						t.Fatalf("turn %d: %v", i+1, got)
					}
					prev = l.Get("response.id").String()
					if got := u.last(); got != turn.want {
						t.Errorf("Codex turn %d went to %s, want %s", i+1, got, turn.want)
					}
				}
			})
		}
	}
}

// Pi's turns on the same group, as Pi sends them (openai-completions):
// each goes to its intent's model, asking for no reasoning as Pi does by
// default, or for some, as any chat client can.
func TestPiTurnsRoutedByIntent(t *testing.T) {
	for _, level := range levels {
		for _, reasoning := range []string{"", `"reasoning_effort":"medium",`} {
			t.Run(level+" "+reasoning, func(t *testing.T) {
				u := intentGroup(t, level)
				s := New()
				msgs := []string{`{"role":"system","content":"You are pi."}`, `{"role":"user","content":"Write a haiku about autumn leaves"}`}
				for i, turn := range []struct{ next, want string }{
					{"", "flash"},
					{"Now refactor the Go function parseConfig in main.go to return errors instead of panicking", "coder"},
					{"What's the capital of France?", "pro"},
				} {
					if turn.next != "" {
						msgs = append(msgs, `{"role":"assistant","content":"from `+u.last()+`"}`, `{"role":"user","content":`+intentJSON(turn.next)+`}`)
					}
					body := `{"model":"group/intent",` + reasoning + `"stream":true,"messages":[` + strings.Join(msgs, ",") + `]}`
					rec := httptest.NewRecorder()
					req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
					req.Header.Set("User-Agent", "pi-coding-agent")
					s.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 {
						t.Fatalf("turn %d: %d %s", i+1, rec.Code, rec.Body.String())
					}
					if got := u.last(); got != turn.want {
						t.Errorf("Pi turn %d went to %s, want %s", i+1, got, turn.want)
					}
				}
			})
		}
	}
}

func intentJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var _ http.Handler = (*intentUpstream)(nil)
