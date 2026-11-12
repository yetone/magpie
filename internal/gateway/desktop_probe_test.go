package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// relayNeedsCacheKey answers as CyberCaius's AnyRouter did a request
// without a prompt_cache_key.
func relayNeedsCacheKey(body []byte) (int, string) {
	if gjson.GetBytes(body, "prompt_cache_key").String() == "" {
		return 400, `{"error":{"message":"Arouter: prompt_cache_key is required (request id: x)","type":"invalid_request_error"},"type":"error"}`
	}
	return 0, ""
}

// Claude Desktop checks the gateway as it starts, and from Test
// connection, with {"max_tokens":1,"messages":[{"role":"user",
// "content":"."}]} on the shortest haiku it is listed:
// magpie-0958175816.anthropic.claude-haiku-5-5[1m], leee's haiku, for
// CyberCaius on Discord (v0.1.1838). It goes to Desktop's default model,
// the first listed (his routing group 天翼云), not to leee nor to the
// relay's GPT model a session ran on last, and is answered there. A
// title still goes to that session's model.
func TestClaudeDesktopProbeToDefaultModel(t *testing.T) {
	claude := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	// as AnyRouter answers a GPT model asked for fewer than 16 tokens: the
	// upstream's floor isn't passed on
	relay := &fake{t: t, reply: deepseekReply, refuse: func(body []byte) (int, string) {
		if c, m := relayNeedsCacheKey(body); c != 0 {
			return c, m
		}
		if n := max(gjson.GetBytes(body, "max_tokens").Int(), gjson.GetBytes(body, "max_completion_tokens").Int()); n > 0 && n < 16 {
			return 400, `{"error":{"message":"AnyRouter: bad response status code 400 (request id: x)","type":"new_api_error"}}`
		}
		return 0, ""
	}}
	ctyun := &fake{t: t, reply: deepseekReply}
	up := setup(t, provider.Anthropic, claude)
	ru := httptest.NewServer(relay)
	t.Cleanup(ru.Close)
	cu := httptest.NewServer(ctyun)
	t.Cleanup(cu.Close)
	for _, p := range []provider.Provider{
		{ID: "leee", Name: "leee", Key: "k", Anthropic: up.URL, Models: []string{"claude-haiku-5-5"}},
		{ID: "anyrouter", Name: "AnyRouter", Key: "k", Chat: ru.URL + "/v1", Models: []string{"gpt-6.1-sol"}},
		{ID: "ctyun", Name: "天翼云", Key: "k", Chat: cu.URL + "/v1", Models: []string{"glm-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "tianyi", Name: "天翼云", Members: []string{"ctyun/glm-5"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOrder("claude-desktop", []string{"group/tianyi"}); err != nil {
		t.Fatal(err)
	}
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	srv := New()
	send := func(body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// a session's turn, on the relay's model, before the restart
	if code, body := send(`{"model":"anyrouter/gpt-6.1-sol","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("turn: %d %s", code, body)
	}
	first := gjson.GetBytes(relay.got, "prompt_cache_key").String()
	if first == "" || strings.Contains(strings.ToLower(first), "magpie") {
		t.Fatalf("turn's cache key %q", first)
	}

	// Desktop's check, by the id it was listed leee's haiku by
	const probe = `{"model":"magpie-0958175816.anthropic.claude-haiku-5-5[1m]","max_tokens":1,"messages":[{"role":"user","content":"."}]}`
	relay.calls, relay.got, claude.got = 0, nil, nil
	if code, body := send(probe); code != 200 {
		t.Fatalf("probe: %d %s", code, body)
	}
	if claude.got != nil || relay.calls != 0 || modelOf(ctyun.got) != "glm-5" {
		t.Fatalf("probe went to leee (%s) or the relay (%d calls), not Desktop's default 天翼云 (got %s)", claude.got, relay.calls, ctyun.got)
	}

	// a title still goes to the session's model
	relay.got = nil
	if code, body := send(`{"model":"magpie-0958175816.anthropic.claude-haiku-5-5[1m]","max_tokens":200,"stream":true,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
		t.Fatalf("title: %d %s", code, body)
	}
	if modelOf(relay.got) != "gpt-6.1-sol" {
		t.Fatalf("title went to %q", modelOf(relay.got))
	}
	// the same conversation keeps its key
	if code, body := send(`{"model":"anyrouter/gpt-6.1-sol","max_tokens":32000,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`); code != 200 {
		t.Fatalf("second turn: %d %s", code, body)
	}
	if k := gjson.GetBytes(relay.got, "prompt_cache_key").String(); k != first {
		t.Fatalf("second turn's key %q, first %q", k, first)
	}

	// with the relay's GPT model first listed, the check is answered there
	// at once: asked for a reply OpenAI's models give, as the relay says
	// no floor to ask again at
	if err := provider.SetModelOrder("claude-desktop", []string{"anyrouter/gpt-6.1-sol"}); err != nil {
		t.Fatal(err)
	}
	relay.calls, relay.got, claude.got = 0, nil, nil
	if code, body := send(probe); code != 200 {
		t.Fatalf("probe on the relay: %d %s", code, body)
	}
	if claude.got != nil || relay.calls != 1 || modelOf(relay.got) != "gpt-6.1-sol" {
		t.Fatalf("probe on the relay: %d calls, leee got %s", relay.calls, claude.got)
	}
}

// desktopProbe knows Desktop's check alone, not a title or a turn.
func TestDesktopProbe(t *testing.T) {
	for body, want := range map[string]bool{
		`{"model":"magpie-0958175816.anthropic.claude-haiku-5-5[1m]","max_tokens":1,"messages":[{"role":"user","content":"."}]}`: true,
		`{"model":"x","max_tokens":200,"messages":[{"role":"user","content":"."}]}`:                                              false,
		`{"model":"x","max_tokens":1,"system":"You write short session titles.","messages":[{"role":"user","content":"."}]}`:     false,
		`{"model":"x","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`:                                               false,
		`{"model":"x","max_tokens":1,"tools":[{"name":"a"}],"messages":[{"role":"user","content":"."}]}`:                         false,
	} {
		if desktopProbe([]byte(body)) != want {
			t.Errorf("desktopProbe(%s) = %v", body, !want)
		}
	}
}

// magpie's own cache key goes only to OpenAI's API and a provider the user
// added by its address; a vendor's preset, a signed-in account and a
// plugin get none. A relay that turns the field away is asked again
// without it, once.
func TestOwnCacheKey(t *testing.T) {
	for _, c := range []struct {
		p    provider.Provider
		want bool
	}{
		{provider.Provider{ID: "relay", Chat: "https://relay.example/v1"}, true},
		{provider.Provider{ID: "openai", Preset: "openai", Responses: "https://api.openai.com/v1"}, true},
		{provider.Provider{ID: "deepseek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1"}, false},
		{provider.Provider{ID: "codex", Account: &provider.Account{}, Responses: "https://chatgpt.com/backend-api/codex"}, false},
		{provider.Provider{ID: "relay-own", Chat: "https://relay.example/v1", ExtraBody: provider.ExtraBody{"prompt_cache_key": json.RawMessage(`"${sessionId}"`)}}, false},
		{provider.Provider{ID: "relay-forced", Chat: "https://relay.example/v1", ExtraBody: provider.ExtraBody{"!prompt_cache_key": json.RawMessage(`"k"`)}}, false},
	} {
		if got := ownCacheKey(c.p); got != c.want {
			t.Errorf("ownCacheKey(%s) = %v", c.p.ID, got)
		}
	}

	refusing := func(body []byte) (int, string) {
		if gjson.GetBytes(body, "prompt_cache_key").Exists() {
			return 400, `{"error":{"message":"Unrecognized request argument supplied: prompt_cache_key"}}`
		}
		return 0, ""
	}
	f := &fake{t: t, reply: deepseekReply, refuse: refusing}
	setup(t, provider.Chat, f)
	srv := New()
	for turn := range 2 {
		calls := f.calls
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"fake/m1","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("x-api-key", Token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", turn+1, rec.Code, rec.Body)
		}
		if n, want := f.calls-calls, 2-turn; n != want {
			t.Errorf("turn %d: %d upstream calls, want %d", turn+1, n, want)
		}
	}
}

// Claude Desktop's check through a routing group with intent rules is
// answered by the group's order, not after its classifier: the check is
// no user's turn, and Desktop gives it 10 seconds as it starts. Asked of
// the classifier, the first check after a start waited up to the
// Classifier timeout (8s) on a ".", and Desktop cancelled it as a 499;
// the second passed on the verdict kept (CyberCaius on Discord,
// v0.1.1847). A user's turn from Desktop still asks it.
func TestClaudeDesktopProbeAsksNoClassifier(t *testing.T) {
	resetClassified(t)
	s, a, _, c := intented(t, provider.Rule{Use: "b/big", Intent: "writing or fixing tests"})
	c.set("1", 0)
	if err := provider.SetModelOrder("claude-desktop", []string{"group/r"}); err != nil {
		t.Fatal(err)
	}
	desktopPicked.Lock()
	desktopPicked.model, desktopPicked.from = "", ""
	desktopPicked.Unlock()
	send := func(body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	const probe = `{"model":"magpie-claude-haiku-5-5","max_tokens":1,"messages":[{"role":"user","content":"."}]}`
	if code, body := send(probe); code != 200 {
		t.Fatalf("probe: %d %s", code, body)
	}
	if n := c.n(); n != 0 {
		t.Fatalf("the check asked the classifier %d times: %s", n, c.last())
	}
	r := lastRoute(s)
	if a.last == "" || r.Rule == nil || r.Rule.Classified == nil || !strings.Contains(r.Rule.Classified.Error, "Claude Desktop's connection check") {
		t.Fatalf("not answered by the group's first member, or the trace doesn't say why: %q %+v", a.last, r.Rule)
	}
	// The Routing page shows the id Desktop named (its first listed model
	// when none is Claude-named), not the model chosen in the
	// conversation, so the row says it is the check (cybercaius on
	// Discord: "requested deepseek-flash" while gpt-6.1-sol answered).
	if r.Kind != KindDesktopCheck || usage.PurposeOf(r.Kind) != "kind:connection_check" {
		t.Fatalf("the check is recorded as kind %q (%s), not %q", r.Kind, usage.PurposeOf(r.Kind), KindDesktopCheck)
	}
	// a user's turn through the same group is still classified
	if code, body := send(`{"model":"group/r","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"add a unit test for the parser"}]}`); code != 200 {
		t.Fatalf("turn: %d %s", code, body)
	}
	if c.n() != 1 {
		t.Fatalf("a user's turn asked the classifier %d times", c.n())
	}
	if k := lastRoute(s).Kind; k != "" {
		t.Fatalf("a user's turn is recorded as kind %q", k)
	}
}

// ctyunRefusal is the body Desktop showed (cybercaius, v0.1.1862) as
// magpie's message after "天翼云: ": a relay's error whose message is
// its own upstream's refusal. The 天翼云 relay wraps that whole body as
// its message in turn, so magpie's reading of it said it word for word.
const ctyunRefusal = `{"error":{"message":"{\"code\":\"400\",\"message\":\"thinking.type must be \\\"enabled\\\" (type: invalid_request_error)\",\"type\":\"invalid_request_error\"} (request id: 20261028170149123456789)","type":"upstream_error","code":"Invalid_request"}}`

// ctyunNeedsThinking answers as cybercaius's 天翼云 did a Messages request
// whose thinking isn't enabled.
func ctyunNeedsThinking(body []byte) (int, string) {
	if gjson.GetBytes(body, "thinking.type").String() != "enabled" {
		return ctyunRefused()
	}
	if b, m := gjson.GetBytes(body, "thinking.budget_tokens").Int(), gjson.GetBytes(body, "max_tokens").Int(); b < 1024 || m <= b {
		return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens must be greater than thinking.budget_tokens"}}`
	}
	return 0, ""
}

func ctyunRefused() (int, string) {
	out, _ := json.Marshal(map[string]any{"error": map[string]any{"message": ctyunRefusal, "type": "upstream_error", "code": "Invalid_request"}})
	return 400, string(out)
}

// Desktop's check to a routing group whose member's upstream takes the
// model only thinking (cybercaius, v0.1.1862: "thinking.type must be
// \"enabled\""): magpie sent it thinking off (a vendor's model) or none
// (a Claude's name), the refusal is read, and the check asked again
// thinking at 1024 under a raised max_tokens, so Desktop gets 200. It is
// remembered for the model: the next check, and a title, go thinking in
// one call. Read off the reply: the provider's name has no part in it.
func TestClaudeDesktopProbeThinkingNeeded(t *testing.T) {
	const msg = `{"id":"m","type":"message","role":"assistant","model":"x","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":3}}`
	for _, c := range []struct {
		name, model string
		proto       provider.Protocol
	}{
		{"vendor model on Messages", "glm-5", provider.Anthropic},
		{"claude name on Messages", "claude-haiku-5-5", provider.Anthropic},
		{"chat", "glm-5", provider.Chat},
		{"chat, refused in its stream", "glm-5", provider.Chat},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctyun := &fake{t: t, reply: msg, ctype: "application/json", refuse: ctyunNeedsThinking}
			if c.proto == provider.Chat {
				// a relay that turns reasoning_effort into thinking enabled
				ctyun = &fake{t: t, reply: deepseekReply, refuse: func(body []byte) (int, string) {
					if gjson.GetBytes(body, "reasoning_effort").String() == "" || gjson.GetBytes(body, "reasoning_effort").String() == "none" {
						return ctyunRefused()
					}
					return 0, ""
				}}
			}
			var h http.Handler = ctyun
			if strings.HasSuffix(c.name, "in its stream") {
				// the refusal as an event of a 200 stream
				h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					ctyun.got = b
					ctyun.calls++
					if e := gjson.GetBytes(b, "reasoning_effort").String(); e == "" || e == "none" {
						_, refusal := ctyunRefused()
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: "+refusal+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, deepseekReply)
				})
			}
			setup(t, provider.Anthropic, &fake{t: t})
			cu := httptest.NewServer(h)
			t.Cleanup(cu.Close)
			p := provider.Provider{ID: "ctyun", Name: "天翼云", Key: "k", Models: []string{c.model}}
			if c.proto == provider.Chat {
				p.Chat = cu.URL + "/v1"
			} else {
				p.Anthropic = cu.URL
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := provider.SaveGroup(provider.Group{ID: "tianyi", Name: "天翼云", Members: []string{"ctyun/" + c.model}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetModelOrder("claude-desktop", []string{"group/tianyi"}); err != nil {
				t.Fatal(err)
			}
			desktopPicked.Lock()
			desktopPicked.model, desktopPicked.from = "", ""
			desktopPicked.Unlock()
			srv := New()
			send := func(body string) (int, string) {
				t.Helper()
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				req.Header.Set("x-api-key", Token+"-claude-desktop")
				req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				return rec.Code, rec.Body.String()
			}
			const probe = `{"model":"magpie-0958175816.anthropic.claude-haiku-5-5[1m]","max_tokens":1,"messages":[{"role":"user","content":"."}]}`
			if code, body := send(probe); code != 200 {
				t.Fatalf("check: %d %s (sent %s)", code, body, ctyun.got)
			}
			if ctyun.calls != 2 {
				t.Fatalf("check: %d calls, want the refusal and one more", ctyun.calls)
			}
			if c.proto == provider.Anthropic {
				if th := gjson.GetBytes(ctyun.got, "thinking"); th.Get("type").String() != "enabled" || th.Get("budget_tokens").Int() != 1024 || gjson.GetBytes(ctyun.got, "max_tokens").Int() <= 1024 {
					t.Fatalf("check asked again with %s", ctyun.got)
				}
			}
			// remembered: the next check and a title go thinking at once
			for _, body := range []string{probe, `{"model":"ctyun/` + c.model + `","max_tokens":200,"system":"You write short session titles.","messages":[{"role":"user","content":"hi"}]}`} {
				ctyun.calls = 0
				if code, out := send(body); code != 200 || ctyun.calls != 1 {
					t.Fatalf("after: %d in %d calls, %s (sent %s)", code, ctyun.calls, out, ctyun.got)
				}
			}
		})
	}
}

// withThinkingEnabled leaves a request that thinks as it is, and gives
// one that doesn't the least budget under a limit raised past it.
func TestWithThinkingEnabled(t *testing.T) {
	for in, want := range map[string]string{
		`{"max_tokens":16,"temperature":0}`:                    `{"max_tokens":1040,"thinking":{"budget_tokens":1024,"type":"enabled"}}`,
		`{"max_tokens":8000,"thinking":{"type":"disabled"}}`:   `{"max_tokens":8000,"thinking":{"budget_tokens":1024,"type":"enabled"}}`,
		`{"max_tokens":2,"thinking":{"type":"between_tools"}}`: `{"max_tokens":1026,"thinking":{"budget_tokens":1024,"type":"enabled"}}`,
	} {
		got, ok := withThinkingEnabled([]byte(in))
		var a, b any
		if !ok || json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !jsonEqual(a, b) {
			t.Errorf("withThinkingEnabled(%s) = %s %v, want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{`{"thinking":{"type":"enabled","budget_tokens":2048}}`, `{"thinking":{"type":"adaptive"}}`, `not json`} {
		if _, ok := withThinkingEnabled([]byte(in)); ok {
			t.Errorf("withThinkingEnabled(%s) changed it", in)
		}
	}
}

// Claude Desktop's check goes to the model the user chose in its own
// model menu, kept in Claude-3p's cowork_account_settings.json
// (__model_selector_state), not to the first model listed: cybercaius
// on Discord (v0.1.2033) had DeepSeek in the Code tab and Cowork off,
// and the check as Desktop started went to another provider's model.
// The menu the last session ran on comes first; a choice magpie no
// longer serves, or no file, leaves it to the first listed.
func TestClaudeDesktopProbeToChosenModel(t *testing.T) {
	claude := &fake{t: t, reply: sse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)}
	ds := &fake{t: t, reply: deepseekReply}
	kimi := &fake{t: t, reply: deepseekReply}
	up := setup(t, provider.Anthropic, claude)
	du := httptest.NewServer(ds)
	t.Cleanup(du.Close)
	ku := httptest.NewServer(kimi)
	t.Cleanup(ku.Close)
	for _, p := range []provider.Provider{
		{ID: "leee", Name: "leee", Key: "k", Anthropic: up.URL, Models: []string{"claude-haiku-5-5"}},
		{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: du.URL + "/v1", Models: []string{"deepseek-v4-flash"}},
		{ID: "kimi", Name: "Kimi", Key: "k", Chat: ku.URL + "/v1", Models: []string{"kimi-k3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// leee's haiku is listed first: Desktop's default
	if err := provider.SetModelOrder("claude-desktop", []string{"leee/claude-haiku-5-5", "deepseek/deepseek-v4-flash", "kimi/kimi-k3"}); err != nil {
		t.Fatal(err)
	}
	idOf := func(id string) string {
		t.Helper()
		for _, e := range provider.Catalog() {
			if e.ID == id {
				return DesktopID(e)
			}
		}
		t.Fatalf("%s not in the catalog", id)
		return ""
	}
	haiku, dsID, kimiID := idOf("leee/claude-haiku-5-5"), idOf("deepseek/deepseek-v4-flash"), idOf("kimi/kimi-k3")
	root := t.TempDir()
	old := desktopSelectorRoots
	desktopSelectorRoots = func() []string { return []string{root} }
	t.Cleanup(func() { desktopSelectorRoots = old })
	dir := filepath.Join(root, "local-agent-mode-sessions", "c774b2e6", "00000000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// the menus are written at the hour, a turn sent at a minute past or
	// before it: no two of them in one tick of the file system's clock
	hour := time.Now().Add(-time.Hour).Truncate(time.Second)
	file := filepath.Join(dir, "cowork_account_settings.json")
	choose := func(state string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(`{"__created_at":1,"__model_selector_state":`+state+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, hour, hour); err != nil {
			t.Fatal(err)
		}
	}
	pickAt := func(m string, at time.Time) {
		desktopPicked.Lock()
		desktopPicked.model, desktopPicked.from, desktopPicked.at = m, desktopPickedPath(), at
		desktopPicked.Unlock()
	}
	pick := func(m string) { pickAt(m, hour.Add(time.Minute)) }
	pick("")
	srv := New()
	probe := `{"model":"` + haiku + `","max_tokens":1,"messages":[{"role":"user","content":"."}]}`
	check := func(want *fake, wantModel, why string) {
		t.Helper()
		claude.got, ds.got, kimi.got = nil, nil, nil
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(probe))
		req.Header.Set("x-api-key", Token+"-claude-desktop")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: probe %d %s", why, rec.Code, rec.Body.String())
		}
		for _, f := range []*fake{claude, ds, kimi} {
			if f != want && f.got != nil {
				t.Fatalf("%s: the check went elsewhere (%s)", why, f.got)
			}
		}
		if want == claude {
			if claude.got == nil {
				t.Fatalf("%s: the check didn't reach leee", why)
			}
			return
		}
		if modelOf(want.got) != wantModel {
			t.Fatalf("%s: the check reached %q, want %q", why, modelOf(want.got), wantModel)
		}
	}

	// no menu chosen yet: the first listed, as before
	check(claude, "", "no choice")
	// the reporter's: DeepSeek in the Code tab, Cowork left on its default
	choose(`{"code":{"model":"` + dsID + `"}}`)
	check(ds, "deepseek-v4-flash", "Code tab on DeepSeek")
	// by the id with Claude Code's "[1m]" too
	choose(`{"code":{"model":"` + dsID + `[1m]"},"chat":{"model":"` + haiku + `"}}`)
	check(ds, "deepseek-v4-flash", "Code tab on DeepSeek [1m]")
	// two menus: the Code tab's, unless the last session ran on the other
	choose(`{"cowork":{"model":"` + kimiID + `"},"code":{"model":"` + dsID + `"}}`)
	check(ds, "deepseek-v4-flash", "Code before Cowork")
	pick(kimiID)
	check(kimi, "kimi-k3", "the last session's menu first")
	// the reporter's (v0.1.2212): a chat on DeepSeek, then the Code tab
	// switched to Kimi while Cowork stays on DeepSeek; the check follows
	// the switch, not the turn sent before it
	pickAt(dsID, hour.Add(-time.Minute))
	choose(`{"code":{"model":"` + kimiID + `"},"cowork":{"model":"` + dsID + `"}}`)
	check(kimi, "kimi-k3", "Code tab switched off the last session's model")
	// a session switched to Kimi after the menu was set to DeepSeek: the
	// model the user is on now
	choose(`{"code":{"model":"` + dsID + `"}}`)
	pick(kimiID)
	check(kimi, "kimi-k3", "a turn after the menu was set")
	// such a turn on a model magpie no longer serves leaves it to the menu
	pick("anthropic/magpie-0000000001")
	check(ds, "deepseek-v4-flash", "a newer turn on an unserved model")
	pick("")
	// a choice magpie no longer serves leaves it to the first listed
	choose(`{"code":{"model":"anthropic/magpie-0000000001"}}`)
	check(claude, "", "unserved choice")
	// a half-written file is no choice
	choose(`{"code":{"model":"` + dsID)
	check(claude, "", "unparsed file")
}
