package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// What a vendor wrote itself and checks when it comes back goes back as the
// vendor wrote it, with masking on (Jeremy.Zhou on Discord: OpenCode Zen
// answered 400 until 脱敏密钥 and 脱敏个人信息 were turned off).

// sign is the fake vendor's thinking signature: it covers the text exactly,
// as Anthropic's does.
func sign(text string) string {
	m := hmac.New(sha256.New, []byte("vendor"))
	m.Write([]byte(text))
	return hex.EncodeToString(m.Sum(nil))
}

// A thinking block that names a placeholder comes back to the agent with
// the value in it; sent again, it goes as the vendor signed it, placeholder
// and all, so its signature still holds and the value doesn't leak.
func TestSignedThinkingGoesBackAsSigned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const email = "jeremy.zhou@gmail.com"
	const secret = "hunter2abc1"
	placeholder := regexp.MustCompile(`\{\{[A-Z_]+_[a-z2-7]{8}\}\}`)
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"invalid JSON"}}`)
			return
		}
		for i, m := range req.Messages {
			var blocks []map[string]any
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			for j, b := range blocks {
				if b["type"] != "thinking" {
					continue
				}
				text, _ := b["thinking"].(string)
				if sig, _ := b["signature"].(string); sig != sign(text) {
					w.WriteHeader(400)
					io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.`+itoa(i)+`.content.`+itoa(j)+`: Invalid `+"`signature`"+` in `+"`thinking`"+` block"}}`)
					return
				}
			}
		}
		// the vendor thinks about what it was given, placeholders and all,
		// in its own words
		ps := placeholder.FindAllString(string(b), -1)
		// and of its own: a number that reads as a phone number
		thinking := "Order 13800138000: the password is " + strings.Join(ps, ", for the user ") + "."
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-4-5",
			"content":     []any{map[string]any{"type": "thinking", "thinking": thinking, "signature": sign(thinking)}, map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "zen", Name: "Zen", Key: "k", Models: []string{"claude-sonnet-4-5"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	user := `{"role":"user","content":"DB_PASSWORD=` + secret + ` and I am ` + email + `"}`
	code, body := post(t, "/v1/messages", `{"model":"zen/claude-sonnet-4-5","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":64},"messages":[`+user+`]}`)
	if code != 200 {
		t.Fatalf("first turn: %d %s", code, body)
	}
	var first struct {
		Content []map[string]any `json:"content"`
	}
	json.Unmarshal([]byte(body), &first)
	if len(first.Content) == 0 || first.Content[0]["type"] != "thinking" {
		t.Fatalf("no thinking: %s", body)
	}
	if th := first.Content[0]["thinking"].(string); !strings.Contains(th, email) || !strings.Contains(th, secret) {
		t.Fatalf("the agent should read its values back in the thinking: %q", th)
	}
	asst, _ := json.Marshal(map[string]any{"role": "assistant", "content": first.Content})
	code, body = post(t, "/v1/messages", `{"model":"zen/claude-sonnet-4-5","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":64},"messages":[`+user+`,`+string(asst)+`,{"role":"user","content":"go on"}]}`)
	if code != 200 {
		t.Fatalf("second turn: %d %s\nsent: %s", code, body, sent[len(sent)-1])
	}
	if last := sent[len(sent)-1]; strings.Contains(last, email) || strings.Contains(last, secret) {
		t.Fatalf("a value went to the vendor: %s", last)
	}
}

// A tool result goes back under the id the vendor gave the call, whatever
// digits the id has: GLM's are call_ and 19 of them, which can read as a
// bank card number.
func TestToolCallIDsGoBackAsGiven(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	const id = "call_-6090648166611204696" // its 19 digits pass Luhn
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		json.Unmarshal(b, &req)
		calls := map[string]bool{}
		for _, m := range req.Messages {
			for _, c := range m.ToolCalls {
				calls[c.ID] = true
			}
			if m.Role == "tool" && !calls[m.ToolCallID] {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Invalid parameter: messages with role 'tool' must be a response to a preceeding message with 'tool_calls'.","type":"invalid_request_error"}}`)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"glm-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "zen", Name: "Zen", Key: "k", Models: []string{"glm-4.6"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true, RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	req := `{"model":"zen/glm-4.6","messages":[{"role":"user","content":"list files"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},` +
		`{"role":"tool","tool_call_id":"` + id + `","content":"a.go"}]}`
	if code, body := post(t, "/v1/chat/completions", req); code != 200 {
		t.Fatalf("%d %s\nsent: %s", code, body, sent)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }
