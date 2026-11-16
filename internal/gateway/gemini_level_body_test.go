package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// levelRelay is a relay serving Gemini under a host of its own, on
// Anthropic's Messages and on Chat completions (Mamba_asr's peta-gemini),
// that turns away a Gemini 4 asked a budget as Gemini does (#2083) and
// keeps every body it was sent.
func levelRelay(t *testing.T) (*httptest.Server, *[]map[string]any) {
	var bodies []map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		model, _ := m["model"].(string)
		if strings.HasPrefix(model, "gemini-4") && (strings.Contains(string(b), `"budget_tokens"`) || budgetNamed(string(b))) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, argonBudgetRefused)
			return
		}
		stream, _ := m["stream"].(bool)
		if strings.HasSuffix(r.URL.Path, "/messages") {
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, ev := range []string{
					`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
					`{"type":"message_stop"}`,
				} {
					io.WriteString(w, "data: "+ev+"\n\n")
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		sigReply(w, stream, map[string]any{"role": "assistant", "content": "ok"}, "stop")
	}))
	t.Cleanup(up.Close)
	return up, &bodies
}

func budgetNamed(b string) bool {
	return strings.Contains(b, `"thinking_budget"`) || strings.Contains(b, `"thinkingBudget"`)
}

// Mamba_asr on X: a Gemini model that takes only a thinking level, on a
// relay of its own host, was sent Claude Code's thinking budget as it
// came (Anthropic upstream) or a Chat client's thinking_config budget,
// which the relay passes on to Gemini as thinkingBudget, and Gemini 4
// turns away. It is now asked a level: the budget bucketed into low,
// medium or high, an effort above the model's highest at its highest.
// Another vendor's model, and a Gemini 2.5, are sent what they were.
func TestGeminiRelayAskedALevel(t *testing.T) {
	up, bodies := levelRelay(t)
	addr := strings.TrimPrefix(up.URL, "http://")
	type want map[string]any
	for _, c := range []struct {
		name, upstream, model, path, body string
		want                              want // fields of the body the relay was sent; nil: absent
	}{
		{"messages budget low to Anthropic", "anth", "gemini-4-argon", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":2000}}`,
			want{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "low"}}},
		{"messages budget large to Anthropic", "anth", "gemini-4-argon", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":31999}}`,
			want{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high"}}},
		{"messages effort max to Anthropic", "anth", "gemini-4-argon", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`,
			want{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high"}}},
		{"chat effort to Anthropic", "anth", "gemini-4-argon", "/v1/chat/completions",
			`{"reasoning_effort":"medium"}`,
			want{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "medium"}}},
		{"messages budget large to Chat", "chat", "gemini-4-argon", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":31999}}`,
			want{"reasoning_effort": "high"}},
		{"chat thinking_config budget to Chat", "chat", "gemini-4-argon", "/v1/chat/completions",
			`{"extra_body":{"google":{"thinking_config":{"thinking_budget":2000,"include_thoughts":true}}}}`,
			want{"extra_body": map[string]any{"google": map[string]any{"thinking_config": map[string]any{"thinking_level": "low", "include_thoughts": true}}}}},
		{"chat reasoning_effort xhigh to Chat", "chat", "gemini-4-argon", "/v1/chat/completions",
			`{"reasoning_effort":"xhigh"}`,
			want{"reasoning_effort": "high"}},
		// another vendor's model, and a Gemini that takes a budget: as sent
		{"Claude budget to Anthropic", "anth", "claude-sonnet-4-5", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":2000}}`,
			want{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(2000)}, "output_config": nil}},
		{"GPT thinking_config budget to Chat", "chat", "gpt-5.5", "/v1/chat/completions",
			`{"reasoning_effort":"xhigh","extra_body":{"google":{"thinking_config":{"thinking_budget":2000}}}}`,
			want{"reasoning_effort": "xhigh", "extra_body": map[string]any{"google": map[string]any{"thinking_config": map[string]any{"thinking_budget": float64(2000)}}}}},
		{"Gemini 2.5 budget to Anthropic", "anth", "gemini-2.5-flash", "/v1/messages",
			`{"max_tokens":40000,"thinking":{"type":"enabled","budget_tokens":2000}}`,
			want{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(2000)}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			p := provider.Provider{ID: "peta", Name: "peta", Key: "k", Models: []string{c.model}}
			if c.upstream == "anth" {
				p.Anthropic = "http://api.peta.example.com"
			} else {
				p.Chat = "http://api.peta.example.com/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}}
			body := `{"model":"peta/` + c.model + `","messages":[{"role":"user","content":"hi"}],` + strings.TrimPrefix(c.body, "{")
			*bodies = nil
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
			if rec.Code != 200 || len(*bodies) == 0 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			sent := (*bodies)[0]
			for k, v := range c.want {
				got, _ := json.Marshal(sent[k])
				exp, _ := json.Marshal(v)
				if string(got) != string(exp) {
					t.Errorf("%s: sent %s, want %s (body %v)", k, got, exp, sent)
				}
			}
		})
	}
}

// geminiLevelFor fits an effort to a Gemini's levels where the catalog
// doesn't list them: nothing above high, medium as high on a Gemini 3 Pro.
func TestGeminiLevelFor(t *testing.T) {
	fresh(t)
	for _, c := range []struct{ model, effort, want string }{
		{"gemini-4-argon", "xhigh", "high"},
		{"gemini-4-argon", "max", "high"},
		{"gemini-4-argon", "medium", "medium"},
		{"gemini-3.8-flash", "medium", "medium"},
		{"gemini-3-pro-preview", "medium", "high"},
		{"gemini-4-argon", "low", "low"},
		{"gemini-4-argon", "none", "low"},
	} {
		if got := geminiLevelFor(c.model, c.effort); got != c.want {
			t.Errorf("%s at %s: %s, want %s", c.model, c.effort, got, c.want)
		}
	}
}

// A relay-listed Gemini 4 model absent from models.dev keeps its provider's
// xhigh level in geminiLevelBody on both Anthropic and Chat upstreams instead
// of being capped at high.
func TestGeminiRelayProviderLevelsAboveHigh(t *testing.T) {
	up, bodies := levelRelay(t)
	addr := strings.TrimPrefix(up.URL, "http://")
	const model = "gemini-4-hydrogen-search"
	for _, c := range []struct {
		name, upstream, path, body, key, want string
	}{
		{"chat xhigh to Chat relay", "chat", "/v1/chat/completions", `{"reasoning_effort":"xhigh"}`, "reasoning_effort", "xhigh"},
		{"chat max to Chat relay", "chat", "/v1/chat/completions", `{"reasoning_effort":"max"}`, "reasoning_effort", "xhigh"},
		{"messages max to Anthropic relay", "anth", "/v1/messages", `{"max_tokens":40000,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, "output_config", `{"effort":"xhigh"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			p := provider.Provider{ID: "peta", Name: "peta", Key: "k", Models: []string{model}}
			var liveBase string
			if c.upstream == "anth" {
				p.Anthropic = "http://api.peta.example.com"
				liveBase = p.Anthropic
			} else {
				p.Chat = "http://api.peta.example.com/v1"
				liveBase = p.Chat
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive("peta", liveBase, []catalog.Model{
				{ID: model, Efforts: []string{"low", "medium", "high", "xhigh"}},
			}); err != nil {
				t.Fatal(err)
			}
			s := New()
			s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}}}
			body := `{"model":"peta/` + model + `","messages":[{"role":"user","content":"hi"}],` + strings.TrimPrefix(c.body, "{")
			*bodies = nil
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(body)))
			if rec.Code != 200 || len(*bodies) == 0 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			sent := (*bodies)[0]
			if c.key == "output_config" {
				got, _ := json.Marshal(sent[c.key])
				if string(got) != c.want {
					t.Errorf("%s: sent %s, want %s", c.key, got, c.want)
				}
				return
			}
			if got, _ := sent[c.key].(string); got != c.want {
				t.Errorf("%s: sent %q, want %q", c.key, got, c.want)
			}
		})
	}
}
