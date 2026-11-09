package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

const ownPrompt = "Answer in Chinese."

// liucl on X: the text the user gave a model goes after the agent's own
// system prompt, in each API's own place for it, the agent's prompt and
// the rest of the body as the agent wrote them.
func TestModelSystemAfterTheAgentsPrompt(t *testing.T) {
	for _, c := range []struct {
		name string
		from provider.Protocol
		in   string
		want string
	}{
		{"anthropic none", provider.Anthropic,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"system":"Answer in Chinese.","model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"anthropic null", provider.Anthropic,
			`{"model":"m","system":null,"messages":[]}`,
			`{"model":"m","system":"Answer in Chinese.","messages":[]}`},
		{"anthropic string", provider.Anthropic,
			`{"model":"m","system":"You are Claude Code.","messages":[]}`,
			`{"model":"m","system":"You are Claude Code.\n\nAnswer in Chinese.","messages":[]}`},
		{"anthropic blocks", provider.Anthropic,
			`{"model":"m","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Rules.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			`{"model":"m","system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Rules.","cache_control":{"type":"ephemeral"}},{"text":"\n\nAnswer in Chinese.","type":"text"}],"messages":[]}`},
		{"chat none", provider.Chat,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"content":"Answer in Chinese.","role":"system"},{"role":"user","content":"hi"}]}`},
		{"chat run of system and developer", provider.Chat,
			`{"model":"m","messages":[{"role":"system","content":"A"},{"role":"developer","content":"B"},{"role":"user","content":"hi"},{"role":"system","content":"late"}]}`,
			`{"model":"m","messages":[{"role":"system","content":"A"},{"role":"developer","content":"B\n\nAnswer in Chinese."},{"role":"user","content":"hi"},{"role":"system","content":"late"}]}`},
		{"chat parts", provider.Chat,
			`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"A"}]},{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"A"},{"text":"\n\nAnswer in Chinese.","type":"text"}]},{"role":"user","content":"hi"}]}`},
		{"responses string input", provider.Responses,
			`{"model":"m","instructions":"base","input":"hi"}`,
			`{"model":"m","instructions":"base","input":[{"content":[{"text":"Answer in Chinese.","type":"input_text"}],"role":"developer","type":"message"},{"content":"hi","role":"user"}]}`},
		{"responses after the leading developer items", provider.Responses,
			`{"model":"m","instructions":"base","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"perms"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			`{"model":"m","instructions":"base","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"perms"}]},{"content":[{"text":"Answer in Chinese.","type":"input_text"}],"role":"developer","type":"message"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
		{"responses with none first", provider.Responses,
			`{"model":"m","input":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","input":[{"content":[{"text":"Answer in Chinese.","type":"input_text"}],"role":"developer","type":"message"},{"role":"user","content":"hi"}]}`},
		{"responses on from a stored response", provider.Responses,
			`{"model":"m","previous_response_id":"resp_1","input":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","previous_response_id":"resp_1","input":[{"role":"user","content":"hi"}]}`},
		{"gemini none", provider.Gemini,
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			`{"systemInstruction":{"parts":[{"text":"Answer in Chinese."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"gemini parts", provider.Gemini,
			`{"systemInstruction":{"parts":[{"text":"A"}]},"contents":[]}`,
			`{"systemInstruction":{"parts":[{"text":"A"},{"text":"\n\nAnswer in Chinese."}]},"contents":[]}`},
		{"gemini snake case", provider.Gemini,
			`{"system_instruction":{"parts":[{"text":"A"}]},"contents":[]}`,
			`{"system_instruction":{"parts":[{"text":"A"},{"text":"\n\nAnswer in Chinese."}]},"contents":[]}`},
		{"gemini null", provider.Gemini,
			`{"systemInstruction":null,"contents":[]}`,
			`{"systemInstruction":{"parts":[{"text":"Answer in Chinese."}]},"contents":[]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := string(addSystem(c.from, []byte(c.in), ownPrompt))
			if got != c.want {
				t.Errorf("got\n%s\nwant\n%s", got, c.want)
			}
			if !gjson.Valid(got) {
				t.Errorf("not JSON: %s", got)
			}
		})
	}
}

// No text, or a body that isn't a JSON object, is sent byte for byte.
func TestModelSystemNoneLeavesTheBody(t *testing.T) {
	for _, from := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses, provider.Gemini} {
		for _, in := range []string{
			`{ "model" : "m", "system":"S", "messages":[ {"role":"system","content":"S"} ], "input":"hi" }`,
			`not json`, `[1]`,
		} {
			text := ownPrompt
			if strings.HasPrefix(in, "{") {
				text = ""
			}
			if got := string(addSystem(from, []byte(in), text)); got != in {
				t.Errorf("%s %q: changed to %q", from, in, got)
			}
		}
	}
}

// magpie's own asks go without it.
func TestModelSystemNotOnMagpiesOwnAsks(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:9/v1", Models: []string{"sol"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelSystemPrompt("fake/sol", ownPrompt); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"sol","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	if got := withModelSystem(agentTurn(r), p, "sol", provider.Chat, body); !strings.Contains(string(got), ownPrompt) {
		t.Errorf("an agent's turn went without it: %s", got)
	}
	if got := withModelSystem(r.WithContext(ownAsk(r.Context())), p, "sol", provider.Chat, body); string(got) != string(body) {
		t.Errorf("magpie's own ask was sent it: %s", got)
	}
	if magpiesOwnAsk(r) || magpiesOwnAsk(agentTurn(r)) {
		t.Error("an agent's request taken for magpie's own")
	}
	if !magpiesOwnAsk(r.WithContext(ownAsk(r.Context()))) {
		t.Error("search and vision stand-ins are sent it")
	}
	if !magpiesOwnAsk(r.WithContext(context.WithValue(r.Context(), titleCheckKey{}, func(string) string { return "" }))) {
		t.Error("a title is sent it")
	}
	cl := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	cl.Header.Set("User-Agent", RouterAgent)
	if !magpiesOwnAsk(cl) {
		t.Error("the router's classifier is sent it")
	}
}

// bodies is a vendor that keeps every body it is sent and answers in
// its API.
type bodies struct {
	mu   sync.Mutex
	seen []string
}

func (b *bodies) last() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.seen) == 0 {
		return ""
	}
	return b.seen[len(b.seen)-1]
}

func (b *bodies) vendor(api provider.Protocol) http.HandlerFunc {
	reply := vendorReply(api, 200, "x")
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.seen = append(b.seen, string(raw))
		b.mu.Unlock()
		switch {
		case api == provider.Responses:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","model":"sol","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case strings.HasSuffix(r.URL.Path, "/count_tokens"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"input_tokens":7}`)
		default:
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			reply(w, r)
		}
	}
}

// The model's own text reaches its vendor after the agent's prompt on
// every route, relayed or translated, and the provider's other model is
// sent the agent's prompt alone.
func TestModelSystemReachesItsModelOnEveryRoute(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		vendor           provider.Protocol
		// the agent's prompt and the model's, where the vendor reads them
		sys func(string) string
	}{
		{"messages relayed", "/v1/messages", `{"model":"fake/MODEL","max_tokens":10,"system":"AGENT","messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic,
			func(b string) string { return gjson.Get(b, "system").String() }},
		{"messages to chat", "/v1/messages", `{"model":"fake/MODEL","max_tokens":10,"system":[{"type":"text","text":"AGENT"}],"messages":[{"role":"user","content":"hi"}]}`, provider.Chat,
			func(b string) string { return chatSystem(b) }},
		{"chat relayed", "/v1/chat/completions", `{"model":"fake/MODEL","messages":[{"role":"system","content":"AGENT"},{"role":"user","content":"hi"}]}`, provider.Chat,
			func(b string) string { return chatSystem(b) }},
		{"chat stream to messages", "/v1/chat/completions", `{"model":"fake/MODEL","stream":true,"messages":[{"role":"system","content":"AGENT"},{"role":"user","content":"hi"}]}`, provider.Anthropic,
			func(b string) string { return anthropicSystem(b) }},
		{"responses relayed", "/v1/responses", `{"model":"fake/MODEL","instructions":"AGENT","input":"hi"}`, provider.Responses,
			func(b string) string {
				return gjson.Get(b, "instructions").String() + "|" + gjson.Get(b, `input.#(role=="developer").content.0.text`).String()
			}},
		{"responses to chat", "/v1/responses", `{"model":"fake/MODEL","instructions":"AGENT","input":"hi"}`, provider.Chat,
			func(b string) string { return chatSystem(b) }},
		{"gemini to chat", "/v1beta/models/fake/MODEL:generateContent", `{"systemInstruction":{"parts":[{"text":"AGENT"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, provider.Chat,
			func(b string) string { return chatSystem(b) }},
		{"count_tokens", "/v1/messages/count_tokens", `{"model":"fake/MODEL","system":"AGENT","messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic,
			func(b string) string { return gjson.Get(b, "system").String() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			up := &bodies{}
			srv := httptest.NewServer(up.vendor(c.vendor))
			t.Cleanup(srv.Close)
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "sk-upstream-0123456789", Models: []string{"sol", "luna"}}
			switch c.vendor {
			case provider.Chat:
				p.Chat = srv.URL + "/v1"
			case provider.Responses:
				p.Responses = srv.URL + "/v1"
			default:
				p.Anthropic = srv.URL
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetModelSystemPrompt("fake/sol", "  "+ownPrompt+"\n"); err != nil {
				t.Fatal(err)
			}
			send := func(model string) string {
				t.Helper()
				rec := httptest.NewRecorder()
				r := httptest.NewRequest("POST", strings.Replace(c.path, "MODEL", model, 1), strings.NewReader(strings.Replace(c.body, "MODEL", model, 1)))
				New().Handler().ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", model, rec.Code, rec.Body)
				}
				b := up.last()
				if b == "" {
					t.Fatalf("%s: the vendor was sent nothing", model)
				}
				return b
			}
			b := send("sol")
			sys := c.sys(b)
			at, own := strings.Index(sys, "AGENT"), strings.Index(sys, ownPrompt)
			if at < 0 || own < 0 || own < at {
				t.Errorf("sol: the vendor read the system as %q, want the agent's then %q\n%s", sys, ownPrompt, b)
			}
			if strings.Count(b, ownPrompt) != 1 {
				t.Errorf("sol: the model's prompt went %d times\n%s", strings.Count(b, ownPrompt), b)
			}
			if b := send("luna"); strings.Contains(b, ownPrompt) {
				t.Errorf("luna was sent sol's prompt:\n%s", b)
			}
		})
	}
}

// chatSystem is the text of a Chat request's system and developer
// messages, in order.
func chatSystem(b string) string {
	var out []string
	for _, m := range gjson.Get(b, "messages").Array() {
		if isSystemRole(m) {
			out = append(out, m.Get("content").String())
		}
	}
	return strings.Join(out, "|")
}

// anthropicSystem is the text of an Anthropic request's system.
func anthropicSystem(b string) string {
	s := gjson.Get(b, "system")
	if s.IsArray() {
		var out []string
		for _, x := range s.Array() {
			out = append(out, x.Get("text").String())
		}
		return strings.Join(out, "|")
	}
	return s.String()
}

// A model with no prompt of its own is sent the body as the agent wrote
// it: the same bytes with and without another model's prompt set.
func TestModelSystemOtherModelSentAsBefore(t *testing.T) {
	fresh(t)
	up := &bodies{}
	rhVendor(t, provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"sol", "luna"}}, provider.Chat, up.vendor(provider.Chat))
	body := `{"model":"fake/luna","messages":[{"role":"system","content":"AGENT"},{"role":"user","content":"hi"}]}`
	send := func() string {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return up.last()
	}
	before := send()
	if err := provider.SetModelSystemPrompt("fake/sol", ownPrompt); err != nil {
		t.Fatal(err)
	}
	if after := send(); after != before {
		t.Errorf("luna's body changed when sol was given a prompt:\nbefore %s\nafter  %s", before, after)
	}
	if err := provider.SetModelSystemPrompt("fake/sol", ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.ModelSystemPrompt("fake", "sol"); got != "" {
		t.Errorf("an empty prompt kept %q", got)
	}
}

// A plugin's model is sent it too: it is added before the plugin's fetch.
func TestModelSystemReachesAPluginsModel(t *testing.T) {
	up := &bodies{}
	pid := besideFake(t, "opencode-zen-free", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.seen = append(up.seen, string(raw))
		up.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, rhChatSSE)
	}))
	if err := provider.SetModelSystemPrompt(pid+"/fake-1", ownPrompt); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+pid+`/fake-1","max_tokens":10,"system":"AGENT","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer magpie")
	New().Handler().ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := up.last()
	sys := chatSystem(b)
	if !gjson.Get(b, "messages").Exists() {
		// the fake plugin asks its vendor on Responses
		sys = gjson.Get(b, "instructions").String() + "|" + gjson.Get(b, `input.#(role=="developer")#.content.0.text`).String()
	}
	if strings.Count(b, ownPrompt) != 1 {
		t.Errorf("the plugin's fetch got the prompt %d times\n%s", strings.Count(b, ownPrompt), b)
	}
	if at, own := strings.Index(sys, "AGENT"), strings.Index(sys, ownPrompt); at < 0 || own < at {
		t.Errorf("the plugin's fetch read the system as %q\n%s", sys, b)
	}
}
