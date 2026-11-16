package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// copilotImageCap is Copilot's refusal of a Gemini request over the
// model's image cap, as it said it to twelve images (probed live
// 2026-11-16, gemini-4-carbon on /chat/completions).
const copilotImageCap = "too many images: maximum allowed for model gemini-4-carbon is 10, got 12"

// capShot is a small PNG's data URL, n in it so each is told apart.
func capShot(n int) string {
	return fmt.Sprintf("data:image/png;base64,iVBORw0KGgoSHOT%02d", n) + strings.Repeat("A", 64)
}

// imageChat is a chat conversation of n earlier turns with an image each,
// then the latest user message with latest images of its own.
func imageChat(model string, n, latest int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"model":%q,"stream":false,"messages":[`, model)
	for i := range n {
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"look at screen %d"},{"type":"image_url","image_url":{"url":%q}}]},{"role":"assistant","content":"seen %d"},`, i, capShot(i), i)
	}
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"the latest ask"}`)
	for i := range latest {
		fmt.Fprintf(&b, `,{"type":"image_url","image_url":{"url":%q}}`, capShot(90+i))
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// overTen refuses a request of more than ten images, as Copilot's
// Gemini does, counting the refusals.
func overTen(refused *atomic.Int32) func(string, []byte) bool {
	return func(_ string, b []byte) bool {
		if strings.Count(string(b), `"type":"image_url"`) > 10 {
			refused.Add(1)
			return true
		}
		return false
	}
}

func postCapChat(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	return rec
}

// A Copilot Gemini conversation of twelve images was refused on every
// turn (yuushabu_buyinn on X). Now the turn goes with its two oldest
// images left out for a text saying so, the latest user message whole,
// and the next turn goes fitted at once: the cap is learned.
func TestCopilotGeminiImagesOverCap(t *testing.T) {
	var sent []string
	var refused atomic.Int32
	copilotTurnFake(t, map[string]string{"gemini-4-carbon": `["/chat/completions"]`}, nil,
		overTen(&refused), copilotImageCap, http.StatusBadRequest, &sent)
	s := New()
	rec := postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 11, 1))
	if rec.Code != 200 {
		t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(sent) != 1 || refused.Load() != 1 {
		t.Fatalf("Copilot answered %d requests, refused %d", len(sent), refused.Load())
	}
	check := func(got string) {
		t.Helper()
		if n := strings.Count(got, `"type":"image_url"`); n != 10 {
			t.Errorf("sent %d images, not 10", n)
		}
		if strings.Contains(got, "SHOT00") || strings.Contains(got, "SHOT01") || !strings.Contains(got, "SHOT02") {
			t.Error("not the two oldest images left out")
		}
		if strings.Count(got, imageOverCap) != 2 {
			t.Errorf("the left-out images aren't said: %.300s", got)
		}
		if !strings.Contains(got, "SHOT90") || !strings.Contains(got, "the latest ask") {
			t.Error("the latest user message was changed")
		}
		for i := range 11 {
			if !strings.Contains(got, fmt.Sprintf("look at screen %d", i)) {
				t.Errorf("text %d is gone", i)
			}
		}
	}
	check(sent[0])

	// the next turn: asked once, fitted before it is sent
	rec = postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 11, 1))
	if rec.Code != 200 || len(sent) != 2 || refused.Load() != 1 {
		t.Fatalf("the next turn: %d, %d answered, %d refused", rec.Code, len(sent), refused.Load())
	}
	check(sent[1])
}

// A request at the cap goes byte for byte as it came, the cap known or
// not; one whose latest message alone is over it has nothing to leave
// out, and Copilot's refusal goes on, asked once.
func TestCopilotGeminiImagesUnderCapUntouched(t *testing.T) {
	var sent []string
	var refused atomic.Int32
	copilotTurnFake(t, map[string]string{"gemini-4-carbon": `["/chat/completions"]`}, nil,
		overTen(&refused), copilotImageCap, http.StatusBadRequest, &sent)
	s := New()
	if rec := postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 11, 1)); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	at := imageChat("copilot/gemini-4-carbon", 9, 1)
	if rec := postCapChat(t, s, at); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := sent[len(sent)-1]; strings.Contains(got, imageOverCap) || strings.Count(got, `"type":"image_url"`) != 10 {
		t.Fatalf("a request at the cap was changed: %.300s", got)
	}
	before := len(sent)
	rec := postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 1, 12))
	if rec.Code == 200 || !strings.Contains(rec.Body.String(), "too many images") {
		t.Fatalf("the refusal didn't go on: %d %s", rec.Code, rec.Body.String())
	}
	if len(sent) != before || refused.Load() != 2 {
		t.Fatalf("the latest user message's images were left out, or it was asked again (%d refused)", refused.Load())
	}
}

// Another vendor's refusal in the same words teaches nothing: its request
// is sent once, as it came, and the refusal goes on.
func TestImageCapOtherVendorUntouched(t *testing.T) {
	fresh(t)
	up := &capped{proto: provider.Chat, most: 0, status: 400, ctype: "application/json",
		body: `{"error":{"message":"` + copilotImageCap + `","code":"invalid_request_body"}}`}
	cappedOn(t, up)
	body := imageChat("fake/m1", 11, 1)
	rec := postCapChat(t, New(), body)
	if rec.Code == 200 {
		t.Fatalf("refused upstream, yet 200: %s", rec.Body.String())
	}
	calls := up.calls()
	if len(calls) != 1 {
		t.Fatalf("asked %d times", len(calls))
	}
	if strings.Contains(string(calls[0]), imageOverCap) || strings.Count(string(calls[0]), `"type":"image_url"`) != 12 {
		t.Fatalf("another vendor's request was changed: %.300s", calls[0])
	}
}
