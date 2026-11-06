package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A custom API-key relay's failure reaches the agent as the relay said it:
// its status and its words, whatever the request's size, on every route a
// client asks by and every protocol the relay speaks, held or streamed.
// magpie only translates the protocol; it never makes it an overflow the
// relay didn't say (#5486, NEKO-CwC: ne9ko9's Responses relay had a 502 at
// 385K told to Codex as more than gpt-6-astra's 272K, and the 400/413/422
// inference stayed for every provider after v0.1.3141).
func TestRelayFailureKeepsItsOwnWords(t *testing.T) {
	big := strings.Repeat("word ", 2400) // about 3000 tokens, past 1.2 of the window of 2000
	asks := []struct{ name, path, body string }{
		{"responses", "/v1/responses", `{"model":"fake/m1","input":"` + big + `"}`},
		{"responses streamed", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"` + big + `"}`},
		{"codex", CodexPath + "/responses", `{"model":"fake/m1","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"` + big + `"}]}]}`},
		{"chat", "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"` + big + `"}]}`},
		{"chat streamed", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"` + big + `"}]}`},
		{"messages", "/v1/messages", `{"model":"fake/m1","max_tokens":10,"messages":[{"role":"user","content":"` + big + `"}]}`},
		{"messages streamed", "/v1/messages", `{"model":"fake/m1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"` + big + `"}]}`},
	}
	type failing struct {
		status int
		reply  string
		said   string
		protos []provider.Protocol
	}
	all := []provider.Protocol{provider.Responses, provider.Chat, provider.Anthropic}
	for _, fl := range []failing{
		{400, `{"error":{"message":"Invalid request parameters","type":"invalid_request_error"}}`, "Invalid request parameters", all},
		{400, `{"error":{"message":"Error in upstream response"}}`, "Error in upstream response", all},
		{413, `{"error":{"message":"Request Entity Too Large"}}`, "Request Entity Too Large", all},
		{422, `{"error":{"message":"Unprocessable request"}}`, "Unprocessable request", all},
		// the reporter's 502, and a busy 503: asked again, then handed on
		{502, `{"code":"upstream_error","message":"Upstream service temporarily unavailable"}`, "Upstream service temporarily unavailable", []provider.Protocol{provider.Responses}},
		{503, `{"error":{"message":"We are experiencing capacity issues"}}`, "capacity issues", []provider.Protocol{provider.Responses}},
	} {
		for _, proto := range fl.protos {
			for _, ask := range asks {
				t.Run(fmt.Sprintf("%d %s/%s via %s", fl.status, fl.said, proto, ask.name), func(t *testing.T) {
					f := &fake{code: fl.status, ctype: "application/json", reply: fl.reply}
					up := setup(t, proto, f)
					p := provider.Provider{ID: "fake", Name: "Relay", Key: "k", Models: []string{"m1"}, Contexts: map[string]int{"m1": 2000}}
					switch proto {
					case provider.Chat:
						p.Chat = up.URL + "/v1"
					case provider.Responses:
						p.Responses = up.URL + "/v1"
					default:
						p.Anthropic = up.URL
					}
					if err := provider.Save(p); err != nil {
						t.Fatal(err)
					}
					code, body := post(t, ask.path, ask.body)
					if code != fl.status || !strings.Contains(body, fl.said) || overflowTold(body) {
						t.Fatalf("want %d with the relay's words, got %d after %d calls: %s", fl.status, code, f.calls, body)
					}
				})
			}
		}
	}
}

// The vendors seen to answer a request past the model's window without
// saying so, each served by its plugin now, still have it told as too
// long, at once (b3ab3e42e, #711): under the plugin's own id or another.
// The same failure from a plugin of any other vendor, or from a user's
// provider that took the vendor's id, is the vendor's error.
func TestUnsaidVendorsStillOverflow(t *testing.T) {
	cases := []struct {
		vendor string
		status int
		body   string
	}{
		{"devin", 502, `{"error":{"message":"We are currently experiencing capacity issues. Please try again later."}}`},
		{"devin", 503, `{"error":{"message":"Service unavailable"}}`},
		{"qoder", 400, `{"error":{"message":"Error in upstream response"}}`},
		{"qoder-cn", 400, `{"error":{"message":"Error in upstream response"}}`},
		{"workbuddy", 400, `{"error":{"message":"Invalid request parameters"}}`},
		{"workbuddy-ai", 400, `{"error":{"message":"Invalid request parameters"}}`},
		{"workbuddy-ai", 422, `{"error":{"message":"Invalid request parameters"}}`},
		{"factory", 413, `{"error":{"message":"Request Entity Too Large"}}`},
	}
	for _, tc := range cases {
		for _, id := range []string{tc.vendor, tc.vendor + "-plugin"} {
			p := provider.PluginForTest(id, tc.vendor)
			p.Name, p.Contexts = tc.vendor, map[string]int{"m": 2000}
			if said, ok := tooLongUnsaid(p, "m", 3000, 0, tc.status, []byte(tc.body)); !ok || !strings.Contains(said, "prompt is too long") {
				t.Errorf("%s (%s) %d: past the window: %q %v", id, tc.vendor, tc.status, said, ok)
			}
			if said, ok := tooLongUnsaid(p, "m", 1000, 0, tc.status, []byte(tc.body)); ok {
				t.Errorf("%s (%s) %d: within the window: %s", id, tc.vendor, tc.status, said)
			}
		}
		other := provider.PluginForTest("fakeco", "fakeco")
		other.Name, other.Contexts = "Fakeco", map[string]int{"m": 2000}
		own := provider.Provider{ID: tc.vendor, Name: tc.vendor, Contexts: map[string]int{"m": 2000}}
		for _, p := range []provider.Provider{other, own} {
			if said, ok := tooLongUnsaid(p, "m", 3000, 0, tc.status, []byte(tc.body)); ok {
				t.Errorf("%s, not %s's plugin, %d: told as too long: %s", p.ID, tc.vendor, tc.status, said)
			}
		}
	}
	// a status a vendor wasn't seen to say it with stays its error
	devin := provider.PluginForTest("devin", "devin")
	devin.Contexts = map[string]int{"m": 2000}
	factory := provider.PluginForTest("factory", "factory")
	factory.Contexts = map[string]int{"m": 2000}
	qoder := provider.PluginForTest("qoder", "qoder")
	qoder.Contexts = map[string]int{"m": 2000}
	for _, x := range []struct {
		p      provider.Provider
		status int
	}{{devin, 400}, {factory, 400}, {factory, 502}, {qoder, 502}} {
		if said, ok := tooLongUnsaid(x.p, "m", 3000, 0, x.status, []byte(`{"error":{"message":"Something went wrong"}}`)); ok {
			t.Errorf("%s %d: told as too long: %s", x.p.ID, x.status, said)
		}
	}
}

// End to end through Qoder's plugin: its 400 "Error in upstream response"
// for a request past fake-gemini's 3000 (the fake plugin's model whose
// window the catalog has) reaches the agent as an overflow, and a
// short one's as Qoder said it.
func TestQoderPluginUnsaidOverflow(t *testing.T) {
	f := &fake{t: t, code: 400, ctype: "application/json", reply: `{"error":{"message":"Error in upstream response"}}`}
	movedFake(t, "qoder", f)
	s := New()
	code, body := postTo(t, s, "/v1/chat/completions", `{"model":"qoder/fake-gemini","messages":[{"role":"user","content":"`+strings.Repeat("word ", 3200)+`"}]}`)
	if code != 400 || !strings.Contains(body, "context_length_exceeded") {
		t.Fatalf("past the window: %d %s", code, body)
	}
	code, body = postTo(t, s, "/v1/chat/completions", `{"model":"qoder/fake-gemini","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || overflowTold(body) || !strings.Contains(body, "Error in upstream response") {
		t.Fatalf("short: %d %s", code, body)
	}
}
