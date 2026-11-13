package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// agentUsage is the prompt, cache read and cache write an agent reads from
// a reply in the protocol it asked in, the way its SDK reads them: on Chat
// as pi-ai does (DeepSeek Harness, Pi), prompt_tokens_details.cached_tokens
// first, a 0 there too, then DeepSeek's prompt_cache_hit_tokens, then
// cached_tokens; on Responses input_tokens_details; on Messages
// Anthropic's own names. A stream's last usage that says any is what
// counts.
func agentUsage(path, body string) (prompt, read, write int) {
	num := func(m map[string]any, k string) (int, bool) {
		v, ok := m[k].(float64)
		return int(v), ok
	}
	from := func(u map[string]any) (p, r, w int) {
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			p, _ = num(u, "prompt_tokens")
			d, _ := u["prompt_tokens_details"].(map[string]any)
			var ok bool
			if r, ok = num(d, "cached_tokens"); !ok {
				if r, ok = num(u, "prompt_cache_hit_tokens"); !ok {
					r, _ = num(u, "cached_tokens")
				}
			}
			w, _ = num(d, "cache_write_tokens")
		case strings.HasSuffix(path, "/responses"):
			p, _ = num(u, "input_tokens")
			d, _ := u["input_tokens_details"].(map[string]any)
			r, _ = num(d, "cached_tokens")
			w, _ = num(d, "cache_write_tokens")
		default:
			in, _ := num(u, "input_tokens")
			r, _ = num(u, "cache_read_input_tokens")
			w, _ = num(u, "cache_creation_input_tokens")
			p = in + r + w
		}
		return
	}
	usages := func(j map[string]any) (out []map[string]any) {
		if u, ok := j["usage"].(map[string]any); ok {
			out = append(out, u)
		}
		for _, k := range []string{"response", "message"} {
			if r, ok := j[k].(map[string]any); ok {
				if u, ok := r["usage"].(map[string]any); ok {
					out = append(out, u)
				}
			}
		}
		return out
	}
	docs := []string{body}
	if strings.Contains(body, "data:") {
		docs = nil
		for _, l := range strings.Split(body, "\n") {
			if d, ok := strings.CutPrefix(l, "data: "); ok {
				docs = append(docs, d)
			}
		}
	}
	for _, d := range docs {
		var j map[string]any
		if json.Unmarshal([]byte(d), &j) != nil {
			continue
		}
		for _, u := range usages(j) {
			if p, r, w := from(u); r > read || p > prompt {
				prompt, read, write = max(p, prompt), max(r, read), max(w, write)
			}
		}
	}
	return
}

// What a vendor read from its prompt cache reaches the agent in the words
// its SDK reads, whatever names the vendor told it in, passed through or
// translated, whole or streamed. A Chat relay that told it in Anthropic's
// names, or DeepSeek's hits beside a 0 of OpenAI's, was passed through as
// it came: the gateway counted the hit, and DeepSeek Harness, reading
// prompt_tokens_details.cached_tokens, showed 缓存命中 0% (B3TT3R on
// Discord).
func TestAgentReadsCacheTokensOnEveryRoute(t *testing.T) {
	vendors := []struct {
		wire  string
		proto provider.Protocol
		write int
	}{
		{"anthropic", provider.Anthropic, 40},
		{"chat", provider.Chat, 0},
		{"chat-anthropic-names", provider.Chat, 40},
		{"chat-deepseek", provider.Chat, 0},
		{"chat-zero-details", provider.Chat, 0},
		{"chat-moonshot", provider.Chat, 0},
		{"chat-mistral", provider.Chat, 0},
		{"chat-mistral-zero-details", provider.Chat, 0},
		{"responses", provider.Responses, 0},
		{"gemini", provider.Gemini, 0},
	}
	agents := []struct{ name, path, body string }{
		{"messages", "/v1/messages", `{"model":"fake/m1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages stream", "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`},
		{"chat", "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`},
		{"chat stream", "/v1/chat/completions", `{"model":"fake/m1","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"fake/m1","input":"hi"}`},
		{"responses stream", "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`},
	}
	for _, v := range vendors {
		for _, a := range agents {
			t.Run(v.wire+" to "+a.name, func(t *testing.T) {
				fresh(t)
				up := httptest.NewServer(cachingVendor{v.wire})
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
				switch v.proto {
				case provider.Chat:
					p.Chat = up.URL + "/v1"
				case provider.Responses:
					p.Responses = up.URL + "/v1"
				case provider.Anthropic:
					p.Anthropic = up.URL
				case provider.Gemini:
					p.Gemini = up.URL + "/v1beta"
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", a.path, strings.NewReader(a.body)))
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				// the whole prompt: what was read, written and neither
				const wantPrompt = 352
				prompt, read, write := agentUsage(a.path, rec.Body.String())
				if read != 300 || write != v.write || prompt != wantPrompt {
					t.Errorf("agent reads prompt %d, cache read %d, cache write %d; want %d, 300, %d\n%s",
						prompt, read, write, wantPrompt, v.write, rec.Body)
				}
			})
		}
	}
}

// A usage told in OpenAI's names already passes byte for byte, and so does
// one with no cache: openAIUsage mends only what an agent would misread.
func TestOpenAIUsageLeavesOpenAIsAlone(t *testing.T) {
	for _, u := range []string{
		`{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":300}}`,
		`{"prompt_tokens":52,"completion_tokens":7,"total_tokens":59}`,
		`{"prompt_tokens":52,"completion_tokens":7,"total_tokens":59,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":0}`,
		`{"prompt_tokens":52,"completion_tokens":7,"total_tokens":59,"num_cached_tokens":0}`,
		`null`,
		`"x"`,
	} {
		if out, changed := openAIUsage(json.RawMessage(u)); changed || string(out) != u {
			t.Errorf("%s came back %s (changed %v)", u, out, changed)
		}
	}
	// a stream's chunk with the usage alone keeps every field it had
	line := []byte(`data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"cache_read_input_tokens":300,"cache_creation_input_tokens":40}}` + "\n")
	got := string((&chatTidy{}).write(line))
	for _, want := range []string{`"prompt_tokens":352`, `"total_tokens":359`, `"cached_tokens":300`, `"cache_write_tokens":40`,
		`"cache_read_input_tokens":300`, `"cache_creation_input_tokens":40`, `"choices":[]`, `"id":"c1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("mended chunk lacks %s: %s", want, got)
		}
	}
}

// A usage told only in OpenAI's names is never read again: every OpenAI
// reply carries one, and mayMendUsage keeps the decode to the replies
// that may need it. Moonshot's top-level cached_tokens beside a 0 of
// prompt_tokens_details' is told in OpenAI's too.
func TestMayMendUsageOnlyOtherNames(t *testing.T) {
	for b, want := range map[string]bool{
		`{"usage":{"prompt_tokens":352,"prompt_tokens_details":{"cached_tokens":300}}}`:                   false,
		`{"usage":{"prompt_tokens":352}}`:                                                                 false,
		`{"usage":{"prompt_tokens":352,"prompt_cache_hit_tokens":300}}`:                                   true,
		`{"usage":{"prompt_tokens":352,"num_cached_tokens":300}}`:                                         true,
		`{"usage":{"prompt_tokens":12,"cache_read_input_tokens":300}}`:                                    true,
		`{"usage":{"prompt_tokens":352,"prompt_tokens_details":{"cached_tokens":0},"cached_tokens":300}}`: true,
		`{"choices":[],"prompt_cache_hit_tokens":300}`:                                                    false,
	} {
		if got := mayMendUsage([]byte(b)); got != want {
			t.Errorf("mayMendUsage(%s) = %v, want %v", b, got, want)
		}
	}
	out, changed := openAIUsage(json.RawMessage(`{"prompt_tokens":352,"completion_tokens":7,"total_tokens":359,"prompt_tokens_details":{"cached_tokens":0},"cached_tokens":300}`))
	if !changed || !strings.Contains(string(out), `"prompt_tokens_details":{"cached_tokens":300}`) || !strings.Contains(string(out), `"prompt_tokens":352`) {
		t.Errorf("Moonshot's usage came back %s (changed %v)", out, changed)
	}
}

// A plugin's reply reaches a Chat agent with its cache in OpenAI's names
// too, whatever its model speaks: the agent reads what the vendor read.
func TestPluginAgentReadsCacheTokens(t *testing.T) {
	movedFake(t, "factory", cachingVendor{})
	for _, c := range []struct {
		model string
		write int
	}{
		{"fake-1", 0},       // @ai-sdk/openai-compatible: Chat
		{"fake-claude", 40}, // @ai-sdk/anthropic: Messages
		{"fake-gemini", 0},  // @ai-sdk/google: generateContent
	} {
		for _, stream := range []bool{false, true} {
			body := `{"model":"factory/` + c.model + `","messages":[{"role":"user","content":"hi"}]}`
			if stream {
				body = `{"model":"factory/` + c.model + `","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
			}
			rec := httptest.NewRecorder()
			New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hi") {
				t.Fatalf("%s stream=%v: %d %s", c.model, stream, rec.Code, rec.Body)
			}
			prompt, read, write := agentUsage("/v1/chat/completions", rec.Body.String())
			if read != 300 || write != c.write || prompt != 352 {
				t.Errorf("%s stream=%v: agent reads prompt %d, cache read %d, cache write %d; want 352, 300, %d\n%s",
					c.model, stream, prompt, read, write, c.write, rec.Body)
			}
		}
	}
}
