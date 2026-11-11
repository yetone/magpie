package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Copilot's Gemini answers 400 "invalid request body" to a tool schema whose
// required names a property it lacks, and to two tools of one name
// (yuushabu_buyinn on X). The fake refuses exactly those; the turn must
// reach it fit, and a clean tool must go as it came.
func TestCopilotGeminiTakesToolsFit(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	copilotTurnFake(t, map[string]string{
		"gemini-3.8-flash": `["/chat/completions"]`,
	}, nil, func(m string, b []byte) bool {
		s := string(b)
		return strings.Contains(s, `"required":["ghost"`) || strings.Contains(s, `"required":["deep"]`) ||
			strings.Count(s, `"name":"dup"`) > 1
	}, "invalid request body", http.StatusBadRequest, &sent)

	req := `{"model":"copilot/gemini-3.8-flash","stream":false,
	"messages":[{"role":"user","content":"hi"}],
	"tools":[
	 {"type":"function","function":{"name":"dup","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["ghost","a"]}}},
	 {"type":"function","function":{"name":"dup","parameters":{"type":"object","properties":{}}}},
	 {"type":"function","function":{"name":"nest","parameters":{"type":"object","properties":{"o":{"type":"object","properties":{"x":{"type":"string"}},"required":["deep"]}},"required":["o"]}}}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(req)))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("Copilot was asked %d times", len(sent))
	}
	if !strings.Contains(sent[0], `"required":["a"]`) || !strings.Contains(sent[0], `"required":["o"]`) {
		t.Errorf("the real required entries were lost: %s", sent[0])
	}
}
