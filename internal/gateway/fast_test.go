package gateway

import (
	"strings"
	"testing"
)

// Codex's /fast (service_tier priority) survives a group's routing to a
// ChatGPT account, and goes nowhere else.
func TestResponsesFastTier(t *testing.T) {
	r, err := parseResponses([]byte(`{"model":"group/g","input":"hi","service_tier":"priority"}`))
	if err != nil || !r.Fast {
		t.Fatalf("fast not read: %v %+v", err, r)
	}
	for host, want := range map[string]bool{"chatgpt.com": true, "api.openai.com": true, "openrouter.ai": false, "": false} {
		b := string(buildResponses(r, "gpt-5.5", host, false))
		if got := strings.Contains(b, `"service_tier":"priority"`); got != want {
			t.Errorf("%q: tier sent %v, want %v: %s", host, got, want, b)
		}
	}
	// Codex's Ultrafast goes to the ChatGPT backend, the one that offers
	// it; OpenAI's API gets it as Fast
	r, _ = parseResponses([]byte(`{"model":"gpt-5.5","input":"hi","service_tier":"ultrafast"}`))
	if !r.Fast || !r.Ultrafast {
		t.Fatalf("ultrafast not read: %+v", r)
	}
	for host, want := range map[string]string{"chatgpt.com": `"service_tier":"ultrafast"`, "api.openai.com": `"service_tier":"priority"`, "openrouter.ai": ""} {
		b := string(buildResponses(r, "gpt-5.5", host, false))
		if want == "" && strings.Contains(b, "service_tier") || want != "" && !strings.Contains(b, want) {
			t.Errorf("ultrafast to %q: %s", host, b)
		}
	}
	r, _ = parseResponses([]byte(`{"model":"m","input":"hi","service_tier":"flex"}`))
	if r.Fast {
		t.Error("flex read as fast")
	}
}

// Chat Completions says priority the same way (Cursor's fast ids).
func TestChatFastTier(t *testing.T) {
	r, err := parseChat([]byte(`{"model":"cursor/grok-4.7","messages":[{"role":"user","content":"hi"}],"service_tier":"priority"}`))
	if err != nil || !r.Fast {
		t.Fatalf("fast not read: %v %+v", err, r)
	}
	r, _ = parseChat([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"service_tier":"auto"}`))
	if r.Fast {
		t.Error("auto read as fast")
	}
}
