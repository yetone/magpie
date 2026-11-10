package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// agyCLITurn is a turn as Antigravity CLI sends it on one of magpie's custom
// models: Gemini's API, its key as x-goog-api-key.
func agyCLITurn(t *testing.T, s *Server, model string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1beta/models/"+model+":streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("User-Agent", "google-genai-sdk/1.71.0 gl-go/go1.28")
	req.Header.Set("x-goog-api-key", TokenFor("agy"))
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// agy on magpie's Antigravity uses every Antigravity account that is on
// (leoooeth on X asked for several accounts in agy): an account whose
// quota is spent is passed over for the next, and the turn after asks the
// one with quota first, while agy itself stays on one model.
func TestAgyUsesEveryAntigravityAccount(t *testing.T) {
	var asked []string
	s, _ := antigravityGroupFor(t, []string{"u1@example.com", "u2@example.com"}, "m", func(w http.ResponseWriter, r *http.Request) {
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		asked = append(asked, who)
		if who == "u1@example.com" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, antigravityQuota)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"from u2"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}}`))
	})
	for range 2 {
		if code, out := agyCLITurn(t, s, "antigravity/m"); code != 200 || !strings.Contains(out, "from u2") {
			t.Fatalf("agy's turn: %d %s (asked %v)", code, out, asked)
		}
	}
	if got := strings.Join(asked, " "); got != "u1@example.com u2@example.com u2@example.com" {
		t.Fatalf("asked %s", got)
	}
}
