package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// maxImagesFake saves a provider with no account and caps as its MaxImages,
// serving model m1 on a chat URL of its own, and answers every request it
// gets with a chat stream, keeping the bodies.
func maxImagesFake(t *testing.T, name string, caps map[string]int) *[]string {
	t.Helper()
	fresh(t)
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"SEEN"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
			`data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: name, Name: name, Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"}, MaxImages: caps}); err != nil {
		t.Fatal(err)
	}
	return &bodies
}

// A provider the user gave a MaxImages fits the request before it is sent,
// with no refusal first: 100milionseek's and Coarena's 400s each cost a
// turn before their number is learned, and a learned cap is forgotten on
// restart.
func TestMaxImagesFitsBeforeSending(t *testing.T) {
	sent := maxImagesFake(t, "eightmax", map[string]int{"*": 8})
	s := New()
	rec := postCapChat(t, s, imageChat("eightmax/m1", 11, 1))
	if rec.Code != 200 {
		t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(*sent) != 1 {
		t.Fatalf("asked %d times, want 1: there is nothing to learn", len(*sent))
	}
	got := (*sent)[0]
	if n := strings.Count(got, `"type":"image_url"`); n != 8 {
		t.Errorf("sent %d images, not 8", n)
	}
	if strings.Contains(got, "SHOT00") || strings.Contains(got, "SHOT03") || !strings.Contains(got, "SHOT04") {
		t.Error("not the four oldest images left out")
	}
	if strings.Count(got, imageOverCap) != 4 {
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
	// the next turn is fitted the same way, still one request
	if rec := postCapChat(t, s, imageChat("eightmax/m1", 11, 1)); rec.Code != 200 || len(*sent) != 2 {
		t.Fatalf("the next turn: %d, %d answered", rec.Code, len(*sent))
	}
	if n := strings.Count((*sent)[1], `"type":"image_url"`); n != 8 {
		t.Errorf("the next turn sent %d images, not 8", n)
	}
}

// A request at the cap goes as it came, and one whose latest message alone
// is over it is left alone too: there is no earlier image to leave out.
// Image for image and text for text, not byte for byte: the gateway sends
// the vendor the model's own id, not the provider-prefixed one the agent
// named.
func TestMaxImagesUnderCapUntouched(t *testing.T) {
	sent := maxImagesFake(t, "eightmax", map[string]int{"*": 8})
	s := New()
	// same says got kept every image was kept: as many as was sent, none
	// left out for the placeholder
	same := func(got, was string) bool {
		return strings.Count(got, `"type":"image_url"`) == strings.Count(was, `"type":"image_url"`) &&
			!strings.Contains(got, imageOverCap) &&
			strings.Contains(got, "the latest ask")
	}
	at := imageChat("eightmax/m1", 7, 1)
	if rec := postCapChat(t, s, at); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !same((*sent)[0], at) || !strings.Contains((*sent)[0], "look at screen 0") {
		t.Fatalf("a request at the cap was changed: %.300s", (*sent)[0])
	}
	before := len(*sent)
	// nine images in the latest message alone and none in history: there
	// is no earlier image to leave out, so the request goes as it came
	over := imageChat("eightmax/m1", 0, 9)
	if rec := postCapChat(t, s, over); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(*sent) != before+1 || !same((*sent)[len(*sent)-1], over) {
		t.Fatalf("the latest user message's images were left out: %.300s", (*sent)[len(*sent)-1])
	}
	if n := strings.Count((*sent)[len(*sent)-1], `"type":"image_url"`); n != 9 {
		t.Errorf("the latest message's %d images, not 9", n)
	}
}

// The user's number and the cap Copilot's refusal taught the same
// provider's model both hold: the smaller one is sent at.
func TestMaxImagesStandsOverTheLearnedCap(t *testing.T) {
	var sent []string
	var refused atomic.Int32
	copilotTurnFake(t, map[string]string{"gemini-4-carbon": `["/chat/completions"]`}, nil,
		overTen(&refused), copilotImageCap, http.StatusBadRequest, &sent)
	s := New()
	// Copilot's own refusal teaches 10 first
	if rec := postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 11, 1)); rec.Code != 200 {
		t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
	}
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ own, want int }{{4, 4}, {20, 10}} {
		p.MaxImages = map[string]int{"*": c.own}
		if err := provider.Save(*p); err != nil {
			t.Fatal(err)
		}
		before := len(sent)
		if rec := postCapChat(t, s, imageChat("copilot/gemini-4-carbon", 11, 1)); rec.Code != 200 {
			t.Fatalf("the turn failed: %d %s", rec.Code, rec.Body.String())
		}
		if len(sent) != before+1 {
			t.Fatalf("own %d: asked %d requests, want 1", c.own, len(sent)-before)
		}
		if n := strings.Count(sent[len(sent)-1], `"type":"image_url"`); n != c.want {
			t.Errorf("own %d: sent %d images, not %d", c.own, n, c.want)
		}
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
