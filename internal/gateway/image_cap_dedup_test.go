package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// shotsChat is a chat conversation with one earlier turn per shot in
// history, showing capShot(n), then the latest user message showing
// latest's.
func shotsChat(model string, history, latest []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"model":%q,"stream":false,"messages":[`, model)
	for i, n := range history {
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"look at screen %d"},{"type":"image_url","image_url":{"url":%q}}]},{"role":"assistant","content":"seen %d"},`, i, capShot(n), i)
	}
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"the latest ask"}`)
	for _, n := range latest {
		fmt.Fprintf(&b, `,{"type":"image_url","image_url":{"url":%q}}`, capShot(n))
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// shotsMessages is shotsChat on Anthropic's Messages.
func shotsMessages(history, latest []int) string {
	img := func(n int) string {
		return fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, strings.TrimPrefix(capShot(n), "data:image/png;base64,"))
	}
	var b strings.Builder
	b.WriteString(`{"model":"m","max_tokens":10,"messages":[`)
	for i, n := range history {
		fmt.Fprintf(&b, `{"role":"user","content":[{"type":"text","text":"look at screen %d"},%s]},{"role":"assistant","content":"seen %d"},`, i, img(n), i)
	}
	b.WriteString(`{"role":"user","content":[{"type":"text","text":"the latest ask"}`)
	for _, n := range latest {
		b.WriteString("," + img(n))
	}
	b.WriteString(`]}]}`)
	return b.String()
}

func shot(n int) string { return fmt.Sprintf("SHOT%02d", n) }

// shotsLeft checks that out shows each of want once and none of gone, and
// says shownLater and overCap left-out images.
func shotsLeft(t *testing.T, out string, want, gone []int, shownLater, overCap int) {
	t.Helper()
	for _, n := range want {
		if c := strings.Count(out, shot(n)); c != 1 {
			t.Errorf("%s shown %d times, not once", shot(n), c)
		}
	}
	for _, n := range gone {
		if strings.Contains(out, shot(n)) {
			t.Errorf("%s was not left out", shot(n))
		}
	}
	if c := strings.Count(out, imageShownLater); c != shownLater {
		t.Errorf("%d images said to be shown later, not %d", c, shownLater)
	}
	if c := strings.Count(out, imageOverCap); c != overCap {
		t.Errorf("%d images said to be over the cap, not %d", c, overCap)
	}
	if !strings.Contains(out, "the latest ask") {
		t.Error("the latest user message was changed")
	}
}

// An image the conversation carries again (a screenshot read twice) is
// left out where it came earlier before any other image is, so a
// conversation over the cap only by its copies keeps every image it
// shows (yuushabu_buyinn on X): screens 0 to 8, then screen 5 twice more,
// and the latest ask's own image, are 12 images and 10 different ones.
func TestImagesShownAgainLeftOutFirst(t *testing.T) {
	history, latest := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 5, 5}, []int{90}
	t.Run("chat", func(t *testing.T) {
		out, left, ok := withoutImagesOver(provider.Chat, []byte(shotsChat("m", history, latest)), 10)
		if !ok || left != 2 {
			t.Fatalf("ok %v, %d left out, not 2", ok, left)
		}
		shotsLeft(t, string(out), []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 90}, nil, 2, 0)
		if strings.Index(string(out), shot(5)) < strings.Index(string(out), "look at screen 10") {
			t.Error("not the last copy kept")
		}
	})
	t.Run("messages", func(t *testing.T) {
		out, left, ok := withoutImagesOver(provider.Anthropic, []byte(shotsMessages(history, latest)), 10)
		if !ok || left != 2 {
			t.Fatalf("ok %v, %d left out, not 2", ok, left)
		}
		shotsLeft(t, string(out), []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 90}, nil, 2, 0)
	})
}

// A history image the latest user message shows again is left out of the
// history, the latest message kept whole; then the oldest go as before.
func TestImagesShownAgainInTheLatestAsk(t *testing.T) {
	body := shotsChat("m", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 90}, []int{90})
	out, left, ok := withoutImagesOver(provider.Chat, []byte(body), 10)
	if !ok || left != 2 {
		t.Fatalf("ok %v, %d left out, not 2", ok, left)
	}
	shotsLeft(t, string(out), []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 90}, []int{0}, 1, 1)
	if !strings.HasSuffix(string(out), fmt.Sprintf(`{"type":"image_url","image_url":{"url":%q}}]}]}`, capShot(90))) {
		t.Error("the latest ask's image is not where it was")
	}
}

// An image left out for the cap goes with every earlier copy of it, each
// said to be over the cap: none is said to be shown later when it isn't.
func TestImageOverCapTakesItsCopies(t *testing.T) {
	body := shotsChat("m", []int{0, 1, 0, 2, 3, 4, 5, 6, 7, 8, 9, 10}, []int{90})
	out, left, ok := withoutImagesOver(provider.Chat, []byte(body), 10)
	if !ok || left != 3 {
		t.Fatalf("ok %v, %d left out, not 3", ok, left)
	}
	shotsLeft(t, string(out), []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 90}, []int{0, 1}, 0, 3)
}

// A request at the cap goes as it came, copies and all; and copies in the
// latest ask alone are its own, kept whole.
func TestImageCopiesUnderCapUntouched(t *testing.T) {
	body := shotsChat("m", []int{0, 1, 2, 3, 3, 3, 4, 5, 6}, []int{7})
	if out, left, ok := withoutImagesOver(provider.Chat, []byte(body), 10); !ok || left != 0 || string(out) != body {
		t.Fatalf("a request at the cap was changed: ok %v, %d left out", ok, left)
	}
	body = shotsChat("m", []int{0}, []int{90, 90, 90, 90, 90, 90, 90, 90, 90, 90, 90})
	if out, _, ok := withoutImagesOver(provider.Chat, []byte(body), 10); ok || string(out) != body {
		t.Fatal("the latest ask's own copies were left out")
	}
}

// Through Copilot: the refused turn is asked again with the copies left
// out and nothing else, and every screen it showed reaches the model.
func TestCopilotGeminiImageCopiesFirst(t *testing.T) {
	var sent []string
	var refused atomic.Int32
	copilotTurnFake(t, map[string]string{"gemini-4-carbon": `["/chat/completions"]`}, nil,
		overTen(&refused), copilotImageCap, http.StatusBadRequest, &sent)
	s := New()
	rec := postCapChat(t, s, shotsChat("copilot/gemini-4-carbon", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 5, 5}, []int{90}))
	if rec.Code != 200 || len(sent) != 1 || refused.Load() != 1 {
		t.Fatalf("%d, %d answered, %d refused: %s", rec.Code, len(sent), refused.Load(), rec.Body.String())
	}
	shotsLeft(t, sent[0], []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 90}, nil, 2, 0)
}
