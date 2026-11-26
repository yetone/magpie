package gateway

import (
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

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// lunaDecides stands in for api.openai.com for an OpenAI key whose
// gpt-6-luna is a routing group's classifier: POST /v1/decisions answers
// the intent by the message's words, as intentUpstream does through chat,
// the kinds as topics and the effort with a middling score; status, when
// set, is how every Decisions call is answered instead, with body. A
// conversation sent to gpt-6-luna as a member is answered on Responses
// or chat completions, and counted in served.
type lunaDecides struct {
	mu      sync.Mutex
	status  int
	body    string
	paths   []string
	served  int
	answers int
	chats   int // the classifier's questions asked through chat
}

func (u *lunaDecides) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path)
	status, body := u.status, u.body
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/chat/completions" && !strings.Contains(string(raw), `"stream":true`) {
		// the classifier asked through chat: as intentUpstream answers it
		n := kindAsked(raw)
		u.mu.Lock()
		u.chats++
		u.mu.Unlock()
		io.WriteString(w, `{"id":"c","object":"chat.completion","model":"gpt-6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"`+n+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":1}}`)
		return
	}
	if r.URL.Path != "/v1/decisions" {
		u.mu.Lock()
		u.served++
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"from luna\"}\n\n"+
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"from luna\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n")
			return
		}
		io.WriteString(w, `data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","content":"from luna"}}]}`+"\n\n"+
			`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		io.WriteString(w, body)
		return
	}
	var q struct {
		Input     any `json:"input"`
		Questions []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Choices []struct {
				Value string `json:"value"`
			} `json:"choices"`
		} `json:"questions"`
	}
	json.Unmarshal(raw, &q)
	said := strings.ToLower(lunaMessage(raw))
	pick := "writing"
	switch {
	case strings.Contains(said, "refactor"):
		pick = "coding"
	case strings.Contains(said, "capital"):
		pick = "question"
	}
	var answers []map[string]any
	for _, x := range q.Questions {
		switch x.Type {
		case "predicate":
			answers = append(answers, map[string]any{"type": "predicate", "name": x.Name, "probability": 0.05})
		case "score":
			answers = append(answers, map[string]any{"type": "score", "name": x.Name, "score": 1.0, "confidence": 0.8})
		case "choice":
			var ps []map[string]any
			for _, c := range x.Choices {
				p := 0.02
				if c.Value == pick {
					p = 0.94
				}
				ps = append(ps, map[string]any{"value": c.Value, "probability": p})
			}
			answers = append(answers, map[string]any{"type": "choice", "name": x.Name, "choice": pick, "confidence": 0.94, "probabilities": ps})
		}
	}
	u.mu.Lock()
	u.answers++
	u.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{"model": "gpt-6-luna", "answers": answers, "usage": map[string]any{"input_tokens": 42, "output_tokens": 0, "total_tokens": 42}})
}

// lunaMessage is the turn's own message in a Decisions request, the
// input_json part's message, not the turns before it.
func lunaMessage(raw []byte) string {
	var q struct {
		Input []struct {
			Content []struct {
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"content"`
		} `json:"input"`
	}
	json.Unmarshal(raw, &q)
	var out string
	for _, m := range q.Input {
		for _, c := range m.Content {
			out += c.Data.Message
		}
	}
	return out
}

// lunaIntentGroup is zyh6036's group (X) with gpt-6-luna of their OpenAI
// API key as its classifier, and a member too, the first: routed by
// intent, the effort Auto.
func lunaIntentGroup(t *testing.T) (*intentUpstream, *lunaDecides) {
	t.Helper()
	u := intentGroup(t, "2")
	decisionsOffForTest(t)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	luna := &lunaDecides{}
	ou := httptest.NewServer(luna)
	t.Cleanup(ou.Close)
	if err := provider.Save(provider.Provider{ID: "openai", Name: "OpenAI", Key: "sk-openai", Chat: "http://api.openai.com/v1", Proxy: ou.URL, Models: []string{"gpt-6-luna"}}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := provider.FindGroup("group/intent")
	g.Members = append([]string{"openai/gpt-6-luna"}, g.Members...)
	g.Classifier = "openai/gpt-6-luna"
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	return u, luna
}

// zyh6036 on X, after 9c68fed95: gpt-6-luna of their OpenAI API key as
// the group's classifier, Codex's turns showed no classifier at all and
// all went where luna is. Each Codex turn is asked at the Decisions API,
// its answer on the route, and goes to its intent's member.
func TestCodexTurnsClassifiedByLuna(t *testing.T) {
	for _, path := range []string{"/v1/responses", CodexPath + "/responses"} {
		t.Run(path, func(t *testing.T) {
			u, luna := lunaIntentGroup(t)
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
				r := lastRoute(s)
				var c Classified
				if r.Rule != nil && r.Rule.Classified != nil {
					c = *r.Rule.Classified
				}
				luna.mu.Lock()
				t.Logf("turn %d: served %q, luna served %d, luna paths %q, rule %+v, classified %+v", i+1, u.last(), luna.served, luna.paths, r.Rule, c)
				luna.mu.Unlock()
				if got := u.last(); got != turn.want || c.By != "openai/gpt-6-luna" || c.Error != "" {
					t.Errorf("Codex turn %d went to %s, want %s; classified %+v", i+1, got, turn.want, c)
				}
			}
		})
	}
}

// A Decisions call OpenAI turns away for the key's billing leaves its
// record, the routing classifier's, with the error, beside the route's,
// and the turn goes as the group's rules say with no intent told: by the
// group's order, past the key resting for it.
func TestCodexLunaDecisionsRefusedRecorded(t *testing.T) {
	u, luna := lunaIntentGroup(t)
	luna.status, luna.body = 429, `{"error":{"message":"Your account is not active, please check your billing details on our website.","type":"billing_not_active","param":null,"code":"billing_not_active"}}`
	s := New()
	h, body, input := codexFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(withInput(body, input[:codexTurns[0].items]))))
	req.Header = h.Clone()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	r := lastRoute(s)
	if r.Rule == nil || r.Rule.Classified == nil {
		t.Fatalf("no classifier on the route: %+v", r.Rule)
	}
	if c := r.Rule.Classified; c.By != "openai/gpt-6-luna" || !strings.Contains(c.Error, "billing") || c.Intent != "" {
		t.Fatalf("classified %+v", c)
	}
	luna.mu.Lock()
	served := luna.served
	luna.mu.Unlock()
	// out of billing: the key rests, its gpt-6-luna with it, and the turn
	// goes to the next of the group's order
	if got := u.last(); got != "flash" || served != 0 {
		t.Fatalf("went to %q (luna served %d), not the group's next, flash", got, served)
	}
	var own []usage.Record
	for _, x := range usage.Load(time.Time{}) {
		if x.Provider == "openai" && x.Kind == "routing" {
			own = append(own, x)
		}
	}
	if len(own) == 0 || own[0].Status != 429 || !own[0].ByMagpie() || own[0].Model != "gpt-6-luna" {
		t.Fatalf("the classifier's records %+v", own)
	}
}

// siwcLuna stands in for api.openai.com for a ChatGPT API sign-in
// (Sign in with ChatGPT): Responses answers a classifier's question as
// intentUpstream does, by the message's words, and any other turn as
// luna; /v1/decisions answers as lunaDecides does.
type siwcLuna struct {
	lunaDecides
	asked []string
}

// kindAsked is the answer to a classifier's question asked through chat
// or Responses, raw its request: the level 2 when asked how hard, else
// the kind by the message's words, as intentUpstream says; "" when raw is
// no classifier's question.
func kindAsked(raw []byte) string {
	var v any
	json.Unmarshal(raw, &v)
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			b.WriteString(v)
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	said := b.String()
	switch {
	case strings.Contains(said, "Levels:"):
		return "2"
	case strings.Contains(said, "Kinds:"):
		if m := classifiedMessage.FindStringSubmatch(said); m != nil {
			switch msg := strings.ToLower(m[1]); {
			case strings.Contains(msg, "refactor"):
				return "1"
			case strings.Contains(msg, "capital"):
				return "2"
			}
		}
		return "3"
	}
	return ""
}

func (u *siwcLuna) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/decisions" {
		u.lunaDecides.ServeHTTP(w, r)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.paths = append(u.paths, r.URL.Path+" "+r.Header.Get("Authorization"))
	u.mu.Unlock()
	answer := kindAsked(raw)
	u.mu.Lock()
	if answer != "" {
		u.asked = append(u.asked, answer)
	} else {
		u.served++
		answer = "from luna"
	}
	u.mu.Unlock()
	a, _ := json.Marshal(answer)
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"type":"response.created","response":{"id":"r1","model":"gpt-6-luna"}}`+"\n\n"+
		`data: {"type":"response.output_text.delta","delta":`+string(a)+`}`+"\n\n"+
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":`+string(a)+`}]}],"usage":{"input_tokens":7,"output_tokens":1}}}`+"\n\n")
}

// The same group with gpt-6-luna of a ChatGPT API sign-in (Sign in with
// ChatGPT, api.openai.com on the account's token) as its classifier and
// first member: each Codex turn's classifier says, on the route, and the
// turn goes to its intent's member.
func TestCodexTurnsClassifiedBySignedInLuna(t *testing.T) {
	for _, path := range []string{"/v1/responses", CodexPath + "/responses"} {
		t.Run(path, func(t *testing.T) {
			u := intentGroup(t, "2")
			for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
				t.Setenv(k, "")
			}
			luna := &siwcLuna{}
			up := httptest.NewServer(luna)
			t.Cleanup(up.Close)
			t.Cleanup(provider.SIWCForTest(up.URL, up.URL+"/v1"))
			auth, _ := json.Marshal(map[string]any{
				"clientId": "oaiapp_1", "hostId": "urn:uuid:00000000-0000-4000-8000-000000000000", "sub": "user-1",
				"email": "me@example.com", "access": "at-live", "refresh": "rt-live",
				"expires": time.Now().Add(time.Hour), "scopes": []string{"chatgpt.tokens.use.direct"},
			})
			dir := filepath.Dir(provider.Path())
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal([]map[string]any{{
				"agent": provider.ChatGPTAPIID, "user": "me@example.com", "plan": "plus", "on": true, "auth": json.RawMessage(auth),
			}})
			if err := os.WriteFile(filepath.Join(dir, "logins.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			g, _, _ := provider.FindGroup("group/intent")
			g.Members = append([]string{"chatgpt-api/gpt-6-luna"}, g.Members...)
			g.Classifier = "chatgpt-api/gpt-6-luna"
			if err := provider.SaveGroup(g); err != nil {
				t.Fatal(err)
			}
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
				r := lastRoute(s)
				var c Classified
				if r.Rule != nil && r.Rule.Classified != nil {
					c = *r.Rule.Classified
				}
				luna.mu.Lock()
				t.Logf("turn %d: served %q, luna served %d asked %q, paths %q, classified %+v", i+1, u.last(), luna.served, luna.asked, luna.paths, c)
				luna.mu.Unlock()
				if got := u.last(); got != turn.want || c.By != "chatgpt-api/gpt-6-luna" || c.Error != "" {
					t.Errorf("Codex turn %d went to %s, want %s; classified %+v", i+1, got, turn.want, c)
				}
			}
		})
	}
}

// decisionsOffForTest starts t with no key known to be refused by
// OpenAI's Decisions API, and leaves none known after it.
func decisionsOffForTest(t *testing.T) {
	t.Helper()
	clear := func() {
		decisionsOff.Lock()
		decisionsOff.until = map[string]time.Time{}
		decisionsOff.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// When OpenAI's Decisions API doesn't serve the key at all (a project
// without it: 404; a request it reads otherwise: 400), gpt-6-luna is asked
// through chat, as before it was asked there: each Codex turn is told and
// goes to its intent's member, the failed call is on record, the key
// is not left resting, and for the next turns Decisions isn't asked
// again.
func TestCodexLunaDecisionsUnservedAskedByChat(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{
		{404, `{"error":{"message":"Invalid URL (POST /v1/decisions)","type":"invalid_request_error","param":null,"code":null}}`},
		{403, `{"error":{"message":"Project proj_x does not have access to model gpt-6-luna on this endpoint","type":"invalid_request_error","param":null,"code":"model_not_found"}}`},
		{400, `{"error":{"message":"Unknown parameter: 'questions[0].name'.","type":"invalid_request_error","param":"questions[0].name","code":"unknown_parameter"}}`},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			u, luna := lunaIntentGroup(t)
			luna.status, luna.body = tt.status, tt.body
			s := New()
			h, body, input := codexFixture(t)
			for i, turn := range codexTurns {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(withInput(body, input[:turn.items]))))
				req.Header = h.Clone()
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("turn %d: %d %s", i+1, rec.Code, rec.Body.String())
				}
				r := lastRoute(s)
				if r.Rule == nil || r.Rule.Classified == nil {
					t.Fatalf("turn %d: no classifier on the route: %+v", i+1, r.Rule)
				}
				if c := r.Rule.Classified; u.last() != turn.want || c.By != "openai/gpt-6-luna" || c.Error != "" {
					t.Errorf("Codex turn %d went to %s, want %s; classified %+v", i+1, u.last(), turn.want, c)
				}
			}
			luna.mu.Lock()
			decisions := 0
			for _, p := range luna.paths {
				if p == "/v1/decisions" {
					decisions++
				}
			}
			chats := luna.chats
			luna.mu.Unlock()
			if decisions != 1 || chats == 0 {
				t.Fatalf("Decisions asked %d times, chat %d", decisions, chats)
			}
			var failed int
			for _, x := range usage.Load(time.Time{}) {
				if x.Provider == "openai" && x.Kind == "routing" && x.Status == tt.status && x.Error != "" {
					failed++
				}
			}
			if failed != 1 {
				t.Fatalf("%d records of the failed Decisions call, want 1", failed)
			}
			// gpt-6-luna as a member still holds a conversation: the key
			// is not left resting
			noneRested(t, "openai")
			code, out := postAs(t, s, "c1", `{"model":"openai/gpt-6-luna","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(out, "from luna") {
				t.Fatalf("gpt-6-luna after: %d %s", code, out)
			}
		})
	}
}
