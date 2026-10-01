package gateway

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #473: dsh (pi-ai, Chat Completions) on WorkBuddy AI's deepseek-v4.1-flash
// at max heard nothing for 300s and gave up. A Chat reply relayed to a Chat
// client hands on DeepSeek's reasoning_content as it comes, each piece
// while the model is still thinking, not once its text begins: the client
// hears from the model as long as the provider streams its thinking.
func TestChatReasoningRelayedAsItComes(t *testing.T) {
	fresh(t)
	think := `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"","reasoning_content":"hmm"},"finish_reason":""}]}`
	up := &slowThinker{events: []string{
		`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":""},"finish_reason":""}]}`,
		think, think, think, think, think,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":""}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, gap: 200 * time.Millisecond, done: make(chan time.Time, 1)}
	vendor := httptest.NewServer(up)
	t.Cleanup(vendor.Close)
	if err := provider.Save(provider.Provider{ID: "wb", Name: "WorkBuddy AI", Key: "k", Chat: vendor.URL + "/v2", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New().Handler())
	t.Cleanup(gw.Close)

	start := time.Now()
	res, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"wb/deepseek-v4.1-flash","stream":true,"reasoning_effort":"max","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var firstThought, firstText time.Time
	rd := bufio.NewReader(res.Body)
	for {
		ln, err := rd.ReadString('\n')
		if firstThought.IsZero() && strings.Contains(ln, `"reasoning_content":"hmm"`) {
			firstThought = time.Now()
		}
		if firstText.IsZero() && strings.Contains(ln, `"content":"done"`) {
			firstText = time.Now()
		}
		if err != nil {
			break
		}
	}
	if firstThought.IsZero() || firstText.IsZero() {
		t.Fatal("no reasoning or no text reached the client")
	}
	// the first thought is sent 200ms in, the text a second after it
	if firstThought.Sub(start) > 700*time.Millisecond || firstText.Sub(firstThought) < 700*time.Millisecond {
		t.Fatalf("reasoning held: first thought after %v, %v before the text", firstThought.Sub(start), firstText.Sub(firstThought))
	}
}
