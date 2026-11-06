package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Some vendors don't say a conversation is too long for the model: Devin
// answered one of about 225k tokens for swe-1.7-lightning (a 202,752
// window) with a 502 "capacity issues", WorkBuddy one for glm-5.1 with a
// 400 "Invalid request parameters", Qoder one of 2M for qmodel with a 400
// "Error in upstream response" — each answered at a size the model holds.
// Asked again in place three times, the agent was then handed a 502 and
// never compacted. A request well past the model's window that fails so
// is told as one too long, at once.
func TestOverflowTheVendorDidntSay(t *testing.T) {
	big := strings.Repeat("word ", 2400)  // about 3000 tokens as estimated
	small := strings.Repeat("word ", 100) // about 125
	near := strings.Repeat("word ", 1760) // about 2200, which may yet fit
	for _, tc := range []struct {
		name, path, body string
		code             int
		reply            string
		wantCode, calls  int
		want             string
	}{
		// a 5xx only from Devin, which answers an overflow so
		// (TestDevinBusyOverflowStillTooLong): a plain provider's is its
		// busy moment, asked again (ne9ko9 on Discord)
		{"502 plain", "/v1/messages", big, 502, `{"error":{"message":"We are experiencing capacity issues"}}`, 502, 1 + lastRetries + replanRounds, "capacity issues"},
		{"400 chat", "/v1/chat/completions", big, 400, `{"error":{"message":"Invalid request parameters"}}`, 400, 1, "context_length_exceeded"},
		// Text alone well past the window is enough to ask for compaction.
		{"413 bytes", "/v1/messages", big, 413, `{"error":{"message":"Request Entity Too Large"}}`, 400, 1, "prompt is too long"},
		{"413 request_too_large", "/v1/messages", big, 413, `{"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`, 400, 1, "prompt is too long"},
		// not the conversation's length: a plain provider's busy reply is
		// asked again past the same-one retries, within its patience,
		// until replan's rounds run out
		{"small 502", "/v1/messages", small, 502, `{"error":{"message":"We are experiencing capacity issues"}}`, 502, 1 + lastRetries + replanRounds, "capacity issues"},
		{"near the window", "/v1/messages", near, 502, `{"error":{"message":"We are experiencing capacity issues"}}`, 502, 1 + lastRetries + replanRounds, "capacity issues"},
		{"429", "/v1/messages", big, 429, `{"error":{"message":"Rate limit reached"}}`, 429, 1 + rateRetries + replanRounds, "Rate limit"},
		{"401", "/v1/messages", big, 401, `{"error":{"message":"invalid token"}}`, 401, 1, "invalid token"},
		{"quota", "/v1/messages", big, 400, `{"error":{"message":"insufficient balance"}}`, 400, 1, "insufficient balance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proto := provider.Anthropic
			if strings.Contains(tc.path, "chat") {
				proto = provider.Chat
			}
			f := &fake{code: tc.code, ctype: "application/json", reply: tc.reply}
			up := setup(t, proto, f)
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Contexts: map[string]int{"m1": 2000}}
			if proto == provider.Chat {
				p.Chat = up.URL + "/v1"
			} else {
				p.Anthropic = up.URL
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			req := `{"model":"fake/m1","max_tokens":10,"messages":[{"role":"user","content":"` + tc.body + `"}]}`
			code, body := post(t, tc.path, req)
			if code != tc.wantCode || !strings.Contains(body, tc.want) || f.calls != tc.calls {
				t.Fatalf("status %d after %d calls (want %d after %d): %s", code, f.calls, tc.wantCode, tc.calls, body)
			}
			if tc.wantCode != 400 || tc.name == "quota" {
				if strings.Contains(body, "prompt is too long") || strings.Contains(body, "context_length_exceeded") {
					t.Fatalf("told as an overflow: %s", body)
				}
			}
		})
	}
}
